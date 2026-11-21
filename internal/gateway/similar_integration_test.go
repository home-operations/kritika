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
// for, up to contextpack.SimilarLimit, so that a run that drops some still
// has ten to show, and the gateway's ten when the request asks for none.
func TestSimilarAnswerLimit(t *testing.T) {
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
		{"none asked for", 0, similarMax},
		{"fewer", 3, 3},
		{"the most", contextpack.SimilarLimit, contextpack.SimilarLimit},
		{"past the most", contextpack.SimilarLimit + 10, contextpack.SimilarLimit},
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
	// similarDepth deep, also found four of another's, under the floor. Each
	// query measured its best match.
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
