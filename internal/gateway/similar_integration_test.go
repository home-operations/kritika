//go:build integration

package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/home-operations/kritika/internal/adapter"
	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/configfile/configfiletest"
	"github.com/home-operations/kritika/internal/contextpack"
	"github.com/home-operations/kritika/internal/metrics"
	"github.com/home-operations/kritika/internal/model"
	"github.com/home-operations/kritika/internal/runspec"
	"github.com/home-operations/kritika/internal/store"
	"github.com/home-operations/kritika/internal/store/storetest"
	"github.com/home-operations/kritika/internal/tuning"
)

type similarEmbedFunc func(context.Context, []string) ([][]float32, int64, error)

func (f similarEmbedFunc) Embed(ctx context.Context, inputs []string) ([][]float32, int64, error) {
	return f(ctx, inputs)
}

func TestSimilarUsage(t *testing.T) {
	ctx := t.Context()
	st := storetest.Open(t)
	t.Setenv("TEST_SECRET", "test-key")
	file := configfiletest.Load(t, `providers:
  test:
    type: openai
    baseUrl: http://unused.invalid/v1
    apiKey: { env: TEST_SECRET }
review: { model: test/reviewer }
embedding: { model: test/embed, dims: 8 }
apps:
  test:
    accounts: [similar-usage]
    clientId: Iv1.test
    privateKey: { env: TEST_SECRET }
    webhookSecret: { env: TEST_SECRET }
`)
	if err := st.ApplyConfig(ctx, file); err != nil {
		t.Fatal(err)
	}
	storetest.Repositories(t, st, file, "similar-usage/repo")
	if _, err := st.EnsureIndexSchema(ctx, "kritika_app", "embed", 8, ""); err != nil {
		t.Fatal(err)
	}
	account, _ := file.Account(configfile.ForgeGitHub, "similar-usage")
	repo := configfile.RepositoryID(account.ID(), "similar-usage/repo")
	var runner string
	if err := st.WithAccount(ctx, account.ID(), func(tx pgx.Tx) error {
		var run string
		if err := tx.QueryRow(ctx, `INSERT INTO index_runs(account_id,repository_id,commit_sha,embed_model,embed_dims,mode,status)
   VALUES($1,$2,'abc','embed',8,'full','completed') RETURNING id`, account.ID(), repo).Scan(&run); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO index_chunks(account_id,repository_id,index_run_id,path,start_line,end_line,text,embedding)
   VALUES($1,$2,$3,'main.go',1,1,'func f() {}','[1,0,0,0,0,0,0,0]')`, account.ID(), repo, run); err != nil {
			return err
		}
		if err := store.ActivateGeneration(ctx, tx, repo, run); err != nil {
			return err
		}
		var err error
		runner, err = store.InsertRunnerRun(ctx, tx, account.ID(), store.RunnerKindIndex, run, 0)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name         string
		tokens       int64
		status, rows int
		searches     uint64
		outcome      string
	}{
		{"success", 7, http.StatusOK, 1, 1, "ok"},
		{"partial failure", 7, http.StatusBadGateway, 1, 0, ""},
		{"search failure", 7, http.StatusBadGateway, 1, 1, "error"},
		{"cancellation", 7, http.StatusBadGateway, 1, 0, ""},
		{"search cancellation", 7, http.StatusBadGateway, 1, 1, "canceled"},
		{"unreported usage", 0, http.StatusOK, 1, 1, "ok"},
		{"unpaid failure", 0, http.StatusBadGateway, 0, 0, ""},
	}
	var wantTokens int64
	var wantRows int
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			token, err := st.MintGatewayToken(ctx, store.GatewayGrant{
				RunID: runner, AccountID: account.ID(), RepositoryID: repo, Model: "test/reviewer", Budget: 1,
			}, time.Now().Add(time.Minute))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := st.RevokeGatewayTokens(context.Background(), runner); err != nil {
					t.Error(err)
				}
			})
			rctx, cancel := context.WithCancel(ctx)
			defer cancel()
			embed := similarEmbedFunc(func(context.Context, []string) ([][]float32, int64, error) {
				switch tt.name {
				case "partial failure", "unpaid failure":
					return nil, tt.tokens, errors.New("embedding failed")
				case "search failure":
					return [][]float32{{1, 0}}, tt.tokens, nil
				case "search cancellation":
					cancel()
				case "cancellation":
					cancel()
					return nil, tt.tokens, context.Canceled
				}
				return [][]float32{{1, 0, 0, 0, 0, 0, 0, 0}}, tt.tokens, nil
			})
			reg := prometheus.NewRegistry()
			g := Server{Metrics: metrics.New(reg), Store: st, Current: configfile.NewCurrent(file), Logger: slog.New(slog.DiscardHandler),
				Embedders: &adapter.Embedders{Build: func(configfile.Embedding) model.Embedder { return embed }},
			}
			req := httptest.NewRequest(http.MethodPost, "/v1/similar", strings.NewReader(`{"queries":["hello"]}`)).WithContext(rctx)
			req.Header.Set("Authorization", "Bearer "+token)
			out := httptest.NewRecorder()
			g.ServeHTTP(out, req)
			if out.Code != tt.status {
				t.Fatalf("status=%d, want %d: %s", out.Code, tt.status, out.Body.String())
			}
			checkVectorSearchMetric(t, reg, tt.searches, tt.outcome)
			grant, err := st.LookupGatewayToken(ctx, token)
			if err != nil || grant.Spent != tt.tokens {
				t.Fatalf("spent=%d, want %d: %v", grant.Spent, tt.tokens, err)
			}
			wantTokens += tt.tokens
			wantRows += tt.rows
			if err := st.WithAccount(ctx, account.ID(), func(tx pgx.Tx) error {
				var tokens int64
				var rows int
				if err := tx.QueryRow(ctx, `SELECT count(*),coalesce(sum(input_tokens),0) FROM usage WHERE repository_id=$1 AND role='embedding'`, repo).Scan(&rows, &tokens); err != nil {
					return err
				}
				if rows != wantRows || tokens != wantTokens {
					t.Errorf("usage rows=%d tokens=%d, want %d/%d", rows, tokens, wantRows, wantTokens)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// TestSimilarReleasesTheSlotBeforeCharging: once the embedder answered, the
// request lets its embedding slot go before it settles the run's budget, so
// a charge that waits for the run's token row holds no other caller of the
// embedder back.
func TestSimilarReleasesTheSlotBeforeCharging(t *testing.T) {
	r := newLexicalRepo(t, "charge-after-release", runspec.IndexFormat, widgets, widgetVectors())
	ctx := t.Context()
	owner, err := pgx.Connect(ctx, storetest.Env(t, "KRITIKA_TEST_OWNER_URL"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = owner.Close(context.Background()) })
	// The application role's own session sees what its pool's sessions wait for.
	watch, err := pgx.Connect(ctx, storetest.Env(t, "KRITIKA_TEST_APP_URL"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = watch.Close(context.Background()) })
	// While the slot is held, the embedder locks the run's token row, which
	// the charge that follows it waits for, and hands the lock over.
	locked := make(chan pgx.Tx, 1)
	embed := similarEmbedFunc(func(ctx context.Context, inputs []string) ([][]float32, int64, error) {
		tx, err := owner.Begin(ctx)
		if err != nil {
			return nil, 0, err
		}
		if _, err := tx.Exec(ctx, `SELECT 1 FROM gateway_tokens WHERE runner_run_id = $1 FOR UPDATE`, r.runner); err != nil {
			return nil, 0, err
		}
		locked <- tx
		out := make([][]float32, len(inputs))
		for i := range out {
			out[i] = e(0)
		}
		return out, 7, nil
	})
	g := Server{Metrics: metrics.New(prometheus.NewRegistry()), Store: r.st, Current: configfile.NewCurrent(r.file),
		Logger: slog.New(slog.DiscardHandler), Embedders: &adapter.Embedders{Build: func(configfile.Embedding) model.Embedder { return embed }},
	}
	token, err := r.st.MintGatewayToken(ctx, store.GatewayGrant{
		RunID: r.runner, AccountID: r.account.ID(), RepositoryID: r.id, Model: "test/reviewer", Budget: 100000,
	}, time.Now().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.st.RevokeGatewayTokens(context.Background(), r.runner) })
	req := httptest.NewRequestWithContext(ctx, http.MethodPost, runspec.SimilarPath, strings.NewReader(`{"queries":["hello"]}`))
	req.Header.Set("Authorization", "Bearer "+token)
	out := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		g.ServeHTTP(out, req)
	}()
	rollback := func(tx pgx.Tx) {
		if err := tx.Rollback(context.Background()); err != nil {
			t.Error(err)
		}
	}
	// Whatever the outcome, the lock is let go before the request is waited for.
	t.Cleanup(func() {
		for {
			select {
			case tx := <-locked:
				rollback(tx)
			case <-done:
				return
			}
		}
	})

	var tx pgx.Tx
	select {
	case tx = <-locked:
	case <-done:
		t.Fatalf("the request ended before it embedded: %d %s", out.Code, out.Body.String())
	case <-time.After(15 * time.Second):
		t.Fatal("timed out waiting for the embedder")
	}
	unlock := sync.OnceFunc(func() { rollback(tx) })
	defer unlock()
	for deadline := time.Now().Add(15 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		var charging, held bool
		if err := watch.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_stat_activity
			WHERE wait_event_type = 'Lock' AND query LIKE 'UPDATE gateway_tokens SET spent_tokens%')`).Scan(&charging); err != nil {
			t.Fatal(err)
		}
		if err := r.st.WithAccount(ctx, r.account.ID(), func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM model_leases
				WHERE account_id = $1 AND model_key LIKE 'embed:%' AND job_id IS NOT NULL)`, r.account.ID()).Scan(&held)
		}); err != nil {
			t.Fatal(err)
		}
		if charging && !held {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("charging %t with the embedding slot held %t, want the charge to wait with the slot let go", charging, held)
		}
	}
	unlock()
	<-done
	if out.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", out.Code, out.Body.String())
	}
	grant, err := r.st.LookupGatewayToken(ctx, token)
	if err != nil || grant.Spent != 7 {
		t.Fatalf("spent = %d, want the 7 tokens the embedder reported: %v", grant.Spent, err)
	}
}

// TestSimilarPrompts: a query reaches the embedder led by its kind's
// prompt, and only an index built with the document prompt answers it.
func TestSimilarPrompts(t *testing.T) {
	ctx := t.Context()
	st := storetest.Open(t)
	t.Setenv("TEST_SECRET", "test-key")
	file := configfiletest.Load(t, `providers:
  test:
    type: openai
    baseUrl: http://unused.invalid/v1
    apiKey: { env: TEST_SECRET }
review: { model: test/reviewer }
embedding: { model: test/embed, dims: 8, queryPrompt: "query: ", searchPrompt: "search: ", documentPrompt: "passage: ", similarFloor: 0.5 }
apps:
  test:
    accounts: [similar-prompts]
    clientId: Iv1.test
    privateKey: { env: TEST_SECRET }
    webhookSecret: { env: TEST_SECRET }
`)
	if err := st.ApplyConfig(ctx, file); err != nil {
		t.Fatal(err)
	}
	storetest.Repositories(t, st, file, "similar-prompts/repo")
	if _, err := st.EnsureIndexSchema(ctx, "kritika_app", "embed", 8, "passage: "); err != nil {
		t.Fatal(err)
	}
	account, _ := file.Account(configfile.ForgeGitHub, "similar-prompts")
	repo := configfile.RepositoryID(account.ID(), "similar-prompts/repo")
	var runner string
	seed := func(prompt string) {
		t.Helper()
		if err := st.WithAccount(ctx, account.ID(), func(tx pgx.Tx) error {
			var run string
			if err := tx.QueryRow(ctx, `INSERT INTO index_runs(account_id,repository_id,commit_sha,embed_model,embed_dims,document_prompt,mode,status)
   VALUES($1,$2,'abc','embed',8,$3,'full','completed') RETURNING id`, account.ID(), repo, prompt).Scan(&run); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO index_chunks(account_id,repository_id,index_run_id,path,start_line,end_line,text,embedding)
   VALUES($1,$2,$3,'main.go',1,1,'func f() {}','[1,0,0,0,0,0,0,0]')`, account.ID(), repo, run); err != nil {
				return err
			}
			if err := store.ActivateGeneration(ctx, tx, repo, run); err != nil {
				return err
			}
			if runner != "" {
				return nil
			}
			var err error
			runner, err = store.InsertRunnerRun(ctx, tx, account.ID(), store.RunnerKindIndex, run, 0)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	var embedded []string
	embed := similarEmbedFunc(func(_ context.Context, inputs []string) ([][]float32, int64, error) {
		embedded = append(embedded, inputs...)
		return [][]float32{{1, 0, 0, 0, 0, 0, 0, 0}}, 1, nil
	})
	g := Server{Metrics: metrics.New(prometheus.NewRegistry()), Store: st, Current: configfile.NewCurrent(file),
		Logger:    slog.New(slog.DiscardHandler),
		Embedders: &adapter.Embedders{Build: func(configfile.Embedding) model.Embedder { return embed }},
	}
	search := func(body string, status int) contextpack.SimilarResponse {
		t.Helper()
		token, err := st.MintGatewayToken(ctx, store.GatewayGrant{
			RunID: runner, AccountID: account.ID(), RepositoryID: repo, Model: "test/reviewer", Budget: 1000,
		}, time.Now().Add(time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPost, "/v1/similar", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		out := httptest.NewRecorder()
		g.ServeHTTP(out, req)
		if out.Code != status {
			t.Fatalf("status=%d: %s", out.Code, out.Body.String())
		}
		if status != http.StatusOK {
			return contextpack.SimilarResponse{}
		}
		var resp contextpack.SimilarResponse
		if err := json.Unmarshal(out.Body.Bytes(), &resp); err != nil {
			t.Fatal(err)
		}
		return resp
	}
	seed("")
	if resp := search(`{"queries":["hello"]}`, http.StatusOK); resp.Indexed || len(resp.Chunks) != 0 {
		t.Fatalf("an index built without the document prompt answered: %+v", resp)
	}
	seed("passage: ")
	if resp := search(`{"queries":["hello"]}`, http.StatusOK); !resp.Indexed || len(resp.Chunks) != 1 {
		t.Fatalf("the index built with the document prompt did not answer: %+v", resp)
	}
	search(`{"queries":["hello"],"kind":"diff"}`, http.StatusOK)
	search(`{"queries":["where is the retry"],"kind":"search"}`, http.StatusOK)
	search(`{"queries":["hello"],"kind":"other"}`, http.StatusBadRequest)
	// A diff request carries a language for each query or none, and a search
	// request none.
	search(`{"queries":["hello"],"languages":["go","go"]}`, http.StatusBadRequest)
	search(`{"queries":["where is the retry"],"kind":"search","languages":["go"]}`, http.StatusBadRequest)
	if want := []string{"query: hello", "query: hello", "search: where is the retry"}; !slices.Equal(embedded, want) {
		t.Fatalf("embedded %q, want %q: each query led by its kind's prompt", embedded, want)
	}
}

func checkVectorSearchMetric(t *testing.T, reg *prometheus.Registry, wantSearches uint64, wantOutcome string) {
	t.Helper()
	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	var searches uint64
	for _, family := range families {
		if family.GetName() != "kritika_vector_search_seconds" {
			continue
		}
		for _, metric := range family.Metric {
			searches += metric.GetHistogram().GetSampleCount()
			for _, label := range metric.Label {
				if label.GetName() == "outcome" {
					if label.GetValue() != wantOutcome {
						t.Errorf("search outcome = %q, want %q", label.GetValue(), wantOutcome)
					}
				}
			}
		}
	}
	if searches != wantSearches {
		t.Errorf("database searches = %d, want %d; embedding failures must not count", searches, wantSearches)
	}
}

// TestSimilarReportedExclusions: a search leaves out the paths that its run
// reported, however many a directory move gives it, which the first report
// sets for good, and the paths that an older runner's request still carries.
// Only the report token reports them.
func TestSimilarReportedExclusions(t *testing.T) {
	ctx := t.Context()
	st := storetest.Open(t)
	t.Setenv("TEST_SECRET", "test-key")
	file := configfiletest.Load(t, `providers:
  test:
    type: openai
    baseUrl: http://unused.invalid/v1
    apiKey: { env: TEST_SECRET }
review: { model: test/reviewer }
embedding: { model: test/embed, dims: 8 }
apps:
  test:
    accounts: [similar-exclude]
    clientId: Iv1.test
    privateKey: { env: TEST_SECRET }
    webhookSecret: { env: TEST_SECRET }
`)
	if err := st.ApplyConfig(ctx, file); err != nil {
		t.Fatal(err)
	}
	storetest.Repositories(t, st, file, "similar-exclude/repo")
	if _, err := st.EnsureIndexSchema(ctx, "kritika_app", "embed", 8, ""); err != nil {
		t.Fatal(err)
	}
	account, _ := file.Account(configfile.ForgeGitHub, "similar-exclude")
	repo := configfile.RepositoryID(account.ID(), "similar-exclude/repo")
	var runner string
	if err := st.WithAccount(ctx, account.ID(), func(tx pgx.Tx) error {
		var run string
		if err := tx.QueryRow(ctx, `INSERT INTO index_runs(account_id,repository_id,commit_sha,embed_model,embed_dims,mode,status)
   VALUES($1,$2,'abc','embed',8,'full','completed') RETURNING id`, account.ID(), repo).Scan(&run); err != nil {
			return err
		}
		for _, path := range []string{"main.go", "old.go", "other.go"} {
			if _, err := tx.Exec(ctx, `INSERT INTO index_chunks(account_id,repository_id,index_run_id,path,start_line,end_line,text,embedding)
   VALUES($1,$2,$3,$4,1,1,'func f() {}','[1,0,0,0,0,0,0,0]')`, account.ID(), repo, run, path); err != nil {
				return err
			}
		}
		if err := store.ActivateGeneration(ctx, tx, repo, run); err != nil {
			return err
		}
		var err error
		runner, err = store.InsertRunnerRun(ctx, tx, account.ID(), store.RunnerKindReview, "", 0)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx := context.Background()
		if err := st.WithAccount(ctx, account.ID(), func(tx pgx.Tx) error { return store.FailRunnerRun(ctx, tx, runner, "test finished") }); err != nil {
			t.Error(err)
		}
	})
	gatewayToken, err := st.MintGatewayToken(ctx, store.GatewayGrant{
		RunID: runner, AccountID: account.ID(), RepositoryID: repo, Model: "test/reviewer", Budget: 1 << 20,
	}, time.Now().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	reportToken, err := st.MintRunnerToken(ctx, account.ID(), runner)
	if err != nil {
		t.Fatal(err)
	}
	embed := similarEmbedFunc(func(_ context.Context, inputs []string) ([][]float32, int64, error) {
		return slices.Repeat([][]float32{{1, 0, 0, 0, 0, 0, 0, 0}}, len(inputs)), 1, nil
	})
	reg := prometheus.NewRegistry()
	g := Server{Metrics: metrics.New(reg), Store: st, Current: configfile.NewCurrent(file),
		Logger:    slog.New(slog.DiscardHandler),
		Embedders: &adapter.Embedders{Build: func(configfile.Embedding) model.Embedder { return embed }},
	}
	call := func(path, token, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		out := httptest.NewRecorder()
		g.ServeHTTP(out, req)
		return out
	}
	foundAt := func(path, body string) []string {
		t.Helper()
		out := call(path, gatewayToken, body)
		if out.Code != http.StatusOK {
			t.Fatalf("status=%d: %s", out.Code, out.Body.String())
		}
		var resp contextpack.SimilarResponse
		if err := json.Unmarshal(out.Body.Bytes(), &resp); err != nil {
			t.Fatal(err)
		}
		paths := make([]string, 0, len(resp.Chunks))
		for _, c := range resp.Chunks {
			paths = append(paths, c.Path)
		}
		slices.Sort(paths)
		return paths
	}
	found := func(body string) []string { t.Helper(); return foundAt(runspec.SimilarPath, body) }
	if got := found(`{"queries":["hello"]}`); !slices.Equal(got, []string{"main.go", "old.go", "other.go"}) {
		t.Fatalf("before the report: %q", got)
	}
	if out := call("/v1/run/similar-exclude", gatewayToken, `{"paths":["main.go"]}`); out.Code != http.StatusUnauthorized {
		t.Fatalf("exclusions reported with the run token: %d %s", out.Code, out.Body.String())
	}
	// A directory move: old.go renamed to main.go, and 2,500 files moved
	// from vendor/ to third_party/, whose old names the index holds, nearer
	// the query than other.go. The run reports both names of each of the
	// 2,501 renames, 5,002 paths.
	moved := []string{"main.go", "old.go"}
	if err := st.WithAccount(ctx, account.ID(), func(tx pgx.Tx) error {
		var run string
		if err := tx.QueryRow(ctx, `SELECT active_index_run_id FROM repositories WHERE id = $1`, repo).Scan(&run); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE index_chunks SET embedding = '[1,0.5,0,0,0,0,0,0]' WHERE index_run_id = $1 AND path = 'other.go'`,
			run); err != nil {
			return err
		}
		for i := range 2500 {
			moved = append(moved, fmt.Sprintf("vendor/lib/f%04d.go", i), fmt.Sprintf("third_party/lib/f%04d.go", i))
		}
		_, err := tx.Exec(ctx, `INSERT INTO index_chunks(account_id,repository_id,index_run_id,path,start_line,end_line,text,embedding)
   SELECT $1,$2,$3,p,1,1,'func f() {}','[1,0,0,0,0,0,0,0]' FROM unnest($4::text[]) p WHERE p LIKE 'vendor/%'`,
			account.ID(), repo, run, moved)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	directoryMove, err := json.Marshal(runspec.SimilarExcludeReport{Paths: moved})
	if err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{string(directoryMove), `{"paths":["other.go"]}`} {
		if out := call("/v1/run/similar-exclude", reportToken, body); out.Code != http.StatusNoContent {
			t.Fatalf("report of %d bytes: %d %s", len(body), out.Code, out.Body.String())
		}
	}
	if got := found(`{"queries":["hello"]}`); !slices.Equal(got, []string{"other.go"}) {
		t.Fatalf("after the report: %q, want the first report's exclusions to hold", got)
	}
	if got := found(`{"queries":["hello"],"kind":"search"}`); !slices.Equal(got, []string{"other.go"}) {
		t.Fatalf("search_code after the report: %q", got)
	}
	// An older runner sends its exclusions in each request, to /v1/similar.
	if got := foundAt("/v1/similar", `{"queries":["hello"],"exclude":["other.go"]}`); len(got) != 0 {
		t.Fatalf("with a request's own exclusions: %q", got)
	}
	// A request without queries embeds nothing, and learns that there is an
	// index.
	if out := call("/v1/similar", gatewayToken, `{"queries":[],"kind":"diff"}`); out.Code != http.StatusOK ||
		strings.TrimSpace(out.Body.String()) != `{"chunks":[],"indexed":true}` {
		t.Fatalf("no queries = %d %s", out.Code, out.Body.String())
	}
	if out := call("/v1/similar", gatewayToken, `{"queries":["hello"],"kind":"other"}`); out.Code != http.StatusBadRequest {
		t.Fatalf("an unknown kind = %d", out.Code)
	}
	want := map[string]float64{"diff/ok": 2, "search/ok": 1, "diff/empty": 1, "diff/no_queries": 1, "unknown/invalid": 1}
	if got := similarRequests(t, reg); !maps.Equal(got, want) {
		t.Fatalf("requests counted %v, want %v", got, want)
	}
}

// similarRequests is kritika_similar_requests_total by kind and outcome.
func similarRequests(t *testing.T, reg *prometheus.Registry) map[string]float64 {
	t.Helper()
	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]float64{}
	for _, family := range families {
		if family.GetName() != "kritika_similar_requests_total" {
			continue
		}
		for _, metric := range family.Metric {
			var kind, outcome string
			for _, label := range metric.Label {
				switch label.GetName() {
				case "kind":
					kind = label.GetValue()
				case "outcome":
					outcome = label.GetValue()
				}
			}
			out[kind+"/"+outcome] += metric.GetCounter().GetValue()
		}
	}
	return out
}

// TestSimilarAnswerLimit: an answer holds as many chunks as its request asks
// for, up to ContextPack.SimilarLimit, so that a run that drops some still
// has ten to show, and the gateway's ten when the request asks for none.
func TestSimilarAnswerLimit(t *testing.T) {
	cp := tuning.Get().ContextPack
	ctx := t.Context()
	st := storetest.Open(t)
	t.Setenv("TEST_SECRET", "test-key")
	file := configfiletest.Load(t, `providers:
  test:
    type: openai
    baseUrl: http://unused.invalid/v1
    apiKey: { env: TEST_SECRET }
review: { model: test/reviewer }
embedding: { model: test/embed, dims: 8 }
apps:
  test:
    accounts: [similar-limit]
    clientId: Iv1.test
    privateKey: { env: TEST_SECRET }
    webhookSecret: { env: TEST_SECRET }
`)
	if err := st.ApplyConfig(ctx, file); err != nil {
		t.Fatal(err)
	}
	storetest.Repositories(t, st, file, "similar-limit/repo")
	if _, err := st.EnsureIndexSchema(ctx, "kritika_app", "embed", 8, ""); err != nil {
		t.Fatal(err)
	}
	account, _ := file.Account(configfile.ForgeGitHub, "similar-limit")
	repo := configfile.RepositoryID(account.ID(), "similar-limit/repo")
	// Query i embeds as the i-th axis, and four chunks, each of a file of
	// its own, lie close to each axis: every query's four neighbours count.
	axis := func(i int) []float32 {
		v := make([]float32, 8)
		v[i] = 1
		return v
	}
	var runner string
	if err := st.WithAccount(ctx, account.ID(), func(tx pgx.Tx) error {
		var run string
		if err := tx.QueryRow(ctx, `INSERT INTO index_runs(account_id,repository_id,commit_sha,embed_model,embed_dims,mode,status)
   VALUES($1,$2,'abc','embed',8,'full','completed') RETURNING id`, account.ID(), repo).Scan(&run); err != nil {
			return err
		}
		for q := range 4 {
			for j := range 4 {
				v := axis(q)
				v[4+j] = 0.1
				if _, err := tx.Exec(ctx, `INSERT INTO index_chunks(account_id,repository_id,index_run_id,path,start_line,end_line,text,embedding)
   VALUES($1,$2,$3,$4,1,1,'func f() {}',$5::halfvec)`, account.ID(), repo, run, fmt.Sprintf("q%d/f%d.go", q, j), model.VectorLiteral(v)); err != nil {
					return err
				}
			}
		}
		if err := store.ActivateGeneration(ctx, tx, repo, run); err != nil {
			return err
		}
		var err error
		runner, err = store.InsertRunnerRun(ctx, tx, account.ID(), store.RunnerKindReview, "", 0)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx := context.Background()
		if err := st.WithAccount(ctx, account.ID(), func(tx pgx.Tx) error { return store.FailRunnerRun(ctx, tx, runner, "test finished") }); err != nil {
			t.Error(err)
		}
	})
	token, err := st.MintGatewayToken(ctx, store.GatewayGrant{
		RunID: runner, AccountID: account.ID(), RepositoryID: repo, Model: "test/reviewer", Budget: 1 << 20,
	}, time.Now().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	reg := prometheus.NewRegistry()
	embed := similarEmbedFunc(func(_ context.Context, inputs []string) ([][]float32, int64, error) {
		out := make([][]float32, len(inputs))
		for i, in := range inputs {
			var q int
			if _, err := fmt.Sscanf(in, "q%d", &q); err != nil {
				return nil, 0, err
			}
			out[i] = axis(q)
		}
		return out, 1, nil
	})
	g := Server{Metrics: metrics.New(reg), Store: st, Current: configfile.NewCurrent(file),
		Logger:    slog.New(slog.DiscardHandler),
		Embedders: &adapter.Embedders{Build: func(configfile.Embedding) model.Embedder { return embed }},
	}
	for _, tt := range []struct {
		name  string
		limit int
		want  int
	}{
		{"none asked for", 0, tuning.Get().Gateway.SimilarMax},
		{"fewer", 3, 3},
		{"the most", cp.SimilarLimit, cp.SimilarLimit},
		{"past the most", cp.SimilarLimit + 10, cp.SimilarLimit},
	} {
		t.Run(tt.name, func(t *testing.T) {
			body, err := json.Marshal(contextpack.SimilarRequest{Queries: []string{"q0", "q1", "q2", "q3"}, Limit: tt.limit})
			if err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest(http.MethodPost, "/v1/similar", bytes.NewReader(body))
			req.Header.Set("Authorization", "Bearer "+token)
			out := httptest.NewRecorder()
			g.ServeHTTP(out, req)
			var resp contextpack.SimilarResponse
			if err := json.Unmarshal(out.Body.Bytes(), &resp); err != nil || out.Code != http.StatusOK {
				t.Fatalf("status=%d: %s", out.Code, out.Body.String())
			}
			if len(resp.Chunks) != tt.want {
				t.Fatalf("%d chunks, want %d", len(resp.Chunks), tt.want)
			}
			// Every query's best comes before any query's second.
			firsts := make([]string, 0, 4)
			for _, c := range resp.Chunks[:min(4, tt.want)] {
				firsts = append(firsts, c.Path[:2])
			}
			slices.Sort(firsts)
			if tt.want >= 4 && !slices.Equal(firsts, []string{"q0", "q1", "q2", "q3"}) {
				t.Fatalf("the first four chunks are of queries %q, want one of each", firsts)
			}
		})
	}
	// Each request found sixteen chunks near its queries, four a query, and
	// kept what it asked for: 10, 3, 16 and 16. Each query, searched
	// Gateway.SimilarDepth deep, also found four of another's, under the
	// floor. Each query measured its best match.
	hits := map[string]float64{}
	var bests uint64
	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, family := range families {
		for _, metric := range family.Metric {
			switch family.GetName() {
			case "kritika_similar_hits_total":
				for _, label := range metric.Label {
					if label.GetName() == "outcome" {
						hits[label.GetValue()] += metric.GetCounter().GetValue()
					}
				}
			case "kritika_similar_best_similarity":
				bests += metric.GetHistogram().GetSampleCount()
			}
		}
	}
	if want := map[string]float64{hitKept: 45, hitLimit: 19, hitFloor: 64}; !maps.Equal(hits, want) || bests != 16 {
		t.Fatalf("hits counted %v, best similarities %d; want %v, 16", hits, bests, want)
	}
}

// TestSimilarSearchesTheGenerationActiveAtTheSearch: a generation published
// while the request embeds its queries supersedes the one active when the
// request arrived, whose chunks it drops. The request searches the one it
// published, or answers that there is no index when that one was built by
// another embedder, rather than search the dropped one and find nothing.
func TestSimilarSearchesTheGenerationActiveAtTheSearch(t *testing.T) {
	for _, tt := range []struct {
		name, model string
		want        []string
		indexed     bool
	}{
		{"published by the same embedder", "embed", []string{"renewed.go"}, true},
		{"published by another embedder", "other-embed", []string{}, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := newLexicalRepo(t, "republished-"+tt.model, runspec.IndexFormat, map[string]string{"old.go": "func old() {}"},
				map[string][]float32{"old.go": e(0)})
			ctx := t.Context()
			embed := similarEmbedFunc(func(_ context.Context, inputs []string) ([][]float32, int64, error) {
				if err := r.st.WithAccount(ctx, r.account.ID(), func(tx pgx.Tx) error {
					var run string
					if err := tx.QueryRow(ctx, `INSERT INTO index_runs(account_id,repository_id,commit_sha,embed_model,embed_dims,mode,status,index_format)
						VALUES($1,$2,'def',$3,8,'full','completed',$4) RETURNING id`, r.account.ID(), r.id, tt.model, runspec.IndexFormat).Scan(&run); err != nil {
						return err
					}
					if tt.model == "embed" {
						if err := store.InsertIndexChunks(ctx, tx, r.account.ID(), r.id, run, []store.StagedChunk{
							{Path: "renewed.go", Language: "go", StartLine: 1, EndLine: 1, Text: "func renewed() {}"},
						}, [][]float32{e(0)}); err != nil {
							return err
						}
					}
					return store.ActivateGeneration(ctx, tx, r.id, run)
				}); err != nil {
					return nil, 0, err
				}
				out := make([][]float32, len(inputs))
				for i := range out {
					out[i] = e(0)
				}
				return out, 1, nil
			})
			reg := prometheus.NewRegistry()
			g := Server{Metrics: metrics.New(reg), Store: r.st, Current: configfile.NewCurrent(r.file), Logger: slog.New(slog.DiscardHandler),
				Embedders: &adapter.Embedders{Build: func(configfile.Embedding) model.Embedder { return embed }},
			}
			token, err := r.st.MintGatewayToken(ctx, store.GatewayGrant{
				RunID: r.runner, AccountID: r.account.ID(), RepositoryID: r.id, Model: "test/reviewer", Budget: 1000,
			}, time.Now().Add(time.Minute))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = r.st.RevokeGatewayTokens(context.Background(), r.runner) })
			req := httptest.NewRequest(http.MethodPost, "/v1/similar", strings.NewReader(`{"queries":["func renewed() {}"]}`))
			req.Header.Set("Authorization", "Bearer "+token)
			out := httptest.NewRecorder()
			g.ServeHTTP(out, req)
			if out.Code != http.StatusOK {
				t.Fatalf("status=%d: %s", out.Code, out.Body.String())
			}
			var resp contextpack.SimilarResponse
			if err := json.Unmarshal(out.Body.Bytes(), &resp); err != nil {
				t.Fatal(err)
			}
			got := make([]string, 0, len(resp.Chunks))
			for _, c := range resp.Chunks {
				got = append(got, c.Path)
			}
			if !slices.Equal(got, tt.want) || resp.Indexed != tt.indexed {
				t.Fatalf("answer %q, indexed %t; want %q, indexed %t", got, resp.Indexed, tt.want, tt.indexed)
			}
		})
	}
}

// TestSimilarSearchFillsItsLimit: a search_code request of one query keeps
// as many chunks as it asks for whenever that many qualify. Its search finds
// at least that many, and looks past the files that the file cap fills
// until the query holds that many, not just Gateway.SimilarPerQuery: q0's
// nearest eight chunks are five of big.go's and three others, and seven more
// files qualify past them; q1's twelve files of one chunk each all qualify.
func TestSimilarSearchFillsItsLimit(t *testing.T) {
	gw := tuning.Get().Gateway
	ctx := t.Context()
	st := storetest.Open(t)
	t.Setenv("TEST_SECRET", "test-key")
	file := configfiletest.Load(t, `providers:
  test:
    type: openai
    baseUrl: http://unused.invalid/v1
    apiKey: { env: TEST_SECRET }
review: { model: test/reviewer }
embedding: { model: test/embed, dims: 8, lexical: false }
apps:
  test:
    accounts: [similar-fill]
    clientId: Iv1.test
    privateKey: { env: TEST_SECRET }
    webhookSecret: { env: TEST_SECRET }
`)
	if err := st.ApplyConfig(ctx, file); err != nil {
		t.Fatal(err)
	}
	storetest.Repositories(t, st, file, "similar-fill/repo")
	if _, err := st.EnsureIndexSchema(ctx, "kritika_app", "embed", 8, ""); err != nil {
		t.Fatal(err)
	}
	account, _ := file.Account(configfile.ForgeGitHub, "similar-fill")
	repo := configfile.RepositoryID(account.ID(), "similar-fill/repo")
	// The k-th chunk near axis q leans k steps toward axis q+2: each
	// is less similar to q than the one before it, and all of them are
	// within the margin of the first. Every chunk is under the floor of the
	// other query.
	near := func(q, k int) []float32 {
		v := make([]float32, 8)
		v[q], v[q+2] = 1, 0.03*float32(k)
		return v
	}
	type placed struct {
		path string
		line int
	}
	q0 := []placed{
		{"big.go", 1}, {"o0.go", 1}, {"big.go", 2}, {"o1.go", 1}, {"big.go", 3}, {"o2.go", 1}, {"big.go", 4}, {"big.go", 5},
		{"o3.go", 1}, {"o4.go", 1}, {"o5.go", 1}, {"o6.go", 1}, {"o7.go", 1}, {"o8.go", 1}, {"o9.go", 1},
	}
	var runner string
	if err := st.WithAccount(ctx, account.ID(), func(tx pgx.Tx) error {
		var run string
		if err := tx.QueryRow(ctx, `INSERT INTO index_runs(account_id,repository_id,commit_sha,embed_model,embed_dims,mode,status)
   VALUES($1,$2,'abc','embed',8,'full','completed') RETURNING id`, account.ID(), repo).Scan(&run); err != nil {
			return err
		}
		insert := func(path string, line int, v []float32) error {
			_, err := tx.Exec(ctx, `INSERT INTO index_chunks(account_id,repository_id,index_run_id,path,start_line,end_line,text,embedding)
   VALUES($1,$2,$3,$4,$5,$5,'func f() {}',$6::halfvec)`, account.ID(), repo, run, path, line, model.VectorLiteral(v))
			return err
		}
		for k, c := range q0 {
			if err := insert("q0/"+c.path, c.line, near(0, k)); err != nil {
				return err
			}
		}
		for k := range 12 {
			if err := insert(fmt.Sprintf("q1/f%d.go", k), 1, near(1, k)); err != nil {
				return err
			}
		}
		if err := store.ActivateGeneration(ctx, tx, repo, run); err != nil {
			return err
		}
		var err error
		runner, err = store.InsertRunnerRun(ctx, tx, account.ID(), store.RunnerKindReview, "", 0)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx := context.Background()
		if err := st.WithAccount(ctx, account.ID(), func(tx pgx.Tx) error { return store.FailRunnerRun(ctx, tx, runner, "test finished") }); err != nil {
			t.Error(err)
		}
	})
	token, err := st.MintGatewayToken(ctx, store.GatewayGrant{
		RunID: runner, AccountID: account.ID(), RepositoryID: repo, Model: "test/reviewer", Budget: 1 << 20,
	}, time.Now().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	embed := similarEmbedFunc(func(_ context.Context, inputs []string) ([][]float32, int64, error) {
		out := make([][]float32, len(inputs))
		for i, in := range inputs {
			var q int
			if _, err := fmt.Sscanf(in, "q%d", &q); err != nil {
				return nil, 0, err
			}
			out[i] = near(q, 0)
		}
		return out, 1, nil
	})
	g := Server{Metrics: metrics.New(prometheus.NewRegistry()), Store: st, Current: configfile.NewCurrent(file),
		Logger:    slog.New(slog.DiscardHandler),
		Embedders: &adapter.Embedders{Build: func(configfile.Embedding) model.Embedder { return embed }},
	}
	for _, tt := range []struct {
		name  string
		query string
		limit int
		want  int
	}{
		{"past a file that fills the first window", "q0", 8, 8},
		{"a deeper first window", "q1", tuning.Get().ContextPack.SimilarLimit, 12},
	} {
		t.Run(tt.name, func(t *testing.T) {
			body, err := json.Marshal(contextpack.SimilarRequest{
				Queries: []string{tt.query}, Kind: contextpack.SimilarKindSearch, Limit: tt.limit,
			})
			if err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest(http.MethodPost, "/v1/similar", bytes.NewReader(body))
			req.Header.Set("Authorization", "Bearer "+token)
			out := httptest.NewRecorder()
			g.ServeHTTP(out, req)
			var resp contextpack.SimilarResponse
			if err := json.Unmarshal(out.Body.Bytes(), &resp); err != nil || out.Code != http.StatusOK {
				t.Fatalf("status=%d: %s", out.Code, out.Body.String())
			}
			perFile := map[string]int{}
			for _, c := range resp.Chunks {
				if !strings.HasPrefix(c.Path, tt.query+"/") {
					t.Errorf("chunk %s:%d is not near %s", c.Path, c.StartLine, tt.query)
				}
				perFile[c.Path]++
			}
			if len(resp.Chunks) != tt.want {
				t.Fatalf("%d chunks, want %d: %v", len(resp.Chunks), tt.want, perFile)
			}
			if n := perFile["q0/big.go"]; n > gw.SimilarPerFile {
				t.Fatalf("%d chunks of big.go, want at most %d", n, gw.SimilarPerFile)
			}
		})
	}
}
