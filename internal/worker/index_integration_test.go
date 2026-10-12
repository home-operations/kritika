//go:build integration

package worker

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"github.com/home-operations/kritika/internal/jobs"
	"github.com/home-operations/kritika/internal/store"
	"github.com/home-operations/kritika/internal/store/storetest"
)

// checkRebuildOverUpdate runs a forced rebuild while an update advances the
// generation the rebuild started from: the rebuild still publishes, and
// queues an update to the tip the other one reached.
func checkRebuildOverUpdate(
	ctx context.Context, t *testing.T, st *store.Store, queue *river.Client[pgx.Tx], lf *localForge, exec *gateExecutor,
	account, repository, head, newer string,
) {
	t.Helper()
	waitFor(t, 20*time.Second, "previous index jobs to finish", func() bool {
		var live int
		if err := st.App().QueryRow(ctx, `SELECT count(*) FROM river_job WHERE kind = 'index' AND args->>'repository_id' = $1
    AND state IN ('available', 'pending', 'retryable', 'running', 'scheduled')`, repository).Scan(&live); err != nil {
			t.Fatal(err)
		}
		return live == 0
	})
	var once atomic.Bool
	var running atomic.Int64
	var advanced, rebuild string
	exec.setBeforeIndex(func() error {
		if once.Swap(true) {
			return nil
		}
		lf.setTip(newer)
		return st.WithAccount(ctx, account, func(tx pgx.Tx) error {
			push, err := queue.InsertTx(ctx, tx, jobs.IndexArgs{
				AccountID: account, RepositoryID: repository, CommitSHA: newer, Trigger: jobs.TriggerPush,
			}, nil)
			if err != nil {
				return err
			}
			running.Store(push.Job.ID)
			// Another replica has published its update but has yet to return
			// from Work, so River still includes it in the live unique key.
			if _, err := tx.Exec(ctx, `UPDATE river_job SET state = 'running', attempted_at = now(), attempt = 1 WHERE id = $1`, push.Job.ID); err != nil {
				return err
			}
			repo, err := store.FindIndexRepo(ctx, tx, repository)
			if err != nil {
				return err
			}
			active, err := store.FindGeneration(ctx, tx, repo.ActiveRun)
			if err != nil {
				return err
			}
			advanced = active.ID
			if err := tx.QueryRow(ctx, `SELECT id::text FROM index_runs WHERE repository_id = $1 AND status = 'running' ORDER BY created_at DESC LIMIT 1`, repository).Scan(&rebuild); err != nil {
				return err
			}
			var step string
			if err := tx.QueryRow(ctx, `INSERT INTO index_runs (account_id, repository_id, commit_sha, embed_model, embed_dims, mode, status)
    VALUES ($1, $2, $3, $4, $5, 'incremental', 'running') RETURNING id`, account, repository, newer, active.Model, active.Dims).Scan(&step); err != nil {
				return err
			}
			batch := []store.StagedChunk{{Path: "main.go", Language: "go", StartLine: 1, EndLine: 3, Text: "package main\n\nfunc newer() {}"}}
			if err := store.InsertIndexChunks(ctx, tx, account, repository, step, batch, [][]float32{{1, 0, 0, 0, 0, 0, 0, 0}}); err != nil {
				return err
			}
			if err := store.AdvanceGeneration(ctx, tx, repository, active, step, newer, []string{"main.go"}); err != nil {
				return err
			}
			return store.FinishIndexRun(ctx, tx, step, store.IndexCompleted, 1, "")
		})
	})
	t.Cleanup(func() {
		exec.setBeforeIndex(nil)
		lf.setTip(head)
		if _, err := st.App().Exec(context.Background(), `DELETE FROM river_job WHERE id = $1`, running.Load()); err != nil {
			t.Error(err)
		}
	})
	job, err := queue.Insert(ctx, jobs.IndexArgs{AccountID: account, RepositoryID: repository, Trigger: jobs.TriggerReindex, Full: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if attempt, snoozes := indexJobDone(ctx, t, st, job.Job.ID); attempt != 1 || snoozes != 0 {
		t.Fatalf("rebuild attempt=%d snoozes=%d, want 1 and 0", attempt, snoozes)
	}
	catchUp := indexCatchUpDone(ctx, t, st, rebuild)
	var state string
	if err := st.App().QueryRow(ctx, `SELECT state FROM river_job WHERE id = $1`, running.Load()).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "running" || catchUp == running.Load() {
		t.Fatalf("catch-up job=%d, push job=%d state=%s, want distinct jobs with the push still running", catchUp, running.Load(), state)
	}
	if _, err := st.App().Exec(ctx, `UPDATE river_job SET state = 'completed', finalized_at = now() WHERE id = $1`, running.Load()); err != nil {
		t.Fatal(err)
	}
	checkIndexAt(ctx, t, st, account, repository, job.Job.CreatedAt, rebuild, newer, "func newer()")
	checkRunStatus(ctx, t, st, account, advanced, store.IndexSuperseded)
}

func checkRebuildQueueFailure(
	ctx context.Context, t *testing.T, st *store.Store, queue *river.Client[pgx.Tx], lf *localForge, exec *gateExecutor,
	account, repository, head, newer string,
) {
	t.Helper()
	active := activeIndexGeneration(ctx, t, st, account, repository)
	live := runChunks(ctx, t, st, account, active)
	exec.setBeforeIndex(func() error { lf.setTip(newer); return nil })
	t.Cleanup(func() { exec.setBeforeIndex(nil); lf.setTip(head) })
	owner, err := pgx.Connect(ctx, storetest.Env(t, "KRITIKA_TEST_OWNER_URL"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := owner.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	// Reject only catch-up inserts; the rebuild job and its error updates
	// must still reach River normally. Existing completed jobs are exempt.
	if _, err := owner.Exec(ctx, `ALTER TABLE river_job ADD CONSTRAINT test_reject_index_catch_up
    CHECK (kind <> 'index' OR args->>'trigger' <> 'push') NOT VALID`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := owner.Exec(context.Background(), `ALTER TABLE river_job DROP CONSTRAINT IF EXISTS test_reject_index_catch_up`); err != nil {
			t.Error(err)
		}
	})
	job, err := queue.Insert(ctx, jobs.IndexArgs{AccountID: account, RepositoryID: repository, Trigger: jobs.TriggerReindex, Full: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := queue.JobCancel(context.Background(), job.Job.ID); err != nil {
			t.Error(err)
		}
	})
	var attempt int
	waitFor(t, 20*time.Second, "the rebuild to fail when catch-up cannot be queued", func() bool {
		var state string
		var reason string
		if err := st.App().QueryRow(ctx, `SELECT state, attempt, coalesce(errors[array_length(errors, 1)]->>'error', '')
    FROM river_job WHERE id = $1`, job.Job.ID).Scan(&state, &attempt, &reason); err != nil {
			t.Fatal(err)
		}
		if state == "completed" || state == "discarded" {
			t.Fatalf("rebuild state=%s, want retryable after catch-up insertion failed: %s", state, reason)
		}
		return state == "retryable" && strings.Contains(reason, "worker: queue index catch-up")
	})
	if now := activeIndexGeneration(ctx, t, st, account, repository); now != active || runChunks(ctx, t, st, account, active) != live {
		t.Fatalf("queue failure changed the live index: generation %s, was %s", now, active)
	}
	checkRunStatus(ctx, t, st, account, active, store.IndexCompleted)
	var queued int
	if err := st.App().QueryRow(ctx, `SELECT count(*) FROM river_job WHERE id > $1 AND args->>'repository_id' = $2
    AND kind = 'index' AND args->>'trigger' = 'push'`, job.Job.ID, repository).Scan(&queued); err != nil || queued != 0 {
		t.Fatalf("catch-up jobs after rollback = %d, %v", queued, err)
	}
	if _, err := owner.Exec(ctx, `ALTER TABLE river_job DROP CONSTRAINT test_reject_index_catch_up`); err != nil {
		t.Fatal(err)
	}
	if _, err := queue.JobRetry(ctx, job.Job.ID); err != nil {
		t.Fatal(err)
	}
	if got, snoozes := indexJobDone(ctx, t, st, job.Job.ID); got != attempt+1 || snoozes != 0 {
		t.Fatalf("rebuild attempt=%d snoozes=%d, want %d and 0", got, snoozes, attempt+1)
	}
	rebuilt := activeIndexGeneration(ctx, t, st, account, repository)
	if rebuilt == active {
		t.Fatal("the recovered rebuild did not replace the generation")
	}
	catchUp := indexCatchUpDone(ctx, t, st, rebuilt)
	if err := st.WithAccount(ctx, account, func(tx pgx.Tx) error {
		var commit string
		if err := tx.QueryRow(ctx, `SELECT commit_sha FROM index_runs WHERE id = $1`, rebuilt).Scan(&commit); err != nil {
			return err
		}
		var runs int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM runner_runs WHERE river_job_id = $1`, catchUp).Scan(&runs); err != nil {
			return err
		}
		if commit != newer || runs != 0 {
			t.Fatalf("rebuild commit=%s, catch-up runner runs=%d, want %s and no work for an unchanged tip", commit, runs, newer)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func indexCatchUpDone(ctx context.Context, t *testing.T, st *store.Store, rebuild string) int64 {
	t.Helper()
	var id int64
	if err := st.App().QueryRow(ctx, `SELECT id FROM river_job WHERE kind = 'index' AND args->>'catch_up' = $1`, rebuild).Scan(&id); err != nil {
		t.Fatalf("catch-up for rebuild %s: %v", rebuild, err)
	}
	if attempt, snoozes := indexJobDone(ctx, t, st, id); attempt != 1 || snoozes != 0 {
		t.Fatalf("catch-up attempt=%d snoozes=%d, want 1 and 0", attempt, snoozes)
	}
	return id
}

// checkUpdateOverRebuild runs an update while a forced rebuild replaces the
// generation the update started from: the update is refused, ends
// superseded, and snoozes to build again on the rebuilt generation.
func checkUpdateOverRebuild(
	ctx context.Context, t *testing.T, st *store.Store, queue *river.Client[pgx.Tx], exec *gateExecutor, account, repository, head string,
) {
	t.Helper()
	var once atomic.Bool
	var rebuilt, displaced string
	exec.setBeforeIndex(func() error {
		if once.Swap(true) {
			return nil
		}
		return st.WithAccount(ctx, account, func(tx pgx.Tx) error {
			repo, err := store.FindIndexRepo(ctx, tx, repository)
			if err != nil {
				return err
			}
			active, err := store.FindGeneration(ctx, tx, repo.ActiveRun)
			if err != nil {
				return err
			}
			if err := tx.QueryRow(ctx, `SELECT id::text FROM index_runs WHERE repository_id = $1 AND status = 'running' ORDER BY created_at DESC LIMIT 1`, repository).Scan(&displaced); err != nil {
				return err
			}
			if err := tx.QueryRow(ctx, `INSERT INTO index_runs (account_id, repository_id, commit_sha, embed_model, embed_dims, mode, status, ignore_hash)
    VALUES ($1, $2, $3, $4, $5, 'full', 'running', $6) RETURNING id`, account, repository, active.Commit, active.Model, active.Dims, active.IgnoreHash).Scan(&rebuilt); err != nil {
				return err
			}
			// The rebuild covers the whole tree, as a full build would; only
			// its main.go is told apart.
			if _, err := tx.Exec(ctx, `INSERT INTO index_chunks
    (account_id, repository_id, index_run_id, path, start_line, end_line, language, symbol, kind, scope, text, embedding)
    SELECT account_id, repository_id, $2, path, start_line, end_line, language, symbol, kind, scope, text, embedding
    FROM index_chunks WHERE index_run_id = $1 AND path <> 'main.go'`, active.ID, rebuilt); err != nil {
				return err
			}
			batch := []store.StagedChunk{{Path: "main.go", Language: "go", StartLine: 1, EndLine: 3, Text: "package main\n\nfunc rebuilt() {}"}}
			if err := store.InsertIndexChunks(ctx, tx, account, repository, rebuilt, batch, [][]float32{{1, 0, 0, 0, 0, 0, 0, 0}}); err != nil {
				return err
			}
			if err := store.ActivateGeneration(ctx, tx, repository, rebuilt); err != nil {
				return err
			}
			return store.FinishIndexRun(ctx, tx, rebuilt, store.IndexCompleted, 1, "")
		})
	})
	t.Cleanup(func() { exec.setBeforeIndex(nil) })
	job, err := queue.Insert(ctx, jobs.IndexArgs{AccountID: account, RepositoryID: repository, CommitSHA: head, Trigger: jobs.TriggerPush}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if attempt, snoozes := indexJobDone(ctx, t, st, job.Job.ID); attempt != 1 || snoozes != 1 {
		t.Fatalf("update attempt=%d snoozes=%d, want 1 and 1", attempt, snoozes)
	}
	if text := checkIndexAt(ctx, t, st, account, repository, job.Job.CreatedAt, rebuilt, head, "func b()"); strings.Contains(text, "func rebuilt()") {
		t.Fatalf("the update kept the rebuilt main.go: %q", text)
	}
	checkRunStatus(ctx, t, st, account, displaced, store.IndexSuperseded)
}

// indexJobDone waits for an index job to complete and returns its attempt
// and snooze counts.
func indexJobDone(ctx context.Context, t *testing.T, st *store.Store, id int64) (attempt, snoozes int) {
	t.Helper()
	waitFor(t, 30*time.Second, fmt.Sprintf("index job %d to complete", id), func() bool {
		var state, errs string
		if err := st.App().QueryRow(ctx, `SELECT state, attempt, coalesce((metadata->>'snoozes')::int, 0), coalesce(array_to_json(errors)::text, '[]')
    FROM river_job WHERE id = $1`, id).Scan(&state, &attempt, &snoozes, &errs); err != nil {
			t.Fatal(err)
		}
		if state == "cancelled" || state == "discarded" {
			t.Fatalf("index job %d ended %s after attempt %d: %s", id, state, attempt, errs)
		}
		return state == "completed"
	})
	return attempt, snoozes
}

// checkIndexAt checks that run is the active generation at commit with
// want in main.go, that no chunk is staged or outside it, and that two
// embedding passes recorded their usage after since. It returns main.go's
// text.
func checkIndexAt(
	ctx context.Context, t *testing.T, st *store.Store, account, repository string, since time.Time, run, commit, want string,
) string {
	t.Helper()
	var text string
	if err := st.WithAccount(ctx, account, func(tx pgx.Tx) error {
		var active, got string
		if err := tx.QueryRow(ctx, `SELECT g.id::text, g.commit_sha, string_agg(c.text, '') FROM repositories r
   JOIN index_runs g ON g.id = r.active_index_run_id JOIN index_chunks c ON c.index_run_id = g.id
   WHERE r.id = $1 AND c.path = 'main.go' GROUP BY g.id, g.commit_sha`, repository).Scan(&active, &got, &text); err != nil {
			return err
		}
		if active != run || got != commit || !strings.Contains(text, want) {
			t.Fatalf("active index = %s at %s with %q, want %s at %s with %q", active, got, text, run, commit, want)
		}
		var staging, unused int
		if err := tx.QueryRow(ctx, `SELECT (SELECT count(*) FROM index_staging), (SELECT count(*) FROM index_chunks c
   WHERE c.repository_id = $1 AND NOT EXISTS (SELECT 1 FROM repositories r WHERE r.active_index_run_id = c.index_run_id))`, repository).Scan(&staging, &unused); err != nil {
			return err
		}
		if staging != 0 || unused != 0 {
			t.Fatalf("staging=%d unused=%d", staging, unused)
		}
		var usage int
		if err := tx.QueryRow(ctx, `SELECT count(DISTINCT runner_run_id) FROM usage WHERE role = 'embedding' AND repository_id = $1 AND created_at >= $2`, repository, since).Scan(&usage); err != nil {
			return err
		}
		if usage != 2 {
			t.Fatalf("embedding runs with usage = %d, want both passes", usage)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return text
}

func checkRunStatus(ctx context.Context, t *testing.T, st *store.Store, account, run string, want store.IndexRunStatus) {
	t.Helper()
	var status string
	if err := st.WithAccount(ctx, account, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT status FROM index_runs WHERE id = $1`, run).Scan(&status)
	}); err != nil {
		t.Fatal(err)
	}
	if status != string(want) {
		t.Fatalf("run %s status = %s, want %s", run, status, want)
	}
}
