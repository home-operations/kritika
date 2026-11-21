package runner

import (
	"cmp"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/home-operations/kritika/internal/contextpack"
	"github.com/home-operations/kritika/internal/gitfetch"
	"github.com/home-operations/kritika/internal/indexer"
	"github.com/home-operations/kritika/internal/runspec"
)

// fakeSimilarGateway answers a run's exclusion report with status, and its
// similar-code requests with answer. It records both. A similar-code request
// over maxBody, when set, is refused as the gateway refuses one over its
// bound.
type fakeSimilarGateway struct {
	status  int
	answer  contextpack.SimilarResponse
	maxBody int64

	mu       sync.Mutex
	excluded [][]string
	requests []contextpack.SimilarRequest
}

func (f *fakeSimilarGateway) serve(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		switch r.URL.Path {
		case runspec.ReportSimilarExcludePath:
			if r.Header.Get("Authorization") != "Bearer report" {
				t.Errorf("exclusions reported with %q, not the report token", r.Header.Get("Authorization"))
			}
			var v runspec.SimilarExcludeReport
			if err := json.NewDecoder(r.Body).Decode(&v); err != nil {
				t.Error(err)
			}
			f.excluded = append(f.excluded, v.Paths)
			w.WriteHeader(f.status)
		case "/v1/similar":
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Error(err)
			}
			if f.maxBody > 0 && int64(len(body)) > f.maxBody {
				http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
				return
			}
			var req contextpack.SimilarRequest
			if err := json.Unmarshal(body, &req); err != nil {
				t.Error(err)
			}
			f.requests = append(f.requests, req)
			_ = json.NewEncoder(w).Encode(f.answer)
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestSimilarContextExclusions: a run first asks, with no queries, whether
// its repository has an index. It reports the paths its searches leave out
// once, both names of a rename among them, and only when there is an index.
// Its requests carry none. A run whose report is refused, or still fails
// once similarReportWindow is over, searches nothing: the index would answer
// with the old copies of the files the prompt shows.
func TestSimilarContextExclusions(t *testing.T) {
	const diff = "diff --git a/old.go b/new.go\nsimilarity index 80%\nrename from old.go\nrename to new.go\n" +
		"--- a/old.go\n+++ b/new.go\n@@ -1,1 +1,2 @@\n package main\n+func n() {}\n"
	res := &gitfetch.Result{Diff: diff, Changed: []string{"new.go", "main.go"}, RenamedFrom: []string{"old.go"}}
	window := similarReportWindow
	similarReportWindow = 300 * time.Millisecond
	t.Cleanup(func() { similarReportWindow = window })
	excluded := [][]string{{"main.go", "new.go", "old.go"}}
	for _, tt := range []struct {
		name     string
		status   int
		indexed  bool
		excluded [][]string
		queries  []int
		offered  bool
	}{
		{name: "reported", status: http.StatusNoContent, indexed: true, excluded: excluded, queries: []int{0, 1}, offered: true},
		{name: "refused", status: http.StatusBadRequest, indexed: true, excluded: excluded, queries: []int{0}},
		{name: "never answered", status: http.StatusServiceUnavailable, indexed: true, queries: []int{0}},
		{name: "no index", status: http.StatusNoContent, queries: []int{0}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			gw := &fakeSimilarGateway{status: tt.status, answer: contextpack.SimilarResponse{Indexed: tt.indexed}}
			s := reviewSpec()
			s.GatewayURL = gw.serve(t).URL
			started := time.Now()
			_, search := similarContext(t.Context(), s, runspec.Secrets{GatewayToken: "run", ReportToken: "report"}, nil, res, nil,
				slog.New(slog.DiscardHandler))
			if elapsed := time.Since(started); elapsed > 5*time.Second {
				t.Fatalf("similar context took %s", elapsed)
			}
			gw.mu.Lock()
			defer gw.mu.Unlock()
			// A report that the gateway fails is sent again until the window
			// ends, however many times that is.
			if tt.status != http.StatusServiceUnavailable && !slices.EqualFunc(gw.excluded, tt.excluded, slices.Equal) {
				t.Fatalf("reported exclusions %q, want %q", gw.excluded, tt.excluded)
			}
			var queries []int
			for _, req := range gw.requests {
				queries = append(queries, len(req.Queries))
			}
			if !slices.Equal(queries, tt.queries) || (search != nil) != tt.offered {
				t.Fatalf("similar requests of %v queries, search_code offered %t; want %v, %t", queries, search != nil, tt.queries, tt.offered)
			}
			for _, req := range gw.requests {
				if req.Exclude != nil || req.Kind != contextpack.SimilarKindDiff {
					t.Fatalf("a request carries exclusions %q, or is of kind %q", req.Exclude, req.Kind)
				}
				if len(req.Queries) > 0 && req.Limit != contextpack.SimilarLimit {
					t.Fatalf("a search asks for %d chunks, want %d", req.Limit, contextpack.SimilarLimit)
				}
			}
		})
	}
}

