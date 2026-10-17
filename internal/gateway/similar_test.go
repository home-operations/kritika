package gateway

import (
	"fmt"
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
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			original := slices.Clone(tt.hits)
			got := keepSimilar(tt.hits, tt.floor, tt.margin)
			if !slices.Equal(original, tt.hits) {
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
