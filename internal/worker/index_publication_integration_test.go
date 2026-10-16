//go:build integration

package worker

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivertest"
	"github.com/riverqueue/river/rivertype"

	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/configfile/configfiletest"
	"github.com/home-operations/kritika/internal/jobs"
	"github.com/home-operations/kritika/internal/store"
	"github.com/home-operations/kritika/internal/store/storetest"
)

func TestIndexPublicationCompletesForcedJob(t *testing.T) {
	st := storetest.Open(t)
	t.Setenv("TEST_PEM", "pem")
	t.Setenv("TEST_SECRET", "test-provider-key")
	file := configfiletest.Load(t, configYAML)
	if err := st.ApplyConfig(t.Context(), file); err != nil {
		t.Fatal(err)
	}
	if _, err := st.EnsureIndexSchema(t.Context(), "kritika_app", "fake-embed", 8); err != nil {
		t.Fatal(err)
	}
	account, _ := file.Account(configfile.ForgeGitHub, "onedr0p")
	queue, err := river.NewClient(riverpgxv5.New(st.App()), &river.Config{})
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name                      string
		full, rejectCompletion    bool
		stateBefore               rivertype.JobState
		attemptBefore             int
		wantError                 string
		wantState                 rivertype.JobState
		wantCatchUp               int
		wantPrevious              bool
		wantStatus                string
		wantStaged, wantOldChunks int
		wantRescue                bool
	}{
		{
			name: "forced publication survives missing acknowledgement", full: true,
			stateBefore: rivertype.JobStateRunning, attemptBefore: 1,
			wantState: rivertype.JobStateCompleted, wantCatchUp: 1, wantStatus: "completed",
		},
		{
			name: "completion failure rolls publication back", full: true, rejectCompletion: true,
			stateBefore: rivertype.JobStateRunning, attemptBefore: 1, wantError: "test_reject_index_completion",
			wantState: rivertype.JobStateRunning, wantPrevious: true, wantStatus: "running",
			wantStaged: 1, wantOldChunks: 1, wantRescue: true,
		},
		{
			name:        "ordinary publication leaves job running",
			stateBefore: rivertype.JobStateRunning, attemptBefore: 1,
			wantState: rivertype.JobStateRunning, wantStatus: "completed", wantRescue: true,
		},
		{
			name: "rescued job cannot publish", full: true,
			stateBefore: rivertype.JobStateRetryable, attemptBefore: 1, wantError: "index job ownership changed",
			wantState: rivertype.JobStateRetryable, wantPrevious: true, wantStatus: "running",
			wantStaged: 1, wantOldChunks: 1,
		},
		{
			name: "older attempt cannot complete newer attempt", full: true,
			stateBefore: rivertype.JobStateRunning, attemptBefore: 2, wantError: "index job ownership changed",
			wantState: rivertype.JobStateRunning, wantPrevious: true, wantStatus: "running",
			wantStaged: 1, wantOldChunks: 1,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx := rivertest.WorkContext(t.Context(), queue)
			job, run, runner, previous := prepareIndexPublication(t, st, queue, file, account, tt.full)
			if tt.rejectCompletion {
				rejectIndexCompletion(t, job.ID)
			}
			// Keep the worker's snapshot at attempt 1 while rescue or a new
			// claim changes the database row before publication.
			if _, err := st.App().Exec(ctx, `UPDATE river_job SET state = $2, attempt = $3 WHERE id = $1`,
				job.ID, tt.stateBefore, tt.attemptBefore); err != nil {
				t.Fatal(err)
			}
			base := Base{Store: st, Logger: slog.New(slog.DiscardHandler)}
			w := Index{Base: base}
			embed := indexEmbedFunc(func(context.Context, []string) ([][]float32, int64, error) {
				return [][]float32{{1, 0, 0, 0, 0, 0, 0, 0}}, 0, nil
			})
			// No River worker acknowledges this attempt after publication, as
			// when the process exits before its asynchronous completion lands.
			_, _, err := w.embed(ctx, job, account, embed, file.Embedding.Model, "new", run, runner, nil, file.Settings(account, "", configfile.RepoTraits{}))
			if tt.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantError) {
					t.Fatalf("embed = %v, want error containing %q", err, tt.wantError)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			wantActive := run
			if tt.wantPrevious {
				wantActive = previous
			}
			row, err := queue.JobGet(ctx, job.ID)
			if err != nil {
				t.Fatal(err)
			}
			if row.State != tt.wantState || row.Attempt != tt.attemptBefore {
				t.Errorf("job = %s/%d, want %s/%d before acknowledgement", row.State, row.Attempt, tt.wantState, tt.attemptBefore)
			}
			if (row.FinalizedAt != nil) != (tt.wantState == rivertype.JobStateCompleted) {
				t.Errorf("job finalized_at = %v, want finalized only on completion", row.FinalizedAt)
			}
			if err := st.WithAccount(ctx, account.ID(), func(tx pgx.Tx) error {
				var active, status string
				var staged, oldChunks, catchUp int
				if err := tx.QueryRow(ctx, `SELECT r.active_index_run_id::text, i.status,
					(SELECT count(*) FROM index_staging WHERE runner_run_id = $3),
					(SELECT count(*) FROM index_chunks WHERE index_run_id = $4),
					(SELECT count(*) FROM river_job WHERE kind = 'index' AND args->>'catch_up' = $2::text)
					FROM repositories r JOIN index_runs i ON i.id = $2::uuid WHERE r.id = $1`,
					job.Args.RepositoryID, run, runner, previous).Scan(&active, &status, &staged, &oldChunks, &catchUp); err != nil {
					return err
				}
				if active != wantActive || status != tt.wantStatus || staged != tt.wantStaged || oldChunks != tt.wantOldChunks || catchUp != tt.wantCatchUp {
					t.Errorf("publication = %s/%s/staged:%d/old:%d/catch-up:%d, want %s/%s/%d/%d/%d",
						active, status, staged, oldChunks, catchUp, wantActive, tt.wantStatus, tt.wantStaged, tt.wantOldChunks, tt.wantCatchUp)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			rescued, err := st.RescueJob(ctx, store.AbandonedJob{ID: job.ID, Kind: job.Kind, Attempt: job.Attempt, MaxAttempts: job.MaxAttempts}, time.Minute, "worker died")
			if err != nil || rescued != tt.wantRescue {
				t.Fatalf("rescue = %v, %v, want %v", rescued, err, tt.wantRescue)
			}
		})
	}
}

func prepareIndexPublication(
	t *testing.T, st *store.Store, queue *river.Client[pgx.Tx], file *configfile.File, account *configfile.Account, full bool,
) (job *river.Job[jobs.IndexArgs], run, runner, previous string) {
	t.Helper()
	ctx := t.Context()
	name := "publication/" + t.Name()
	repo := configfile.RepositoryID(account.ID(), name)
	args := jobs.IndexArgs{AccountID: account.ID(), RepositoryID: repo, Trigger: jobs.TriggerPush, Full: full}
	if full {
		args.Trigger = jobs.TriggerReindex
	}
	inserted, err := queue.Insert(ctx, args, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := st.WithAccount(cleanup, account.ID(), func(tx pgx.Tx) error {
			if runner != "" {
				for _, stmt := range []string{
					`DELETE FROM index_staging WHERE runner_run_id = $1`,
					`DELETE FROM index_packs WHERE runner_run_id = $1`,
					`DELETE FROM usage WHERE runner_run_id = $1`,
					`DELETE FROM runner_runs WHERE id = $1`,
				} {
					if _, err := tx.Exec(cleanup, stmt, runner); err != nil {
						return err
					}
				}
			}
			for _, stmt := range []string{
				`UPDATE repositories SET active_index_run_id = NULL WHERE id = $1`,
				`DELETE FROM index_runs WHERE repository_id = $1`,
				`DELETE FROM repositories WHERE id = $1`,
				`DELETE FROM river_job WHERE args->>'repository_id' = $1`,
			} {
				if _, err := tx.Exec(cleanup, stmt, repo); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			t.Error(err)
		}
	})
	if _, err := st.App().Exec(ctx, `UPDATE river_job SET state = 'running', attempt = 1, attempted_at = now() - interval '10 minutes' WHERE id = $1`, inserted.Job.ID); err != nil {
		t.Fatal(err)
	}
	row, err := queue.JobGet(ctx, inserted.Job.ID)
	if err != nil {
		t.Fatal(err)
	}
	job = &river.Job[jobs.IndexArgs]{JobRow: row, Args: args}
	if err := st.WithAccount(ctx, account.ID(), func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO repositories(id,account_id,name,managed_by) VALUES($1,$2,$3,'file')`, repo, account.ID(), name); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO index_runs(account_id,repository_id,commit_sha,embed_model,embed_dims,mode,status)
			VALUES($1,$2,'old',$3,8,'full','completed') RETURNING id`, account.ID(), repo, file.Embedding.Model).Scan(&previous); err != nil {
			return err
		}
		if err := store.InsertIndexChunks(ctx, tx, account.ID(), repo, previous,
			[]store.StagedChunk{{Path: "old.go", StartLine: 1, EndLine: 1, Text: "package old"}}, [][]float32{{1, 0, 0, 0, 0, 0, 0, 0}}); err != nil {
			return err
		}
		if err := store.ActivateGeneration(ctx, tx, repo, previous); err != nil {
			return err
		}
		var err error
		run, runner, err = store.StartIndexRun(ctx, tx, store.NewIndexRun{
			AccountID: account.ID(), RepositoryID: repo, Commit: "new", Mode: store.IndexModeFull, Trigger: args.Trigger,
			Embedding: *file.Embedding, JobID: job.ID, StrayAfter: time.Hour,
		})
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO index_packs(runner_run_id,account_id,mode,chunk_count) VALUES($1,$2,'full',1)`, runner, account.ID()); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO index_staging(runner_run_id,account_id,path,start_line,end_line,text) VALUES($1,$2,'new.go',1,1,'package new')`, runner, account.ID())
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return job, run, runner, previous
}

func rejectIndexCompletion(t *testing.T, jobID int64) {
	t.Helper()
	owner, err := pgx.Connect(t.Context(), storetest.Env(t, "KRITIKA_TEST_OWNER_URL"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := owner.Exec(ctx, `ALTER TABLE river_job DROP CONSTRAINT IF EXISTS test_reject_index_completion`); err != nil {
			t.Error(err)
		}
		if err := owner.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	if _, err := owner.Exec(t.Context(), fmt.Sprintf(`ALTER TABLE river_job ADD CONSTRAINT test_reject_index_completion
		CHECK (id <> %d OR state <> 'completed') NOT VALID`, jobID)); err != nil {
		t.Fatal(err)
	}
}