// TestSimilarContextLargeChange: a change of thousands of files still gets
// similar code. Its exclusions, which outgrow the gateway's 256 KiB bound on
// a similar-code request, are reported once, and its requests carry only
// their queries.
func TestSimilarContextLargeChange(t *testing.T) {
	changed := make([]string, 0, 4000)
	for i := range cap(changed) {
		changed = append(changed, fmt.Sprintf("vendor/example.com/a-module-with-a-long-name/pkg/subpackage/file%04d.go", i))
	}
	res := &gitfetch.Result{Diff: fileDiff("", "a.go", added("func a() {}")), Changed: append(changed, "a.go")}
	found := contextpack.Chunk{Stage: contextpack.StageSimilar, Path: "lib/b.go", StartLine: 1, EndLine: 1, Text: "func b() {}"}
	gw := &fakeSimilarGateway{
		status: http.StatusNoContent, maxBody: 256 << 10,
		answer: contextpack.SimilarResponse{Indexed: true, Chunks: []contextpack.Chunk{found}},
	}
	s := reviewSpec()
	s.GatewayURL = gw.serve(t).URL
	chunks, search := similarContext(t.Context(), s, runspec.Secrets{GatewayToken: "run", ReportToken: "report"},
		tree(t, map[string]string{"lib/b.go": "func b() {}\n"}), res, nil, slog.New(slog.DiscardHandler))
	if !slices.Equal(chunks, []contextpack.Chunk{found}) || search == nil {
		t.Fatalf("similar code %+v, search_code offered %t; want the gateway's chunk and search_code", chunks, search != nil)
	}
	gw.mu.Lock()
	defer gw.mu.Unlock()
	if len(gw.excluded) != 1 || len(gw.excluded[0]) != len(res.Changed) {
		t.Fatalf("reported %d exclusion lists, want one of %d paths", len(gw.excluded), len(res.Changed))
	}
}

// fileDiff is one file's diff of hunks, each given as its "@@" line and
// its lines.
func fileDiff(oldPath, newPath string, hunks ...string) string {
	from, to := "a/"+oldPath, "b/"+newPath
	if oldPath == "" {
		from = "/dev/null"
	}
	if newPath == "" {
		to = "/dev/null"
	}
	name := cmp.Or(newPath, oldPath)
	return "diff --git a/" + name + " b/" + name + "\n--- " + from + "\n+++ " + to + "\n" + strings.Join(hunks, "")
}

// added is a hunk of a new file of lines.
func added(lines ...string) string {
	return fmt.Sprintf("@@ -0,0 +1,%d @@\n+", len(lines)) + strings.Join(lines, "\n+") + "\n"
}

