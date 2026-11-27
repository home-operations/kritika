package gateway

import (
	"errors"
	"fmt"
	"maps"
	"math"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/contextpack"
	"github.com/home-operations/kritika/internal/store"
	"github.com/home-operations/kritika/internal/tuning"
)

func TestKeepSimilar(t *testing.T) {
	hit := func(path string, line int, sim float64) store.SimilarHit {
		return store.SimilarHit{Chunk: contextpack.Chunk{Path: path, StartLine: line}, Similarity: sim}
	}
	near := func(query int, path string, sim float64) store.SimilarHit {
		h := hit(path, 1, sim)
		h.Query = query
		return h
	}
	hits := []store.SimilarHit{
		hit("a.go", 1, 0.65), hit("b.go", 1, 0.91), hit("a.go", 1, 0.65), hit("c.go", 10, 0.70), hit("d.go", 1, 0.49),
	}
	// Query 0 finds best.go twice, and five more queries two candidates
	// each, so that the answer's limit, not a query's, is what cuts.
	best := make([]store.SimilarHit, 0, 3+tuning.Get().Gateway.SimilarMax)
	best = append(best, hit("best.go", 1, 0.55), hit("other.go", 1, 0.80), hit("best.go", 1, 0.99))
	for i := range tuning.Get().Gateway.SimilarMax {
		best = append(best, near(1+i/2, fmt.Sprintf("candidate%d.go", i), 0.70))
	}
	wantBest := make([]string, 0, tuning.Get().Gateway.SimilarMax)
	wantBest = append(wantBest, "best.go:1:similarity 0.99")
	for _, i := range []int{0, 2, 4, 6, 8} {
		wantBest = append(wantBest, fmt.Sprintf("candidate%d.go:1:similarity 0.70", i))
	}
	wantBest = append(wantBest, "other.go:1:similarity 0.80")
	for _, i := range []int{1, 3, 5} {
		wantBest = append(wantBest, fmt.Sprintf("candidate%d.go:1:similarity 0.70", i))
	}
	tests := []struct {
		name          string
		hits          []store.SimilarHit
		floor, margin float64
		want          []string
	}{
		{"default floor", hits, 0.5, 1, []string{"b.go:1:similarity 0.91", "c.go:10:similarity 0.70", "a.go:1:similarity 0.65"}},
		{"higher floor", hits, 0.7, 1, []string{"b.go:1:similarity 0.91", "c.go:10:similarity 0.70"}},
		{"nothing clears floor", hits, 0.95, 1, nil},
		{"best duplicate survives result limit", best, 0.5, 1, wantBest},
		{"empty", nil, 0.5, 0.1, nil},
		// y.go outscores both of query 1's, but is well below its own
		// query's best.
		{"margin from each query's best", []store.SimilarHit{
			near(0, "x.go", 0.95), near(0, "y.go", 0.80), near(1, "z.go", 0.70), near(1, "w.go", 0.62),
		}, 0.5, 0.1, []string{"x.go:1:similarity 0.95", "z.go:1:similarity 0.70", "w.go:1:similarity 0.62"}},
		{"a chunk one query cuts and another keeps", []store.SimilarHit{
			near(0, "x.go", 0.95), near(0, "s.go", 0.80), near(1, "z.go", 0.70), near(1, "s.go", 0.66),
		}, 0.5, 0.1, []string{"x.go:1:similarity 0.95", "z.go:1:similarity 0.70", "s.go:1:similarity 0.66"}},
		{"the floor holds within the margin", []store.SimilarHit{near(0, "a.go", 0.55), near(0, "b.go", 0.48)},
			0.5, 0.1, []string{"a.go:1:similarity 0.55"}},
		// 0.8 - 0.1 rounds to 0.7000000000000001.
		{"a hit at the margin is kept", []store.SimilarHit{near(0, "a.go", 0.8), near(0, "b.go", 0.7)},
			0.5, 0.1, []string{"a.go:1:similarity 0.80", "b.go:1:similarity 0.70"}},
		{"a hit past the margin is not", []store.SimilarHit{near(0, "a.go", 0.8), near(0, "b.go", 0.69)},
			0.5, 0.1, []string{"a.go:1:similarity 0.80"}},
		{"no margin keeps each query's best", []store.SimilarHit{near(0, "a.go", 0.8), near(0, "b.go", 0.79), near(1, "c.go", 0.6)},
			0.5, 0, []string{"a.go:1:similarity 0.80", "c.go:1:similarity 0.60"}},
		{"each query's best before any query's second", []store.SimilarHit{
			near(0, "a.go", 0.95), near(0, "b.go", 0.94), near(0, "c.go", 0.93), near(1, "d.go", 0.80),
		}, 0.5, 0.1, []string{"a.go:1:similarity 0.95", "d.go:1:similarity 0.80", "b.go:1:similarity 0.94", "c.go:1:similarity 0.93"}},
		{"at most two chunks of a file", []store.SimilarHit{
			hit("f.go", 1, 0.90), hit("f.go", 10, 0.89), hit("f.go", 20, 0.88), hit("g.go", 1, 0.87),
		}, 0.5, 0.1, []string{"f.go:1:similarity 0.90", "f.go:10:similarity 0.89", "g.go:1:similarity 0.87"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			original := slices.Clone(tt.hits)
			got, _ := keepSimilar(tt.hits, tt.floor, tt.margin)
			if !reflect.DeepEqual(original, tt.hits) {
				t.Error("caller hits were mutated")
			}
			if len(got) != len(tt.want) {
				t.Fatalf("kept %d chunks, want %d: %+v", len(got), len(tt.want), got)
			}
			for i, c := range got {
				if key := fmt.Sprintf("%s:%d:%s", c.Path, c.StartLine, c.Ref); key != tt.want[i] {
					t.Errorf("chunk %d = %q, want %q", i, key, tt.want[i])
				}
			}
		})
	}
}

