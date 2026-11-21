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

	"github.com/home-operations/kritika/internal/chunk"
	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/configfile/configfiletest"
	"github.com/home-operations/kritika/internal/contextpack"
	"github.com/home-operations/kritika/internal/indexer"
	"github.com/home-operations/kritika/internal/model"
	"github.com/home-operations/kritika/internal/store"
	"github.com/home-operations/kritika/internal/store/storetest"
	"github.com/home-operations/kritika/internal/textcut"
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
		queries, paths := evalQueries(diff)
		if len(queries) == 0 {
			continue
		}
		changed := truth.changes[c]
		defs, related := truth.defs(added, changed), truth.related(changed, changed)
		qv := embed(queries)
		depth := evalDepth
		if reranker != nil {
			depth = max(depth, candidates)
		}
		hits, named := ix.search(t, &timing, queries, qv, changed, depth)
		for _, v := range variants {
			var cands []candidate
			f := floor
			switch strings.TrimSuffix(v, "+rerank") {
			case "vector":
				cands = fuse(evalTop(hits, v), nil)
			case "lexical":
				// No similarity vouches for a chunk.
				cands, f = fuse(nil, evalTop(named, v)), 2
			case "hybrid":
				cands = fuse(evalTop(hits, v), evalTop(named, v))
			}
			counts := map[hitKey]int{}
			kept, _, _ := gateCandidates(cands, f, margin)
			if strings.HasSuffix(v, "+rerank") {
				kept = topPerQuery(kept, candidates, counts)
				started := time.Now()
				evalRerank(t, reranker, queries, kept)
				timing.rerank += time.Since(started)
			}
			top := topPerQuery(kept, similarPerQuery, counts)
			for q, text := range queries {
				tallies[v].addQuery(top, q, truth.defs(text, changed), truth.related([]string{paths[q]}, changed))
			}
			tallies[v].add(keepCandidates(top, counts, similarMax), defs, related)
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
			StartLine: c.StartLine, EndLine: c.EndLine}
		texts[i] = rerankDocument(contextpack.Chunk{Path: c.Path, Symbol: c.Symbol, Kind: c.Kind, Scope: c.Scope, Text: c.Text})
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
		if err := tx.QueryRow(ctx, `INSERT INTO index_runs (account_id, repository_id, commit_sha, embed_model, embed_dims, mode, status)
			VALUES ($1, $2, $3, 'eval', $4, 'full', 'completed') RETURNING id`, account.ID(), repoID, base.Hash.String(), dims).Scan(&run); err != nil {
			return err
		}
		for start := 0; start < len(staged); start += 500 {
			end := min(start+500, len(staged))
			if err := store.InsertIndexChunks(ctx, tx, account.ID(), repoID, run, staged[start:end], vectors[start:end]); err != nil {
				return err
			}
		}
		return nil
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

// evalTiming is the time the searches of the evaluation took.
type evalTiming struct {
	requests                int
	vector, lexical, rerank time.Duration
}

func (e evalTiming) String() string {
	per := func(d time.Duration) time.Duration {
		return (d / time.Duration(max(1, e.requests))).Round(100 * time.Microsecond)
	}
	return fmt.Sprintf("per request of %d: vector search %s, lexical search %s, reranking %s",
		e.requests, per(e.vector), per(e.lexical), per(e.rerank))
}