func TestHunkQueries(t *testing.T) {
	license := strings.Repeat("// Licensed under the terms that are long enough to fill a query on their own.\n", 50)
	newFile := added(append(strings.Split(strings.TrimSuffix(license, "\n"), "\n"),
		"package lib", "", "import \"fmt\"", "", "func A() {", "\tfmt.Println(\"a\")", "}", "", "func B() {}")...)
	filler := make([]string, 0, 200)
	for i := range 200 {
		filler = append(filler, fmt.Sprintf("line %d of a text with no declarations to cut it by", i))
	}
	tests := []struct {
		name   string
		diff   string
		ignore []string
		want   []string
	}{
		{name: "the changed region as the head has it", diff: agentDiff, want: []string{"package main\n\nfunc b() {}\n"}},
		{
			name: "a little context around the changed lines",
			diff: fileDiff("a.go", "a.go", "@@ -1,9 +1,9 @@\n c1\n c2\n c3\n c4\n c5\n-old\n+new\n c6\n c7\n"),
			want: []string{"c3\nc4\nc5\nnew\nc6\nc7\n"},
		},
		{
			name: "ignored paths and paths the index holds no chunk of",
			diff: fileDiff("vendor/x.go", "vendor/x.go", added("func v() {}")) + fileDiff("", "go.sum", added("example.com/m v1.0.0 h1:x")) +
				fileDiff("", "a.go", added("func a() {}")),
			ignore: []string{"vendor/**"}, want: []string{"func a() {}\n"},
		},
		{
			name: "encrypted and generated files the index leaves out",
			diff: fileDiff("", "secrets.sops.yaml", added("password: ENC[AES256_GCM,data:x]")) +
				fileDiff("", "api/v1/api_pb2.py", added("DESCRIPTOR = _descriptor.FileDescriptor()")) +
				fileDiff("", "a.go", added("func a() {}")),
			want: []string{"func a() {}\n"},
		},
		{
			name: "a hunk that adds nothing and a deleted file",
			diff: fileDiff("a.go", "a.go", "@@ -1,3 +1,2 @@\n x\n-y\n z\n", "@@ -9,2 +8,3 @@\n p\n+\n q\n") +
				fileDiff("gone.go", "", "@@ -1,1 +0,0 @@\n-func g() {}\n"),
		},
		{
			name: "files in turn, the most changed first",
			diff: fileDiff("a.go", "a.go", "@@ -1,1 +1,2 @@\n a\n+a1\n", "@@ -20,1 +21,2 @@\n a\n+a2\n", "@@ -40,1 +42,2 @@\n a\n+a3\n") +
				fileDiff("b.go", "b.go", "@@ -1,1 +1,5 @@\n b\n+b1\n+b2\n+b3\n+b4\n"),
			want: []string{"b\nb1\nb2\nb3\nb4\n", "a\na1\n", "a\na2\n", "a\na3\n"},
		},
		{
			name: "a large new file by its declarations, not its license",
			diff: fileDiff("", "lib.go", newFile),
			want: []string{"func A() {\n\tfmt.Println(\"a\")\n}\n", "func B() {}\n"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := hunkQueries(tt.diff, tt.ignore); !slices.Equal(got, tt.want) {
				t.Fatalf("hunkQueries = %q, want %q", got, tt.want)
			}
		})
	}
	t.Run("a large region without declarations in windows of its changed lines", func(t *testing.T) {
		got := hunkQueries(fileDiff("", "notes.md", added(filler...)), nil)
		if len(got) < 2 || !strings.HasPrefix(got[0], "line 0 ") || !strings.HasPrefix(got[1], "line ") {
			t.Fatalf("windows = %q", got)
		}
		for _, q := range got {
			if len(q) > contextpack.SimilarQueryChars {
				t.Fatalf("a window of %d characters", len(q))
			}
		}
	})
	t.Run("bounded", func(t *testing.T) {
		var many strings.Builder
		for i := range contextpack.SimilarQueries + 3 {
			many.WriteString(fileDiff("", fmt.Sprintf("f%d.go", i), added(fmt.Sprintf("func f%d() {}", i))))
		}
		if got := hunkQueries(many.String(), nil); len(got) != contextpack.SimilarQueries {
			t.Fatalf("%d queries, want %d", len(got), contextpack.SimilarQueries)
		}
		if got := hunkQueries("", nil); len(got) != 0 {
			t.Fatalf("queries of an empty diff = %q", got)
		}
	})
}

