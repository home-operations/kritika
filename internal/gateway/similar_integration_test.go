//go:build integration

package gateway

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/home-operations/kritika/internal/adapter"
	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/configfile/configfiletest"
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
	if _, err := st.EnsureIndexSchema(ctx, "kritika_app", "embed", 8); err != nil {
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
	}{
		{"success", 7, http.StatusOK, 1},
		{"partial failure", 7, http.StatusBadGateway, 1},
		{"search failure", 7, http.StatusBadGateway, 1},
		{"cancellation", 7, http.StatusBadGateway, 1},
		{"unreported usage", 0, http.StatusOK, 1},
		{"unpaid failure", 0, http.StatusBadGateway, 0},
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
			var wantSearches uint64
			if tt.status == http.StatusOK || tt.name == "search failure" {
				wantSearches = 1
			}
			outcome := "ok"
			if tt.name == "search failure" {
				outcome = "error"
			}
			checkVectorSearchMetric(t, reg, wantSearches, outcome)
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
					want := wantOutcome
					if label.GetValue() != want {
						t.Errorf("search outcome = %q, want %q", label.GetValue(), want)
					}
				}
			}
		}
	}
	if searches != wantSearches {
		t.Errorf("database searches = %d, want %d; embedding failures must not count", searches, wantSearches)
	}
}
