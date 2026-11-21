//go:build integration

package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"maps"
	"math"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
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

// lexicalAccount is the account whose repositories the search by name's
// integration tests index.
const lexicalAccount = "similar-lexical"

// lexicalConfig is an instance whose embedder searches by name, with the
// embedding given after it.
func lexicalConfig(t *testing.T, embedding string) *configfile.File {
	t.Helper()
	t.Setenv("TEST_SECRET", "test-key")
	return configfiletest.Load(t, `providers:
  test:
    type: openai
    baseUrl: http://unused.invalid/v1
    apiKey: { env: TEST_SECRET }
review: { model: test/reviewer }
embedding: `+embedding+`
apps:
  test:
    accounts: [`+lexicalAccount+`]
    clientId: Iv1.test
    privateKey: { env: TEST_SECRET }
    webhookSecret: { env: TEST_SECRET }
`)
}

// lexicalRepo is an indexed repository of lexicalAccount.
type lexicalRepo struct {
	st              *store.Store
	account         *configfile.Account
	id, run, runner string
	file            *configfile.File
}

// e is the unit vector of dimension i of the tests' eight; every query
// embeds as e(0).
func e(i int) []float32 {
	v := make([]float32, 8)
	v[i] = 1
	return v
}

// newLexicalRepo indexes chunks, path and text, with their vectors, as the
// active generation of a repository built by a build of format. A path
// path@n is a chunk of path that starts on line n, which is 1 otherwise.
func newLexicalRepo(t *testing.T, name string, format int, chunks map[string]string, vectors map[string][]float32) lexicalRepo {
	t.Helper()
	ctx := t.Context()
	st := storetest.Open(t)
	file := lexicalConfig(t, `{ model: test/embed, dims: 8 }`)
	if err := st.ApplyConfig(ctx, file); err != nil {
		t.Fatal(err)
	}
	full := lexicalAccount + "/" + name
	storetest.Repositories(t, st, file, full)
	if _, err := st.EnsureIndexSchema(ctx, "kritika_app", "embed", 8, ""); err != nil {
		t.Fatal(err)
	}
	account, _ := file.Account(configfile.ForgeGitHub, lexicalAccount)
	r := lexicalRepo{st: st, account: account, id: configfile.RepositoryID(account.ID(), full), file: file}
	if err := st.WithAccount(ctx, account.ID(), func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `INSERT INTO index_runs(account_id,repository_id,commit_sha,embed_model,embed_dims,mode,status,index_format)
			VALUES($1,$2,'abc','embed',8,'full','completed',$3) RETURNING id`, account.ID(), r.id, format).Scan(&r.run); err != nil {
			return err
		}
		var batch []store.StagedChunk
		var vecs [][]float32
		for _, key := range slices.Sorted(maps.Keys(chunks)) {
			path, line := key, 1
			if p, n, ok := strings.Cut(key, "@"); ok {
				var err error
				if line, err = strconv.Atoi(n); err != nil {
					return err
				}
				path = p
			}
			batch = append(batch, store.StagedChunk{Path: path, Language: "go", StartLine: line, EndLine: line, Text: chunks[key]})
			vecs = append(vecs, vectors[key])
		}
		if err := store.InsertIndexChunks(ctx, tx, account.ID(), r.id, r.run, batch, vecs); err != nil {
			return err
		}
		if err := store.ActivateGeneration(ctx, tx, r.id, r.run); err != nil {
			return err
		}
		var err error
		r.runner, err = store.InsertRunnerRun(ctx, tx, account.ID(), store.RunnerKindIndex, r.run, 0)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return r
}

// ask sends queries to the gateway, configured by file and with reranker,
// and returns the answer's status and its chunks as path:label.
func (r lexicalRepo) ask(t *testing.T, file *configfile.File, reranker model.Reranker, reg *prometheus.Registry, queries ...string) (int, []string) {
	t.Helper()
	return r.askWithin(t.Context(), t, file, reranker, reg, queries...)
}

// askWithin is ask within ctx.
func (r lexicalRepo) askWithin(
	ctx context.Context, t *testing.T, file *configfile.File, reranker model.Reranker, reg *prometheus.Registry, queries ...string,
) (int, []string) {
	t.Helper()
	return r.askRequest(ctx, t, file, reranker, reg, contextpack.SimilarRequest{Queries: queries})
}

