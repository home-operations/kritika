//go:build integration

package gateway

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing/object"
	"github.com/jackc/pgx/v5"

	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/configfile/configfiletest"
	"github.com/home-operations/kritika/internal/contextpack"
	"github.com/home-operations/kritika/internal/indexer"
	"github.com/home-operations/kritika/internal/model"
	"github.com/home-operations/kritika/internal/runspec"
	"github.com/home-operations/kritika/internal/store"
	"github.com/home-operations/kritika/internal/store/storetest"
	"github.com/home-operations/kritika/internal/udiff"
)

// TestRetrievalEval measures what the similar-code search finds for the
// commits of a repository's history, by the vector search alone, the
// lexical search alone, and both fused. It runs only when
// KRITIKA_RETRIEVAL_EVAL is set, as it indexes a whole tree:
//
//	KRITIKA_RETRIEVAL_EVAL=1 mise run test-integration -- -run TestRetrievalEval -v ./internal/gateway/
//
// It indexes the tree KRITIKA_EVAL_COMMITS (60) first-parent commits
// before the head of KRITIKA_EVAL_REPO (this repository), and asks, for
// each of those commits, what a review of it would: its changed regions,
// with its changed paths left out. With KRITIKA_EVAL_EMBED_URL, an
// OpenAI-compatible endpoint, it embeds with KRITIKA_EVAL_EMBED_MODEL at
// KRITIKA_EVAL_EMBED_DIMS. Without it, it embeds with hashed character
// trigrams, which measures the plumbing and not an embedder. Vectors are
// cached under KRITIKA_EVAL_CACHE.
//
// A commit's relevant code is judged two ways, neither of them a reviewer's
// judgement. Definitions: the chunks outside the change that declare a
// name, declared at most twice in the tree, that its added lines use.
// Co-change: the files that changed with its files in at least two other
// commits of the history.
func TestRetrievalEval(t *testing.T) {
	if os.Getenv("KRITIKA_RETRIEVAL_EVAL") == "" {
		t.Skip("KRITIKA_RETRIEVAL_EVAL is not set")
	}
	history, window := evalHistory(t)
	embed, dims := evalEmbedder(t)
	ix := evalIndex(t, history[window], embed, dims)
	truth := newEvalTruth(t, ix.chunks, history)
	floor, margin := evalFloat(t, "KRITIKA_EVAL_FLOOR", configfile.DefaultSimilarFloor), configfile.DefaultSimilarMargin
	variants := []string{"vector", "lexical", "hybrid"}
	var reranker model.Reranker
	candidates := evalInt(t, "KRITIKA_EVAL_RERANK_CANDIDATES", configfile.DefaultTEIRerankCandidates)
	evalDepth = evalInt(t, "KRITIKA_EVAL_DEPTH", similarDepth)
	if url := os.Getenv("KRITIKA_EVAL_RERANK_URL"); url != "" {
		reranker = model.NewReranker(model.RerankTEI, url, "", "", time.Minute)
		variants = append(variants, "vector+rerank", "hybrid+rerank")
	}
	tallies := map[string]*evalTally{}
	for _, v := range variants {
		tallies[v] = &evalTally{}
	}
	var timing evalTiming
	for _, c := range history[:window] {
		if c.NumParents() != 1 {
			continue
		}
		diff, added := commitDiff(t, c)
		queries, paths := indexer.HunkQueries(diff, configfile.DefaultIgnore)
		if len(queries) == 0 {
			continue
		}
		changed := truth.changes[c]
		defs, related := truth.defs(added, changed), truth.related(changed, changed)
		qv := embed(queries)
		// The gateway searches as deep with a reranker as without one, and
		// deeper for one sent more candidates than that.
		base, deep := evalSearches(func(depth int, timed *evalSearchTime) evalHits {
			return ix.search(t, timed, queries, qv, changed, depth)
		}, &timing, evalDepth, max(evalDepth, candidates), reranker != nil)
		for _, v := range variants {
			found := base
			if strings.HasSuffix(v, "+rerank") {
				found = deep
			}
			var cands []candidate
			f := floor
			switch strings.TrimSuffix(v, "+rerank") {
			case "vector":
				cands = fuse(found.hits, nil)
			case "lexical":
				// No similarity vouches for a chunk.
				cands, f = fuse(nil, found.named), 2
			case "hybrid":
				cands = fuse(found.hits, found.named)
			}
			counts := map[hitKey]int{}
			kept, _, _ := gateCandidates(cands, f, margin)
			if strings.HasSuffix(v, "+rerank") {
				started := time.Now()
				evalRerank(t, reranker, queries, kept, candidates)
				timing.rerank += time.Since(started)
				timing.reranks++
			}
			top := evalBest(kept, similarPerQuery)
			for q, text := range queries {
				tallies[v].addQuery(top, q, truth.defs(text, changed), truth.related([]string{paths[q]}, changed))
			}
			tallies[v].add(keepCandidates(kept, counts, similarMax), defs, related)
		}
	}
	t.Logf("floor %.2f, margin %.2f; %s", floor, margin, timing.String())
	t.Logf("%-14s %7s %7s | %5s %9s %9s | %5s %9s %9s || %5s %8s %6s | %5s %8s %6s", "variant", "commits", "kept/c", "defs",
		"hit@10", "recall", "co", "hit@10", "precision", "q-defs", "hit@4", "mrr", "q-co", "hit@4", "mrr")
	for _, v := range variants {
		t.Logf("%-14s %s", v, tallies[v])
	}
}

