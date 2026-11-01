//go:build integration

package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river/rivertest"

	"github.com/home-operations/kritika/internal/adapter"
	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/jobs"
	"github.com/home-operations/kritika/internal/model"
	"github.com/home-operations/kritika/internal/store"
	"github.com/home-operations/kritika/internal/store/storetest"
)

func TestIndexStageFencing(t *testing.T) {
	for _, scenario := range []string{"git token failure", "stale attempt", "discarded run", "shutdown after staging"} {
		t.Run(scenario, func(t *testing.T) {
			env := setupIndexResume(t, scenario)
			ctx := t.Context()
			job := env.claim(1)
			workCtx, cancel := context.WithCancel(rivertest.WorkContext(ctx, env.queue))
			defer cancel()
			f := &resumeForge{tip: "head"}
			exec := &resumeExecutor{st: env.st, account: env.account.ID()}
			current := configfile.NewCurrent(env.file)
			embed := &resumeFault{current: current, calls: map[string]int{}}
			w := &Index{Store: env.st, Current: current, Forges: &forges{f: f}, Logger: slog.New(slog.DiscardHandler),
				Executor: exec, Embedders: &adapter.Embedders{Build: func(configfile.Embedding) model.Embedder { return embed }}, batch: 1}
			f.gitToken = func() error {
				switch scenario {
				case "git token failure":
					return errors.New("token unavailable")
				case "stale attempt":
					_, err := env.st.App().Exec(ctx, `UPDATE river_job SET attempt=2 WHERE id=$1`, job.ID)
					return err
				case "discarded run":
					return env.st.WithAccount(ctx, env.account.ID(), func(tx pgx.Tx) error {
						var run, runnerID string
						if err := tx.QueryRow(ctx, `SELECT x.id::text,r.id::text FROM index_runs x JOIN runner_runs r ON r.index_run_id=x.id WHERE r.river_job_id=$1`, job.ID).Scan(&run, &runnerID); err != nil {
							return err
						}
						return store.DiscardIndexProgress(ctx, tx, run, runnerID)
					})
				}
				return nil
			}
			if scenario == "shutdown after staging" {
				exec.after = cancel
			}
			if err := w.Work(workCtx, job); err == nil {
				t.Fatal("expected interrupted attempt")
			}
			var ready, credential, finished bool
			var phase string
			if err := env.st.WithAccount(ctx, env.account.ID(), func(tx pgx.Tx) error {
				return tx.QueryRow(ctx, `SELECT x.embedding_ready,r.access_token_hash IS NOT NULL,r.finished_at IS NOT NULL,r.phase
     FROM index_runs x JOIN runner_runs r ON r.index_run_id=x.id WHERE r.river_job_id=$1`, job.ID).Scan(&ready, &credential, &finished, &phase)
			}); err != nil {
				t.Fatal(err)
			}
			wantStages := 0
			if scenario == "shutdown after staging" {
				wantStages = 1
			}
			if exec.calls != wantStages || credential || !finished || ready != (wantStages == 1) {
				t.Fatalf("stages=%d credential=%v finished=%v ready=%v phase=%s", exec.calls, credential, finished, ready, phase)
			}
			if wantStages == 0 && phase != "failed" {
				t.Fatalf("phase=%s", phase)
			}
			if wantStages == 1 {
				exec.after = nil
				if err := w.Work(rivertest.WorkContext(ctx, env.queue), env.claim(2)); err != nil {
					t.Fatal(err)
				}
				if exec.calls != 1 {
					t.Fatal("completed staging reran")
				}
			}
		})
	}
}