// TestKeepSimilarCounts: each hit is counted once, kept or dropped, by the
// first thing that drops it.
func TestKeepSimilarCounts(t *testing.T) {
	hit := func(path string, line int, sim float64) store.SimilarHit {
		return store.SimilarHit{Chunk: contextpack.Chunk{Path: path, StartLine: line}, Similarity: sim}
	}
	many := make([]store.SimilarHit, 0, tuning.Get().Gateway.SimilarMax+2)
	for i := range tuning.Get().Gateway.SimilarMax + 2 {
		h := hit(fmt.Sprintf("f%d.go", i), 1, 0.9)
		h.Query = i % 3
		many = append(many, h)
	}
	for _, tt := range []struct {
		name string
		hits []store.SimilarHit
		want map[string]int
	}{
		{"every reason", []store.SimilarHit{
			hit("a.go", 1, 0.9), hit("a.go", 1, 0.9), hit("f.go", 1, 0.85), hit("f.go", 10, 0.84), hit("f.go", 20, 0.83),
			hit("c.go", 1, 0.3), hit("d.go", 1, 0.7),
		}, map[string]int{hitKept: 3, hitDuplicate: 1, hitFileCap: 1, hitFloor: 1, hitMargin: 1}},
		{"past the limit", many, map[string]int{hitKept: tuning.Get().Gateway.SimilarMax, hitLimit: 2}},
		{"none", nil, map[string]int{}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, got := keepSimilar(tt.hits, 0.5, 0.1)
			if !maps.Equal(countsByOutcome(got), tt.want) {
				t.Fatalf("counts = %v, want %v", got, tt.want)
			}
			for k := range got {
				if k.source != sourceVector {
					t.Errorf("a vector hit counted as found by %s", k.source)
				}
			}
		})
	}
}

// keepSimilar is what the gateway keeps of vector hits alone.
func keepSimilar(hits []store.SimilarHit, floor, margin float64) ([]contextpack.Chunk, map[hitKey]int) {
	kept, counts, _ := gateCandidates(fuse(hits, nil), floor, margin)
	return keepCandidates(kept, counts, tuning.Get().Gateway.SimilarMax, tuning.Get().Gateway.SimilarPerQuery), counts
}