// askRequest is askWithin for the request req.
func (r lexicalRepo) askRequest(
	ctx context.Context, t *testing.T, file *configfile.File, reranker model.Reranker, reg *prometheus.Registry, req contextpack.SimilarRequest,
) (int, []string) {
	t.Helper()
	embed := similarEmbedFunc(func(_ context.Context, inputs []string) ([][]float32, int64, error) {
		out := make([][]float32, len(inputs))
		for i := range out {
			out[i] = e(0)
		}
		return out, 1, nil
	})
	g := Server{Metrics: metrics.New(reg), Store: r.st, Current: configfile.NewCurrent(file), Logger: slog.New(slog.DiscardHandler),
		Embedders: &adapter.Embedders{Build: func(configfile.Embedding) model.Embedder { return embed }},
		Rerankers: &adapter.Rerankers{Build: func(configfile.Rerank) model.Reranker { return reranker }},
	}
	token, err := r.st.MintGatewayToken(ctx, store.GatewayGrant{
		RunID: r.runner, AccountID: r.account.ID(), RepositoryID: r.id, Model: "test/reviewer", Budget: 100000,
	}, time.Now().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.st.RevokeGatewayTokens(context.Background(), r.runner) })
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	hreq := httptest.NewRequestWithContext(ctx, http.MethodPost, runspec.SimilarPath, strings.NewReader(string(body)))
	hreq.Header.Set("Authorization", "Bearer "+token)
	out := httptest.NewRecorder()
	g.ServeHTTP(out, hreq)
	if out.Code != http.StatusOK {
		return out.Code, nil
	}
	var resp contextpack.SimilarResponse
	if err := json.Unmarshal(out.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	got := make([]string, 0, len(resp.Chunks))
	for _, c := range resp.Chunks {
		got = append(got, c.Path+":"+c.Ref)
	}
	return http.StatusOK, got
}

// widgets are chunks of which one holds the name widgetFactory, and seven
// the word widget, which is too common to find a chunk alone but not to
// search for. Only near.go is near the queries.
var widgets = map[string]string{
	"near.go": "func near() {}", "factory.go": "func widgetFactory() {}",
	"w1.go": "var widget = 1", "w2.go": "var widget = 2", "w3.go": "var widget = 3",
	"w4.go": "var widget = 4", "w5.go": "var widget = 5", "w6.go": "var widget = 6",
}

func widgetVectors() map[string][]float32 {
	out := map[string][]float32{"near.go": e(0), "factory.go": {0.3, 0.954, 0, 0, 0, 0, 0, 0}}
	for _, w := range []string{"w1.go", "w2.go", "w3.go", "w4.go", "w5.go", "w6.go"} {
		out[w] = e(2)
	}
	return out
}

// TestSimilarLexicalFloor: a query's one common word finds no chunk by name,
// so a close vector hit is not crowded out by chunks that merely share it,
// while a compound name finds the chunk that holds it. A generation built
// before postings is searched by vector alone.
func TestSimilarLexicalFloor(t *testing.T) {
	current := newLexicalRepo(t, "floor", runspec.IndexFormat, widgets, widgetVectors())
	older := newLexicalRepo(t, "floor-old", 1, widgets, widgetVectors())
	for _, tt := range []struct {
		name  string
		repo  lexicalRepo
		query string
		want  []string
	}{
		{"one common word", current, "widget", []string{"near.go:similarity 1.00"}},
		{"a compound name", current, "widgetFactory()", []string{"factory.go:shares widgetFactory", "near.go:similarity 1.00"}},
		{"a generation without postings", older, "widgetFactory()", []string{"near.go:similarity 1.00"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			code, got := tt.repo.ask(t, tt.repo.file, nil, prometheus.NewRegistry(), tt.query)
			if code != http.StatusOK || !slices.Equal(got, tt.want) {
				t.Errorf("answer = %d %q, want 200 %q", code, got, tt.want)
			}
		})
	}
}

// TestSimilarLexicalSmallGeneration: in a generation of a few chunks, a word
// that every one of them holds weighs next to nothing, so it finds no chunk
// by name, though it would be its query's best match by name. A name that
// one chunk holds still finds it.
func TestSimilarLexicalSmallGeneration(t *testing.T) {
	r := newLexicalRepo(t, "small", runspec.IndexFormat, map[string]string{
		"near.go": "func near() { widget() }", "w1.go": "var widget = 1", "w2.go": "var widget = 2",
		"rare.go": "func zebraCrossing() { widget() }",
	}, map[string][]float32{"near.go": e(0), "w1.go": e(2), "w2.go": e(2), "rare.go": e(3)})
	for _, tt := range []struct {
		query string
		want  []string
	}{
		{"widget", []string{"near.go:similarity 1.00"}},
		{"zebraCrossing()", []string{"rare.go:shares zebraCrossing", "near.go:similarity 1.00"}},
	} {
		t.Run(tt.query, func(t *testing.T) {
			code, got := r.ask(t, r.file, nil, prometheus.NewRegistry(), tt.query)
			if code != http.StatusOK || !slices.Equal(got, tt.want) {
				t.Errorf("answer = %d %q, want 200 %q", code, got, tt.want)
			}
		})
	}
}

