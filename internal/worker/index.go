package worker

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"github.com/home-operations/kritika/internal/adapter"
	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/executor"
	"github.com/home-operations/kritika/internal/forge"
	"github.com/home-operations/kritika/internal/forge/forgejo"
	"github.com/home-operations/kritika/internal/forge/github"
	"github.com/home-operations/kritika/internal/forge/gitlab"
	"github.com/home-operations/kritika/internal/jobs"
	"github.com/home-operations/kritika/internal/jobtimeout"
	"github.com/home-operations/kritika/internal/metrics"
	"github.com/home-operations/kritika/internal/model"
	"github.com/home-operations/kritika/internal/review"
	"github.com/home-operations/kritika/internal/runspec"
	"github.com/home-operations/kritika/internal/store"
)

// embedBatch is how many staged chunks are embedded and inserted at once.
const embedBatch = 128

// indexSlots is how many of the embedder's slots index runs can hold at
// once: all but one. So a review's similar-code lookup, which takes a slot
// for one short call, always has one to take, however many index jobs wait
// for theirs. An embedder with one slot has none to spare.
func indexSlots(slots configfile.Slots) configfile.Slots {
	slots.Limit = max(1, slots.Limit-1)
	return slots
}

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
	repo, configRead, err := w.findRepo(ctx, args)
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

	eff, off, err := w.repoConfig(ctx, job, account.Key(), client, repo, commit, configRead, settings, logger)
	if err != nil {
		return err
	}
	if off {
		return w.optedOut(ctx, job, account.Key(), client, repo, commit, logger)
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
	resumeKey := indexResumeKey(commit, mode, base, vectorSource(active, args.Full), emb, ignoreHash)
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
			return w.stageFailed(ctx, job, account.Key(), mode, runID, runnerRunID, err, logger)
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
	clearForgeWaits(ctx, job.Metadata, logger)
	// A forced rebuild leaves the tip to the catch-up it queued as it published.
	if args.Full {
		return nil
	}
	// A push while this ran was absorbed into this job. If the branch moved,
	// index again at once, as a snooze that is not an attempt. A tip that the
	// forge did not give is asked for again the same way. The index is
	// published, so the job must not fail over it and lose the push, nor
	// spend an attempt on it. A rate limit of any forge that names
	// a later time to ask again is asked again then, not within its limit.
	tip, _, err := client.BranchTip(ctx, owner, name, repo.DefaultBranch)
	if err != nil {
		wait := tipCheckDelay(err)
		logger.Warn("index tip not checked after publication, checking again", "error", err, "in", wait)
		return river.JobSnooze(wait)
	}
	if tip != commit {
		logger.Info("index again: the branch moved while it ran", "tip", review.ShortSHA(tip))
		return river.JobSnooze(0)
	}
	return nil
}

// findRepo reads the repository that the job is for, and reserves its read
// of the repository's .kritika.yaml (see store.RecordRepoConfigOff), which
// comes before the job reads the branch tip.
func (w *Index) findRepo(ctx context.Context, args jobs.IndexArgs) (repo store.IndexRepo, configRead int64, err error) {
	err = w.Store.WithAccount(ctx, args.AccountID, func(tx pgx.Tx) error {
		var err error
		if repo, err = store.FindIndexRepo(ctx, tx, args.RepositoryID); err != nil {
			return err
		}
		configRead, err = store.ReserveRepoConfigRead(ctx, tx, args.RepositoryID)
		return err
	})
	return repo, configRead, err
}

// repoConfig reads the repository's own .kritika.yaml as of commit, which
// can turn it off and add ignore globs, and returns what applies to the
// index job. off says that the file turns the repository off, and the job
// indexes nothing. Only an index job reads the default branch's file, so it
// records whether it does for SweepStoppedIndexes, which drops the index of
// a repository off for longer than the grace.
func (w *Index) repoConfig(
	ctx context.Context, job *river.Job[jobs.IndexArgs], account string, client forge.Client, repo store.IndexRepo, commit string,
	read int64, settings configfile.Settings, logger *slog.Logger,
) (eff Effective, off bool, err error) {
	owner, name, _ := strings.Cut(repo.Name, "/")
	// The job waits out GitLab's rate limit itself, by waitOutForge, rather
	// than the SDK sleeping on it, which can outlast the job's deadline.
	doc, _, err := readRepoConfig(gitlab.WaitOwnRateLimit(ctx), client, owner, name, commit)
	if err != nil {
		return eff, false, w.waitOutForge(ctx, job, account, logger, err)
	}
	eff, _ = effective(settings, doc)
	var recorded bool
	err = w.Store.WithAccount(ctx, job.Args.AccountID, func(tx pgx.Tx) error {
		var err error
		recorded, err = store.RecordRepoConfigOff(ctx, tx, job.Args.RepositoryID, read, !eff.Enabled)
		return err
	})
	if err != nil {
		return eff, false, err
	}
	if !recorded {
		logger.Info("index again: another job read .kritika.yaml since, at a tip that can be newer")
		return eff, true, river.JobSnooze(0)
	}
	if !eff.Enabled {
		logger.Info("index skipped, disabled in .kritika.yaml")
		return eff, true, nil
	}
	return eff, false, nil
}