// TestSimilarLimit: an answer holds the chunks its request asks for, up to
// tuning's ContextPack.SimilarLimit, and Gateway.SimilarMax when it asks for none.
func TestSimilarLimit(t *testing.T) {
	for _, tt := range []struct{ asked, want int }{
		{0, tuning.Get().Gateway.SimilarMax}, {-1, tuning.Get().Gateway.SimilarMax}, {3, 3}, {tuning.Get().ContextPack.SimilarLimit, tuning.Get().ContextPack.SimilarLimit},
		{tuning.Get().ContextPack.SimilarLimit + 1, tuning.Get().ContextPack.SimilarLimit},
	} {
		t.Run(fmt.Sprint(tt.asked), func(t *testing.T) {
			if got := answerLimit(tt.asked); got != tt.want {
				t.Fatalf("answerLimit(%d) = %d, want %d", tt.asked, got, tt.want)
			}
		})
	}
	hits := make([]store.SimilarHit, 0, tuning.Get().ContextPack.SimilarLimit)
	for i := range tuning.Get().ContextPack.SimilarLimit {
		hits = append(hits, store.SimilarHit{Chunk: contextpack.Chunk{Path: fmt.Sprintf("f%d.go", i), StartLine: 1}, Similarity: 0.9,
			Query: i % 4})
	}
	kept, counts, _ := gateCandidates(fuse(hits, nil), 0.5, 0.1)
	if got := keepCandidates(kept, counts, tuning.Get().ContextPack.SimilarLimit, tuning.Get().Gateway.SimilarPerQuery); len(got) != tuning.Get().ContextPack.SimilarLimit {
		t.Fatalf("kept %d, want %d", len(got), tuning.Get().ContextPack.SimilarLimit)
	}
}

// TestQueryLimit: a query keeps Gateway.SimilarPerQuery chunks, but for a search of
// one query that asks for more, which no other query shares the answer with:
// search_code asks for spares, as it leaves out those the head no longer
// has.
func TestQueryLimit(t *testing.T) {
	for _, tt := range []struct {
		name           string
		kind           string
		queries, asked int
		want           int
	}{
		{"a search that asks for spares", contextpack.SimilarKindSearch, 1, 8, 8},
		{"a search that asks for past the most", contextpack.SimilarKindSearch, 1, tuning.Get().ContextPack.SimilarLimit + 1, tuning.Get().ContextPack.SimilarLimit},
		{"a search that asks for fewer", contextpack.SimilarKindSearch, 1, 2, tuning.Get().Gateway.SimilarPerQuery},
		{"a search that asks for none", contextpack.SimilarKindSearch, 1, 0, tuning.Get().Gateway.SimilarPerQuery},
		{"a search of two queries", contextpack.SimilarKindSearch, 2, 8, tuning.Get().Gateway.SimilarPerQuery},
		{"a diff of one query", contextpack.SimilarKindDiff, 1, 8, tuning.Get().Gateway.SimilarPerQuery},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := queryLimit(tt.kind, tt.queries, tt.asked); got != tt.want {
				t.Fatalf("queryLimit(%q, %d, %d) = %d, want %d", tt.kind, tt.queries, tt.asked, got, tt.want)
			}
		})
	}
	hits := make([]store.SimilarHit, 0, 2*tuning.Get().Gateway.SimilarPerQuery)
	for i := range 2 * tuning.Get().Gateway.SimilarPerQuery {
		hits = append(hits, store.SimilarHit{Chunk: contextpack.Chunk{Path: fmt.Sprintf("f%d.go", i), StartLine: 1}, Similarity: 0.9})
	}
	kept, counts, _ := gateCandidates(fuse(hits, nil), 0.5, 0.1)
	if got := keepCandidates(kept, counts, tuning.Get().Gateway.SimilarMax, 2*tuning.Get().Gateway.SimilarPerQuery); len(got) != 2*tuning.Get().Gateway.SimilarPerQuery {
		t.Fatalf("kept %d of one query, want %d", len(got), 2*tuning.Get().Gateway.SimilarPerQuery)
	}
}

