package gateway

import (
	"fmt"
	"maps"
	"math"
	"reflect"
	"slices"
	"testing"

	"github.com/home-operations/kritika/internal/contextpack"
	"github.com/home-operations/kritika/internal/store"
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
	best := make([]store.SimilarHit, 0, 3+similarMax)
	best = append(best, hit("best.go", 1, 0.55), hit("other.go", 1, 0.80), hit("best.go", 1, 0.99))
	wantBest := make([]string, 0, similarMax)
	wantBest = append(wantBest, "best.go:1:similarity 0.99", "other.go:1:similarity 0.80")
	for i := range similarMax {
		path := fmt.Sprintf("candidate%d.go", i)
		best = append(best, hit(path, 1, 0.70))
		if len(wantBest) < similarMax {
			wantBest = append(wantBest, path+":1:similarity 0.70")
		}
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
	many := make([]store.SimilarHit, 0, similarMax+2)
	for i := range similarMax + 2 {
		many = append(many, hit(fmt.Sprintf("f%d.go", i), 1, 0.9))
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
		{"past the limit", many, map[string]int{hitKept: similarMax, hitLimit: 2}},
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
	return keepCandidates(kept, counts, similarMax), counts
}

// TestSimilarLimit: an answer holds the chunks its request asks for, up to
// contextpack.SimilarLimit, and similarMax when it asks for none.
func TestSimilarLimit(t *testing.T) {
	for _, tt := range []struct{ asked, want int }{
		{0, similarMax}, {-1, similarMax}, {3, 3}, {contextpack.SimilarLimit, contextpack.SimilarLimit},
		{contextpack.SimilarLimit + 1, contextpack.SimilarLimit},
	} {
		t.Run(fmt.Sprint(tt.asked), func(t *testing.T) {
			if got := answerLimit(tt.asked); got != tt.want {
				t.Fatalf("answerLimit(%d) = %d, want %d", tt.asked, got, tt.want)
			}
		})
	}
	hits := make([]store.SimilarHit, 0, contextpack.SimilarLimit)
	for i := range contextpack.SimilarLimit {
		hits = append(hits, store.SimilarHit{Chunk: contextpack.Chunk{Path: fmt.Sprintf("f%d.go", i), StartLine: 1}, Similarity: 0.9})
	}
	kept, counts, _ := gateCandidates(fuse(hits, nil), 0.5, 0.1)
	if got := keepCandidates(kept, counts, contextpack.SimilarLimit); len(got) != contextpack.SimilarLimit {
		t.Fatalf("kept %d, want %d", len(got), contextpack.SimilarLimit)
	}
}

// TestHybridSimilar: the lexical search's hits join the vector search's.
// Either search can vouch for a chunk, which is labelled by what vouched
// for it, and each query keeps its similarPerQuery best by fused rank.
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
				{hitKept, sourceVector}: 1, {hitKept, sourceLexical}: 1, {hitMargin, sourceLexical}: 1,
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
			got := keepCandidates(topPerQuery(kept, similarPerQuery, counts), counts, similarMax)
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

// TestGateRanks: a candidate scores 1 / (rrfK + rank) for each search that
// found it, ranked among those of its query that the search found, whether
// or not that search vouches for it.
func TestGateRanks(t *testing.T) {
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
		"low.go": 1.0/(rrfK+2) + 1.0/(rrfK+2), "high.go": 1.0 / (rrfK + 1), "other.go": 1.0 / (rrfK + 1),
		"under.go": 1.0/(rrfK+3) + 1.0/(rrfK+1),
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
