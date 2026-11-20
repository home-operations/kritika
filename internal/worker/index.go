package worker

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"github.com/home-operations/kritika/internal/adapter"
	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/executor"
	"github.com/home-operations/kritika/internal/jobs"
	"github.com/home-operations/kritika/internal/jobtimeout"
	"github.com/home-operations/kritika/internal/metrics"
	"github.com/home-operations/kritika/internal/model"
	"github.com/home-operations/kritika/internal/review"
	"github.com/home-operations/kritika/internal/runspec"
	"github.com/home-operations/kritika/internal/store"
	"github.com/home-operations/kritika/internal/textcut"
)

// embedBatch is how many staged chunks are embedded and inserted at once.
const embedBatch = 128

// indexSlots is how many of an account's embed slots index runs can hold at
// once: all but one. So a review's similar-code lookup, which takes a slot
// for one short call, always has one to take. That is true while an
// onboarding wave holds the rest for the length of a repository's embedding
// pass. An account with one slot has none to spare.
func indexSlots(concurrency int) int { return max(1, concurrency-1) }

// Index works the index queue: one job builds or advances a repository's
// embedding index to a commit. The runner Job chunks the tree. This worker
// embeds the chunks and swaps or advances the active generation.
type Index struct {
	river.WorkerDefaults[jobs.IndexArgs]
	Base
	Executor executor.Executor
	// Embedders resolves the instance's embedder. A generation built with
	// another model or dimension is rebuilt in full.
	Embedders *adapter.Embedders
	// GatewayURL is where an index run reports, and GatewayTokenTTL how long
	// the run token that admits it to the egress proxy outlives its Job's
	// deadline.
	GatewayURL      string
	GatewayTokenTTL time.Duration

	// superviseEvery overrides superviseInterval.
	superviseEvery time.Duration
	// batch overrides embedBatch.
	batch int
}

