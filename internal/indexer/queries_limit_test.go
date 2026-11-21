package indexer

import (
	"fmt"
	"runtime"
	"slices"
	"strconv"
	"strings"
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

// TestRegionQueriesDoNotCopyDeclarationsOverTheBound: a declaration over the
// bound that gives way to a nested one is measured in place, so the queries
// of a region nested two hundred deep around one changed line cost a few
// copies of the region for its parse, and not a copy of each of the two
// hundred declarations around the change. Allocation is counted in bytes,
// which does not depend on how fast the machine is.
func TestRegionQueriesDoNotCopyDeclarationsOverTheBound(t *testing.T) {
	const depth = 200
	region := make([]udiff.Line, 0, 2*depth+4)
	for i := range depth {
		region = append(region, udiff.Line{Kind: ' ', Text: fmt.Sprintf("function outer%d() {", i)})
	}
	region = append(region,
		udiff.Line{Kind: ' ', Text: "  const padding = " + strconv.Quote(strings.Repeat("p", 200<<10)) + ";"},
		udiff.Line{Kind: ' ', Text: "function inner() {"}, udiff.Line{Kind: '+', Text: "  return 1;"}, udiff.Line{Kind: ' ', Text: "}"})
	for range depth {
		region = append(region, udiff.Line{Kind: ' ', Text: "}"})
	}
	size := 0
	for _, l := range region {
		size += len(l.Text) + 1
	}
	var parser chunk.Parser
	if got, want := regionQueries(&parser, "x.js", region, contextpack.SimilarQueries), []string{"function inner() {\n  return 1;\n}\n"}; !slices.Equal(got, want) {
		t.Fatalf("queries = %q, want %q", got, want)
	}
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	regionQueries(&parser, "x.js", region, contextpack.SimilarQueries)
	runtime.ReadMemStats(&after)
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated > depth/2*uint64(size) {
		t.Errorf("the queries allocated %d bytes, over %d copies of the %d-byte region", allocated, depth/2, size)
	}
}