// TestHybridSimilar: the lexical search's hits join the vector search's.
// Either search can vouch for a chunk, which is labelled by what vouched
// for it, and each query keeps its Gateway.SimilarPerQuery best by fused rank.
func TestHybridSimilar(t *testing.T) {
	vec := func(query int, path string, sim float64) store.SimilarHit {
		return store.SimilarHit{Chunk: contextpack.Chunk{Path: path, StartLine: 1}, Similarity: sim, Query: query}
	}
	lex := func(query int, path string, sim, weight float64, matched ...string) store.SimilarHit {
		h := vec(query, path, sim)
		h.Lexical, h.Matched = weight, matched
		return h
	}
	tests := []struct {
		name        string
		hits, named []store.SimilarHit
		want        []string
		wantCounts  map[hitKey]int
	}{
		{
			name:  "a chunk both find comes first",
			hits:  []store.SimilarHit{vec(0, "a.go", 0.9), vec(0, "b.go", 0.85)},
			named: []store.SimilarHit{lex(0, "b.go", 0.85, 9, "AcquireLease", "lease")},
			want:  []string{"b.go:similarity 0.85; shares AcquireLease, lease", "a.go:similarity 0.90"},
			wantCounts: map[hitKey]int{
				{hitKept, sourceBoth}: 1, {hitKept, sourceVector}: 1,
			},
		},
		{
			name:  "names vouch for a chunk under the floor",
			hits:  []store.SimilarHit{vec(0, "a.go", 0.7)},
			named: []store.SimilarHit{lex(0, "c.go", 0.3, 8, "parseHTTPRequest"), lex(0, "d.go", 0.2, 3, "request")},
			want:  []string{"a.go:similarity 0.70", "c.go:shares parseHTTPRequest"},
			wantCounts: map[hitKey]int{
				{hitKept, sourceVector}: 1, {hitKept, sourceLexical}: 1, {"lexical_floor", sourceLexical}: 1,
			},
		},
		{
			name:  "a lexical hit close to the query shows its similarity",
			hits:  []store.SimilarHit{vec(0, "a.go", 0.7)},
			named: []store.SimilarHit{lex(0, "c.go", 0.68, 8, "Lease")},
			want:  []string{"a.go:similarity 0.70", "c.go:similarity 0.68; shares Lease"},
			wantCounts: map[hitKey]int{
				{hitKept, sourceVector}: 1, {hitKept, sourceLexical}: 1,
			},
		},
		{
			name: "each query keeps its best four",
			hits: []store.SimilarHit{vec(0, "a.go", 0.9), vec(0, "b.go", 0.89), vec(0, "c.go", 0.88), vec(0, "d.go", 0.87)},
			named: []store.SimilarHit{
				lex(0, "e.go", 0.4, 9, "X"), lex(0, "f.go", 0.4, 8, "Y"), lex(0, "g.go", 0.4, 7, "Z"), lex(0, "h.go", 0.4, 6, "W"),
			},
			want: []string{
				"a.go:similarity 0.90", "e.go:shares X", "b.go:similarity 0.89", "f.go:shares Y",
			},
			wantCounts: map[hitKey]int{
				{hitKept, sourceVector}: 2, {hitKept, sourceLexical}: 2, {hitLimit, sourceVector}: 2, {hitLimit, sourceLexical}: 2,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			kept, counts, _ := gateCandidates(fuse(tt.hits, tt.named), 0.5, 0.1)
			got := keepCandidates(kept, counts, tuning.Get().Gateway.SimilarMax, tuning.Get().Gateway.SimilarPerQuery)
			var keys []string
			for _, c := range got {
				keys = append(keys, c.Path+":"+c.Ref)
			}
			if !slices.Equal(keys, tt.want) {
				t.Errorf("kept %q, want %q", keys, tt.want)
			}
			if !maps.Equal(counts, tt.wantCounts) {
				t.Errorf("counts = %v, want %v", counts, tt.wantCounts)
			}
		})
	}
}

