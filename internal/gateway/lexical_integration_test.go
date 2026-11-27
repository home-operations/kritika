//go:build integration

package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	"github.com/home-operations/kritika/internal/tuning"
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

// TestSimilarFileCapLooksPastAFullWindow: when one file holds every chunk
// that the vector search finds for a query, and the cap keeps only two of
// them, the search looks again past that file for the query's other chunks
// that qualify, and finds none that do not.
func TestSimilarFileCapLooksPastAFullWindow(t *testing.T) {
	at := func(similarity float64) []float32 {
		return []float32{float32(similarity), float32(math.Sqrt(1 - similarity*similarity)), 0, 0, 0, 0, 0, 0}
	}
	chunks, vectors := map[string]string{}, map[string][]float32{}
	for i := range tuning.Get().Gateway.SimilarDepth() {
		key := fmt.Sprintf("a.go@%d", 10*i+1)
		chunks[key], vectors[key] = fmt.Sprintf("func a%d() {}", i), at(0.99-0.01*float64(i))
	}
	chunks["b.go@1"], vectors["b.go@1"] = "func b() {}", at(0.91)
	chunks["c.go@1"], vectors["c.go@1"] = "func c() {}", at(0.4)
	r := newLexicalRepo(t, "full-window", runspec.IndexFormat, chunks, vectors)
	code, got := r.ask(t, r.file, nil, prometheus.NewRegistry(), "zzyzx quux")
	want := []string{"a.go:similarity 0.99", "a.go:similarity 0.98", "b.go:similarity 0.91"}
	if code != http.StatusOK || !slices.Equal(got, want) {
		t.Fatalf("answer = %d %q, want 200 %q", code, got, want)
	}
}

// TestSimilarFileCapAnswerDue: a request whose answer is due once its
// vectors are searched makes no look past a full window, and answers with
// the capped hits that the first window found, rather than look on past
// the runner's wait or fail the request.
func TestSimilarFileCapAnswerDue(t *testing.T) {
	at := func(similarity float64) []float32 {
		return []float32{float32(similarity), float32(math.Sqrt(1 - similarity*similarity)), 0, 0, 0, 0, 0, 0}
	}
	chunks, vectors := map[string]string{}, map[string][]float32{}
	for i := range tuning.Get().Gateway.SimilarDepth() {
		key := fmt.Sprintf("a.go@%d", 10*i+1)
		chunks[key], vectors[key] = fmt.Sprintf("func a%d() {}", i), at(0.99-0.01*float64(i))
	}
	chunks["b.go@1"], vectors["b.go@1"] = "func b() {}", at(0.91)
	r := newLexicalRepo(t, "full-window-due", runspec.IndexFormat, chunks, vectors)
	tuning.Override(t, func(v *tuning.Values) { v.Gateway.SimilarWithin = 0 })
	code, got := r.ask(t, r.file, nil, prometheus.NewRegistry(), "zzyzx quux")
	want := []string{"a.go:similarity 0.99", "a.go:similarity 0.98"}
	if code != http.StatusOK || !slices.Equal(got, want) {
		t.Fatalf("answer = %d %q, want 200 %q", code, got, want)
	}
}