// TestWithoutOverlap: a similar chunk is left out when other stages already
// show more than half of its lines, and kept when they show fewer.
func TestWithoutOverlap(t *testing.T) {
	similar := func(path string, start, end int) contextpack.Chunk {
		return contextpack.Chunk{Stage: contextpack.StageSimilar, Path: path, StartLine: start, EndLine: end}
	}
	others := []contextpack.Chunk{
		{Stage: contextpack.StageDefinition, Path: "util.go", StartLine: 10, EndLine: 20},
		{Stage: contextpack.StageCaller, Path: "caller.go", StartLine: 5, EndLine: 5},
		{Stage: contextpack.StageCaller, Path: "two.go", StartLine: 1, EndLine: 3},
		{Stage: contextpack.StageDefinition, Path: "two.go", StartLine: 6, EndLine: 8},
		similar("kept.go", 1, 9),
	}
	for _, tt := range []struct {
		name string
		in   contextpack.Chunk
		kept bool
	}{
		{"apart", similar("util.go", 1, 9), true},
		{"a few lines shown", similar("util.go", 18, 30), true},
		{"half shown", similar("util.go", 16, 25), true},
		{"most shown", similar("util.go", 15, 24), false},
		{"inside", similar("util.go", 12, 14), false},
		{"one line of five", similar("caller.go", 1, 5), true},
		{"most shown by two", similar("two.go", 2, 8), false},
		{"another file", similar("other.go", 10, 20), true},
		{"under another similar chunk", similar("kept.go", 1, 9), true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := withoutOverlap([]contextpack.Chunk{tt.in}, others); (len(got) == 1) != tt.kept {
				t.Fatalf("withoutOverlap = %+v, want kept %t", got, tt.kept)
			}
		})
	}
}

// TestSimilarShown: a prompt shows at most maxSimilarShown of the chunks
// found, counted after it drops those that the head no longer has or that
// other stages show.
func TestSimilarShown(t *testing.T) {
	files := map[string]string{}
	found := make([]contextpack.Chunk, 0, contextpack.SimilarLimit)
	for i := range contextpack.SimilarLimit {
		path := fmt.Sprintf("f%02d.go", i)
		files[path] = fmt.Sprintf("func f%d() {}\n", i)
		found = append(found, contextpack.Chunk{Stage: contextpack.StageSimilar, Path: path, StartLine: 1, EndLine: 1, Text: fmt.Sprintf("func f%d() {}", i)})
	}
	found[0].Text = "func gone() {}"
	others := []contextpack.Chunk{{Stage: contextpack.StageDefinition, Path: "f01.go", StartLine: 1, EndLine: 1}}
	got := similarShown(tree(t, files), found, others, slog.New(slog.DiscardHandler))
	if want := found[2 : 2+maxSimilarShown]; !slices.Equal(got, want) {
		t.Fatalf("shown %+v, want %+v", got, want)
	}
}

// TestSimilarContextWithoutQueries:a diff with nothing to search for still
// asks whether the repository has an index, with no queries, and offers
// search_code when it has.
func TestSimilarContextWithoutQueries(t *testing.T) {
	res := &gitfetch.Result{Diff: fileDiff("go.sum", "go.sum", "@@ -1,1 +1,2 @@\n a\n+b\n"), Changed: []string{"go.sum"}}
	for _, indexed := range []bool{true, false} {
		t.Run(fmt.Sprint(indexed), func(t *testing.T) {
			gw := &fakeSimilarGateway{status: http.StatusNoContent, answer: contextpack.SimilarResponse{Indexed: indexed, Chunks: []contextpack.Chunk{}}}
			s := reviewSpec()
			s.GatewayURL = gw.serve(t).URL
			chunks, search := similarContext(t.Context(), s, runspec.Secrets{GatewayToken: "run", ReportToken: "report"}, nil, res, nil,
				slog.New(slog.DiscardHandler))
			if len(gw.requests) != 1 || len(gw.requests[0].Queries) != 0 || len(chunks) != 0 || (search != nil) != indexed {
				t.Fatalf("requests %+v, %d chunks, search_code offered %t; want one without queries, offered %t", gw.requests, len(chunks),
					search != nil, indexed)
			}
		})
	}
}

