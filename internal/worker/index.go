package worker

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivertype"

	"github.com/home-operations/kritika/internal/adapter"
	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/executor"
	"github.com/home-operations/kritika/internal/jobs"
	"github.com/home-operations/kritika/internal/jobtimeout"
	"github.com/home-operations/kritika/internal/metrics"
	"github.com/home-operations/kritika/internal/model"
	"github.com/home-operations/kritika/internal/review"
	"github.com/home-operations/kritika/internal/runner"
	"github.com/home-operations/kritika/internal/store"
)

// embedBatch is how many staged chunks are embedded and inserted at once.
const embedBatch = 128

// indexSlots is how many of an account's embed slots index runs may hold
// at once: all but one, so a review's similar-code lookup, which takes a
// slot for one short call, always has one to take while an onboarding
// wave holds the rest for the length of a repository's embedding pass. An
// account with one slot has none to spare.
func indexSlots(concurrency int) int { return max(1, concurrency-1) }

// Index works the index queue: one job builds or advances a repository's
// embedding index to a commit. The runner Job chunks the tree; this worker
// embeds the chunks and swaps or advances the active generation.
type Index struct {
	river.WorkerDefaults[jobs.IndexArgs]
	Base
	Executor executor.Executor
	// Embedders resolves the instance's embedder; a generation built with
	// another model or dimension is rebuilt in full.
	Embedders *adapter.Embedders

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
	settings := file.Settings(account, repo.Name)
	if !repo.Enabled || !file.Runs(account, repo.Name, repo.Traits) {
		logger.Info("index skipped, repository disabled")
		return nil
	}
	client, err := w.client(ctx, file, account, repo.Name)
	if err != nil {
		return err
	}
	owner, name, _ := strings.Cut(repo.Name, "/")
	// The job indexes the tip, not the commit of the push that queued it:
	// pushes while it waited were absorbed into it.
	commit, branch, err := client.BranchTip(ctx, owner, name, repo.DefaultBranch)
	if err != nil {
		return err
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
		return err
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
	if !args.Full && indexMatches(active, emb, ignoreHash) {
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
		if err := store.LockIndexJob(ctx, tx, args.AccountID, job.ID, job.Attempt); err != nil {
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
		err := w.stage(ctx, job, account, repo.Name, client, runID, runner.Spec{
			Version: runner.SpecVersion, Kind: runner.KindIndex, RunID: runnerRunID, CloneURL: client.CloneURL(owner, name),
			Head: commit, Base: base, Ignore: eff.Ignore,
		}, logger)
		if err != nil {
			w.Metrics.IndexRun(account.Key(), mode, string(store.IndexFailed), 0)
			fctx, cancel := detach(ctx)
			defer cancel()
			return errors.Join(err, w.finish(fctx, job, runID, store.IndexFailed, 0, err.Error()))
		}
	} else {
		logger.Info("index embedding resumed", "run", runID)
	}
	n, mode, err := w.embed(ctx, job, account, embedder, emb.Model, commit, runID, runnerRunID, active, settings)
	if errors.Is(err, store.ErrGenerationChanged) {
		logger.Info("index again: another run published while it ran")
		w.Metrics.IndexRun(account.Key(), mode, string(store.IndexSuperseded), n)
		fctx, fcancel := detach(ctx)
		defer fcancel()
		if err := w.finish(fctx, job, runID, store.IndexSuperseded, n, ""); err != nil {
			return err
		}
		return river.JobSnooze(0)
	}
	if err != nil {
		logger.Error("index embedding or publication failed", "error", err)
		w.Metrics.IndexRun(account.Key(), mode, string(store.IndexFailed), 0)
		fctx, fcancel := detach(ctx)
		defer fcancel()
		return errors.Join(err, w.finish(fctx, job, runID, store.IndexFailed, n, err.Error()))
	}
	logger.Info("index completed", "mode", mode, "chunks", n)
	w.Metrics.IndexRun(account.Key(), mode, string(store.IndexCompleted), n)
	// A forced rebuild leaves the tip to the catch-up it queued as it published.
	if args.Full {
		return nil
	}
	// A push while this ran was absorbed into this job: if the branch has
	// moved, index again at once, as a snooze that is not an attempt.
	if tip, _, err := client.BranchTip(ctx, owner, name, repo.DefaultBranch); err == nil && tip != commit {
		logger.Info("index again: the branch moved while it ran", "tip", review.ShortSHA(tip))
		return river.JobSnooze(0)
	}
	return nil
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
	ctx context.Context, job *river.Job[jobs.IndexArgs], runID string, status store.IndexRunStatus, chunks int, errText string,
) error {
	return w.Store.WithAccount(ctx, job.Args.AccountID, func(tx pgx.Tx) error {
		if err := store.LockIndexJob(ctx, tx, job.Args.AccountID, job.ID, job.Attempt); err != nil {
			return err
		}
		if err := store.FinishIndexRun(ctx, tx, runID, status, chunks, errText); err != nil {
			return err
		}
		if status == store.IndexSuperseded {
			var runner string
			if err := tx.QueryRow(ctx, `SELECT id::text FROM runner_runs WHERE index_run_id = $1`, runID).Scan(&runner); err != nil {
				return err
			}
			return store.DiscardIndexProgress(ctx, tx, runID, runner)
		}
		return nil
	})
}

// embed turns the staged chunks into index_chunks rows under the run, a
// batch at a time with no transaction open while the embedder works, then
// swaps them in and ends the run as completed with one short transaction,
// so a review never sees a half-built index and a retry never finds an
// active generation still running: a full build makes its run the active
// generation, and an incremental step moves its chunks into the active
// generation, in place of the changed paths' chunks.
// For a forced rebuild, this transaction also completes the River job;
// that completion takes precedence over any later Work error or snooze.
func (w *Index) embed(
	ctx context.Context, job *river.Job[jobs.IndexArgs], account *configfile.Account, embedder model.Embedder, embedModel string,
	commit, runID, runnerRunID string, active *store.Generation, settings configfile.Settings,
) (int, string, error) {
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
		return 0, pack.Mode, err
	}
	err = w.withLease(ctx, account, "embed:"+embedModel, indexSlots(settings.Limits.Concurrency), job.ID, func(ctx context.Context) error {
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
			texts := make([]string, len(batch))
			for i, c := range batch {
				texts[i] = embedText(c)
			}
			vectors, used, err := embedder.Embed(ctx, texts)
			w.Metrics.ModelCall(account.Key(), embedModel, store.RoleEmbedding, adapter.Outcome(err), used, 0, 0, 0, false)
			if err == nil || used > 0 {
				// Provider usage survives cancellation and failures to store its vectors.
				dctx, cancel := detach(ctx)
				uerr := w.Store.WithAccount(dctx, args.AccountID, func(tx pgx.Tx) error {
					return store.InsertUsage(dctx, tx, store.Usage{
						AccountID: args.AccountID, RepositoryID: args.RepositoryID, RunnerRunID: runnerRunID,
						Role: store.RoleEmbedding, Model: embedModel, Input: used,
					})
				})
				cancel()
				err = errors.Join(err, uerr)
			}
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
		return total, pack.Mode, err
	}
	err = w.Store.WithAccount(ctx, args.AccountID, func(tx pgx.Tx) error {
		if err := store.LockIndexJob(ctx, tx, args.AccountID, job.ID, job.Attempt); err != nil {
			return err
		}
		if _, _, err := store.IndexProgress(ctx, tx, runID); err != nil {
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
			// Check the tip after publication even if a push already published
			// and is still running. The catch-up and generation commit together.
			_, err := river.ClientFromContext[pgx.Tx](ctx).InsertTx(ctx, tx, jobs.IndexArgs{
				AccountID: args.AccountID, RepositoryID: args.RepositoryID, Trigger: jobs.TriggerPush, CatchUp: runID,
			}, nil)
			if err != nil {
				return fmt.Errorf("worker: queue index catch-up: %w", err)
			}
			// A forced rebuild repeats even at the same commit, so its
			// completion must survive a crash immediately after publication.
			completed, err := river.JobCompleteTx[*riverpgxv5.Driver](ctx, tx, job)
			if err != nil {
				return fmt.Errorf("worker: complete index job: %w", err)
			}
			// River can return an unchanged non-running row or complete a
			// newer attempt. Neither may commit this attempt's publication.
			if completed.State != rivertype.JobStateCompleted || completed.Attempt != job.Attempt {
				return fmt.Errorf("worker: index job ownership changed: state %s, attempt %d (want %d)",
					completed.State, completed.Attempt, job.Attempt)
			}
		}
		return nil
	})
	return total, pack.Mode, err
}

func indexMatches(g *store.Generation, emb *configfile.Embedding, ignoreHash string) bool {
	return g != nil && g.Model == emb.Model && g.Dims == emb.Dims && g.IgnoreHash == ignoreHash
}

// indexIgnoreHash treats ignore rules as a set; order and duplicates do not
// change which files the index covers. An empty stored hash requires a rebuild.
func indexIgnoreHash(ignore []string) string {
	patterns := slices.Clone(ignore)
	slices.Sort(patterns)
	patterns = slices.Compact(patterns)
	return fmt.Sprintf("%x", sha256.Sum256([]byte(fmt.Sprintf("%q", patterns))))
}

// embedText is what the embedder sees: the path and symbol give the
// vector a little of the file's identity, the way a reader would know
// where a snippet came from.
func embedText(c store.StagedChunk) string {
	head := c.Path
	if c.Symbol != "" {
		head += " " + c.Kind + " " + c.Symbol
	}
	return head + "\n" + c.Text
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
// change vector contents. The format version invalidates incompatible code
// changes to staging or embedText.
func indexResumeKey(commit, mode, base string, active *store.Generation, emb *configfile.Embedding, ignoreHash string) string {
	baseRun := ""
	if mode == store.IndexModeIncremental && active != nil {
		baseRun = active.ID
	}
	batch, batchChars, itemChars := emb.Bounds()
	identity := struct {
		Version                                              int
		Commit, Mode, Base, BaseRun, Ignore, Endpoint, Model string
		Dims, Batch, BatchChars, ItemChars                   int
	}{1, commit, mode, base, baseRun, ignoreHash, emb.BaseURL, emb.Model, emb.Dims, batch, batchChars, itemChars}
	data, _ := json.Marshal(identity) // Only strings and integers; encoding cannot fail.
	return fmt.Sprintf("%x", sha256.Sum256(data))
}
