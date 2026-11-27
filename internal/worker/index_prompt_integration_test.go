//go:build integration

package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivertest"
	"github.com/riverqueue/river/rivertype"

	"github.com/home-operations/kritika/internal/adapter"
	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/configfile/configfiletest"
	"github.com/home-operations/kritika/internal/jobs"
	"github.com/home-operations/kritika/internal/metrics"
	"github.com/home-operations/kritika/internal/model"
	"github.com/home-operations/kritika/internal/runspec"
	"github.com/home-operations/kritika/internal/store"
	"github.com/home-operations/kritika/internal/store/storetest"
	"github.com/home-operations/kritika/internal/tuning"
)

// TestIndexDocumentPrompt: the worker embeds every chunk led by the document
// prompt, and a staged window's signature under its header. The index that
// it publishes records the prompt. So the configuration that built it finds
// it, and one without the prompt does not.
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
	storetest.Repositories(t, st, file, "onedr0p/home-ops")
	if _, err := st.EnsureIndexSchema(ctx, "kritika_app", "fake-embed", 8, "passage: "); err != nil {
		t.Fatal(err)
	}
	account, _ := file.Account(configfile.ForgeGitHub, "onedr0p")
	repo := configfile.RepositoryID(account.ID(), "prompt/repo")
	if err := st.WithAccount(ctx, account.ID(), func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO repositories(id,account_id,name) VALUES($1,$2,'prompt/repo')`, repo, account.ID())
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
		if _, err = tx.Exec(ctx, `INSERT INTO index_packs(runner_run_id,account_id,mode,chunk_count) VALUES($1,$2,'full',2)`, runner, account.ID()); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO index_staging(runner_run_id,account_id,path,start_line,end_line,text)
     VALUES($1,$2,'main.go',1,1,'func f() {}')`, runner, account.ID()); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO index_staging(runner_run_id,account_id,path,start_line,end_line,symbol,kind,signature,text)
     VALUES($1,$2,'big.go',60,61,'G','function','func G(a int) int {',E'\ta++\n\treturn a')`, runner, account.ID())
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
		file.Embedding, "abc", run, runner, nil, configfile.Slots{Limit: 1}); err != nil {
		t.Fatalf("embed: %v", err)
	}
	if !slices.Equal(embedded, []string{"passage: main.go\nfunc f() {}", "passage: big.go function G\nfunc G(a int) int {\n\ta++\n\treturn a"}) {
		t.Fatalf("embedded %q, want each chunk led by the document prompt, and a window by its signature", embedded)
	}
	if err := st.WithAccount(ctx, account.ID(), func(tx pgx.Tx) error {
		g, err := store.FindGeneration(ctx, tx, run)
		if err != nil {
			return err
		}
		if !indexMatches(&g, file.Embedding, indexIgnoreHash(nil), jobs.TriggerOnboard) {
			t.Errorf("index %+v does not match the embedder that built it", g)
		}
		if _, _, err := store.ActiveGenerationFor(ctx, tx, repo, "fake-embed", 8, "passage: "); err != nil {
			t.Errorf("the configuration that built the index does not find it: %v", err)
		}
		if _, _, err := store.ActiveGenerationFor(ctx, tx, repo, "fake-embed", 8, ""); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("a configuration without the prompt found the index: %v", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// TestIndexWaitsForTheAppliedEmbedder: a pod on another embedder than the
// one the leader applied, as during a rollout, snoozes the job instead of
// indexing with it.
func TestIndexWaitsForTheAppliedEmbedder(t *testing.T) {
	ctx := t.Context()
	st := storetest.Open(t)
	t.Setenv("TEST_PEM", "pem")
	t.Setenv("TEST_SECRET", "test-provider-key")
	file := configfiletest.Load(t, configYAML)
	if err := st.ApplyConfig(ctx, file); err != nil {
		t.Fatal(err)
	}
	storetest.Repositories(t, st, file, "onedr0p/home-ops")
	// The leader applied the same model with a document prompt.
	if _, err := st.EnsureIndexSchema(ctx, "kritika_app", "fake-embed", 8, "passage: "); err != nil {
		t.Fatal(err)
	}
	account, _ := file.Account(configfile.ForgeGitHub, "onedr0p")
	repo := configfile.RepositoryID(account.ID(), "onedr0p/home-ops")
	before := indexRunCount(t, st, account.ID(), repo)
	w := &Index{Store: st, Current: configfile.NewCurrent(file), Logger: slog.New(slog.DiscardHandler), Embedders: refusingEmbedders()}
	job := &river.Job[jobs.IndexArgs]{JobRow: &rivertype.JobRow{ID: 1, Attempt: 1},
		Args: jobs.IndexArgs{AccountID: account.ID(), RepositoryID: repo, Trigger: jobs.TriggerPush}}
	err := w.Work(ctx, job)
	if _, ok := errors.AsType[*river.JobSnoozeError](err); !ok {
		t.Fatalf("Work = %v, want the job snoozed", err)
	}
	if after := indexRunCount(t, st, account.ID(), repo); after != before {
		t.Fatalf("index runs = %d, want %d: the job started one", after, before)
	}
}

// TestIndexWaitsForTheAppliedFormat: a pod of another index format than the
// one the leader's build applied, as during a rollout, snoozes the job
// instead of building an index that the onboarding feeder rebuilds again.
// Each wait is counted on kritika_index_snoozes_total{reason="build"}, and a
// job that has waited tuning's Worker.BuildWaitWarn times says so as a warning, since a
// leader that never applies this pod's build keeps it waiting for good.
func TestIndexWaitsForTheAppliedFormat(t *testing.T) {
	ctx := t.Context()
	st := storetest.Open(t)
	t.Setenv("TEST_PEM", "pem")
	t.Setenv("TEST_SECRET", "test-provider-key")
	file := configfiletest.Load(t, configYAML)
	if err := st.ApplyConfig(ctx, file); err != nil {
		t.Fatal(err)
	}
	storetest.Repositories(t, st, file, "onedr0p/home-ops")
	if _, err := st.EnsureIndexSchema(ctx, "kritika_app", "fake-embed", 8, ""); err != nil {
		t.Fatal(err)
	}
	owner, err := pgx.Connect(ctx, storetest.Env(t, "KRITIKA_TEST_OWNER_URL"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := owner.Exec(context.Background(), `UPDATE index_schema SET index_format = $1`, runspec.IndexFormat); err != nil {
			t.Error(err)
		}
		_ = owner.Close(context.Background())
	})
	// The leader runs a build of the next format.
	if _, err := owner.Exec(ctx, `UPDATE index_schema SET index_format = $1`, runspec.IndexFormat+1); err != nil {
		t.Fatal(err)
	}
	account, _ := file.Account(configfile.ForgeGitHub, "onedr0p")
	repo := configfile.RepositoryID(account.ID(), "onedr0p/home-ops")
	before := indexRunCount(t, st, account.ID(), repo)
	for _, tc := range []struct {
		name string
		// metadata is the job's, with the waits before this one.
		metadata string
		warn     bool
	}{
		{name: "first wait", metadata: `{}`},
		{name: "a rollout's wait", metadata: fmt.Sprintf(`{"build_waits": %d}`, tuning.Get().Worker.BuildWaitWarn-2)},
		{name: "a long wait", metadata: fmt.Sprintf(`{"build_waits": %d}`, tuning.Get().Worker.BuildWaitWarn-1), warn: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var logs bytes.Buffer
			reg := prometheus.NewRegistry()
			w := &Index{Store: st, Current: configfile.NewCurrent(file), Logger: slog.New(slog.NewTextHandler(&logs, nil)),
				Embedders: refusingEmbedders(), Metrics: metrics.New(reg)}
			job := &river.Job[jobs.IndexArgs]{JobRow: &rivertype.JobRow{ID: 1, Attempt: 1, Metadata: []byte(tc.metadata)},
				Args: jobs.IndexArgs{AccountID: account.ID(), RepositoryID: repo, Trigger: jobs.TriggerOnboard}}
			err := w.Work(ctx, job)
			if snooze, ok := errors.AsType[*river.JobSnoozeError](err); !ok || snooze.Duration != tuning.Get().Worker.AppliedBuildWait {
				t.Fatalf("Work = %v, want the job snoozed for %s", err, tuning.Get().Worker.AppliedBuildWait)
			}
			if after := indexRunCount(t, st, account.ID(), repo); after != before {
				t.Fatalf("index runs = %d, want %d: the job started one", after, before)
			}
			if got := strings.Contains(logs.String(), `level=WARN msg="index has waited long`); got != tc.warn {
				t.Fatalf("warned = %v, want %v:\n%s", got, tc.warn, logs.String())
			}
			want := "# HELP kritika_index_snoozes_total Index jobs put back on the queue to wait, by reason.\n" +
				"# TYPE kritika_index_snoozes_total counter\n" +
				fmt.Sprintf("kritika_index_snoozes_total{account=%q,reason=\"build\"} 1\n", account.Key())
			if err := testutil.GatherAndCompare(reg, strings.NewReader(want), "kritika_index_snoozes_total"); err != nil {
				t.Fatal(err)
			}
		})
	}
	// River keeps each wait's count in the job's metadata for the next attempt,
	// so a job that keeps waiting reaches the warning.
	t.Run("waits counted across attempts", func(t *testing.T) {
		var logs bytes.Buffer
		w := &Index{Store: st, Current: configfile.NewCurrent(file), Logger: slog.New(slog.NewTextHandler(&logs, nil)),
			Embedders: refusingEmbedders()}
		tx, err := st.App().Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(context.Background()) }()
		// Earlier suites leave the repository's index jobs, onto which this
		// one would be deduplicated.
		if _, err := tx.Exec(ctx, `DELETE FROM river_job WHERE kind = 'index' AND args->>'repository_id' = $1`, repo); err != nil {
			t.Fatal(err)
		}
		tw := rivertest.NewWorker(t, riverpgxv5.New(st.App()), &river.Config{}, river.Worker[jobs.IndexArgs](w))
		client, err := river.NewClient(riverpgxv5.New(st.App()), &river.Config{})
		if err != nil {
			t.Fatal(err)
		}
		inserted, err := client.InsertTx(ctx, tx, jobs.IndexArgs{AccountID: account.ID(), RepositoryID: repo, Trigger: jobs.TriggerOnboard}, nil)
		if err != nil {
			t.Fatal(err)
		}
		row := inserted.Job
		for waits := 1; waits <= tuning.Get().Worker.BuildWaitWarn; waits++ {
			logs.Reset()
			res, err := tw.WorkJob(ctx, t, tx, row)
			if err != nil {
				t.Fatal(err)
			}
			var meta struct {
				Waits int `json:"build_waits"`
			}
			if err := json.Unmarshal(res.Job.Metadata, &meta); err != nil {
				t.Fatal(err)
			}
			warned := strings.Contains(logs.String(), `level=WARN msg="index has waited long`)
			if res.EventKind != river.EventKindJobSnoozed || meta.Waits != waits || warned != (waits == tuning.Get().Worker.BuildWaitWarn) {
				t.Fatalf("attempt %d: %s with %d build waits recorded, warned %v; want snoozed with %d, warned only at %d",
					waits, res.EventKind, meta.Waits, warned, waits, tuning.Get().Worker.BuildWaitWarn)
			}
			row = res.Job
		}
	})
}