// TestAtHead: a similar chunk is shown as the head has it. One whose text
// moved is shown at its new lines, and one cut short claims only the lines
// it shows. One whose text or file the head no longer has is left out, as is
// one not cut short whose last line the head has more of.
func TestAtHead(t *testing.T) {
	long := "var x = \"" + strings.Repeat("x", indexer.DefaultOptions.MaxChunkBytes) + "\""
	head := tree(t, map[string]string{"lib.go": "package lib\n\nfunc a() {\n\treturn\n}\n\nfunc b() {}\n", "long.go": "package lib\n" + long + "\n"})
	chunk := func(path string, start, end int, text string) contextpack.Chunk {
		return contextpack.Chunk{Stage: contextpack.StageSimilar, Path: path, StartLine: start, EndLine: end, Text: text}
	}
	for _, tt := range []struct {
		name string
		in   contextpack.Chunk
		want []contextpack.Chunk
	}{
		{"where the index has it", chunk("lib.go", 3, 5, "func a() {\n\treturn\n}"), []contextpack.Chunk{chunk("lib.go", 3, 5, "func a() {\n\treturn\n}")}},
		{"moved", chunk("lib.go", 1, 3, "func a() {\n\treturn\n}"), []contextpack.Chunk{chunk("lib.go", 3, 5, "func a() {\n\treturn\n}")}},
		{"cut short", chunk("lib.go", 3, 5, "func a() {\n\tret"), []contextpack.Chunk{chunk("lib.go", 3, 4, "func a() {\n\tret")}},
		{
			"one line cut short", chunk("long.go", 2, 2, long[:indexer.DefaultOptions.MaxChunkBytes]),
			[]contextpack.Chunk{chunk("long.go", 2, 2, long[:indexer.DefaultOptions.MaxChunkBytes])},
		},
		{"a last line the head has more of", chunk("lib.go", 3, 4, "func a() {\n\tret"), nil},
		{"changed", chunk("lib.go", 3, 5, "func a() {\n\tpanic(1)\n}"), nil},
		{"past the file's end", chunk("lib.go", 7, 9, "func b() {}\n\nfunc c() {}"), nil},
		{"gone", chunk("old.go", 1, 1, "func b() {}"), nil},
		{"empty", chunk("lib.go", 1, 1, ""), nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := atHead(head, []contextpack.Chunk{tt.in}); !slices.Equal(got, tt.want) {
				t.Fatalf("atHead = %+v, want %+v", got, tt.want)
			}
		})
	}
	t.Run("found at lines an earlier one mostly shows", func(t *testing.T) {
		a := "func a() {\n\treturn\n}"
		got := atHead(head, []contextpack.Chunk{chunk("lib.go", 3, 5, a), chunk("lib.go", 1, 3, a), chunk("lib.go", 5, 7, "}\n\nfunc b() {}")})
		if want := []contextpack.Chunk{chunk("lib.go", 3, 5, a), chunk("lib.go", 5, 7, "}\n\nfunc b() {}")}; !slices.Equal(got, want) {
			t.Fatalf("atHead = %+v, want %+v", got, want)
		}
	})
}

// TestSearchToolAtHead: search_code shows what it finds as the head has it,
// at the head's lines, and leaves out what the head no longer has.
func TestSearchToolAtHead(t *testing.T) {
	head := tree(t, map[string]string{"retry.go": "package r\n\n// retry tries again.\nfunc retry() {}\n"})
	moved := contextpack.Chunk{Stage: contextpack.StageSimilar, Path: "retry.go", StartLine: 1, EndLine: 1, Ref: "similarity 0.81",
		Text: "func retry() {}"}
	gone := contextpack.Chunk{Stage: contextpack.StageSimilar, Path: "backoff.go", StartLine: 1, EndLine: 1, Ref: "similarity 0.80",
		Text: "func backoff() {}"}
	changed := moved
	changed.Text = "func retry() { panic(1) }"
	for _, tt := range []struct {
		name  string
		found []contextpack.Chunk
		want  string
	}{
		{"moved", []contextpack.Chunk{moved}, "retry.go:4-4 similarity 0.81\nfunc retry() {}"},
		{"the file gone", []contextpack.Chunk{gone, moved}, "retry.go:4-4 similarity 0.81\nfunc retry() {}"},
		{"the text changed", []contextpack.Chunk{changed}, "No similar code found."},
	} {
		t.Run(tt.name, func(t *testing.T) {
			gw := &fakeSimilarGateway{answer: contextpack.SimilarResponse{Indexed: true, Chunks: tt.found}}
			tool := &searchTool{gatewayURL: gw.serve(t).URL, token: "run", head: head, maxBytes: 1 << 10}
			if out, err := tool.Run(t.Context(), json.RawMessage(`{"query":"retry"}`)); err != nil || out != tt.want {
				t.Fatalf("Run = %q, %v; want %q", out, err, tt.want)
			}
		})
	}
}