// Work implements river.Worker.
func (w *Index) Work(ctx context.Context, job *river.Job[jobs.IndexArgs]) error {
	args := job.Args
	file := w.Current.Get()
	embedder, emb := w.Embedders.Embedder(file)
	if embedder == nil {
		return river.JobCancel(errors.New("worker: no embedder is configured, indexing is off"))
	}
	account, err := w.account(file, args.AccountID)
	if err != nil {
		return err
	}
	var repo store.IndexRepo
	err = w.Store.WithAccount(ctx, args.AccountID, func(tx pgx.Tx) error {
		var err error
		repo, err = store.FindIndexRepo(ctx, tx, args.RepositoryID)
		return err
	})
	if errors.Is(err, store.ErrNotFound) {
		return river.JobCancel(fmt.Errorf("worker: repository %s is unknown", args.RepositoryID))
	}
	if err != nil {
		return err
	}
	logger := w.Logger.With("account", account.Key(), "repository", repo.Name, "trigger", args.Trigger)
	if err := w.waitForAppliedBuild(ctx, job, account.Key(), emb, logger); err != nil {
		return err
	}
	settings := file.Settings(account)
	if !repo.Enabled || !file.Runs(account, repo.Traits) {
		logger.Info("index skipped, repository disabled")
		return nil
	}
	client, err := w.client(ctx, file, account, repo.Name)
	if err != nil {
		return w.waitOutForge(ctx, job, account.Key(), logger, err)
	}
	owner, name, _ := strings.Cut(repo.Name, "/")
	// The job indexes the tip, not the commit of the push that queued it:
	// pushes while it waited were absorbed into it.
	commit, branch, err := client.BranchTip(ctx, owner, name, repo.DefaultBranch)
	if err != nil {
		return w.waitOutForge(ctx, job, account.Key(), logger, err)
	}
	if repo.DefaultBranch == "" {
		// Best effort: the run has the branch either way, and the next one
		// looks it up again until this lands.
		err := w.Store.WithAccount(ctx, args.AccountID, func(tx pgx.Tx) error {
			return store.RecordDefaultBranch(ctx, tx, args.RepositoryID, branch)
		})
		if err != nil {
			logger.Warn("default branch not recorded", "branch", branch, "error", err)
		}
	}
	logger = logger.With("commit", review.ShortSHA(commit))

	// The repository's own .kritika.yaml, as of the commit indexed, can stop
	// indexing and add ignore globs.
	doc, _, err := readRepoConfig(ctx, client, owner, name, commit)
	if err != nil {
		return w.waitOutForge(ctx, job, account.Key(), logger, err)
	}
	eff, _ := effective(settings, doc)
	if !eff.Enabled {
		logger.Info("index skipped, disabled in .kritika.yaml")
		return nil
	}
	ignoreHash := indexIgnoreHash(eff.Ignore)
	active, err := w.activeGeneration(ctx, args.AccountID, repo.ActiveRun)
	if err != nil {
		return err
	}
	mode, base := store.IndexModeFull, ""
	if !args.Full && indexMatches(active, emb, ignoreHash, args.Trigger) {
		if active.Commit == commit {
			logger.Info("index already at this commit")
			return nil
		}
		mode, base = store.IndexModeIncremental, active.Commit
	}
	resumeKey := indexResumeKey(commit, mode, base, active, emb, ignoreHash)
	var runID, runnerRunID string
	var resumed bool
	err = w.Store.WithAccount(ctx, args.AccountID, func(tx pgx.Tx) error {
		if err := w.lockAppliedIndexBuild(ctx, tx, job, account.Key(), emb, logger); err != nil {
			return err
		}
		var err error
		runID, runnerRunID, err = store.ResumeIndexRun(ctx, tx, job.ID, resumeKey)
		if err != nil {
			return err
		}
		if runID != "" {
			resumed = true
			return nil
		}
		runID, runnerRunID, err = store.StartIndexRun(ctx, tx, store.NewIndexRun{
			AccountID: args.AccountID, RepositoryID: args.RepositoryID, Commit: commit, Base: base, Mode: mode, Trigger: args.Trigger,
			Embedding: *emb, IgnoreHash: ignoreHash, ResumeKey: resumeKey, StrayAfter: jobtimeout.RescueStuckJobsAfter, JobID: job.ID,
		})
		return err
	})
	if err != nil {
		return err
	}
	if !resumed {
		err := w.stage(ctx, job, account, repo.Name, client, runID, runspec.Spec{
			Version: runspec.Version, Kind: runspec.KindIndex, RunID: runnerRunID, CloneURL: client.CloneURL(owner, name), GatewayURL: w.GatewayURL,
			Forge: string(account.Forge), Head: commit, Base: base, Ignore: eff.Ignore,
		}, logger)
		if err != nil {
			w.Metrics.IndexRun(account.Key(), mode, string(store.IndexFailed), 0)
			fctx, cancel := detach(ctx)
			defer cancel()
			return errors.Join(err, w.finish(fctx, job, runID, runnerRunID, store.IndexFailed, 0, err.Error()))
		}
	} else {
		logger.Info("index embedding resumed", "run", runID)
	}
	n, pack, err := w.embed(ctx, job, account, embedder, emb, commit, runID, runnerRunID, active, indexSlots(file.Slots(account, emb.Ref)))
	mode = pack.Mode
	if errors.Is(err, store.ErrGenerationChanged) {
		logger.Info("index again: another run published while it ran")
		w.Metrics.IndexRun(account.Key(), mode, string(store.IndexSuperseded), n)
		fctx, fcancel := detach(ctx)
		defer fcancel()
		if err := w.finish(fctx, job, runID, runnerRunID, store.IndexSuperseded, n, ""); err != nil {
			return err
		}
		return river.JobSnooze(0)
	}
	if err != nil {
		logger.Error("index embedding or publication failed", "error", err)
		w.Metrics.IndexRun(account.Key(), mode, string(store.IndexFailed), 0)
		fctx, fcancel := detach(ctx)
		defer fcancel()
		return errors.Join(err, w.finish(fctx, job, runID, runnerRunID, store.IndexFailed, n, err.Error()))
	}
	w.completed(account, pack, n, logger)
	// A forced rebuild leaves the tip to the catch-up it queued as it published.
	if args.Full {
		return nil
	}
	// A push while this ran was absorbed into this job. If the branch moved,
	// index again at once, as a snooze that is not an attempt. A tip that the
	// forge did not give is asked for again the same way. The index is
	// published, so the job must not fail over it and lose the push, nor
	// spend an attempt on it.
	tip, _, err := client.BranchTip(ctx, owner, name, repo.DefaultBranch)
	if err != nil {
		logger.Warn("index tip not checked after publication, checking again", "error", err, "in", tipCheckWait)
		return river.JobSnooze(tipCheckWait)
	}
	if tip != commit {
		logger.Info("index again: the branch moved while it ran", "tip", review.ShortSHA(tip))
		return river.JobSnooze(0)
	}
	return nil
}

