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
	"github.com/prometheus/client_golang/prometheus"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"

	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/configfile/configfiletest"
	"github.com/home-operations/kritika/internal/jobs"
	"github.com/home-operations/kritika/internal/metrics"
	"github.com/home-operations/kritika/internal/model"
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
	storetest.Repositories(t, st, file, "onedr0p/home-ops")
	if _, err := st.EnsureIndexSchema(t.Context(), "kritika_app", "fake-embed", 8, ""); err != nil {
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
		// embedded is the vectors counted as embedded: those of the calls
		// that answered with a vector for each text, and no others.
		embedded int
	}{
		{"success", 20, 2, false, 2},
		{"unreported usage", 0, 2, false, 2},
		{"partial failure", 17, 2, true, 1},
		{"cancellation", 17, 2, true, 1},
		{"vector insert failure", 10, 1, true, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			repo := configfile.RepositoryID(account.ID(), "usage/"+tt.name)
			if err := st.WithAccount(ctx, account.ID(), func(tx pgx.Tx) error {
				_, err := tx.Exec(ctx, `INSERT INTO repositories(id,account_id,name) VALUES($1,$2,$3)`, repo, account.ID(), "usage/"+tt.name)
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
     SELECT $1,$2,'main.go',n,n,'func f'||n||'() {}' FROM generate_series(1,2) n`, runner, account.ID())
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
					// One vector, as asked, but of a dimension the table refuses.
					return [][]float32{{1, 0}}, 10, nil
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
			reg := prometheus.NewRegistry()
			base := Base{Store: st, Logger: slog.New(slog.DiscardHandler), Metrics: metrics.New(reg)}
			w := Index{Base: base, batch: 1}
			_, _, err = w.embed(ctx, &river.Job[jobs.IndexArgs]{JobRow: job.Job, Args: args}, account, embed,
				file.Embedding, "abc", run, runner, nil, 1)
			if (err != nil) != tt.wantError {
				t.Fatalf("embed error = %v, want error %v", err, tt.wantError)
			}
			if got := embeddedVectors(t, reg); got != tt.embedded {
				t.Errorf("vectors counted as embedded = %d, want %d", got, tt.embedded)
			}
			if tt.name == "cancellation" && !errors.Is(err, context.Canceled) {
				t.Fatalf("lost cancellation error: %v", err)
			}
			if tt.name == "vector insert failure" && (err == nil || !strings.Contains(err.Error(), "store: insert chunks")) {
				t.Fatalf("embed error = %v, want the insert of the chunks to fail", err)
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

// embeddedVectors sums kritika_index_vectors_total{outcome="embedded"}.
func embeddedVectors(t *testing.T, reg *prometheus.Registry) int {
	t.Helper()
	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, f := range families {
		if f.GetName() != "kritika_index_vectors_total" {
			continue
		}
		for _, m := range f.GetMetric() {
			for _, l := range m.GetLabel() {
				if l.GetName() == "outcome" && l.GetValue() == "embedded" {
					n += int(m.GetCounter().GetValue())
				}
			}
		}
	}
	return n
}

// TestIndexReusesVectors: a chunk whose text the repository's active
// generation or the run being built already holds, embedded by the same
// embedder, takes the vector it holds rather than be embedded again, and a
// text that a batch holds twice is embedded once. Only what the embedder is
// sent counts as usage.
func TestIndexReusesVectors(t *testing.T) {
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
	repo := configfile.RepositoryID(account.ID(), "reuse/repo")
	if err := st.WithAccount(ctx, account.ID(), func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO repositories(id,account_id,name) VALUES($1,$2,'reuse/repo')`, repo, account.ID())
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

	// Each text the embedder is sent gets a vector of its own, which halfvec
	// stores exactly.
	vectors := map[string][]float32{}
	var sent [][]string
	embed := indexEmbedFunc(func(_ context.Context, inputs []string) ([][]float32, int64, error) {
		sent = append(sent, slices.Clone(inputs))
		out := make([][]float32, len(inputs))
		for i, in := range inputs {
			v := make([]float32, 8)
			v[len(vectors)%8] = float32(len(vectors) + 1)
			vectors[in], out[i] = v, v
		}
		return out, int64(10 * len(inputs)), nil
	})
	w := Index{Store: st, Logger: slog.New(slog.DiscardHandler), batch: 2}
	// build stages texts as one file's chunks, in order, and embeds them as
	// a full build at commit. It returns the run's usage rows and tokens.
	build := func(commit string, texts ...string) (rows int, tokens int64) {
		t.Helper()
		sent = nil
		var run, runner string
		var active *store.Generation
		if err := st.WithAccount(ctx, account.ID(), func(tx pgx.Tx) error {
			var activeRun *string
			if err := tx.QueryRow(ctx, `SELECT active_index_run_id::text FROM repositories WHERE id = $1`, repo).Scan(&activeRun); err != nil {
				return err
			}
			if activeRun != nil {
				active = &store.Generation{ID: *activeRun}
			}
			var err error
			run, runner, err = store.StartIndexRun(ctx, tx, store.NewIndexRun{
				AccountID: account.ID(), RepositoryID: repo, Commit: commit, Mode: store.IndexModeFull, Trigger: jobs.TriggerPush,
				Embedding: *file.Embedding, IgnoreHash: indexIgnoreHash(nil), JobID: job.ID, StrayAfter: time.Hour,
			})
			if err != nil {
				return err
			}
			if _, err = tx.Exec(ctx, `INSERT INTO index_packs(runner_run_id,account_id,mode,chunk_count) VALUES($1,$2,'full',$3)`,
				runner, account.ID(), len(texts)); err != nil {
				return err
			}
			_, err = tx.Exec(ctx, `INSERT INTO index_staging(runner_run_id,account_id,path,start_line,end_line,text)
				SELECT $1, $2, 'main.go', n, n, t FROM unnest($3::text[]) WITH ORDINALITY AS s(t, n)`, runner, account.ID(), texts)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		if _, _, err := w.embed(ctx, job, account, embed, file.Embedding, commit, run, runner, active, 1); err != nil {
			t.Fatalf("embed: %v", err)
		}
		if err := st.WithAccount(ctx, account.ID(), func(tx pgx.Tx) error {
			if err := tx.QueryRow(ctx, `SELECT count(*), coalesce(sum(input_tokens), 0) FROM usage WHERE runner_run_id = $1 AND role = 'embedding'`,
				runner).Scan(&rows, &tokens); err != nil {
				return err
			}
			return checkStagedVectors(t, tx, run, texts, vectors)
		}); err != nil {
			t.Fatal(err)
		}
		return rows, tokens
	}
	// Batches of two: A twice in one, A again in the next.
	if rows, tokens := build("one", "func reuseA() {}", "func reuseA() {}", "func reuseB() {}", "func reuseA() {}", "func reuseC() {}"); rows != 3 || tokens != 30 {
		t.Fatalf("first build: %d usage rows, %d tokens; want 3 and 30 for the three texts", rows, tokens)
	}
	if want := [][]string{{"main.go\nfunc reuseA() {}"}, {"main.go\nfunc reuseB() {}"}, {"main.go\nfunc reuseC() {}"}}; !slices.EqualFunc(sent, want, slices.Equal) {
		t.Fatalf("first build sent %q, want %q: each text once", sent, want)
	}
	// A rebuild that changed one function embeds that one alone.
	if rows, tokens := build("two", "func reuseA() {}", "func reuseB() {}", "func reuseC2() {}", "func reuseC() {}"); rows != 1 || tokens != 10 {
		t.Fatalf("rebuild: %d usage rows, %d tokens; want 1 and 10", rows, tokens)
	}
	if want := [][]string{{"main.go\nfunc reuseC2() {}"}}; !slices.EqualFunc(sent, want, slices.Equal) {
		t.Fatalf("rebuild sent %q, want %q", sent, want)
	}
	// A rebuild of an unchanged tree sends nothing.
	if rows, tokens := build("three", "func reuseA() {}", "func reuseB() {}", "func reuseC2() {}", "func reuseC() {}"); rows != 0 || tokens != 0 || len(sent) != 0 {
		t.Fatalf("unchanged rebuild: %d usage rows, %d tokens, sent %q; want none", rows, tokens, sent)
	}
}

// checkStagedVectors checks that run has a chunk for each of texts, staged as
// main.go's lines in order, that holds the vector its text was first
// embedded to, by the embedder input in vectors, and a content hash.
func checkStagedVectors(t *testing.T, tx pgx.Tx, run string, texts []string, vectors map[string][]float32) error {
	t.Helper()
	for i, text := range texts {
		want, ok := vectors["main.go\n"+text]
		if !ok {
			t.Errorf("line %d, %q: never embedded", i+1, text)
			continue
		}
		var right, hashed bool
		err := tx.QueryRow(t.Context(), `SELECT embedding = $3::halfvec, content_hash IS NOT NULL FROM index_chunks
			WHERE index_run_id = $1 AND start_line = $2`, run, i+1, model.VectorLiteral(want)).Scan(&right, &hashed)
		if errors.Is(err, pgx.ErrNoRows) {
			t.Errorf("line %d, %q: no chunk", i+1, text)
			continue
		}
		if err != nil {
			return err
		}
		if !right || !hashed {
			t.Errorf("line %d, %q: holds its text's vector %t, has a content hash %t; want both", i+1, text, right, hashed)
		}
	}
	return nil
}
