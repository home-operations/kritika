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
		name  string
		hits  []store.SimilarHit
		floor float64
		want  []string
	}{
		{"default floor", hits, 0.5, []string{"b.go:1:similarity 0.91", "c.go:10:similarity 0.70", "a.go:1:similarity 0.65"}},
		{"higher floor", hits, 0.7, []string{"b.go:1:similarity 0.91", "c.go:10:similarity 0.70"}},
		{"nothing clears floor", hits, 0.95, nil},
		{"best duplicate survives result limit", best, 0.5, wantBest},
		{"empty", nil, 0.5, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			original := slices.Clone(tt.hits)
			got := keepSimilar(tt.hits, tt.floor)
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