// completed logs and counts an index run that published n chunks from pack.
func (w *Index) completed(account *configfile.Account, pack store.IndexPack, n int, logger *slog.Logger) {
	logger.Info("index completed", "mode", pack.Mode, "chunks", n)
	w.Metrics.IndexRun(account.Key(), pack.Mode, string(store.IndexCompleted), n)
	if pack.Truncated {
		logger.Warn("index published truncated: the build stopped at the indexer's budget and left out the files past it", "chunks", n)
		w.Metrics.IndexTruncated(account.Key())
	}
}

// appliedBuildWait is how long an index job waits on a pod whose embedder or
// index format the leader did not apply, before it is tried again.
// tipCheckWait is how long one waits to ask the forge for the branch tip
// again after the forge did not give it.
const (
	appliedBuildWait = time.Minute
	tipCheckWait     = time.Minute
)

// An index job waits out a forge that failed transiently before its run
// starts, from forgeWaitMin doubling to forgeWaitMax, forgeWaits times: up to
// about an hour in all. River's backoff would spend the job's few attempts
// within seconds and drop the push that queued it. A snooze spends none, so
// forgeWaitsKey counts the waits in the job's metadata, where River's own
// snooze count also counts waits for other reasons. Past forgeWaits, the
// failure spends an attempt.
const (
	forgeWaitMin  = 30 * time.Second
	forgeWaitMax  = 15 * time.Minute
	forgeWaits    = 8
	forgeWaitsKey = "forge_waits"
)

// forgeWait is how long an index job, with metadata, waits out err from a
// forge, and the count of its waits with this one. A count of 0 says that it
// does not wait: err is not transient, the job used up its waits, or its
// count cannot be read.
func forgeWait(err error, metadata []byte) (time.Duration, int) {
	if !transientForge(err) {
		return 0, 0
	}
	var meta struct {
		Waits int `json:"forge_waits"`
	}
	if json.Unmarshal(metadata, &meta) != nil || meta.Waits >= forgeWaits {
		return 0, 0
	}
	return store.Backoff(meta.Waits, forgeWaitMin, forgeWaitMax), meta.Waits + 1
}

// waitOutForge snoozes the job, as its error, when forgeWait says that it
// waits out err. Otherwise it returns err.
func (w *Index) waitOutForge(ctx context.Context, job *river.Job[jobs.IndexArgs], account string, logger *slog.Logger, err error) error {
	if ctx.Err() != nil {
		return err
	}
	d, waits := forgeWait(err, job.Metadata)
	if waits == 0 {
		return err
	}
	if serr := river.MetadataSet(ctx, forgeWaitsKey, waits); serr != nil {
		logger.Warn("forge wait not counted, the failure spends an attempt", "error", serr)
		return err
	}
	logger.Warn("index waits out a forge failure", "waits", waits, "of", forgeWaits, "for", d.Round(time.Second), "error", err)
	w.Metrics.IndexSnoozed(account, "forge")
	return river.JobSnooze(d)
}

// buildApplied reports whether emb is the embedder that the leader applied,
// and this build's index format the format that it applied. index_schema
// records both once the leader applies an embedder. A pod on another
// embedder, as during a rollout, will otherwise build an index that the
// gateway never queries, or take an index that another embedder built for
// current. A pod on another format will otherwise build indexes that the
// onboarding feeder rebuilds again, and the pods of the two builds take
// turns rebuilding each other's.
func buildApplied(ctx context.Context, tx pgx.Tx, emb *configfile.Embedding) (bool, error) {
	applied, ok, err := store.IndexSchemaIn(ctx, tx)
	return ok && applied.Model == emb.Model && applied.Dims == emb.Dims && applied.DocumentPrompt == emb.DocumentPrompt &&
		applied.Format == runspec.IndexFormat, err
}

