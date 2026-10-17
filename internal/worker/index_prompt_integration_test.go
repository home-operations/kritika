//go:build integration

package worker

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"
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

// TestIndexDocumentPrompt: the worker embeds every chunk led by the
// document prompt, and the index it publishes records the prompt, so the
// configuration that built it finds it and one without the prompt does not.
func TestIndexDocumentPrompt(t *testing.T) {
	ctx := t.Context()
	st := storetest.Open(t)
	t.Setenv("TEST_PEM", "pem")
	t.Setenv("TEST_SECRET", "test-provider-key")
	file := configfiletest.Load(t, strings.Replace(configYAML, "embedding: { model: test/fake-embed, dims: 8 }",
		`embedding: { model: test/fake-embed, dims: 8, documentPrompt: "passage: ", similarFloor: 0.5 }`, 1))
	if err := st.ApplyConfig(ctx, file); err != nil {
		t.Fatal(err)
	}
	if _, err := st.EnsureIndexSchema(ctx, "kritika_app", "fake-embed", 8, "passage: "); err != nil {
		t.Fatal(err)
	}
	account, _ := file.Account(configfile.ForgeGitHub, "onedr0p")
	repo := configfile.RepositoryID(account.ID(), "prompt/repo")
	if err := st.WithAccount(ctx, account.ID(), func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO repositories(id,account_id,name,managed_by) VALUES($1,$2,'prompt/repo','file')`, repo, account.ID())
		return err
	}); err != nil {
		t.Fatal(err)
	}
	queue, err := river.NewClient(riverpgxv5.New(st.App()), &river.Config{})
	if err != nil {
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
		if _, err = tx.Exec(ctx, `INSERT INTO index_packs(runner_run_id,account_id,mode,chunk_count) VALUES($1,$2,'full',1)`, runner, account.ID()); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO index_staging(runner_run_id,account_id,path,start_line,end_line,text)
     VALUES($1,$2,'main.go',1,1,'func f() {}')`, runner, account.ID())
		return err
	}); err != nil {
		t.Fatal(err)
	}
	var embedded []string
	embed := indexEmbedFunc(func(_ context.Context, inputs []string) ([][]float32, int64, error) {
		embedded = append(embedded, inputs...)
		vectors := make([][]float32, len(inputs))
		for i := range vectors {
			vectors[i] = []float32{1, 0, 0, 0, 0, 0, 0, 0}
		}
		return vectors, 1, nil
	})
	w := Index{Store: st, Logger: slog.New(slog.DiscardHandler)}
	if _, _, err := w.embed(ctx, &river.Job[jobs.IndexArgs]{JobRow: job.Job, Args: args}, account, embed,
		file.Embedding, "abc", run, runner, nil, file.Settings(account, "")); err != nil {
		t.Fatalf("embed: %v", err)
	}
	if !slices.Equal(embedded, []string{"passage: main.go\nfunc f() {}"}) {
		t.Fatalf("embedded %q, want each chunk led by the document prompt", embedded)
	}
	if err := st.WithAccount(ctx, account.ID(), func(tx pgx.Tx) error {
		g, err := store.FindGeneration(ctx, tx, run)
		if err != nil {
			return err
		}
		if !indexMatches(&g, file.Embedding, indexIgnoreHash(nil)) {
			t.Errorf("index %+v does not match the embedder that built it", g)
		}
		if _, err := store.ActiveGenerationFor(ctx, tx, repo, "fake-embed", 8, "passage: "); err != nil {
			t.Errorf("the configuration that built the index does not find it: %v", err)
		}
		if _, err := store.ActiveGenerationFor(ctx, tx, repo, "fake-embed", 8, ""); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("a configuration without the prompt found the index: %v", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