// TestGateRanks: a candidate scores 1 / (Gateway.RRFK + rank) for each
// search that found it, ranked among those of its query that the search
// found, whether or not that search vouches for it. The constant is the
// configured one.
func TestGateRanks(t *testing.T) {
	tuning.Override(t, func(v *tuning.Values) { v.Gateway.RRFK = 10 })
	k := 10.0
	hits := []store.SimilarHit{
		{Chunk: contextpack.Chunk{Path: "low.go"}, Similarity: 0.6},
		{Chunk: contextpack.Chunk{Path: "high.go"}, Similarity: 0.9},
		{Chunk: contextpack.Chunk{Path: "other.go"}, Similarity: 0.8, Query: 1},
		{Chunk: contextpack.Chunk{Path: "under.go"}, Similarity: 0.2},
	}
	named := []store.SimilarHit{
		{Chunk: contextpack.Chunk{Path: "low.go"}, Similarity: 0.6, Lexical: 2, Matched: []string{"x"}},
		{Chunk: contextpack.Chunk{Path: "under.go"}, Similarity: 0.2, Lexical: 3, Matched: []string{"y"}},
	}
	want := map[string]float64{
		"low.go": 1.0/(k+2) + 1.0/(k+2), "high.go": 1.0 / (k + 1), "other.go": 1.0 / (k + 1),
		"under.go": 1.0/(k+3) + 1.0/(k+1),
	}
	got, _, best := gateCandidates(fuse(hits, named), 0.5, 0.5)
	if wantBest := map[int]float64{0: 0.9, 1: 0.8}; !maps.Equal(best, wantBest) {
		t.Fatalf("best similarity of each query = %v, want %v", best, wantBest)
	}
	if len(got) != len(want) {
		t.Fatalf("kept %d candidates, want %d", len(got), len(want))
	}
	for _, c := range got {
		if math.Abs(c.score-want[c.Chunk.Path]) > 1e-12 {
			t.Errorf("%s scored %g, want %g", c.Chunk.Path, c.score, want[c.Chunk.Path])
		}
	}
}

// TestGateRanksTies: of chunks of equal weight by name, the one nearest the
// query ranks first in the search by name.
func TestGateRanksTies(t *testing.T) {
	named := []store.SimilarHit{
		{Chunk: contextpack.Chunk{Path: "far.go"}, Similarity: 0.2, Lexical: 5, Matched: []string{"x"}},
		{Chunk: contextpack.Chunk{Path: "near.go"}, Similarity: 0.4, Lexical: 5, Matched: []string{"x"}},
	}
	got, _, _ := gateCandidates(fuse(nil, named), 0.5, 0.1)
	scores := map[string]float64{}
	for _, c := range got {
		scores[c.Chunk.Path] = c.score
	}
	if want := 1.0 / float64(tuning.Get().Gateway.RRFK+1); scores["near.go"] != want || scores["far.go"] >= want {
		t.Fatalf("scores = %v, want near.go first at %g", scores, want)
	}
}

