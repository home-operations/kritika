//go:build integration

package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
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
repositories:
  similar-usage/repo: {}
`)
	if err := st.ApplyConfig(ctx, file); err != nil {
		t.Fatal(err)
	}
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

// TestSimilarPrompts: a query reaches the embedder led by the query
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
embedding: { model: test/embed, dims: 8, queryPrompt: "query: ", documentPrompt: "passage: ", similarFloor: 0.5 }
apps:
  test:
    accounts: [similar-prompts]
    clientId: Iv1.test
    privateKey: { env: TEST_SECRET }
    webhookSecret: { env: TEST_SECRET }
repositories:
  similar-prompts/repo: {}
`)
	if err := st.ApplyConfig(ctx, file); err != nil {
		t.Fatal(err)
	}
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
	search := func() contextpack.SimilarResponse {
		t.Helper()
		token, err := st.MintGatewayToken(ctx, store.GatewayGrant{
			RunID: runner, AccountID: account.ID(), RepositoryID: repo, Model: "test/reviewer", Budget: 1000,
		}, time.Now().Add(time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPost, "/v1/similar", strings.NewReader(`{"queries":["hello"]}`))
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
		return resp
	}
	seed("")
	if resp := search(); resp.Indexed || len(resp.Chunks) != 0 {
		t.Fatalf("an index built without the document prompt answered: %+v", resp)
	}
	seed("passage: ")
	if resp := search(); !resp.Indexed || len(resp.Chunks) != 1 {
		t.Fatalf("the index built with the document prompt did not answer: %+v", resp)
	}
	if !slices.Equal(embedded, []string{"query: hello"}) {
		t.Fatalf("embedded %q, want the query led by its prompt", embedded)
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
