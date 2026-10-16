//go:build integration

package worker

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"

	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/configfile/configfiletest"
	"github.com/home-operations/kritika/internal/jobs"
	"github.com/home-operations/kritika/internal/store"
	"github.com/home-operations/kritika/internal/store/storetest"
)

type indexEmbedFunc func(context.Context, []string) ([][]float32, int64, error)

func (f indexEmbedFunc) Embed(ctx context.Context, inputs []string) ([][]float32, int64, error) {
	return f(ctx, inputs)
}

func TestIndexBatchUsage(t *testing.T) {
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
	tests := []struct {
		name      string
		tokens    int64
		rows      int
		wantError bool
	}{
		{"success", 20, 2, false},
		{"unreported usage", 0, 2, false},
		{"partial failure", 17, 2, true},
		{"cancellation", 17, 2, true},
		{"vector insert failure", 10, 1, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			repo := configfile.RepositoryID(account.ID(), "usage/"+tt.name)
			if err := st.WithAccount(ctx, account.ID(), func(tx pgx.Tx) error {
				_, err := tx.Exec(ctx, `INSERT INTO repositories(id,account_id,name,managed_by) VALUES($1,$2,$3,'file')`, repo, account.ID(), "usage/"+tt.name)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			args := jobs.IndexArgs{AccountID: account.ID(), RepositoryID: repo, Trigger: jobs.TriggerPush}
			job, err := queue.Insert(ctx, args, nil)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if _, err := st.App().Exec(context.Background(), `DELETE FROM river_job WHERE id=$1`, job.Job.ID); err != nil {
					t.Error(err)
				}
			})
			if _, err := st.App().Exec(ctx, `UPDATE river_job SET state = 'running', attempt = 1 WHERE id = $1`, job.Job.ID); err != nil {
				t.Fatal(err)
			}
			job.Job.State, job.Job.Attempt = "running", 1
			var run, runner string
			if err := st.WithAccount(ctx, account.ID(), func(tx pgx.Tx) error {
				var err error
				run, runner, err = store.StartIndexRun(ctx, tx, store.NewIndexRun{
					AccountID: account.ID(), RepositoryID: repo, Commit: "abc", Mode: store.IndexModeFull, Trigger: jobs.TriggerPush,
					Embedding: *file.Embedding, IgnoreHash: indexIgnoreHash(nil), JobID: job.Job.ID, StrayAfter: time.Hour,
				})
				if err != nil {
					return err
				}
				if _, err = tx.Exec(ctx, `INSERT INTO index_packs(runner_run_id,account_id,mode,chunk_count) VALUES($1,$2,'full',2)`, runner, account.ID()); err != nil {
					return err
				}
				_, err = tx.Exec(ctx, `INSERT INTO index_staging(runner_run_id,account_id,path,start_line,end_line,text)
     SELECT $1,$2,'main.go',n,n,'func f() {}' FROM generate_series(1,2) n`, runner, account.ID())
				return err
			}); err != nil {
				t.Fatal(err)
			}
			calls := 0
			embed := indexEmbedFunc(func(context.Context, []string) ([][]float32, int64, error) {
				calls++
				if tt.name == "unreported usage" {
					return [][]float32{{1, 0, 0, 0, 0, 0, 0, 0}}, 0, nil
				}
				if tt.name == "vector insert failure" {
					return nil, 10, nil
				}
				if calls == 2 {
					switch tt.name {
					case "partial failure":
						return nil, 7, errors.New("provider failed after a paid sub-batch")
					case "cancellation":
						cancel()
						return nil, 7, context.Canceled
					}
				}
				return [][]float32{{1, 0, 0, 0, 0, 0, 0, 0}}, 10, nil
			})
			base := Base{Store: st, Logger: slog.New(slog.DiscardHandler)}
			w := Index{Base: base, batch: 1}
			_, _, err = w.embed(ctx, &river.Job[jobs.IndexArgs]{JobRow: job.Job, Args: args}, account, embed,
				file.Embedding.Model, "abc", run, runner, nil, file.Settings(account, "", configfile.RepoTraits{}))
			if (err != nil) != tt.wantError {
				t.Fatalf("embed error = %v, want error %v", err, tt.wantError)
			}
			if tt.name == "cancellation" && !errors.Is(err, context.Canceled) {
				t.Fatalf("lost cancellation error: %v", err)
			}
			if err := st.WithAccount(t.Context(), account.ID(), func(tx pgx.Tx) error {
				var rows int
				var tokens int64
				if err := tx.QueryRow(t.Context(), `SELECT count(*),coalesce(sum(input_tokens),0) FROM usage WHERE runner_run_id=$1 AND role='embedding'`, runner).Scan(&rows, &tokens); err != nil {
					return err
				}
				if rows != tt.rows || tokens != tt.tokens {
					t.Errorf("usage rows=%d tokens=%d, want %d/%d", rows, tokens, tt.rows, tt.tokens)
				}
				return store.ClearIndexStaging(t.Context(), tx, run, runner)
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}