// TestSimilarFileCapBackfills: when one file holds a query's nearest chunks,
// those past its cap give their places to the query's next chunks of other
// files, so the query is answered by as many files as qualify. A reranker
// sent fewer candidates than the search finds without one leaves the query
// as deep a pool: the chunks it is not sent follow those it scores, and all
// of them keep their fused order when it fails.
func TestSimilarFileCapBackfills(t *testing.T) {
	at := func(similarity float64) []float32 {
		return []float32{float32(similarity), float32(math.Sqrt(1 - similarity*similarity)), 0, 0, 0, 0, 0, 0}
	}
	r := newLexicalRepo(t, "file-cap", runspec.IndexFormat, map[string]string{
		"a.go@1": "func one() {}", "a.go@10": "func two() {}", "a.go@20": "func three() {}", "a.go@30": "func four() {}",
		"b.go@1": "func five() {}",
	}, map[string][]float32{"a.go@1": at(0.99), "a.go@10": at(0.98), "a.go@20": at(0.97), "a.go@30": at(0.96), "b.go@1": at(0.95)})
	fourCandidates := lexicalConfig(t, `{ model: test/embed, dims: 8, rerank: { model: test/rerank, api: tei, candidates: 4 } }`)
	var sent []int
	reversed := rerankFunc(func(_ context.Context, _ string, docs []string) ([]float64, model.RerankUsage, error) {
		sent = append(sent, len(docs))
		scores := make([]float64, len(docs))
		for i := range scores {
			scores[i] = float64(i)
		}
		return scores, model.RerankUsage{}, nil
	})
	failing := rerankFunc(func(context.Context, string, []string) ([]float64, model.RerankUsage, error) {
		return nil, model.RerankUsage{}, errors.New("reranker down")
	})
	for _, tt := range []struct {
		name     string
		file     *configfile.File
		reranker model.Reranker
		want     []string
		wantSent []int
	}{
		{"no reranker", r.file, nil, []string{"a.go:similarity 0.99", "a.go:similarity 0.98", "b.go:similarity 0.95"}, nil},
		{"a failing reranker", fourCandidates, failing, []string{"a.go:similarity 0.99", "a.go:similarity 0.98", "b.go:similarity 0.95"}, nil},
		// It puts a.go's last first, and is not sent b.go.
		{"a reranker", fourCandidates, reversed, []string{"a.go:similarity 0.96", "a.go:similarity 0.97", "b.go:similarity 0.95"}, []int{4}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			sent = nil
			code, got := r.ask(t, tt.file, tt.reranker, prometheus.NewRegistry(), "zzyzx quux")
			if code != http.StatusOK || !slices.Equal(got, tt.want) {
				t.Fatalf("answer = %d %q, want 200 %q", code, got, tt.want)
			}
			if !slices.Equal(sent, tt.wantSent) {
				t.Errorf("sent the reranker %v candidates, want %v", sent, tt.wantSent)
			}
		})
	}
}

