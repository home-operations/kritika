//go:build integration

package gateway

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
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

type rerankFunc func(context.Context, string, []string) ([]float64, model.RerankUsage, error)

func (f rerankFunc) Rerank(ctx context.Context, query string, documents []string) ([]float64, model.RerankUsage, error) {
	return f(ctx, query, documents)
}

// TestSimilarHybridAndRerank: the lexical search adds the chunk that shares
// the query's rare name to the one the vector search finds, unless it is
// turned off. A reranker orders them, and its failure leaves the fused
// order. What the reranker reports spending is recorded as its usage and
// charged to the run's budget, and a run that the embedding left less than
// the reranking would read keeps the fused order.
func TestSimilarHybridAndRerank(t *testing.T) {
	ctx := t.Context()
	st := storetest.Open(t)
	t.Setenv("TEST_SECRET", "test-key")
	config := func(embedding string) *configfile.File {
		return configfiletest.Load(t, `providers:
  test:
    type: openai
    baseUrl: http://unused.invalid/v1
    apiKey: { env: TEST_SECRET }
review: { model: test/reviewer }
embedding: `+embedding+`
apps:
  test:
    accounts: [similar-hybrid]
    clientId: Iv1.test
    privateKey: { env: TEST_SECRET }
    webhookSecret: { env: TEST_SECRET }
`)
	}
	hybrid := config(`{ model: test/embed, dims: 8, rerank: { model: test/rerank, api: tei, candidates: 5 } }`)
	if err := st.ApplyConfig(ctx, hybrid); err != nil {
		t.Fatal(err)
	}
	storetest.Repositories(t, st, hybrid, "similar-hybrid/repo")
	if _, err := st.EnsureIndexSchema(ctx, "kritika_app", "embed", 8, ""); err != nil {
		t.Fatal(err)
	}
	account, _ := hybrid.Account(configfile.ForgeGitHub, "similar-hybrid")
	repo := configfile.RepositoryID(account.ID(), "similar-hybrid/repo")
	var runner string
	if err := st.WithAccount(ctx, account.ID(), func(tx pgx.Tx) error {
		var run string
		if err := tx.QueryRow(ctx, `INSERT INTO index_runs(account_id,repository_id,commit_sha,embed_model,embed_dims,mode,status,index_format)
			VALUES($1,$2,'abc','embed',8,'full','completed',$3) RETURNING id`, account.ID(), repo, runspec.IndexFormat).Scan(&run); err != nil {
			return err
		}
		chunks := []store.StagedChunk{
			{Path: "near.go", StartLine: 1, EndLine: 1, Text: "func near() {}"},
			{Path: "named.go", StartLine: 1, EndLine: 1, Text: "func ParseHTTPRequest() {}"},
			{Path: "far.go", StartLine: 1, EndLine: 1, Text: "func far() {}"},
		}
		vectors := [][]float32{{1, 0, 0, 0, 0, 0, 0, 0}, {0, 1, 0, 0, 0, 0, 0, 0}, {0, 0, 1, 0, 0, 0, 0, 0}}
		if err := store.InsertIndexChunks(ctx, tx, account.ID(), repo, run, chunks, vectors); err != nil {
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
	embed := similarEmbedFunc(func(_ context.Context, inputs []string) ([][]float32, int64, error) {
		return [][]float32{{1, 0, 0, 0, 0, 0, 0, 0}}, 1, nil
	})
	byNear := rerankFunc(func(_ context.Context, query string, docs []string) ([]float64, model.RerankUsage, error) {
		if query != "x := ParseHTTPRequest(r)" {
			t.Errorf("reranked for %q, want the query without a prompt", query)
		}
		scores := make([]float64, len(docs))
		for i, d := range docs {
			if strings.Contains(d, "near") {
				scores[i] = 0.9
			}
		}
		return scores, model.RerankUsage{Tokens: 11}, nil
	})
	failing := rerankFunc(func(context.Context, string, []string) ([]float64, model.RerankUsage, error) {
		return nil, model.RerankUsage{}, errors.New("reranker down")
	})
	rerankUsage := func() int64 {
		t.Helper()
		var used int64
		if err := st.WithAccount(ctx, account.ID(), func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT coalesce(sum(input_tokens), 0) FROM usage WHERE role = 'rerank' AND repository_id = $1`, repo).Scan(&used)
		}); err != nil {
			t.Fatal(err)
		}
		return used
	}
	for _, tt := range []struct {
		name      string
		file      *configfile.File
		reranker  model.Reranker
		want      []string
		failures  float64
		rerankUse int64
		// budget is the run's, 1000 when 0, and spent what the request
		// charged to it.
		budget, spent int64
	}{
		{"vector alone", config(`{ model: test/embed, dims: 8, lexical: false }`), nil, []string{"near.go:similarity 1.00"}, 0, 0, 0, 1},
		// Both searches find named.go: the vector search second of three, as
		// it finds every chunk, and the search by name first. So the fusion
		// puts it ahead of near.go, which only the vector search finds.
		{"hybrid", config(`{ model: test/embed, dims: 8 }`), nil,
			[]string{"named.go:shares ParseHTTPRequest", "near.go:similarity 1.00"}, 0, 0, 0, 1},
		{"reranked", hybrid, byNear, []string{"near.go:similarity 1.00", "named.go:shares ParseHTTPRequest"}, 0, 11, 0, 12},
		{"reranker failure", hybrid, failing, []string{"named.go:shares ParseHTTPRequest", "near.go:similarity 1.00"}, 1, 0, 0, 1},
		// The embedding spends the run's budget, so nothing is reranked.
		{"budget spent", hybrid, byNear, []string{"named.go:shares ParseHTTPRequest", "near.go:similarity 1.00"}, 0, 0, 1, 1},
		// The embedding leaves the run less than the reranking would read, so
		// nothing is reranked: the review keeps what is left.
		{"budget short of the reranking", hybrid, byNear, []string{"named.go:shares ParseHTTPRequest", "near.go:similarity 1.00"}, 0, 0, 10, 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			reg := prometheus.NewRegistry()
			g := Server{Metrics: metrics.New(reg), Store: st, Current: configfile.NewCurrent(tt.file), Logger: slog.New(slog.DiscardHandler),
				Embedders: &adapter.Embedders{Build: func(configfile.Embedding) model.Embedder { return embed }},
				Rerankers: &adapter.Rerankers{Build: func(configfile.Rerank) model.Reranker { return tt.reranker }},
			}
			token, err := st.MintGatewayToken(ctx, store.GatewayGrant{
				RunID: runner, AccountID: account.ID(), RepositoryID: repo, Model: "test/reviewer", Budget: cmp.Or(tt.budget, 1000),
			}, time.Now().Add(time.Minute))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = st.RevokeGatewayTokens(context.Background(), runner) })
			before := rerankUsage()
			resp := askSimilar(t, &g, token)
			got := make([]string, 0, len(resp.Chunks))
			for _, c := range resp.Chunks {
				got = append(got, c.Path+":"+c.Ref)
			}
			if !slices.Equal(got, tt.want) {
				t.Errorf("chunks %q, want %q", got, tt.want)
			}
			if n := rerankFailures(t, reg); n != tt.failures {
				t.Errorf("rerank failures counted %g, want %g", n, tt.failures)
			}
			if grant, err := st.LookupGatewayToken(ctx, token); err != nil || grant.Spent != tt.spent {
				t.Errorf("the run spent %d tokens (%v), want %d", grant.Spent, err, tt.spent)
			}
			if used := rerankUsage(); used-before != tt.rerankUse {
				t.Errorf("rerank usage %d tokens, want %d", used-before, tt.rerankUse)
			}
		})
	}
	// The runner stops waiting a minute after it asks. A reranking that starts
	// before the answer is due ends when it is due, not at the reranker's
	// timeout, and one that would start after it is skipped, and the search
	// by name with it: either way the fused order of what was searched is
	// answered in time. The reranker answers at once, and checks
	// the deadline it was given, so how long the request takes decides nothing.
	for _, tt := range []struct {
		name string
		// within is how long after the request arrives its answer is due.
		within  time.Duration
		reranks int32
		want    []string
	}{
		{"answer due during the reranking", 20 * time.Second, 1, []string{"named.go", "near.go"}},
		{"answer due before the reranking", 0, 0, []string{"near.go"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			defer func(was time.Duration) { similarAnswerWithin = was }(similarAnswerWithin)
			similarAnswerWithin = tt.within
			var reranks atomic.Int32
			dueChecked := rerankFunc(func(ctx context.Context, _ string, _ []string) ([]float64, model.RerankUsage, error) {
				reranks.Add(1)
				// The request arrived before this call, and its answer is due
				// within of its arrival, well before the reranker's 30s timeout.
				if due, ok := ctx.Deadline(); !ok || due.After(time.Now().Add(tt.within)) {
					t.Errorf("reranked until %v (%t), want no later than the answer, due %s after the request arrived", due, ok, tt.within)
				}
				return nil, model.RerankUsage{}, errors.New("not reranked")
			})
			g := Server{Metrics: metrics.New(prometheus.NewRegistry()), Store: st, Logger: slog.New(slog.DiscardHandler),
				Current:   configfile.NewCurrent(config(`{ model: test/embed, dims: 8, rerank: { model: test/rerank, api: tei, timeout: 30s } }`)),
				Embedders: &adapter.Embedders{Build: func(configfile.Embedding) model.Embedder { return embed }},
				Rerankers: &adapter.Rerankers{Build: func(configfile.Rerank) model.Reranker { return dueChecked }},
			}
			token, err := st.MintGatewayToken(ctx, store.GatewayGrant{
				RunID: runner, AccountID: account.ID(), RepositoryID: repo, Model: "test/reviewer", Budget: 1000,
			}, time.Now().Add(time.Minute))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = st.RevokeGatewayTokens(context.Background(), runner) })
			resp := askSimilar(t, &g, token)
			paths := make([]string, 0, len(resp.Chunks))
			for _, c := range resp.Chunks {
				paths = append(paths, c.Path)
			}
			if !slices.Equal(paths, tt.want) {
				t.Errorf("chunks %+v, want the fused order of %q", resp.Chunks, tt.want)
			}
			if n := reranks.Load(); n != tt.reranks {
				t.Errorf("%d rerank calls, want %d", n, tt.reranks)
			}
			if grant, err := st.LookupGatewayToken(ctx, token); err != nil || grant.Spent != 1 {
				t.Errorf("the run spent %d tokens (%v), want the embedding's 1 alone", grant.Spent, err)
			}
		})
	}
}

// askSimilar asks g, with the run token, for the code similar to the
// tests' query, and fails the test unless g answers it.
func askSimilar(t *testing.T, g *Server, token string) contextpack.SimilarResponse {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/similar", strings.NewReader(`{"queries":["x := ParseHTTPRequest(r)"]}`))
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

// rerankFailures counts the reranker's failed calls.
func rerankFailures(t *testing.T, reg *prometheus.Registry) float64 {
	t.Helper()
	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	var n float64
	for _, family := range families {
		if family.GetName() != "kritika_model_calls_total" {
			continue
		}
		for _, metric := range family.Metric {
			labels := map[string]string{}
			for _, label := range metric.Label {
				labels[label.GetName()] = label.GetValue()
			}
			if labels["role"] == store.RoleRerank && labels["outcome"] == "error" {
				n += metric.GetCounter().GetValue()
			}
		}
	}
	return n
}
