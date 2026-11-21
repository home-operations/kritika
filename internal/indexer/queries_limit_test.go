package indexer

import (
	"testing"

	"github.com/home-operations/kritika/internal/chunk"
	"github.com/home-operations/kritika/internal/contextpack"
	"github.com/home-operations/kritika/internal/udiff"
)

// TestRegionQueriesStopAtTheRequestBound: a region is cut into no more
// queries than one request carries, however many declarations or windows of
// its lines it holds, so a large added file costs the queries a request can
// send and not one for each of its declarations.
func TestRegionQueriesStopAtTheRequestBound(t *testing.T) {
	for _, first := range []bool{true, false} {
		files := udiff.Parse(manyDeclarations(first))
		if len(files) != 1 {
			t.Fatalf("%d files", len(files))
		}
		f := &queryFile{path: files[0].NewPath}
		for _, h := range files[0].Hunks {
			if region, n := changedRegion(h); n > 0 {
				f.regions = append(f.regions, region)
			}
		}
		var parser chunk.Parser
		if _, ok := f.next(&parser); !ok {
			t.Fatalf("declarations first %v: no query", first)
		}
		if built := len(f.pending) + 1; built > contextpack.SimilarQueries {
			t.Errorf("declarations first %v: the region was cut into %d queries, more than the %d a request carries",
				first, built, contextpack.SimilarQueries)
		}
	}
}