// TestSimilarLexicalTies: of chunks that hold a query's names alike, the
// search by name keeps the one nearest the query.
func TestSimilarLexicalTies(t *testing.T) {
	r := newLexicalRepo(t, "ties", runspec.IndexFormat,
		map[string]string{"a.go": "func leaseSlots() {}", "b.go": "func leaseSlots() {}", "c.go": "func other() {}", "d.go": "func fourth() {}",
			"e.go": "func fifth() {}"},
		map[string][]float32{"a.go": e(2), "b.go": e(0), "c.go": e(3), "d.go": e(3), "e.go": e(3)})
	var got []string
	if err := r.st.WithAccount(t.Context(), r.account.ID(), func(tx pgx.Tx) error {
		hits, err := lexicalHits(t.Context(), tx, r.run, []string{"leaseSlots()"}, [][]float32{e(0)}, store.Exclusions{}, 1)
		for _, h := range hits {
			got = append(got, h.Chunk.Path)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, []string{"b.go"}) {
		t.Fatalf("search by name kept %q, want the nearer of the two that hold leaseSlots, b.go", got)
	}
}

// TestSimilarLexicalReadsPostings: the search by name reads from index_chunks
// the chunks it ranks, not every chunk of the generation.
func TestSimilarLexicalReadsPostings(t *testing.T) {
	chunks := map[string]string{"rare.go": "func zebraCrossing() { serveWidget() }"}
	vectors := map[string][]float32{"rare.go": e(1)}
	for i := range 300 {
		path := "handler" + strings.Repeat("x", i%7) + string(rune('a'+i%26)) + string(rune('a'+i/26)) + ".go"
		chunks[path], vectors[path] = "func handler() { serveWidget() }", e(2)
	}
	r := newLexicalRepo(t, "postings", runspec.IndexFormat, chunks, vectors)
	ctx := t.Context()
	// Analyzed, as autovacuum leaves a table, so that the plan reads a chunk by
	// its key.
	owner, err := pgx.Connect(ctx, storetest.Env(t, "KRITIKA_TEST_OWNER_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = owner.Close(context.Background()) }()
	if _, err := owner.Exec(ctx, `ANALYZE index_chunks, index_postings`); err != nil {
		t.Fatal(err)
	}
	// A connection of its own, whose counts of rows read are flushed before
	// the search, so that they hold the search's alone.
	conn, err := pgx.Connect(ctx, storetest.Env(t, "KRITIKA_TEST_APP_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(context.Background()) }()
	if _, err := conn.Exec(ctx, `SELECT pg_stat_force_next_flush()`); err != nil {
		t.Fatal(err)
	}
	var got []string
	var read int64
	if err := pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT set_config('app.account_id', $1, true)`, r.account.ID()); err != nil {
			return err
		}
		hits, err := lexicalHits(ctx, tx, r.run, []string{"zebraCrossing(serveWidget)"}, [][]float32{e(0)}, store.Exclusions{}, 4)
		if err != nil {
			return err
		}
		for _, h := range hits {
			got = append(got, h.Chunk.Path)
		}
		return tx.QueryRow(ctx, `SELECT coalesce(sum(seq_tup_read + coalesce(idx_tup_fetch, 0)), 0)::bigint
			FROM pg_stat_xact_user_tables WHERE relname LIKE 'index\_chunks%'`).Scan(&read)
	}); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, []string{"rare.go"}) || read > 10 {
		t.Fatalf("search by name found %q reading %d rows of index_chunks, want rare.go reading a few of its 301", got, read)
	}
}

// TestSimilarLexicalFailure: when the search by name fails, the request is
// answered by the vector search alone, and the failure is counted. A table
// without its chunks' words column still has their postings, which are what
// the search by name reads, so it still finds the chunk by its name.
func TestSimilarLexicalFailure(t *testing.T) {
	r := newLexicalRepo(t, "failure", runspec.IndexFormat, widgets, widgetVectors())
	owner, err := pgx.Connect(t.Context(), storetest.Env(t, "KRITIKA_TEST_OWNER_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = owner.Close(context.Background()) }()
	for _, tt := range []struct {
		name           string
		damage, repair string
		failures       float64
		want           []string
	}{
		// A table that an older leader rebuilt mid-rollout lacks the column.
		{"the chunks' words are gone", `ALTER TABLE index_chunks RENAME COLUMN lexemes TO lexemes_hidden`,
			`ALTER TABLE index_chunks RENAME COLUMN lexemes_hidden TO lexemes`, 0,
			[]string{"factory.go:shares widgetFactory", "near.go:similarity 1.00"}},
		{"the postings cannot be read", `REVOKE SELECT ON index_postings FROM kritika_app`,
			`GRANT SELECT ON index_postings TO kritika_app`, 1, []string{"near.go:similarity 1.00"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := owner.Exec(t.Context(), tt.damage); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if _, err := owner.Exec(context.Background(), tt.repair); err != nil {
					t.Errorf("repair %q: %v", tt.repair, err)
				}
			})
			reg := prometheus.NewRegistry()
			code, got := r.ask(t, r.file, nil, reg, "widgetFactory()")
			if code != http.StatusOK || !slices.Equal(got, tt.want) {
				t.Fatalf("answer = %d %q, want 200 %q", code, got, tt.want)
			}
			if n := lexicalFailures(t, reg); n != tt.failures {
				t.Errorf("search-by-name failures counted %g, want %g", n, tt.failures)
			}
		})
	}
	// Hiding the words keeps them: the table is shared, and a publication
	// reads them to update the postings.
	var lost int
	if err := owner.QueryRow(t.Context(), `SELECT count(*) FROM index_chunks WHERE index_run_id = $1 AND lexemes IS NULL`, r.run).
		Scan(&lost); err != nil || lost != 0 {
		t.Fatalf("%d of the generation's chunks lost their words (%v)", lost, err)
	}
}

// lexicalFailures counts the failed searches by name.
func lexicalFailures(t *testing.T, reg *prometheus.Registry) float64 {
	t.Helper()
	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	var n float64
	for _, family := range families {
		if family.GetName() != "kritika_lexical_search_seconds" {
			continue
		}
		for _, metric := range family.Metric {
			for _, label := range metric.Label {
				if label.GetName() == "outcome" && label.GetValue() == "error" {
					n += float64(metric.GetHistogram().GetSampleCount())
				}
			}
		}
	}
	return n
}

// TestSimilarRerankSlotWait: a reranker whose every slot is held waits no
// longer than its timeout for one, and the fused order answers.
func TestSimilarRerankSlotWait(t *testing.T) {
	r := newLexicalRepo(t, "rerank-wait", runspec.IndexFormat, widgets, widgetVectors())
	file := lexicalConfig(t, `{ model: test/embed, dims: 8, rerank: { model: test/rerank, api: tei, timeout: 1s } }`)
	for job := range int64(configfile.DefaultConcurrency) {
		lease, err := r.st.AcquireLease(t.Context(), r.account.ID(), "rerank:rerank", configfile.DefaultConcurrency, 424242+job)
		if err != nil || lease == nil {
			t.Fatalf("hold a rerank slot: %v, %v", lease, err)
		}
		t.Cleanup(func() { _, _ = lease.Release(context.Background()) })
	}
	reranked := rerankFunc(func(context.Context, string, []string) ([]float64, model.RerankUsage, error) {
		t.Error("reranked without a slot")
		return nil, model.RerankUsage{}, nil
	})
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()
	started := time.Now()
	code, got := r.askWithin(ctx, t, file, reranked, prometheus.NewRegistry(), "widgetFactory()")
	if took := time.Since(started); took > 4*time.Second {
		t.Fatalf("the request took %s waiting for a rerank slot, want about the reranker's 1s timeout", took.Round(time.Second))
	}
	if want := []string{"factory.go:shares widgetFactory", "near.go:similarity 1.00"}; code != http.StatusOK || !slices.Equal(got, want) {
		t.Fatalf("answer = %d %q, want 200 %q", code, got, want)
	}
}

// TestSimilarRerankUsage: each reranked request records its usage as the
// reranker's, whether Cohere reports tokens, or search units alone.
func TestSimilarRerankUsage(t *testing.T) {
	r := newLexicalRepo(t, "rerank-usage", runspec.IndexFormat, widgets, widgetVectors())
	file := lexicalConfig(t, `{ model: test/embed, dims: 8, rerank: { model: test/rerank, api: cohere } }`)
	usage := func() (rows, tokens int64) {
		t.Helper()
		if err := r.st.WithAccount(t.Context(), r.account.ID(), func(tx pgx.Tx) error {
			return tx.QueryRow(t.Context(), `SELECT count(*), coalesce(sum(input_tokens), 0) FROM usage
				WHERE role = 'rerank' AND repository_id = $1`, r.id).Scan(&rows, &tokens)
		}); err != nil {
			t.Fatal(err)
		}
		return rows, tokens
	}
	for _, tt := range []struct {
		name, meta string
		tokens     int64
	}{
		{"search units alone", `{"billed_units": {"search_units": 1}}`, 0},
		{"tokens beside the billed units", `{"billed_units": {"search_units": 1}, "tokens": {"input_tokens": 150}}`, 150},
		{"billed tokens", `{"billed_units": {"input_tokens": 120}}`, 120},
	} {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				var in struct{ Documents []string }
				if err := json.NewDecoder(req.Body).Decode(&in); err != nil {
					t.Error(err)
				}
				results := make([]map[string]any, len(in.Documents))
				for i := range results {
					results[i] = map[string]any{"index": i, "relevance_score": 0.5}
				}
				out, _ := json.Marshal(map[string]any{"results": results, "meta": json.RawMessage(tt.meta)})
				_, _ = w.Write(out)
			}))
			defer srv.Close()
			rows, tokens := usage()
			code, _ := r.ask(t, file, model.NewReranker(model.RerankCohere, srv.URL, "key", "rerank-v3.5", time.Minute),
				prometheus.NewRegistry(), "widgetFactory()")
			afterRows, afterTokens := usage()
			if code != http.StatusOK || afterRows-rows != 1 || afterTokens-tokens != tt.tokens {
				t.Fatalf("answer %d recorded %d rerank usage rows of %d tokens, want 200 and one of %d",
					code, afterRows-rows, afterTokens-tokens, tt.tokens)
			}
		})
	}
}

// TestSimilarPartialRerank: when the reranker answers some of a request's
// queries and not others, their best chunks are compared on the fused scale
// that every query has, not the reranker's against the fusion's.
func TestSimilarPartialRerank(t *testing.T) {
	r := newLexicalRepo(t, "partial-rerank", runspec.IndexFormat, widgets, widgetVectors())
	file := lexicalConfig(t, `{ model: test/embed, dims: 8, rerank: { model: test/rerank, api: tei, candidates: 5 } }`)
	partial := rerankFunc(func(_ context.Context, query string, docs []string) ([]float64, model.RerankUsage, error) {
		if query != "widget" {
			return nil, model.RerankUsage{}, errors.New("reranker down")
		}
		scores := make([]float64, len(docs))
		for i := range scores {
			scores[i] = 0.9
		}
		return scores, model.RerankUsage{Tokens: 1}, nil
	})
	// widget's best, near.go, only the vector search finds; widgetFactory's,
	// factory.go, both searches find.
	code, got := r.askRequest(t.Context(), t, file, partial, prometheus.NewRegistry(),
		contextpack.SimilarRequest{Queries: []string{"widget", "widgetFactory()"}, Limit: 1})
	if want := []string{"factory.go:shares widgetFactory"}; code != http.StatusOK || !slices.Equal(got, want) {
		t.Fatalf("answer = %d %q, want 200 %q", code, got, want)
	}
}

// TestSearchByNameClaims: what the models docs' "Search by name" says of a
// search through the gateway. An identifier is found whatever its
// convention, a chunk found by name alone shows the names it shares rather
// than a similarity, a rarer name weighs more than a common one, and a chunk
// that both searches find comes before one that one search finds.
func TestSearchByNameClaims(t *testing.T) {
	r := newLexicalRepo(t, "claims", runspec.IndexFormat, map[string]string{
		"near.go":  "func near() {}",
		"snake.go": "def parse_http_request(): pass",
		"both.go":  "func fuseName() {}",
		"rare.go":  "func rareName() {}",
		"c1.go":    "func commonName() {}", "c2.go": "var commonName = 2", "c3.go": "var commonName = 3",
	}, map[string][]float32{
		"near.go": e(0), "snake.go": e(3), "both.go": {0.95, 0.3122, 0, 0, 0, 0, 0, 0},
		"rare.go": e(4), "c1.go": e(5), "c2.go": e(5), "c3.go": e(5),
	})
	t.Run("splitting and the label of a chunk found by name alone", func(t *testing.T) {
		code, got := r.ask(t, r.file, nil, prometheus.NewRegistry(), "req := parseHTTPRequest(r)")
		if code != http.StatusOK || !slices.Contains(got, "snake.go:shares parseHTTPRequest") {
			t.Fatalf("answer = %d %q, want snake.go found by its snake_case name, labelled by it alone", code, got)
		}
	})
	t.Run("fusion order", func(t *testing.T) {
		want := []string{"both.go:similarity 0.95; shares fuseName", "near.go:similarity 1.00"}
		if code, got := r.ask(t, r.file, nil, prometheus.NewRegistry(), "fuseName()"); code != http.StatusOK || !slices.Equal(got, want) {
			t.Fatalf("answer = %d %q, want %q", code, got, want)
		}
	})
	t.Run("rarity", func(t *testing.T) {
		var got []string
		if err := r.st.WithAccount(t.Context(), r.account.ID(), func(tx pgx.Tx) error {
			hits, err := lexicalHits(t.Context(), tx, r.run, []string{"commonName(rareName)"}, [][]float32{e(0)}, store.Exclusions{}, 4)
			for _, h := range hits {
				got = append(got, h.Chunk.Path)
			}
			return err
		}); err != nil {
			t.Fatal(err)
		}
		if len(got) != 4 || got[0] != "rare.go" {
			t.Fatalf("search by name found %q, want rare.go, which holds the rarer name, first of four", got)
		}
	})
}