// TestKeepCandidatesTurns: a query whose next candidate a duplicate or the
// file cap turns away competes with its next one for the same round's
// places as the other queries' first, rather than losing its turn to
// another query's second.
func TestKeepCandidatesTurns(t *testing.T) {
	cand := func(query int, path string, line int, score float64) candidate {
		c := candidate{vector: true, close: true, score: score, shared: score}
		c.Chunk, c.Similarity, c.Query = contextpack.Chunk{Path: path, StartLine: line}, 0.9, query
		return c
	}
	for _, tt := range []struct {
		name  string
		cands []candidate
		limit int
		want  []string
	}{
		{
			// Both queries' best is x.go, which query 0 keeps. Query 1's
			// z.go then comes before query 0's second, y.go.
			name: "a shared best", limit: 2,
			cands: []candidate{
				cand(0, "x.go", 1, 0.9), cand(0, "y.go", 1, 0.8),
				cand(1, "x.go", 1, 0.85), cand(1, "z.go", 1, 0.5),
			},
			want: []string{"x.go", "z.go"},
		},
		{
			// Queries 1 and 2 fill a.go, so query 2's best is capped, and its
			// d.go comes before the others' second.
			name: "a capped best", limit: 4,
			cands: []candidate{
				cand(1, "a.go", 1, 0.95), cand(1, "e.go", 1, 0.8),
				cand(2, "a.go", 10, 0.94), cand(2, "f.go", 1, 0.7),
				cand(0, "a.go", 20, 0.9), cand(0, "d.go", 1, 0.6),
			},
			want: []string{"a.go", "a.go", "d.go", "e.go"},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			counts := map[hitKey]int{}
			got := make([]string, 0, tt.limit)
			for _, c := range keepCandidates(tt.cands, counts, tt.limit, tuning.Get().Gateway.SimilarPerQuery) {
				got = append(got, c.Path)
			}
			if !slices.Equal(got, tt.want) {
				t.Fatalf("kept %q, want %q", got, tt.want)
			}
			if n := counts[hitKey{hitKept, sourceVector}] + counts[hitKey{hitDuplicate, sourceVector}] +
				counts[hitKey{hitFileCap, sourceVector}] + counts[hitKey{hitLimit, sourceVector}]; n != len(tt.cands) {
				t.Errorf("counts %v add up to %d, want every one of the %d candidates", counts, n, len(tt.cands))
			}
		})
	}
}

// TestRerankBackfill: a reranked query's candidates past those sent to the
// reranker rank after them, in their fused order. A query that the reranker
// did not answer keeps all of its candidates in their fused order.
func TestRerankBackfill(t *testing.T) {
	cand := func(query int, path string, fused float64) candidate {
		c := candidate{vector: true, close: true, score: fused, shared: fused}
		c.Chunk, c.Similarity, c.Query = contextpack.Chunk{Path: path, StartLine: 1}, 0.9, query
		return c
	}
	cands := []candidate{
		cand(0, "a.go", 0.05), cand(0, "b.go", 0.04), cand(0, "c.go", 0.03), cand(0, "d.go", 0.02),
		cand(1, "e.go", 0.05), cand(1, "f.go", 0.04), cand(1, "g.go", 0.03),
	}
	batches := rerankBatches(cands, 2)
	if want := map[int][]int{0: {0, 1}, 1: {4, 5}}; !reflect.DeepEqual(batches, want) {
		t.Fatalf("batches %v, want %v", batches, want)
	}
	// The reranker puts b.go first, and scores below the fused scale. As it
	// left query 1 out, queries are still compared on the fused scale.
	rescore(cands, 0, batches[0], []float64{-3, -2})
	got := make([]string, 0, len(cands))
	for _, c := range keepCandidates(cands, map[hitKey]int{}, tuning.Get().Gateway.SimilarMax, tuning.Get().Gateway.SimilarPerQuery) {
		got = append(got, c.Path)
	}
	if want := []string{"e.go", "b.go", "a.go", "f.go", "g.go", "c.go", "d.go"}; !slices.Equal(got, want) {
		t.Fatalf("kept %q, want %q", got, want)
	}
}

// TestLexicalWithin: the search by name takes Gateway.LexicalTimeout, or what is
// left before the answer is due when that is less, in the whole
// milliseconds a statement timeout takes, and none under a millisecond.
func TestLexicalWithin(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	for _, tt := range []struct {
		name string
		left time.Duration
		want time.Duration
	}{
		{"plenty left", time.Minute, tuning.Get().Gateway.LexicalTimeout},
		{"exactly the timeout left", tuning.Get().Gateway.LexicalTimeout, tuning.Get().Gateway.LexicalTimeout},
		{"less than the timeout left", 2*time.Second + 500*time.Microsecond, 2 * time.Second},
		{"a millisecond left", time.Millisecond, time.Millisecond},
		{"under a millisecond left", 999 * time.Microsecond, 0},
		{"due now", 0, 0},
		{"past due", -time.Second, 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := lexicalWithin(now, now.Add(tt.left)); got != tt.want {
				t.Errorf("lexicalWithin with %v left = %v, want %v", tt.left, got, tt.want)
			}
		})
	}
}