// evalHistory is the first-parent history of the repository's head, and
// how many of its commits the evaluation asks about.
func evalHistory(t *testing.T) ([]*object.Commit, int) {
	repo, err := git.PlainOpenWithOptions(cmp.Or(os.Getenv("KRITIKA_EVAL_REPO"), "../.."), &git.PlainOpenOptions{DetectDotGit: true})
	if err != nil {
		t.Fatal(err)
	}
	head, err := repo.Head()
	if err != nil {
		t.Fatal(err)
	}
	window := evalInt(t, "KRITIKA_EVAL_COMMITS", 60)
	var history []*object.Commit
	for c, err := repo.CommitObject(head.Hash()); err == nil; c, err = c.Parent(0) {
		history = append(history, c)
		if c.NumParents() == 0 {
			break
		}
	}
	if len(history) <= window {
		t.Fatalf("the history has %d commits, fewer than the window's %d", len(history), window)
	}
	return history, window
}

// evalIndexed is an index generation of the tree that the evaluation asks
// about.
type evalIndexed struct {
	st      *store.Store
	account string
	run     string
	chunks  []indexer.Chunk
}

// evalIndex chunks base's tree as an index run does, embeds its chunks, and
// stores them as a completed generation.
func evalIndex(t *testing.T, base *object.Commit, embed func([]string) [][]float32, dims int) evalIndexed {
	ctx := t.Context()
	tree, err := base.Tree()
	if err != nil {
		t.Fatal(err)
	}
	chunks, _, stats, err := indexer.Build(ctx, tree, nil, configfile.DefaultIgnore, indexer.DefaultOptions)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("indexed %s: %d chunks of %d files", base.Hash.String()[:12], len(chunks), stats.Files)
	staged := make([]store.StagedChunk, len(chunks))
	texts := make([]string, len(chunks))
	for i, c := range chunks {
		staged[i] = store.StagedChunk{Path: c.Path, Language: c.Language, Symbol: c.Symbol, Kind: c.Kind, Scope: c.Scope, Text: c.Text,
			StartLine: c.StartLine, EndLine: c.EndLine, Signature: c.Signature}
		texts[i] = staged[i].EmbedText("")
	}
	started := time.Now()
	vectors := embed(texts)
	t.Logf("embedded %d chunks in %s", len(texts), time.Since(started).Round(time.Second))

	st := storetest.Open(t)
	t.Setenv("TEST_SECRET", "test-key")
	file := configfiletest.Load(t, `apps:
  test:
    accounts: [retrieval-eval]
    clientId: Iv1.test
    privateKey: { env: TEST_SECRET }
    webhookSecret: { env: TEST_SECRET }
`)
	if err := st.ApplyConfig(ctx, file); err != nil {
		t.Fatal(err)
	}
	storetest.Repositories(t, st, file, "retrieval-eval/repo")
	if _, err := st.EnsureIndexSchema(ctx, "kritika_app", "eval", dims, ""); err != nil {
		t.Fatal(err)
	}
	account, _ := file.Account(configfile.ForgeGitHub, "retrieval-eval")
	repoID := configfile.RepositoryID(account.ID(), "retrieval-eval/repo")
	var run string
	if err := st.WithAccount(ctx, account.ID(), func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `INSERT INTO index_runs (account_id, repository_id, commit_sha, embed_model, embed_dims, mode, status, index_format)
			VALUES ($1, $2, $3, 'eval', $4, 'full', 'completed', $5) RETURNING id`,
			account.ID(), repoID, base.Hash.String(), dims, runspec.IndexFormat).Scan(&run); err != nil {
			return err
		}
		for start := 0; start < len(staged); start += 500 {
			end := min(start+500, len(staged))
			if err := store.InsertIndexChunks(ctx, tx, account.ID(), repoID, run, staged[start:end], vectors[start:end]); err != nil {
				return err
			}
		}
		return store.ActivateGeneration(ctx, tx, repoID, run)
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = st.WithAccount(context.Background(), account.ID(), func(tx pgx.Tx) error {
			for _, table := range []string{"index_postings", "index_chunks"} {
				if _, err := tx.Exec(context.Background(), `DELETE FROM `+table+` WHERE index_run_id = $1`, run); err != nil {
					return err
				}
			}
			return nil
		})
	})
	owner, err := pgx.Connect(ctx, storetest.Env(t, "KRITIKA_TEST_OWNER_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = owner.Close(context.Background()) }()
	if _, err := owner.Exec(ctx, `ANALYZE index_chunks, index_postings`); err != nil {
		t.Fatal(err)
	}
	return evalIndexed{st: st, account: account.ID(), run: run, chunks: chunks}
}

// evalTiming is the time the searches and the reranking of the evaluation
// took: base the searches at the depth without a reranker, deep those at a
// reranker's deeper one.
type evalTiming struct {
	base, deep evalSearchTime
	reranks    int
	rerank     time.Duration
}

// evalSearchTime is the time that requests searches took.
type evalSearchTime struct {
	requests        int
	vector, lexical time.Duration
}

func (e evalTiming) String() string {
	per := func(d time.Duration, n int) time.Duration {
		return (d / time.Duration(max(1, n))).Round(100 * time.Microsecond)
	}
	s := fmt.Sprintf("per request of %d: vector search %s, lexical search %s",
		e.base.requests, per(e.base.vector, e.base.requests), per(e.base.lexical, e.base.requests))
	if e.deep.requests > 0 {
		s += fmt.Sprintf("; at the reranker's depth, per request of %d: vector search %s, lexical search %s",
			e.deep.requests, per(e.deep.vector, e.deep.requests), per(e.deep.lexical, e.deep.requests))
	}
	if e.reranks > 0 {
		s += fmt.Sprintf("; per reranking of %d: %s", e.reranks, per(e.rerank, e.reranks))
	}
	return s
}

// evalHits is what both searches of a request found.
type evalHits struct{ hits, named []store.SimilarHit }

// evalSearches is what a request's searches find at depth, base, and, with a
// reranker, at rerankDepth, deep, each by a search of its own, as the gateway
// searches with or without one. A deeper search cut to depth is not one at
// depth: the search by name compares chunks of one weight by their vectors
// only so far past the depth, and cuts the rest by their IDs, so a deeper
// one can find a nearer chunk that one at depth never sees. One search
// serves both when the depths are the same.
func evalSearches(
	search func(depth int, timed *evalSearchTime) evalHits, timing *evalTiming, depth, rerankDepth int, reranking bool,
) (base, deep evalHits) {
	base = search(depth, &timing.base)
	if !reranking || rerankDepth == depth {
		return base, base
	}
	return base, search(rerankDepth, &timing.deep)
}

// search runs both searches of a request, depth deep, as the gateway does.
func (ix evalIndexed) search(
	t *testing.T, timed *evalSearchTime, queries []string, vectors [][]float32, exclude []string, depth int,
) evalHits {
	ctx := t.Context()
	var found evalHits
	if err := ix.st.WithAccount(ctx, ix.account, func(tx pgx.Tx) error {
		started := time.Now()
		var err error
		if found.hits, err = store.SimilarChunks(ctx, tx, ix.run, vectors, store.Exclusions{Paths: exclude}, depth); err != nil {
			return err
		}
		searched := time.Now()
		if found.named, err = lexicalHits(ctx, tx, ix.run, queries, vectors, store.Exclusions{Paths: exclude}, depth); err != nil {
			return err
		}
		timed.requests++
		timed.vector += searched.Sub(started)
		timed.lexical += time.Since(searched)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return found
}

// evalBest is each query's n best candidates, in its order.
func evalBest(cands []candidate, n int) []candidate {
	cands = slices.Clone(cands)
	slices.SortStableFunc(cands, candidateOrder)
	taken := map[int]int{}
	return slices.DeleteFunc(cands, func(c candidate) bool {
		taken[c.Query]++
		return taken[c.Query] > n
	})
}

// evalRerank scores each query's n best candidates as the gateway's rerank
// does, and, as it fails unless every query is reranked, compares queries by
// the reranker's scores.
func evalRerank(t *testing.T, reranker model.Reranker, queries []string, cands []candidate, n int) {
	for q, idx := range rerankBatches(cands, n) {
		docs := make([]string, len(idx))
		for j, i := range idx {
			docs[j] = rerankDocument(cands[i].Chunk)
		}
		scores, _, err := reranker.Rerank(t.Context(), queries[q], docs)
		if err != nil {
			t.Fatal(err)
		}
		rescore(cands, q, idx, scores)
	}
	rerankedAll(cands)
}

// evalTruth is what the evaluation judges an answer by.
type evalTruth struct {
	// declared are the chunks that declare each symbol of four bytes or
	// more.
	declared map[string][]defKey
	// changes are each commit's changed paths, and pairs how many commits
	// of at most 20 paths change each pair of paths.
	changes map[*object.Commit][]string
	pairs   map[[2]string]int
}

func newEvalTruth(t *testing.T, chunks []indexer.Chunk, history []*object.Commit) evalTruth {
	e := evalTruth{declared: map[string][]defKey{}, changes: map[*object.Commit][]string{}, pairs: map[[2]string]int{}}
	for _, c := range chunks {
		if len(c.Symbol) >= 4 {
			e.declared[c.Symbol] = append(e.declared[c.Symbol], defKey{c.Path, c.StartLine})
		}
	}
	for _, c := range history {
		paths := changedPaths(t, c)
		e.changes[c] = paths
		if len(paths) > 20 {
			continue
		}
		for _, a := range paths {
			for _, b := range paths {
				if a != b {
					e.pairs[[2]string{a, b}]++
				}
			}
		}
	}
	return e
}

// defs are the chunks outside changed that declare a name of added that
// at most two chunks declare.
func (e evalTruth) defs(added string, changed []string) map[defKey]bool {
	out := map[defKey]bool{}
	for _, id := range identifierRe.FindAllString(added, -1) {
		if where := e.declared[id]; len(where) <= 2 {
			for _, k := range where {
				if !slices.Contains(changed, k.path) {
					out[k] = true
				}
			}
		}
	}
	return out
}

// related are the paths outside changed that changed with one of from in at
// least two commits. A commit pairs none of its own paths with a path it
// does not change, so it is not among them.
func (e evalTruth) related(from, changed []string) map[string]bool {
	out := map[string]bool{}
	for pair, n := range e.pairs {
		if n >= 2 && slices.Contains(from, pair[0]) && !slices.Contains(changed, pair[1]) {
			out[pair[1]] = true
		}
	}
	return out
}

// evalTally adds up one variant's answers, and its queries' four best
// candidates each.
type evalTally struct {
	commits, defCommits, defHits, defFound, defTotal, coCommits, coHits, coKept, kept int
	qDefs, qDefHits, qCos, qCoHits                                                    int
	qDefRR, qCoRR                                                                     float64
}

// addQuery adds query q's candidates of top, in their order.
func (s *evalTally) addQuery(top []candidate, q int, defs map[defKey]bool, related map[string]bool) {
	first := func(hit func(candidate) bool) (bool, float64) {
		rank := 0
		for _, c := range top {
			if c.Query != q {
				continue
			}
			rank++
			if hit(c) {
				return true, 1 / float64(rank)
			}
		}
		return false, 0
	}
	if len(defs) > 0 {
		s.qDefs++
		ok, rr := first(func(c candidate) bool { return defs[defKey{c.Chunk.Path, c.Chunk.StartLine}] })
		if ok {
			s.qDefHits++
		}
		s.qDefRR += rr
	}
	if len(related) > 0 {
		s.qCos++
		ok, rr := first(func(c candidate) bool { return related[c.Chunk.Path] })
		if ok {
			s.qCoHits++
		}
		s.qCoRR += rr
	}
}

func (s *evalTally) add(got []contextpack.Chunk, defs map[defKey]bool, related map[string]bool) {
	s.commits++
	s.kept += len(got)
	if len(defs) > 0 {
		s.defCommits++
		s.defTotal += len(defs)
		found := 0
		for _, ch := range got {
			if defs[defKey{ch.Path, ch.StartLine}] {
				found++
			}
		}
		s.defFound += found
		if found > 0 {
			s.defHits++
		}
	}
	if len(related) > 0 {
		s.coCommits++
		in := 0
		for _, ch := range got {
			if related[ch.Path] {
				in++
			}
		}
		s.coKept += in
		if in > 0 {
			s.coHits++
		}
	}
}

func (s *evalTally) String() string {
	ratio := func(a, b int) string {
		if b == 0 {
			return "-"
		}
		return strconv.FormatFloat(float64(a)/float64(b), 'f', 3, 64)
	}
	return fmt.Sprintf("%7d %7s | %5d %9s %9s | %5d %9s %9s", s.commits, ratio(s.kept, s.commits),
		s.defCommits, ratio(s.defHits, s.defCommits), ratio(s.defFound, s.defTotal),
		s.coCommits, ratio(s.coHits, s.coCommits), ratio(s.coKept, s.kept)) +
		fmt.Sprintf(" || %5d %8s %6.3f | %5d %8s %6.3f", s.qDefs, ratio(s.qDefHits, s.qDefs), s.qDefRR/float64(max(1, s.qDefs)),
			s.qCos, ratio(s.qCoHits, s.qCos), s.qCoRR/float64(max(1, s.qCos)))
}

// evalDepth is how many hits of each query each search answers without a
// reranker.
var evalDepth int

var identifierRe = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]*`)

// defKey is a chunk that declares a name, by its path and first line.
type defKey struct {
	path string
	line int
}

func evalInt(t *testing.T, key string, def int) int {
	if v := os.Getenv(key); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			t.Fatalf("%s: %v", key, err)
		}
		return n
	}
	return def
}

func evalFloat(t *testing.T, key string, def float64) float64 {
	if v := os.Getenv(key); v != "" {
		f, err := strconv.ParseFloat(v, 64)
		if err != nil {
			t.Fatalf("%s: %v", key, err)
		}
		return f
	}
	return def
}

// changedPaths are the paths that c changes against its first parent.
func changedPaths(t *testing.T, c *object.Commit) []string {
	if c.NumParents() == 0 {
		return nil
	}
	parent, err := c.Parent(0)
	if err != nil {
		t.Fatal(err)
	}
	from, err := parent.Tree()
	if err != nil {
		t.Fatal(err)
	}
	to, err := c.Tree()
	if err != nil {
		t.Fatal(err)
	}
	changes, err := from.Diff(to)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, ch := range changes {
		for _, p := range []string{ch.From.Name, ch.To.Name} {
			if p != "" && !slices.Contains(out, p) {
				out = append(out, p)
			}
		}
	}
	return out
}

// commitDiff is c's unified diff against its first parent, and its added
// lines.
func commitDiff(t *testing.T, c *object.Commit) (string, string) {
	parent, err := c.Parent(0)
	if err != nil {
		t.Fatal(err)
	}
	patch, err := parent.Patch(c)
	if err != nil {
		t.Fatal(err)
	}
	diff := patch.String()
	var added strings.Builder
	for _, f := range udiff.Parse(diff) {
		for _, h := range f.Hunks {
			for _, l := range h.Lines {
				if l.Kind == '+' {
					added.WriteString(l.Text)
					added.WriteByte('\n')
				}
			}
		}
	}
	return diff, added.String()
}

// evalEmbedder embeds with the configured endpoint, or with hashed
// character trigrams, caching each text's vector by its hash.
func evalEmbedder(t *testing.T) (func([]string) [][]float32, int) {
	url := os.Getenv("KRITIKA_EVAL_EMBED_URL")
	dims := evalInt(t, "KRITIKA_EVAL_EMBED_DIMS", 256)
	name := cmp.Or(os.Getenv("KRITIKA_EVAL_EMBED_MODEL"), "trigrams")
	cachePath := filepath.Join(cmp.Or(os.Getenv("KRITIKA_EVAL_CACHE"), t.TempDir()),
		fmt.Sprintf("vectors-%x.json", sha256.Sum256([]byte(name+url+strconv.Itoa(dims)))))
	cache := map[string][]float32{}
	if b, err := os.ReadFile(cachePath); err == nil {
		if err := json.Unmarshal(b, &cache); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		b, err := json.Marshal(cache)
		if err == nil {
			err = os.WriteFile(cachePath, b, 0o600)
		}
		if err != nil {
			t.Error(err)
		}
	})
	var remote model.Embedder
	if url != "" {
		e := model.NewOpenAIEmbedder(url, "unused", name, dims)
		e.MaxBatch = 32
		remote = e
	}
	return func(texts []string) [][]float32 {
		out := make([][]float32, len(texts))
		var missing []int
		for i, s := range texts {
			key := fmt.Sprintf("%x", sha256.Sum256([]byte(s)))
			if v, ok := cache[key]; ok {
				out[i] = v
			} else {
				missing = append(missing, i)
			}
		}
		for start := 0; start < len(missing); start += 256 {
			batch := missing[start:min(start+256, len(missing))]
			inputs := make([]string, len(batch))
			for j, i := range batch {
				inputs[j] = texts[i]
			}
			var vectors [][]float32
			if remote != nil {
				var err error
				if vectors, _, err = remote.Embed(t.Context(), inputs); err != nil {
					t.Fatal(err)
				}
			} else {
				for _, s := range inputs {
					vectors = append(vectors, trigramVector(s, dims))
				}
			}
			for j, i := range batch {
				out[i] = vectors[j]
				cache[fmt.Sprintf("%x", sha256.Sum256([]byte(texts[i])))] = vectors[j]
			}
		}
		return out
	}, dims
}

// trigramVector is s's character trigrams hashed into dims buckets and
// normalised.
func trigramVector(s string, dims int) []float32 {
	v := make([]float32, dims)
	s = strings.ToLower(s)
	for i := 0; i+3 <= len(s); i++ {
		h := fnv.New32a()
		_, _ = h.Write([]byte(s[i : i+3]))
		v[h.Sum32()%uint32(dims)]++
	}
	var norm float64
	for _, x := range v {
		norm += float64(x) * float64(x)
	}
	if norm > 0 {
		for i := range v {
			v[i] /= float32(math.Sqrt(norm))
		}
	}
	return v
}

// TestEvalRerankScale: once every query is reranked, the replay compares
// queries' candidates by the reranker's scores, as the gateway does, not by
// the fusion's.
func TestEvalRerankScale(t *testing.T) {
	cand := func(query int, path string, fused float64) candidate {
		c := candidate{vector: true, close: true, score: fused, shared: fused}
		c.Chunk, c.Similarity, c.Query = contextpack.Chunk{Path: path, StartLine: 1}, 0.9, query
		return c
	}
	// The fusion puts a.go first, the reranker b.go.
	cands := []candidate{cand(0, "a.go", 0.02), cand(1, "b.go", 0.01)}
	reranker := rerankFunc(func(_ context.Context, query string, docs []string) ([]float64, model.RerankUsage, error) {
		return []float64{map[string]float64{"q0": 0.1, "q1": 0.9}[query]}, model.RerankUsage{}, nil
	})
	evalRerank(t, reranker, []string{"q0", "q1"}, cands, 1)
	got := keepCandidates(cands, map[hitKey]int{}, 1)
	if len(got) != 1 || got[0].Path != "b.go" {
		t.Fatalf("kept %+v, want b.go, which the reranker scores higher", got)
	}
}

// TestEvalSearchesAtEachDepth: the replay's variants without a reranker read
// a search at their own depth, not a reranker's deeper one cut to it, and the
// +rerank variants one at the reranker's depth. A depth is searched once.
func TestEvalSearchesAtEachDepth(t *testing.T) {
	for _, tt := range []struct {
		name               string
		depth, rerankDepth int
		reranking          bool
		searched           []int
		base, deep         string
	}{
		{name: "no reranker", depth: 8, rerankDepth: 32, searched: []int{8}, base: "8.go", deep: "8.go"},
		{name: "a deeper reranker", depth: 8, rerankDepth: 32, reranking: true, searched: []int{8, 32}, base: "8.go", deep: "32.go"},
		{name: "a reranker as deep", depth: 8, rerankDepth: 8, reranking: true, searched: []int{8}, base: "8.go", deep: "8.go"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var searched []int
			// Each depth finds a chunk of its own, as the search by name can
			// when chunks tie.
			search := func(depth int, timed *evalSearchTime) evalHits {
				searched = append(searched, depth)
				timed.requests++
				return evalHits{named: []store.SimilarHit{{Chunk: contextpack.Chunk{Path: strconv.Itoa(depth) + ".go"}}}}
			}
			var timing evalTiming
			base, deep := evalSearches(search, &timing, tt.depth, tt.rerankDepth, tt.reranking)
			if !slices.Equal(searched, tt.searched) || timing.base.requests+timing.deep.requests != len(searched) {
				t.Fatalf("searched at %v, timed %d and %d; want %v, each timed once", searched, timing.base.requests,
					timing.deep.requests, tt.searched)
			}
			if base.named[0].Chunk.Path != tt.base || deep.named[0].Chunk.Path != tt.deep {
				t.Fatalf("base found %s and deep %s, want %s and %s", base.named[0].Chunk.Path, deep.named[0].Chunk.Path, tt.base, tt.deep)
			}
		})
	}
}

// TestEvalTruthLeavesOutAChangedPathWithAColon: a definition in a changed
// file is no answer the search can give, whatever its path holds, a colon
// included.
func TestEvalTruthLeavesOutAChangedPathWithAColon(t *testing.T) {
	truth := newEvalTruth(t, []indexer.Chunk{
		{Path: "docs/a:b.go", StartLine: 3, Symbol: "AcquireLease"},
		{Path: "lease.go", StartLine: 7, Symbol: "ReleaseLease"},
	}, nil)
	defs := truth.defs("AcquireLease(ctx); ReleaseLease(ctx)", []string{"docs/a:b.go"})
	if len(defs) != 1 || !defs[defKey{"lease.go", 7}] {
		t.Fatalf("definitions %v, want lease.go's alone", defs)
	}
}