func TestIndexPublicationLockOrder(t *testing.T) {
	env := setupIndexResume(t, "publication lock order")
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	job := env.claim(1)
	held := make(chan struct{})
	release := make(chan struct{})
	ingestion := make(chan error, 1)
	go func() {
		ingestion <- env.st.WithAccount(ctx, env.account.ID(), func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, `SELECT id FROM repositories WHERE id=$1 FOR UPDATE`, env.repo); err != nil {
				return err
			}
			close(held)
			select {
			case <-release:
			case <-ctx.Done():
				return ctx.Err()
			}
			// River's uniqueness conflict locks the already-running index job.
			_, err := env.queue.InsertTx(ctx, tx, job.Args, nil)
			return err
		})
	}()
	select {
	case <-held:
	case err := <-ingestion:
		t.Fatal(err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	publication := make(chan error, 1)
	pid := make(chan uint32, 1)
	go func() {
		publication <- env.st.WithAccount(ctx, env.account.ID(), func(tx pgx.Tx) error {
			pid <- tx.Conn().PgConn().PID()
			return store.LockIndexBuild(ctx, tx, env.account.ID(), env.repo, job.ID, job.Attempt)
		})
	}()
	workerPID := <-pid
	// Observe the actual lock wait before allowing ingestion to insert the job.
	waitFor(t, 5*time.Second, "publication waiting on the repository", func() bool {
		var blocked bool
		if err := env.st.App().QueryRow(ctx, `SELECT cardinality(pg_blocking_pids($1)) > 0`, workerPID).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		return blocked
	})
	close(release)
	if err := <-ingestion; err != nil {
		t.Fatalf("ingestion: %v", err)
	}
	if err := <-publication; err != nil {
		t.Fatalf("publication: %v", err)
	}
}

func TestIndexCancelledJobRetry(t *testing.T) {
	env := setupIndexResume(t, "cancelled retry")
	ctx := t.Context()
	job := env.claim(1)
	// Cancel a waiting job so River can finalize it without a worker.
	if _, err := env.st.App().Exec(ctx, `UPDATE river_job SET state='retryable' WHERE id=$1`, job.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := env.queue.JobCancel(ctx, job.ID); err != nil {
		t.Fatal(err)
	}
	// River clears the earlier attempt's cancellation request on an
	// explicit retry (riverqueue/river#1564), so the retried attempt runs.
	retried, err := env.queue.JobRetry(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(retried.Metadata), "cancel_attempted_at") {
		t.Fatalf("River retry kept the cancellation request: %s", retried.Metadata)
	}
	job = env.claim(2)
	if err := env.st.WithAccount(ctx, env.account.ID(), func(tx pgx.Tx) error { return store.LockIndexJob(ctx, tx, env.account.ID(), job.ID, job.Attempt) }); err != nil {
		t.Fatalf("retried attempt refused: %v", err)
	}
	// A cancellation of the running attempt fences it as before.
	if _, err := env.queue.JobCancel(ctx, job.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := env.st.App().Exec(ctx, `UPDATE river_job SET state='cancelled',finalized_at=now() WHERE id=$1`, job.ID); err != nil {
		t.Fatal(err)
	}
	if err := env.st.WithAccount(ctx, env.account.ID(), func(tx pgx.Tx) error {
		id, err := jobs.EnqueueReindex(ctx, tx, env.queue, env.account.ID(), env.repo)
		if err != nil {
			return err
		}
		if id == job.ID {
			return errors.New("reindex reused cancelled job")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestIndexProgressSweepContinues(t *testing.T) {
	env := setupIndexResume(t, "sweep continues")
	ctx := t.Context()
	job := env.claim(1)
	owner, err := pgx.Connect(ctx, storetest.Env(t, "KRITIKA_TEST_OWNER_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = owner.Close(context.Background()) }()
	var runs []string
	for range 2 {
		if err := env.st.WithAccount(ctx, env.account.ID(), func(tx pgx.Tx) error {
			run, runnerID, err := store.StartIndexRun(ctx, tx, store.NewIndexRun{AccountID: env.account.ID(), RepositoryID: env.repo, Commit: "head", Mode: store.IndexModeFull, Embedding: *env.file.Embedding, ResumeKey: "key", JobID: job.ID})
			if err != nil {
				return err
			}
			runs = append(runs, run)
			return store.FailRunnerRun(ctx, tx, runnerID, "fixture finished")
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := env.st.App().Exec(ctx, `UPDATE river_job SET state='discarded',finalized_at=now() WHERE id=$1`, job.ID); err != nil {
		t.Fatal(err)
	}
	// The identifier is a database-generated UUID, not user input.
	if _, err := owner.Exec(ctx, fmt.Sprintf(`ALTER TABLE index_runs ADD CONSTRAINT test_block_discard CHECK(id <> '%s'::uuid OR resume_key <> '') NOT VALID`, runs[0])); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := owner.Exec(context.Background(), `ALTER TABLE index_runs DROP CONSTRAINT test_block_discard`); err != nil {
			t.Error(err)
		}
	}()
	if err := env.st.SweepIndexProgress(ctx, 100); err == nil {
		t.Fatal("expected first cleanup to fail")
	}
	if err := env.st.WithAccount(ctx, env.account.ID(), func(tx pgx.Tx) error {
		var key, reason string
		if err := tx.QueryRow(ctx, `SELECT resume_key,error FROM index_runs WHERE id=$1`, runs[1]).Scan(&key, &reason); err != nil {
			return err
		}
		if key != "" || reason == "" {
			t.Fatalf("second candidate key=%q reason=%q", key, reason)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestIndexTerminalPublicationRollsBack(t *testing.T) {
	env := setupIndexResume(t, "terminal publication")
	ctx := t.Context()
	job := env.claim(1)
	var run string
	if err := env.st.WithAccount(ctx, env.account.ID(), func(tx pgx.Tx) error {
		var runnerID string
		var err error
		run, runnerID, err = store.StartIndexRun(ctx, tx, store.NewIndexRun{AccountID: env.account.ID(), RepositoryID: env.repo, Commit: "head", Mode: store.IndexModeFull, Embedding: *env.file.Embedding, ResumeKey: "key", JobID: job.ID})
		if err != nil {
			return err
		}
		if err := store.FailRunnerRun(ctx, tx, runnerID, "fixture finished"); err != nil {
			return err
		}
		return store.FinishIndexRun(ctx, tx, run, store.IndexFailed, 0, "ended")
	}); err != nil {
		t.Fatal(err)
	}
	err := env.st.WithAccount(ctx, env.account.ID(), func(tx pgx.Tx) error {
		if err := store.LockIndexBuild(ctx, tx, env.account.ID(), env.repo, job.ID, job.Attempt); err != nil {
			return err
		}
		if err := store.ActivateGeneration(ctx, tx, env.repo, run); err != nil {
			return err
		}
		return store.FinishIndexRun(ctx, tx, run, store.IndexCompleted, 0, "")
	})
	if err == nil || !strings.Contains(err.Error(), "no longer running") {
		t.Fatalf("publication error=%v", err)
	}
	if err := env.st.WithAccount(ctx, env.account.ID(), func(tx pgx.Tx) error {
		repo, err := store.FindIndexRepo(ctx, tx, env.repo)
		if err == nil && repo.ActiveRun != "" {
			t.Fatal("terminal run was published")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}