// TestRerankDeadline: reranking, the wait for a slot included, ends at the
// reranker's configured timeout, not the default one, unless the request's
// answer is due first.
func TestRerankDeadline(t *testing.T) {
	now := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name     string
		timeout  time.Duration
		answerBy time.Time
		want     time.Time
	}{
		{name: "the configured timeout", timeout: time.Second, answerBy: now.Add(time.Hour), want: now.Add(time.Second)},
		{name: "a configured timeout past the default", timeout: time.Minute, answerBy: now.Add(time.Hour), want: now.Add(time.Minute)},
		{name: "the default timeout", answerBy: now.Add(time.Hour), want: now.Add(tuning.Get().ConfigFile.DefaultRerankTimeout)},
		{name: "an answer due first", timeout: time.Minute, answerBy: now.Add(time.Second), want: now.Add(time.Second)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := rerankDeadline(now, &configfile.Rerank{Timeout: tc.timeout}, tc.answerBy); !got.Equal(tc.want) {
				t.Fatalf("rerankDeadline = %s, want %s", got, tc.want)
			}
		})
	}
}

// TestPastFull: a query whose full window the file cap leaves short is
// searched again past the files that fill it, and a search that fails ends
// the looking but keeps every hit found until then.
func TestPastFull(t *testing.T) {
	hit := func(query int, path string, line int) store.SimilarHit {
		return store.SimilarHit{Query: query, Chunk: contextpack.Chunk{Path: path, StartLine: line}}
	}
	hits := make([]store.SimilarHit, 0, 2*tuning.Get().Gateway.SimilarDepth()+1)
	for q := range 2 {
		for i := range tuning.Get().Gateway.SimilarDepth() {
			hits = append(hits, hit(q, "a.go", i+1))
		}
	}
	hits = append(hits, hit(2, "c.go", 1))
	full := func(found, window []store.SimilarHit) []string {
		return fullFiles(found, window, tuning.Get().Gateway.SimilarDepth(), tuning.Get().Gateway.SimilarPerQuery, func([]store.SimilarHit, store.SimilarHit) bool { return true })
	}
	failed := errors.New("search failed")
	for _, tt := range []struct {
		name     string
		fail     int
		want     []string
		wantErr  error
		searched []int
	}{
		{"every search answers", -1, []string{"0:b.go", "1:b.go"}, nil, []int{0, 1}},
		{"the second search fails", 1, []string{"0:b.go"}, failed, []int{0, 1}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var searched []int
			got, err := pastFull(hits, 3, tuning.Get().Gateway.SimilarPerQuery, full, func(q int, files []string) ([]store.SimilarHit, error) {
				searched = append(searched, q)
				if !slices.Equal(files, []string{"a.go"}) {
					t.Errorf("query %d searched past %q, want a.go", q, files)
				}
				if q == tt.fail {
					return nil, failed
				}
				return []store.SimilarHit{hit(0, "b.go", 1)}, nil
			})
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("pastFull error = %v, want %v", err, tt.wantErr)
			}
			if !slices.Equal(searched, tt.searched) {
				t.Errorf("searched queries %v, want %v", searched, tt.searched)
			}
			var past []string
			for _, h := range got {
				if h.Chunk.Path != "a.go" && h.Chunk.Path != "c.go" {
					past = append(past, fmt.Sprintf("%d:%s", h.Query, h.Chunk.Path))
				}
			}
			if !slices.Equal(past, tt.want) {
				t.Errorf("found past the full files %q, want %q", past, tt.want)
			}
			if len(got) != len(hits)+len(tt.want) {
				t.Errorf("%d hits, want the first search's %d kept beside those past the full files", len(got), len(hits))
			}
		})
	}
}