// waitForAppliedBuild snoozes the job, as its error, unless this pod builds
// what the leader applied. It leaves the job to a pod that does before this
// pod asks the forge anything or takes an index for current.
func (w *Index) waitForAppliedBuild(
	ctx context.Context, job *river.Job[jobs.IndexArgs], account string, emb *configfile.Embedding, logger *slog.Logger,
) error {
	var applied bool
	err := w.Store.WithAccount(ctx, job.Args.AccountID, func(tx pgx.Tx) error {
		var err error
		applied, err = buildApplied(ctx, tx, emb)
		return err
	})
	if err != nil || applied {
		return err
	}
	return w.awaitAppliedBuild(ctx, job, account, logger)
}

// lockAppliedIndexBuild locks the repository's index build, as
// store.LockIndexBuild does, and then snoozes the job, as its error,
// unless this pod builds what the leader applied. The lock is on the
// repository's row, which the leader's rebuild for a new embedder updates
// first: a run started before the rebuild is superseded by it, and a run
// after sees the new embedder here.
func (w *Index) lockAppliedIndexBuild(
	ctx context.Context, tx pgx.Tx, job *river.Job[jobs.IndexArgs], account string, emb *configfile.Embedding, logger *slog.Logger,
) error {
	if err := store.LockIndexBuild(ctx, tx, job.Args.AccountID, job.Args.RepositoryID, job.ID, job.Attempt); err != nil {
		return err
	}
	applied, err := buildApplied(ctx, tx, emb)
	if err != nil || applied {
		return err
	}
	return w.awaitAppliedBuild(ctx, job, account, logger)
}

// buildWaitWarn is the wait for a pod on the applied build from which an
// index job logs each wait as a warning. A rollout's waits end within
// minutes. A leader that never applies the pods' embedder or index format, as
// when the store refused the configuration that the leader applies them with,
// keeps every index job waiting, with the pushes that its unique key folds
// into it, until a restart. buildWaitsKey counts the waits in the job's
// metadata.
const (
	buildWaitWarn = 10
	buildWaitsKey = "build_waits"
)

// awaitAppliedBuild snoozes the job, as its error, for appliedBuildWait, to
// leave it to a pod on the embedder and index format that the leader applied.
func (w *Index) awaitAppliedBuild(ctx context.Context, job *river.Job[jobs.IndexArgs], account string, logger *slog.Logger) error {
	var meta struct {
		Waits int `json:"build_waits"`
	}
	if err := json.Unmarshal(job.Metadata, &meta); err != nil {
		logger.Warn("job metadata not read; counting the build wait as the first", "error", err)
	}
	waits := meta.Waits + 1
	if err := river.MetadataSet(ctx, buildWaitsKey, waits); err != nil {
		logger.Warn("build wait not counted", "error", err)
	}
	w.Metrics.IndexSnoozed(account, "build")
	if waits >= buildWaitWarn {
		logger.Warn("index has waited long for a pod on the embedder and index format the leader applied; "+
			"check kritika_config_error for a configuration the store refused",
			"waits", waits, "for", appliedBuildWait, "format", runspec.IndexFormat)
	} else {
		logger.Info("index waits for a pod on the embedder and index format the leader applied",
			"waits", waits, "for", appliedBuildWait, "format", runspec.IndexFormat)
	}
	return river.JobSnooze(appliedBuildWait)
}

// activeGeneration reads the repository's active generation, nil when it
// has none or the generation is not completed.
func (w *Index) activeGeneration(ctx context.Context, accountID, runID string) (*store.Generation, error) {
	if runID == "" {
		return nil, nil
	}
	var g store.Generation
	err := w.Store.WithAccount(ctx, accountID, func(tx pgx.Tx) error {
		var err error
		g, err = store.FindGeneration(ctx, tx, runID)
		return err
	})
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &g, nil
}