// TestInSavepoint: a look past full files runs under a statement timeout of
// what is left of its time, none is made once that is spent, and one that
// fails leaves the transaction to the statements after it.
func TestInSavepoint(t *testing.T) {
	r := newLexicalRepo(t, "savepoint", runspec.IndexFormat, map[string]string{"a.go": "func a() {}"}, map[string][]float32{"a.go": e(0)})
	ctx := t.Context()
	if err := r.st.WithAccount(ctx, r.account.ID(), func(tx pgx.Tx) error {
		var timeout int64
		err := inSavepoint(ctx, tx, time.Now().Add(time.Hour), func(sp pgx.Tx) error {
			return sp.QueryRow(ctx, `SELECT setting::bigint FROM pg_settings WHERE name = 'statement_timeout'`).Scan(&timeout)
		})
		if err != nil || timeout <= 0 || timeout > time.Hour.Milliseconds() {
			t.Errorf("statement timeout = %dms, %v; want at most the hour left", timeout, err)
		}
		called := false
		if err := inSavepoint(ctx, tx, time.Now(), func(pgx.Tx) error { called = true; return nil }); !errors.Is(err, errSearchDue) || called {
			t.Errorf("past its deadline: %v, searched %v; want errSearchDue, no search", err, called)
		}
		err = inSavepoint(ctx, tx, time.Now().Add(time.Hour), func(sp pgx.Tx) error {
			_, err := sp.Exec(ctx, `SELECT 1 / 0`)
			return err
		})
		if err == nil {
			t.Error("a failing statement: nil, want its error")
		}
		var one int
		if err := tx.QueryRow(ctx, `SELECT 1`).Scan(&one); err != nil {
			t.Errorf("the transaction after a failed look: %v", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// TestSimilarLexicalLooksPastAFullWindow: when one file holds every chunk
// that the search by name finds for a query, all of them under the floor,
// and the cap keeps only two of them, the search by name looks again past
// that file for the query's other chunks that hold its names. The other
// files make the name rare enough to search for.
func TestSimilarLexicalLooksPastAFullWindow(t *testing.T) {
	chunks, vectors := map[string]string{}, map[string][]float32{}
	for i := range tuning.Get().Gateway.SimilarDepth() {
		key := fmt.Sprintf("a.go@%d", 10*i+1)
		chunks[key], vectors[key] = "func widgetFactory() {}", e(2)
	}
	chunks["b.go@1"], vectors["b.go@1"] = "func widgetFactory() {}", e(2)
	for i := range 2 * tuning.Get().Gateway.SimilarDepth() {
		key := fmt.Sprintf("other%d.go", i)
		chunks[key], vectors[key] = fmt.Sprintf("func other%d() {}", i), e(3)
	}
	r := newLexicalRepo(t, "lexical-full-window", runspec.IndexFormat, chunks, vectors)
	code, got := r.ask(t, r.file, nil, prometheus.NewRegistry(), "widgetFactory()")
	want := []string{"a.go:shares widgetFactory", "a.go:shares widgetFactory", "b.go:shares widgetFactory"}
	if code != http.StatusOK || !slices.Equal(got, want) {
		t.Fatalf("answer = %d %q, want 200 %q", code, got, want)
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
		hits, err := lexicalHits(t.Context(), tx, r.run, []string{"leaseSlots()"}, nil, [][]float32{e(0)}, store.Exclusions{}, 1)
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
		hits, err := lexicalHits(ctx, tx, r.run, []string{"zebraCrossing(serveWidget)"}, nil, [][]float32{e(0)}, store.Exclusions{}, 4)
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
// longer than its timeout for one, and the fused order answers. The answer is
// due an hour on and the slots are held for good, so only the reranker's
// timeout can end the wait. The test's minute is a guard against a hang, not
// a bound on the wait: TestRerankDeadline shows that the timeout is the
// configured one rather than the default.
func TestSimilarRerankSlotWait(t *testing.T) {
	r := newLexicalRepo(t, "rerank-wait", runspec.IndexFormat, widgets, widgetVectors())
	file := lexicalConfig(t, `{ model: test/embed, dims: 8, rerank: { model: test/rerank, api: tei, timeout: 1s } }`)
	slots := file.Slots(r.account, file.Embedding.Rerank.Ref)
	for job := range int64(slots.Limit) {
		lease, err := r.st.AcquireLease(t.Context(), r.account.ID(), "rerank:rerank", slots, 424242+job)
		if err != nil || lease == nil {
			t.Fatalf("hold a rerank slot: %v, %v", lease, err)
		}
		t.Cleanup(func() { _, _ = lease.Release(context.Background()) })
	}
	reranked := rerankFunc(func(context.Context, string, []string) ([]float64, model.RerankUsage, error) {
		t.Error("reranked without a slot")
		return nil, model.RerankUsage{}, nil
	})
	tuning.Override(t, func(v *tuning.Values) { v.Gateway.SimilarWithin = time.Hour })
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	code, got := r.askWithin(ctx, t, file, reranked, prometheus.NewRegistry(), "widgetFactory()")
	if ctx.Err() != nil {
		t.Fatal("the request waited out the test's minute for a rerank slot, past the reranker's 1s timeout")
	}
	if want := []string{"factory.go:shares widgetFactory", "near.go:similarity 1.00"}; code != http.StatusOK || !slices.Equal(got, want) {
		t.Fatalf("answer = %d %q, want 200 %q", code, got, want)
	}
}

// TestSimilarRerankUsage: each reranked request records its usage as the
// reranker's, whether Cohere reports tokens, or search units alone, and so
// does one that Cohere billed a search unit for but whose ranks are refused,
// which keeps the fused order.
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
		// invalid ranks each document at an index past those sent.
		invalid bool
	}{
		{"search units alone", `{"billed_units": {"search_units": 1}}`, 0, false},
		{"tokens beside the billed units", `{"billed_units": {"search_units": 1}, "tokens": {"input_tokens": 150}}`, 150, false},
		{"billed tokens", `{"billed_units": {"input_tokens": 120}}`, 120, false},
		{"search units for invalid ranks", `{"billed_units": {"search_units": 1}}`, 0, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				var in struct{ Documents []string }
				if err := json.NewDecoder(req.Body).Decode(&in); err != nil {
					t.Error(err)
				}
				results := make([]map[string]any, len(in.Documents))
				for i := range results {
					index := i
					if tt.invalid {
						index += len(in.Documents)
					}
					results[i] = map[string]any{"index": index, "relevance_score": 0.5}
				}
				out, _ := json.Marshal(map[string]any{"results": results, "meta": json.RawMessage(tt.meta)})
				_, _ = w.Write(out)
			}))
			defer srv.Close()
			rows, tokens := usage()
			code, got := r.ask(t, file, model.NewReranker(model.RerankCohere, srv.URL, "key", "rerank-v3.5", time.Minute),
				prometheus.NewRegistry(), "widgetFactory()")
			if want := []string{"factory.go:shares widgetFactory", "near.go:similarity 1.00"}; tt.invalid && !slices.Equal(got, want) {
				t.Errorf("answer = %q, want the fused order %q", got, want)
			}
			afterRows, afterTokens := usage()
			if code != http.StatusOK || afterRows-rows != 1 || afterTokens-tokens != tt.tokens {
				t.Fatalf("answer %d recorded %d rerank usage rows of %d tokens, want 200 and one of %d",
					code, afterRows-rows, afterTokens-tokens, tt.tokens)
			}
		})
	}
}

// TestSimilarRerankMonthCap: a request whose embedding brings the account to
// its tokensPerMonth is answered in the fused order, without reranking, as a
// model call admitted after it would be refused. The embedder reports a
// token a request.
func TestSimilarRerankMonthCap(t *testing.T) {
	r := newLexicalRepo(t, "rerank-cap", runspec.IndexFormat, widgets, widgetVectors())
	used, err := r.st.AccountUsage(t.Context(), r.account.ID())
	if err != nil {
		t.Fatal(err)
	}
	// The embedding's value ends where the instance's limits begin.
	file := lexicalConfig(t, fmt.Sprintf("{ model: test/embed, dims: 8, rerank: { model: test/rerank, api: tei } }\nlimits: { tokensPerMonth: %d }",
		used.Tokens+1))
	reranked := rerankFunc(func(_ context.Context, _ string, docs []string) ([]float64, model.RerankUsage, error) {
		t.Error("reranked past the month's cap")
		return make([]float64, len(docs)), model.RerankUsage{}, nil
	})
	code, got := r.ask(t, file, reranked, prometheus.NewRegistry(), "widgetFactory()")
	if want := []string{"factory.go:shares widgetFactory", "near.go:similarity 1.00"}; code != http.StatusOK || !slices.Equal(got, want) {
		t.Fatalf("answer = %d %q, want 200 %q", code, got, want)
	}
}

// TestSimilarRerankSlotFreeForUsage: a reranked request lets its slot go
// before it records its usage, so a usage write that waits on Postgres
// holds none of the reranker's slots. A trigger holds the write on an
// advisory lock that the test takes, until the slot is seen free.
func TestSimilarRerankSlotFreeForUsage(t *testing.T) {
	r := newLexicalRepo(t, "rerank-slot-usage", runspec.IndexFormat, widgets, widgetVectors())
	file := lexicalConfig(t, `{ model: test/embed, dims: 8, rerank: { model: test/slotfree, api: tei } }`)
	owner, err := pgx.Connect(t.Context(), storetest.Env(t, "KRITIKA_TEST_OWNER_URL"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = owner.Close(context.Background()) })
	const lock = 7321554
	for _, sql := range []string{
		`CREATE OR REPLACE FUNCTION test_hold_rerank_usage() RETURNS trigger LANGUAGE plpgsql AS $$
			BEGIN PERFORM pg_advisory_xact_lock(` + strconv.Itoa(lock) + `); RETURN NEW; END $$`,
		`DROP TRIGGER IF EXISTS test_hold_rerank_usage ON usage`,
		`CREATE TRIGGER test_hold_rerank_usage BEFORE INSERT ON usage FOR EACH ROW
			WHEN (NEW.role = 'rerank' AND NEW.repository_id = '` + r.id + `') EXECUTE FUNCTION test_hold_rerank_usage()`,
	} {
		if _, err := owner.Exec(t.Context(), sql); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		if _, err := owner.Exec(context.Background(), `DROP TRIGGER IF EXISTS test_hold_rerank_usage ON usage`); err != nil {
			t.Errorf("drop the usage trigger: %v", err)
		}
	})
	if _, err := owner.Exec(t.Context(), `SELECT pg_advisory_lock($1)`, lock); err != nil {
		t.Fatal(err)
	}
	unlocked := false
	unlock := func() {
		if !unlocked {
			unlocked = true
			if _, err := owner.Exec(context.Background(), `SELECT pg_advisory_unlock($1)`, lock); err != nil {
				t.Errorf("unlock: %v", err)
			}
		}
	}
	defer unlock()
	reranker := rerankFunc(func(_ context.Context, _ string, docs []string) ([]float64, model.RerankUsage, error) {
		return make([]float64, len(docs)), model.RerankUsage{Tokens: 1}, nil
	})
	answered := make(chan int, 1)
	go func() {
		code, _ := r.ask(t, file, reranker, prometheus.NewRegistry(), "widgetFactory()")
		answered <- code
	}()
	// A hang guard: the usage write reaches the lock, whatever the slot.
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	for {
		var waiting int
		if err := owner.QueryRow(ctx, `SELECT count(*) FROM pg_locks
			WHERE locktype = 'advisory' AND classid = 0 AND objid = $1 AND objsubid = 1 AND NOT granted`, lock).Scan(&waiting); err != nil {
			t.Fatalf("the rerank usage write never waited on the lock: %v", err)
		}
		if waiting > 0 {
			break
		}
		select {
		case code := <-answered:
			t.Fatalf("answered %d without recording rerank usage", code)
		case <-time.After(10 * time.Millisecond):
		}
	}
	var held int
	if err := r.st.WithAccount(t.Context(), r.account.ID(), func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT count(*) FROM model_leases
			WHERE account_id = $1 AND model_key = 'rerank:slotfree' AND job_id IS NOT NULL AND expires_at >= now()`,
			r.account.ID()).Scan(&held)
	}); err != nil {
		t.Fatal(err)
	}
	if held != 0 {
		t.Errorf("%d rerank slots held while the request's usage write waits, want none", held)
	}
	unlock()
	if code := <-answered; code != http.StatusOK {
		t.Fatalf("answer = %d, want 200", code)
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
			hits, err := lexicalHits(t.Context(), tx, r.run, []string{"commonName(rareName)"}, nil, [][]float32{e(0)}, store.Exclusions{}, 4)
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

// TestSimilarLexicalAnswerDue: a request whose answer is due by the time
// its vectors are searched is answered by the vector search alone, and the
// search by name is not made, rather than given a timeout of its own that
// would outlast the runner's wait for the vector search's hits.
func TestSimilarLexicalAnswerDue(t *testing.T) {
	r := newLexicalRepo(t, "due", runspec.IndexFormat, widgets, widgetVectors())
	tuning.Override(t, func(v *tuning.Values) { v.Gateway.SimilarWithin = 0 })
	reg := prometheus.NewRegistry()
	code, got := r.ask(t, r.file, nil, reg, "widgetFactory()")
	if want := []string{"near.go:similarity 1.00"}; code != http.StatusOK || !slices.Equal(got, want) {
		t.Fatalf("answer = %d %q, want 200 %q", code, got, want)
	}
	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, family := range families {
		if family.GetName() != "kritika_lexical_search_seconds" {
			continue
		}
		for _, metric := range family.Metric {
			if n := metric.GetHistogram().GetSampleCount(); n != 0 {
				t.Errorf("searches by name counted %d (%v), want none made", n, metric.Label)
			}
		}
	}
}

// TestSimilarLexicalQueryLanguage: a diff query's hyphen is what it is in its
// file's language, as the index has it of the chunks of that language. In
// Go, end-start is a minus between end and start, which most chunks hold, so
// it finds no chunk by name, and not the one of endStart, which a hyphen
// joining end-start would name. A query that names endStart still finds it.
// A query of no language, as an older runner sends, or of one whose names
// hold hyphens, joins end-start as it always has.
func TestSimilarLexicalQueryLanguage(t *testing.T) {
	chunks := map[string]string{"near.go": "func near() {}", "span.go": "var endStart = spanEnd()"}
	vectors := map[string][]float32{"near.go": e(0), "span.go": e(3)}
	for i := range 5 {
		key := fmt.Sprintf("range%d.go", i)
		chunks[key], vectors[key] = fmt.Sprintf("var end, start = %d, %d", i, i+1), e(2)
	}
	r := newLexicalRepo(t, "query-language", runspec.IndexFormat, chunks, vectors)
	for _, tt := range []struct {
		name      string
		query     string
		languages []string
		want      []string
	}{
		{"a minus in Go", "d := end-start", []string{"go"}, []string{"near.go:similarity 1.00"}},
		{"a minus in JavaScript", "const d = end-start;", []string{"javascript"}, []string{"near.go:similarity 1.00"}},
		{"the identifier in Go", "d := endStart", []string{"go"}, []string{"span.go:shares endStart", "near.go:similarity 1.00"}},
		{"a key in YAML", "end-start: 1", []string{"yaml"}, []string{"span.go:shares end-start", "near.go:similarity 1.00"}},
		{"no language", "d := end-start", nil, []string{"span.go:shares end-start", "near.go:similarity 1.00"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			code, got := r.askRequest(t.Context(), t, r.file, nil, prometheus.NewRegistry(), contextpack.SimilarRequest{
				Queries: []string{tt.query}, Languages: tt.languages,
			})
			if code != http.StatusOK || !slices.Equal(got, tt.want) {
				t.Errorf("answer = %d %q, want 200 %q", code, got, tt.want)
			}
		})
	}
}