// search runs both searches of a request, depth deep, as the gateway does.
func (ix evalIndexed) search(
	t *testing.T, timing *evalTiming, queries []string, vectors [][]float32, exclude []string, depth int,
) (hits, named []store.SimilarHit) {
	ctx := t.Context()
	if err := ix.st.WithAccount(ctx, ix.account, func(tx pgx.Tx) error {
		started := time.Now()
		var err error
		if hits, err = store.SimilarChunks(ctx, tx, ix.run, vectors, exclude, depth); err != nil {
			return err
		}
		searched := time.Now()
		if named, err = lexicalHits(ctx, tx, ix.run, queries, vectors, exclude, depth); err != nil {
			return err
		}
		timing.requests++
		timing.vector += searched.Sub(started)
		timing.lexical += time.Since(searched)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return hits, named
}

// evalTop is each query's evalDepth first hits, as the gateway searches
// without a reranker, unless the variant reranks, which takes them all.
func evalTop(hits []store.SimilarHit, variant string) []store.SimilarHit {
	if strings.HasSuffix(variant, "+rerank") {
		return hits
	}
	taken := map[int]int{}
	return slices.DeleteFunc(slices.Clone(hits), func(h store.SimilarHit) bool {
		taken[h.Query]++
		return taken[h.Query] > evalDepth
	})
}

// evalRerank scores each query's candidates as the gateway's rerank does.
func evalRerank(t *testing.T, reranker model.Reranker, queries []string, cands []candidate) {
	byQuery := map[int][]int{}
	for i, c := range cands {
		byQuery[c.Query] = append(byQuery[c.Query], i)
	}
	for q, idx := range byQuery {
		docs := make([]string, len(idx))
		for j, i := range idx {
			docs[j] = rerankDocument(cands[i].Chunk)
		}
		scores, _, err := reranker.Rerank(t.Context(), queries[q], docs)
		if err != nil {
			t.Fatal(err)
		}
		for j, i := range idx {
			cands[i].score = scores[j]
		}
	}
}

// evalTruth is what the evaluation judges an answer by.
type evalTruth struct {
	// declared are the chunks that declare each symbol of four bytes or
	// more.
	declared map[string][]string
	// changes are each commit's changed paths, and pairs how many commits
	// of at most 20 paths change each pair of paths.
	changes map[*object.Commit][]string
	pairs   map[[2]string]int
}

func newEvalTruth(t *testing.T, chunks []indexer.Chunk, history []*object.Commit) evalTruth {
	e := evalTruth{declared: map[string][]string{}, changes: map[*object.Commit][]string{}, pairs: map[[2]string]int{}}
	for _, c := range chunks {
		if len(c.Symbol) >= 4 {
			e.declared[c.Symbol] = append(e.declared[c.Symbol], chunkKey(c.Path, c.StartLine))
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
func (e evalTruth) defs(added string, changed []string) map[string]bool {
	out := map[string]bool{}
	for _, id := range identifierRe.FindAllString(added, -1) {
		if where := e.declared[id]; len(where) <= 2 {
			for _, k := range where {
				if !slices.Contains(changed, strings.Split(k, ":")[0]) {
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
func (s *evalTally) addQuery(top []candidate, q int, defs, related map[string]bool) {
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
		ok, rr := first(func(c candidate) bool { return defs[chunkKey(c.Chunk.Path, c.Chunk.StartLine)] })
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

func (s *evalTally) add(got []contextpack.Chunk, defs, related map[string]bool) {
	s.commits++
	s.kept += len(got)
	if len(defs) > 0 {
		s.defCommits++
		s.defTotal += len(defs)
		found := 0
		for _, ch := range got {
			if defs[chunkKey(ch.Path, ch.StartLine)] {
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

func chunkKey(path string, line int) string { return fmt.Sprintf("%s:%d", path, line) }

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

// evalQueries are the changed regions of the diff's hunks, and the path of
// each, as the runner's
// stage 4 picks them in the simple case: each hunk's added lines with three
// lines around them, of the files that the index has a language for, the
// most changed files first, at most contextpack.SimilarQueries.
func evalQueries(diff string) (texts, paths []string) {
	type region struct {
		text, path string
		added      int
	}
	var regions []region
	for _, f := range udiff.Parse(diff) {
		if f.NewPath == "" || chunk.Matches(configfile.DefaultIgnore, f.NewPath) {
			continue
		}
		if lang, _ := chunk.Language(f.NewPath); lang == "" {
			continue
		}
		for _, h := range f.Hunks {
			var lines []udiff.Line
			first, last, n := -1, -1, 0
			for _, l := range h.Lines {
				if l.Kind != '+' && l.Kind != ' ' {
					continue
				}
				if l.Kind == '+' && strings.TrimSpace(l.Text) != "" {
					if first < 0 {
						first = len(lines)
					}
					last = len(lines)
					n++
				}
				lines = append(lines, l)
			}
			if n == 0 {
				continue
			}
			var b strings.Builder
			for _, l := range lines[max(0, first-3):min(len(lines), last+4)] {
				b.WriteString(l.Text)
				b.WriteByte('\n')
			}
			regions = append(regions, region{textcut.Prefix(b.String(), contextpack.SimilarQueryChars), f.NewPath, n})
		}
	}
	slices.SortStableFunc(regions, func(a, b region) int { return cmp.Compare(b.added, a.added) })
	for _, r := range regions[:min(len(regions), contextpack.SimilarQueries)] {
		texts, paths = append(texts, r.text), append(paths, r.path)
	}
	return texts, paths
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