func (w *Index) finish(
	ctx context.Context, job *river.Job[jobs.IndexArgs], runID, runnerRunID string, status store.IndexRunStatus, chunks int, errText string,
) error {
	return w.Store.WithAccount(ctx, job.Args.AccountID, func(tx pgx.Tx) error {
		if err := store.LockIndexJob(ctx, tx, job.Args.AccountID, job.ID, job.Attempt); err != nil {
			return err
		}
		if err := store.FinishIndexRun(ctx, tx, runID, status, chunks, errText); err != nil {
			return err
		}
		if status == store.IndexSuperseded {
			return store.DiscardIndexProgress(ctx, tx, runID, runnerRunID)
		}
		return nil
	})
}

// embed turns the staged chunks into index_chunks rows under the run, a batch
// at a time, with no transaction open while the embedder works. It then swaps
// them in and ends the run as completed with one short transaction. So a
// review never sees a half-built index, and a retry never finds an active
// generation still running. A full build makes its run the active generation.
// An incremental step moves its chunks into the active generation, in place
// of the changed paths' chunks. It returns the runner's pack.
// For a forced rebuild, this transaction also completes the River job. That
// completion takes precedence over any later Work error or snooze.
func (w *Index) embed(
	ctx context.Context, job *river.Job[jobs.IndexArgs], account *configfile.Account, embedder model.Embedder, emb *configfile.Embedding,
	commit, runID, runnerRunID string, active *store.Generation, slots int,
) (int, store.IndexPack, error) {
	args := job.Args
	var pack store.IndexPack
	var last int64
	var total int
	err := w.Store.WithAccount(ctx, args.AccountID, func(tx pgx.Tx) error {
		var err error
		if err := store.LockIndexJob(ctx, tx, args.AccountID, job.ID, job.Attempt); err != nil {
			return err
		}
		pack, err = store.ReadIndexPack(ctx, tx, runID, runnerRunID)
		if err != nil {
			return err
		}
		last, total, err = store.IndexProgress(ctx, tx, runID)
		return err
	})
	if err != nil {
		return 0, pack, err
	}
	err = w.withLease(ctx, account, "embed:"+emb.Model, slots, job.ID, func(ctx context.Context) error {
		for {
			var batch []store.StagedChunk
			err := w.Store.WithAccount(ctx, args.AccountID, func(tx pgx.Tx) error {
				var err error
				batch, err = store.ReadStagedChunks(ctx, tx, runnerRunID, last, cmp.Or(w.batch, embedBatch))
				return err
			})
			if err != nil || len(batch) == 0 {
				return err
			}
			vectors, err := w.vectorsFor(ctx, args, account, embedder, emb, runnerRunID, batch)
			if err != nil {
				return err
			}
			err = w.Store.WithAccount(ctx, args.AccountID, func(tx pgx.Tx) error {
				if err := store.LockIndexJob(ctx, tx, args.AccountID, job.ID, job.Attempt); err != nil {
					return err
				}
				if err := store.InsertIndexChunks(ctx, tx, args.AccountID, args.RepositoryID, runID, batch, vectors); err != nil {
					return err
				}
				return store.CheckpointIndexBatch(ctx, tx, runID, last, batch[len(batch)-1].ID, len(batch))
			})
			if err != nil {
				return err
			}
			total += len(batch)
			last = batch[len(batch)-1].ID
		}
	})
	if err != nil {
		return total, pack, err
	}
	err = w.Store.WithAccount(ctx, args.AccountID, func(tx pgx.Tx) error {
		if err := store.LockIndexBuild(ctx, tx, args.AccountID, args.RepositoryID, job.ID, job.Attempt); err != nil {
			return err
		}
		if pack.Mode == store.IndexModeIncremental && active != nil {
			if err := store.AdvanceGeneration(ctx, tx, args.RepositoryID, *active, runID, commit, pack.ChangedPaths); err != nil {
				return err
			}
		} else {
			if err := store.ActivateGeneration(ctx, tx, args.RepositoryID, runID); err != nil {
				return err
			}
		}
		if err := store.ClearIndexStaging(ctx, tx, runID, runnerRunID); err != nil {
			return err
		}
		if err := store.FinishIndexRun(ctx, tx, runID, store.IndexCompleted, total, ""); err != nil {
			return err
		}
		if args.Full {
			// The catch-up job reads the tip after publication, even if a push
			// already published and is still running. The catch-up and
			// generation commit together.
			_, err := river.ClientFromContext[pgx.Tx](ctx).InsertTx(ctx, tx, jobs.IndexArgs{
				AccountID: args.AccountID, RepositoryID: args.RepositoryID, Trigger: jobs.TriggerPush, CatchUp: runID,
			}, nil)
			if err != nil {
				return fmt.Errorf("worker: queue index catch-up: %w", err)
			}
			// A forced rebuild repeats even at the same commit, so its
			// completion must survive a crash immediately after publication.
			return completeJob(ctx, tx, job)
		}
		return nil
	})
	return total, pack, err
}