// optedOut ends a job that found the repository turned off by its
// .kritika.yaml at commit, unless the branch moved since. A push that turns
// it on again while the file was read was absorbed into this job, so the
// job indexes again at once, as a snooze that is not an attempt, to read
// the new tip's file.
func (w *Index) optedOut(
	ctx context.Context, job *river.Job[jobs.IndexArgs], account string, client forge.Client, repo store.IndexRepo, commit string,
	logger *slog.Logger,
) error {
	owner, name, _ := strings.Cut(repo.Name, "/")
	tip, _, err := client.BranchTip(ctx, owner, name, repo.DefaultBranch)
	if err != nil {
		return w.waitOutForge(ctx, job, account, logger, err)
	}
	if tip != commit {
		logger.Info("index again: the branch moved while .kritika.yaml was read", "tip", review.ShortSHA(tip))
		return river.JobSnooze(0)
	}
	return nil
}

// stageFailed ends the run that stage failed with err as failed, and returns
// what the job returns: a wait when the forge did not mint the runner's git
// token, err otherwise.
func (w *Index) stageFailed(
	ctx context.Context, job *river.Job[jobs.IndexArgs], account, mode, runID, runnerRunID string, err error, logger *slog.Logger,
) error {
	w.Metrics.IndexRun(account, mode, string(store.IndexFailed), 0)
	fctx, cancel := detach(ctx)
	defer cancel()
	if ferr := w.finish(fctx, job, runID, runnerRunID, store.IndexFailed, 0, err.Error()); ferr != nil {
		return errors.Join(err, ferr)
	}
	// Not errors.As: stage joins a failure of its own cleanup to the token's,
	// and that one must spend the attempt.
	if tokenErr, ok := err.(*runnerTokenError); ok {
		return w.waitOutForge(ctx, job, account, logger, tokenErr)
	}
	return err
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

// tipCheckDelay is how long an index job waits to ask for the branch tip
// again after the forge did not give it, with err: tipCheckWait, or until
// the later time that GitHub's rate limit or another forge's 429 names
// (forgeRetryAfter). The snooze replaces err, so ForgeRateLimit never sees
// GitHub's limit here.
func tipCheckDelay(err error) time.Duration {
	now := time.Now()
	if d, limited := github.RetryAfter(err, now); limited {
		return max(tipCheckWait, d)
	}
	if d, limited := forgeRetryAfter(err, now); limited {
		return max(tipCheckWait, d)
	}
	return tipCheckWait
}

// forgeRetryAfter is how long to wait before asking again after err, when
// err is a 429 of GitLab, Forgejo or Gitea that names when to ask again.
func forgeRetryAfter(err error, now time.Time) (time.Duration, bool) {
	if d, limited := gitlab.RetryAfter(err, now); limited {
		return d, true
	}
	return forgejo.RetryAfter(err, now)
}

// An index job waits out a forge that failed transiently before its run
// starts, from forgeWaitMin doubling to forgeWaitMax, forgeWaits times: up to
// about an hour in all. River's backoff would spend the job's few attempts
// within seconds and drop the push that queued it. A snooze spends none, so
// forgeWaitsKey counts the waits in the job's metadata, where River's own
// snooze count also counts waits for other reasons. Past forgeWaits, the
// failure spends an attempt. A publication clears the count, so a job that
// indexes again, for a branch that moved while it ran, has every wait again.
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

// clearForgeWaits clears the forge waits that metadata counts, when it
// counts any, as the job's attempt ends.
func clearForgeWaits(ctx context.Context, metadata []byte, logger *slog.Logger) {
	var meta struct {
		Waits int `json:"forge_waits"`
	}
	if json.Unmarshal(metadata, &meta) != nil || meta.Waits == 0 {
		return
	}
	if err := river.MetadataSet(ctx, forgeWaitsKey, 0); err != nil {
		logger.Warn("forge waits not cleared after publication", "error", err)
	}
}

// waitOutForge snoozes the job, as its error, when forgeWait says that it
// waits out err. Otherwise it returns err. A 429 of GitLab, Forgejo or Gitea
// that names when to ask again snoozes the job until then instead, which can
// be past any forge wait, and spends none: ForgeRateLimit does the same for
// GitHub's.
func (w *Index) waitOutForge(ctx context.Context, job *river.Job[jobs.IndexArgs], account string, logger *slog.Logger, err error) error {
	if ctx.Err() != nil {
		return err
	}
	if d, limited := forgeRetryAfter(err, time.Now()); limited {
		logger.Warn("index waits out the forge's rate limit", "for", d.Round(time.Second), "error", err)
		w.Metrics.IndexSnoozed(account, "forge")
		return river.JobSnooze(d)
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
// at a time, with no transaction open while the embedder works, and a slot
// of the embedder held for each batch's call alone (vectorsFor). It then swaps
// them in and ends the run as completed with one short transaction. So a
// review never sees a half-built index, and a retry never finds an active
// generation still running. A full build makes its run the active generation.
// An incremental step moves its chunks into the active generation, in place
// of the changed paths' chunks. It returns the runner's pack.
// For a forced rebuild, this transaction also completes the River job. That
// completion takes precedence over any later Work error or snooze.
func (w *Index) embed(
	ctx context.Context, job *river.Job[jobs.IndexArgs], account *configfile.Account, embedder model.Embedder, emb *configfile.Embedding,
	commit, runID, runnerRunID string, active *store.Generation, slots configfile.Slots,
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
	activeRun := vectorSource(active, args.Full)
	for {
		var batch []store.StagedChunk
		err := w.Store.WithAccount(ctx, args.AccountID, func(tx pgx.Tx) error {
			var err error
			batch, err = store.ReadStagedChunks(ctx, tx, runnerRunID, last, cmp.Or(w.batch, embedBatch))
			return err
		})
		if err != nil {
			return total, pack, err
		}
		if len(batch) == 0 {
			break
		}
		vectors, err := w.vectorsFor(ctx, job, account, embedder, emb, runID, runnerRunID, activeRun, batch, slots)
		if err != nil {
			return total, pack, err
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
			return total, pack, err
		}
		total += len(batch)
		last = batch[len(batch)-1].ID
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
			// A build that took vectors from the active generation must not
			// replace another that published since, as a forced rebuild that
			// replaced those very vectors.
			if activeRun != "" {
				repo, err := store.FindIndexRepo(ctx, tx, args.RepositoryID)
				if err != nil {
					return err
				}
				if repo.ActiveRun != activeRun {
					return store.ErrGenerationChanged
				}
			}
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

// indexMatches reports whether a job of trigger can step g, the active
// generation, incrementally rather than build the index in full. A push steps
// a generation of another index format too: the changed paths are chunked in
// this build's format, and the generation keeps its own, so the onboarding
// feeder still rebuilds it in full. That paces the rebuilds of a new format
// to the feeder's window, where every push after a deploy will otherwise
// rebuild its repository at once. A generation of this format that a step
// of another format advanced is rebuilt the same way.
func indexMatches(g *store.Generation, emb *configfile.Embedding, ignoreHash, trigger string) bool {
	return g != nil && g.Model == emb.Model && g.Dims == emb.Dims && g.DocumentPrompt == emb.DocumentPrompt && g.IgnoreHash == ignoreHash &&
		(g.Format == runspec.IndexFormat && !g.Mixed || trigger == jobs.TriggerPush)
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
// invalidates incompatible code changes to staging or StagedChunk.EmbedText.
// A field that is empty unless configured is omitted when empty, so adding
// one leaves the keys of runs in flight valid. source is the generation the
// run takes vectors from, see vectorSource: progress that took them from a
// generation since replaced is not resumed.
func indexResumeKey(commit, mode, base, source string, emb *configfile.Embedding, ignoreHash string) string {
	_, _, itemChars := emb.Bounds()
	identity := struct {
		Version                                              int
		Commit, Mode, Base, BaseRun, Ignore, Endpoint, Model string
		Dims, ItemChars                                      int
		DocumentPrompt                                       string `json:",omitempty"`
	}{runspec.IndexFormat, commit, mode, base, source, ignoreHash, emb.BaseURL, emb.Model, emb.Dims, itemChars, emb.DocumentPrompt}
	data, _ := json.Marshal(identity) // Only strings and integers, so encoding cannot fail.
	return fmt.Sprintf("%x", sha256.Sum256(data))
}

// vectorSource is the generation whose vectors a build of the repository
// whose active generation is active takes for the chunks of the same text,
// "" for none. A forced rebuild, full, is how an admin replaces vectors that
// the active generation holds, as for a model replaced behind an unchanged
// endpoint, so it reuses only what it embedded itself.
func vectorSource(active *store.Generation, full bool) string {
	if active == nil || full {
		return ""
	}
	return active.ID
}