// TestPastFullNamesLanguage: the search by name's look past a query's full
// files splits the query by its own file's language, as its first search
// did, so a minus in Go joins no name there either.
func TestPastFullNamesLanguage(t *testing.T) {
	named := make([]store.SimilarHit, 0, tuning.Get().Gateway.SimilarDepth())
	for i := range tuning.Get().Gateway.SimilarDepth() {
		named = append(named, store.SimilarHit{Query: 1, Lexical: 1, Chunk: contextpack.Chunk{Path: "a.go", StartLine: i + 1}})
	}
	type searched struct{ texts, languages []string }
	var got []searched
	_, err := pastFullNames(named, []string{"key-name: 1", "d := end-start"}, []string{"yaml", "go"}, [][]float32{{1}, {1}},
		store.Exclusions{}, tuning.Get().Gateway.SimilarDepth(), tuning.Get().Gateway.SimilarPerQuery, func(texts, languages []string, _ [][]float32, _ store.Exclusions) ([]store.SimilarHit, error) {
			got = append(got, searched{texts, languages})
			return nil, nil
		})
	if err != nil {
		t.Fatal(err)
	}
	want := []searched{{[]string{"d := end-start"}, []string{"go"}}}
	if !slices.EqualFunc(got, want, func(a, b searched) bool {
		return slices.Equal(a.texts, b.texts) && slices.Equal(a.languages, b.languages)
	}) {
		t.Fatalf("looked past the full files with %q, want %q", got, want)
	}
}

// TestSimilarFollowsTuning: the file cap, an answer's default size and a
// query's share come from tuning, as the process configured it.
func TestSimilarFollowsTuning(t *testing.T) {
	hits := []store.SimilarHit{
		{Chunk: contextpack.Chunk{Path: "a.go", StartLine: 1}, Similarity: 0.95},
		{Chunk: contextpack.Chunk{Path: "a.go", StartLine: 10}, Similarity: 0.94},
		{Chunk: contextpack.Chunk{Path: "a.go", StartLine: 20}, Similarity: 0.93},
		{Chunk: contextpack.Chunk{Path: "b.go", StartLine: 1}, Similarity: 0.92},
	}
	perFile := func() map[string]int {
		got, _ := keepSimilar(hits, 0.5, 0.1)
		n := map[string]int{}
		for _, c := range got {
			n[c.Path]++
		}
		return n
	}
	for _, tt := range []struct {
		name                 string
		mutate               func(*tuning.Values)
		perFile              map[string]int
		answer, query, depth int
	}{
		{"defaults", func(*tuning.Values) {}, map[string]int{"a.go": 2, "b.go": 1}, 10, 4, 8},
		{"one chunk of a file", func(v *tuning.Values) { v.Gateway.SimilarPerFile = 1 }, map[string]int{"a.go": 1, "b.go": 1}, 10, 4, 8},
		{"a smaller answer", func(v *tuning.Values) { v.Gateway.SimilarMax = 2 }, map[string]int{"a.go": 2}, 2, 4, 8},
		{"a smaller share", func(v *tuning.Values) { v.Gateway.SimilarPerQuery = 2 }, map[string]int{"a.go": 2}, 10, 2, 4},
	} {
		t.Run(tt.name, func(t *testing.T) {
			tuning.Override(t, tt.mutate)
			if got := perFile(); !maps.Equal(got, tt.perFile) {
				t.Errorf("kept per file = %v, want %v", got, tt.perFile)
			}
			if got := answerLimit(0); got != tt.answer {
				t.Errorf("answerLimit(0) = %d, want %d", got, tt.answer)
			}
			if got := queryLimit(contextpack.SimilarKindDiff, 1, 0); got != tt.query {
				t.Errorf("queryLimit = %d, want %d", got, tt.query)
			}
			if got := tuning.Get().Gateway.SimilarDepth(); got != tt.depth {
				t.Errorf("SimilarDepth = %d, want %d", got, tt.depth)
			}
		})
	}
}