// vectorsFor returns a vector for each chunk of batch and sets each chunk's
// content hash. A chunk whose hash the repository's index already holds
// takes the vector held. The others are embedded, each distinct text once, and
// only their tokens count as usage.
func (w *Index) vectorsFor(
	ctx context.Context, args jobs.IndexArgs, account *configfile.Account, embedder model.Embedder, emb *configfile.Embedding,
	runnerRunID string, batch []store.StagedChunk,
) ([][]float32, error) {
	texts := make([]string, len(batch))
	hashes := make([][]byte, len(batch))
	for i := range batch {
		texts[i] = embedText(emb.DocumentPrompt, batch[i])
		hashes[i] = contentHash(emb, texts[i])
		batch[i].ContentHash = hashes[i]
	}
	var held map[string][]float32
	err := w.Store.WithAccount(ctx, args.AccountID, func(tx pgx.Tx) error {
		var err error
		held, err = store.IndexVectors(ctx, tx, args.RepositoryID, hashes)
		return err
	})
	if err != nil {
		return nil, err
	}
	vectors := make([][]float32, len(batch))
	var missing []string
	sent := map[string]int{}
	for i, h := range hashes {
		if v, ok := held[string(h)]; ok {
			vectors[i] = v
		} else if _, ok := sent[string(h)]; !ok {
			sent[string(h)] = len(missing)
			missing = append(missing, texts[i])
		}
	}
	w.Metrics.IndexVectors(account.Key(), len(batch)-len(missing), len(missing))
	if len(missing) == 0 {
		return vectors, nil
	}
	embedded, used, err := embedder.Embed(ctx, missing)
	w.Metrics.ModelCall(account.Key(), emb.Model, store.RoleEmbedding, adapter.Outcome(err), used, 0, 0, 0, false)
	if err == nil || used > 0 {
		// Provider usage survives cancellation and failures to store its vectors.
		dctx, cancel := detach(ctx)
		uerr := w.Store.WithAccount(dctx, args.AccountID, func(tx pgx.Tx) error {
			return store.InsertUsage(dctx, tx, store.Usage{
				AccountID: args.AccountID, RepositoryID: args.RepositoryID, RunnerRunID: runnerRunID,
				Role: store.RoleEmbedding, Model: emb.Model, Input: used,
			})
		})
		cancel()
		err = errors.Join(err, uerr)
	}
	if err != nil {
		return nil, err
	}
	if len(embedded) != len(missing) {
		return nil, fmt.Errorf("worker: %d vectors for %d texts", len(embedded), len(missing))
	}
	for i, h := range hashes {
		if vectors[i] == nil {
			vectors[i] = embedded[sent[string(h)]]
		}
	}
	return vectors, nil
}

// contentHash identifies the vector of an index chunk whose embedded text is
// text: the text as the embedder is sent it, cut to its item limit, and the
// embedder that embeds it. Two chunks of one hash take the same vector.
func contentHash(emb *configfile.Embedding, text string) []byte {
	_, _, itemChars := emb.Bounds()
	h := sha256.New()
	_, _ = fmt.Fprintf(h, "%q %d %q\n", emb.Model, emb.Dims, emb.DocumentPrompt) // A hash.Hash never fails to write.
	_, _ = io.WriteString(h, textcut.Prefix(text, itemChars))
	return h.Sum(nil)
}