// rebuildingForge is a forge during whose BranchTip the leader applies
// another embedder.
type rebuildingForge struct {
	resumeForge
	rebuild func()
}

func (f *rebuildingForge) BranchTip(ctx context.Context, owner, name, branch string) (string, string, error) {
	f.rebuild()
	return f.resumeForge.BranchTip(ctx, owner, name, branch)
}

// TestIndexWaitsForARebuildMidway: the leader applies another embedder
// while a worker already past its first check asks the forge for the tip.
// The rebuild supersedes only the runs that exist, so the worker must not
// start one for the embedder no longer applied.
func TestIndexWaitsForARebuildMidway(t *testing.T) {
	ctx := t.Context()
	st := storetest.Open(t)
	t.Setenv("TEST_PEM", "pem")
	t.Setenv("TEST_SECRET", "test-provider-key")
	file := configfiletest.Load(t, configYAML)
	if err := st.ApplyConfig(ctx, file); err != nil {
		t.Fatal(err)
	}
	storetest.Repositories(t, st, file, "onedr0p/home-ops")
	if _, err := st.EnsureIndexSchema(ctx, "kritika_app", "fake-embed", 8, ""); err != nil {
		t.Fatal(err)
	}
	account, _ := file.Account(configfile.ForgeGitHub, "onedr0p")
	repo := configfile.RepositoryID(account.ID(), "onedr0p/home-ops")
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
	before := indexRunCount(t, st, account.ID(), repo)
	f := &rebuildingForge{tip: "midway", rebuild: func() {
		if _, err := st.EnsureIndexSchema(ctx, "kritika_app", "fake-embed", 8, "passage: "); err != nil {
			t.Error(err)
		}
	}}
	w := &Index{Store: st, Current: configfile.NewCurrent(file), Forges: &forges{f: f}, Logger: slog.New(slog.DiscardHandler),
		Executor: &resumeExecutor{st: st, account: account.ID()}, Embedders: refusingEmbedders()}
	err = w.Work(ctx, &river.Job[jobs.IndexArgs]{JobRow: inserted.Job, Args: args})
	if _, ok := errors.AsType[*river.JobSnoozeError](err); !ok {
		t.Fatalf("Work = %v, want the job snoozed", err)
	}
	if after := indexRunCount(t, st, account.ID(), repo); after != before {
		t.Fatalf("index runs = %d, want %d: the job started one for the embedder no longer applied", after, before)
	}
}

// refusingEmbedders build an embedder that fails any call: a pod on
// another embedder than the applied one must not embed.
func refusingEmbedders() *adapter.Embedders {
	return &adapter.Embedders{Build: func(configfile.Embedding) model.Embedder {
		return indexEmbedFunc(func(context.Context, []string) ([][]float32, int64, error) {
			return nil, 0, errors.New("a pod on another embedder must not embed")
		})
	}}
}

// indexRunCount is how many index runs the repository has.
func indexRunCount(t *testing.T, st *store.Store, accountID, repo string) int {
	t.Helper()
	var n int
	if err := st.WithAccount(t.Context(), accountID, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT count(*) FROM index_runs WHERE repository_id = $1`, repo).Scan(&n)
	}); err != nil {
		t.Fatal(err)
	}
	return n
}
