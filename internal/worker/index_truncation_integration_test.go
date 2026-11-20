//go:build integration

package worker

import (
	"context"
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

// TestIndexTruncation: a full build that stopped at the indexer's budget is
// published as truncated, which the repository's index state and contents
// say. An incremental step on it keeps the generation truncated.
func TestIndexTruncation(t *testing.T) {
	ctx := t.Context()
	st := storetest.Open(t)
	t.Setenv("TEST_PEM", "pem")
	t.Setenv("TEST_SECRET", "test-provider-key")
	file := configfiletest.Load(t, configYAML)
	if err := st.ApplyConfig(ctx, file); err != nil {
		t.Fatal(err)
	}
	if _, err := st.EnsureIndexSchema(ctx, "kritika_app", "fake-embed", 8, ""); err != nil {
		t.Fatal(err)
	}
	account, _ := file.Account(configfile.ForgeGitHub, "onedr0p")
	repo := configfile.RepositoryID(account.ID(), "truncated/repo")
	if err := st.WithAccount(ctx, account.ID(), func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO repositories(id,account_id,name) VALUES($1,$2,'truncated/repo')`, repo, account.ID())
		return err
	}); err != nil {
		t.Fatal(err)
	}
	queue, err := river.NewClient(riverpgxv5.New(st.App()), &river.Config{})
	if err != nil {
		t.Fatal(err)
	}
	args := jobs.IndexArgs{AccountID: account.ID(), RepositoryID: repo, Trigger: jobs.TriggerPush}
	inserted, err := queue.Insert(ctx, args, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := st.App().Exec(context.Background(), `DELETE FROM river_job WHERE id=$1`, inserted.Job.ID); err != nil {
			t.Error(err)
		}
	})
	if _, err := st.App().Exec(ctx, `UPDATE river_job SET state = 'running', attempt = 1 WHERE id = $1`, inserted.Job.ID); err != nil {
		t.Fatal(err)
	}
	inserted.Job.State, inserted.Job.Attempt = "running", 1
	job := &river.Job[jobs.IndexArgs]{JobRow: inserted.Job, Args: args}
	embed := indexEmbedFunc(func(_ context.Context, inputs []string) ([][]float32, int64, error) {
		out := make([][]float32, len(inputs))
		for i := range out {
			out[i] = []float32{1, 0, 0, 0, 0, 0, 0, 0}
		}
		return out, 1, nil
	})
	w := Index{Store: st, Logger: slog.New(slog.DiscardHandler)}
	// build embeds a pack the runner wrote, as mode, at commit.
	build := func(commit, mode string, truncated bool, active *store.Generation) store.IndexPack {
		t.Helper()
		var run, runner string
		if err := st.WithAccount(ctx, account.ID(), func(tx pgx.Tx) error {
			var err error
			run, runner, err = store.StartIndexRun(ctx, tx, store.NewIndexRun{
				AccountID: account.ID(), RepositoryID: repo, Commit: commit, Mode: mode, Trigger: jobs.TriggerPush,
				Embedding: *file.Embedding, IgnoreHash: indexIgnoreHash(nil), JobID: job.ID, StrayAfter: time.Hour,
			})
			if err != nil {
				return err
			}
			if _, err = tx.Exec(ctx, `INSERT INTO index_packs(runner_run_id,account_id,mode,changed_paths,chunk_count,truncated)
				VALUES($1,$2,$3,'{main.go}',1,$4)`, runner, account.ID(), mode, truncated); err != nil {
				return err
			}
			_, err = tx.Exec(ctx, `INSERT INTO index_staging(runner_run_id,account_id,path,start_line,end_line,text)
				VALUES($1,$2,'main.go',1,1,$3)`, runner, account.ID(), "func at"+commit+"() {}")
			return err
		}); err != nil {
			t.Fatal(err)
		}
		_, pack, err := w.embed(ctx, job, account, embed, file.Embedding, commit, run, runner, active, 1)
		if err != nil {
			t.Fatalf("embed: %v", err)
		}
		return pack
	}
	// state reads the repository's index as the dashboard does.
	state := func() (row store.RepoRow, contents store.IndexContents, active store.Generation) {
		t.Helper()
		if err := st.WithAccount(ctx, account.ID(), func(tx pgx.Tx) error {
			var err error
			if row, err = store.FindRepo(ctx, tx, "truncated/repo"); err != nil {
				return err
			}
			if contents, err = store.ReadIndexContents(ctx, tx, repo); err != nil {
				return err
			}
			active, err = store.FindGeneration(ctx, tx, contents.RunID)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return row, contents, active
	}
	if pack := build("one", store.IndexModeFull, true, nil); !pack.Truncated {
		t.Fatalf("pack = %+v, want truncated", pack)
	}
	row, contents, active := state()
	if !row.IndexTruncated || !contents.Truncated {
		t.Fatalf("after a truncated build: list %v, contents %v; want both truncated", row.IndexTruncated, contents.Truncated)
	}
	build("two", store.IndexModeIncremental, false, &active)
	row, contents, _ = state()
	if contents.Commit != "two" || !row.IndexTruncated || !contents.Truncated {
		t.Fatalf("after a step to %s: list %v, contents %v; want the generation still truncated", contents.Commit, row.IndexTruncated, contents.Truncated)
	}
	build("three", store.IndexModeFull, false, nil)
	row, contents, _ = state()
	if row.IndexTruncated || contents.Truncated {
		t.Fatalf("after a whole build: list %v, contents %v; want neither truncated", row.IndexTruncated, contents.Truncated)
	}
}