// indexMatches reports whether a job of trigger can step g, the active
// generation, incrementally rather than build the index in full. A push steps
// a generation of another index format too: the changed paths are chunked in
// this build's format, and the generation keeps its own, so the onboarding
// feeder still rebuilds it in full. That paces the rebuilds of a new format
// to the feeder's window, where every push after a deploy will otherwise
// rebuild its repository at once.
func indexMatches(g *store.Generation, emb *configfile.Embedding, ignoreHash, trigger string) bool {
	return g != nil && g.Model == emb.Model && g.Dims == emb.Dims && g.DocumentPrompt == emb.DocumentPrompt && g.IgnoreHash == ignoreHash &&
		(g.Format == runspec.IndexFormat || trigger == jobs.TriggerPush)
}

// indexIgnoreHash treats ignore rules as a set. Order and duplicates do not
// change which files the index covers. An empty stored hash requires a
// rebuild.
func indexIgnoreHash(ignore []string) string {
	patterns := slices.Clone(ignore)
	slices.Sort(patterns)
	patterns = slices.Compact(patterns)
	return fmt.Sprintf("%x", sha256.Sum256([]byte(fmt.Sprintf("%q", patterns))))
}

// embedText is what the embedder sees: the embedder's document prompt, then
// the path and symbol, qualified by its scope. They give the vector a little
// of the file's identity, the way a reader knows where a snippet came from.
// A window of a large declaration has the declaration's symbol, so it is
// placed though it does not show the declaration's first line.
func embedText(prompt string, c store.StagedChunk) string {
	head := c.Path
	if c.Symbol != "" {
		head += " " + c.Kind + " "
		if c.Scope != "" {
			head += c.Scope + "."
		}
		head += c.Symbol
	}
	return prompt + head + "\n" + c.Text
}

// recordRun writes what the executor learned about a runner Job and counts
// it.
func recordRun(
	ctx context.Context, st *store.Store, m *metrics.Metrics, account, accountID, runID, kind string, res executor.Result,
) error {
	var took time.Duration
	if !res.StartedAt.IsZero() {
		took = time.Since(res.StartedAt)
	}
	m.RunnerRun(account, kind, runOutcome(res), took)
	return st.WithAccount(ctx, accountID, func(tx pgx.Tx) error {
		return store.RecordRunnerRun(ctx, tx, runID, store.RunnerResult{
			JobName: res.JobName, PodName: res.PodName, NodeName: res.NodeName, ScheduledAt: res.ScheduledAt, StartedAt: res.StartedAt,
			ExitCode: res.ExitCode, TerminationReason: res.TerminationReason, DeadlineExceeded: res.DeadlineExceeded, LogTail: res.LogTail,
			Error: errText(res.Err),
		})
	})
}

// runOutcome names how a runner Job ended for the metric label.
func runOutcome(res executor.Result) string {
	switch {
	case res.DeadlineExceeded:
		return "deadline"
	case res.Err != nil:
		return "failed"
	default:
		return "success"
	}
}

// indexResumeKey excludes credentials and scheduling limits, which do not
// change vector contents. The version is the index format, which
// invalidates incompatible code changes to staging or embedText. A field that is empty unless configured
// is omitted when empty, so adding one leaves the keys of runs in flight
// valid.
func indexResumeKey(commit, mode, base string, active *store.Generation, emb *configfile.Embedding, ignoreHash string) string {
	baseRun := ""
	if mode == store.IndexModeIncremental && active != nil {
		baseRun = active.ID
	}
	_, _, itemChars := emb.Bounds()
	identity := struct {
		Version                                              int
		Commit, Mode, Base, BaseRun, Ignore, Endpoint, Model string
		Dims, ItemChars                                      int
		DocumentPrompt                                       string `json:",omitempty"`
	}{runspec.IndexFormat, commit, mode, base, baseRun, ignoreHash, emb.BaseURL, emb.Model, emb.Dims, itemChars, emb.DocumentPrompt}
	data, _ := json.Marshal(identity) // Only strings and integers, so encoding cannot fail.
	return fmt.Sprintf("%x", sha256.Sum256(data))
}
