package runner

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-git/go-git/v6/plumbing/object"

	"github.com/home-operations/kritika/internal/contextpack"
	"github.com/home-operations/kritika/internal/gitfetch"
	"github.com/home-operations/kritika/internal/indexer"
	"github.com/home-operations/kritika/internal/runspec"
	"github.com/home-operations/kritika/internal/textcut"
	"github.com/home-operations/kritika/internal/tuning"
)

// fakeSimilarGateway answers a run's exclusion report with status, and its
// similar-code requests with answer. It records both. A similar-code request
// over maxBody, when set, is refused as the gateway refuses one over its
// bound. When unanswered is set, it answers no exclusion report, and closes
// it once the run gives the report up. When reported is set, it is called
// with the number of exclusion reports so far as each arrives. When perQuery
// is set, an answer to a request of one query holds that many of answer's
// chunks at most, or the request's Limit when that is more.
type fakeSimilarGateway struct {
	status     int
	answer     contextpack.SimilarResponse
	maxBody    int64
	unanswered chan struct{}
	reported   func(n int)
	perQuery   int

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
			if f.reported != nil {
				f.reported(len(f.excluded))
			}
			if f.unanswered != nil {
				f.mu.Unlock()
				<-r.Context().Done()
				close(f.unanswered)
				f.mu.Lock()
				return
			}
			w.WriteHeader(f.status)
		case runspec.SimilarPath:
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
			answer := f.answer
			if f.perQuery > 0 && len(req.Queries) == 1 {
				answer.Chunks = answer.Chunks[:min(len(answer.Chunks), max(f.perQuery, req.Limit))]
			}
			_ = json.NewEncoder(w).Encode(answer)
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
// Its requests carry none. A run whose report is refused, fails until the
// run ends, or is not answered within Runner.SimilarReportWindow, searches nothing:
// the index would answer with the old copies of the files the prompt shows.
// A report that fails is sent again, and the window, not the report's own
// retries, ends the wait.
func TestSimilarContextExclusions(t *testing.T) {
	const diff = "diff --git a/old.go b/new.go\nsimilarity index 80%\nrename from old.go\nrename to new.go\n" +
		"--- a/old.go\n+++ b/new.go\n@@ -1,1 +1,2 @@\n package main\n+func n() {}\n"
	res := &gitfetch.Result{Diff: diff, Changed: []string{"new.go", "main.go"}, RenamedFrom: []string{"old.go"}}
	excluded := [][]string{{"main.go", "new.go", "old.go"}}
	for _, tt := range []struct {
		name       string
		status     int
		unanswered bool
		indexed    bool
		excluded   [][]string
		// endAt is the report at which the gateway ends the run, when set.
		endAt   int
		queries []int
		offered bool
	}{
		{name: "reported", status: http.StatusNoContent, indexed: true, excluded: excluded, queries: []int{0, 1}, offered: true},
		{name: "refused", status: http.StatusBadRequest, indexed: true, excluded: excluded, queries: []int{0}},
		{name: "failing", status: http.StatusServiceUnavailable, indexed: true, excluded: slices.Repeat(excluded, 2), endAt: 2, queries: []int{0}},
		{name: "never answered", unanswered: true, indexed: true, excluded: excluded, queries: []int{0}},
		{name: "no index", status: http.StatusNoContent, queries: []int{0}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			fastRetries(t, time.Minute)
			// Only a report never answered waits for the window to end.
			window := time.Minute
			gw := &fakeSimilarGateway{status: tt.status, answer: contextpack.SimilarResponse{Indexed: tt.indexed}}
			if tt.unanswered {
				window = 300 * time.Millisecond
				gw.unanswered = make(chan struct{})
			}
			tuning.Override(t, func(v *tuning.Values) { v.Runner.SimilarReportWindow = window })
			// A report that fails is sent again until the run ends.
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			gw.reported = func(n int) {
				if n == tt.endAt {
					cancel()
				}
			}
			s := reviewSpec()
			s.GatewayURL = gw.serve(t).URL
			searched := make(chan *searchTool, 1)
			go func() {
				_, search := similarContext(ctx, s, runspec.Secrets{GatewayToken: "run", ReportToken: "report"}, nil, res, nil,
					slog.New(slog.DiscardHandler))
				searched <- search
			}()
			// The report's own retries last a minute, and its timeout longer:
			// only the window or the run's end ends the wait within the test's
			// minute.
			var search *searchTool
			select {
			case search = <-searched:
			case <-time.After(time.Minute):
				t.Fatalf("similar context still waits on its report a minute on, past its %s window", window)
			}
			if gw.unanswered != nil {
				// The run gave the report up rather than being answered. A run
				// that never sent it would leave this wait to the package's
				// timeout.
				select {
				case <-gw.unanswered:
				case <-time.After(time.Minute):
					t.Fatal("the exclusion report was never sent, or never given up, a minute after the run returned")
				}
			}
			gw.mu.Lock()
			defer gw.mu.Unlock()
			if !slices.EqualFunc(gw.excluded, tt.excluded, slices.Equal) {
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
				if len(req.Queries) > 0 && req.Limit != tuning.Get().ContextPack.SimilarLimit {
					t.Fatalf("a search asks for %d chunks, want %d", req.Limit, tuning.Get().ContextPack.SimilarLimit)
				}
				want := slices.Repeat([]string{"go"}, len(req.Queries))
				if !slices.Equal(req.Languages, want) {
					t.Fatalf("a request of %d queries of Go files carries the languages %q, want %q", len(req.Queries), req.Languages, want)
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
		status: http.StatusNoContent, maxBody: int64(tuning.Get().Gateway.MaxSimilarRequest),
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

// TestWithoutOverlap: a similar chunk is left out when other stages already
// show more than half of its lines, and kept when they show fewer.
func TestWithoutOverlap(t *testing.T) {
	similar := func(path string, start, end int) contextpack.Chunk {
		return contextpack.Chunk{Stage: contextpack.StageSimilar, Path: path, StartLine: start, EndLine: end}
	}
	shown := func(stage, path string, start, end int) contextpack.Chunk {
		return contextpack.Chunk{Stage: stage, Path: path, StartLine: start, EndLine: end,
			Text: strings.TrimSuffix(strings.Repeat("code\n", end-start+1), "\n")}
	}
	// Definitions of 200 lines cut as contextpack cuts one over
	// MaxChunkBytes. Of 100-byte lines it shows the first 60 whole, the cut
	// falling after the newline that ends line 60; of 99-byte lines it shows
	// the first 60 and line 61 in part.
	cutAt := func(path string, width int) contextpack.Chunk {
		c := shown(contextpack.StageDefinition, path, 1, 200)
		c.Text = strings.TrimSuffix(strings.Repeat(strings.Repeat("x", width-1)+"\n", 200), "\n")
		c.Text = textcut.Prefix(c.Text, contextpack.DefaultOptions().MaxChunkBytes) + "\n… (truncated)"
		return c
	}
	others := []contextpack.Chunk{
		shown(contextpack.StageDefinition, "util.go", 10, 20),
		shown(contextpack.StageCaller, "caller.go", 5, 5),
		shown(contextpack.StageCaller, "two.go", 1, 3),
		shown(contextpack.StageDefinition, "two.go", 6, 8),
		cutAt("cut.go", 100),
		cutAt("part.go", 99),
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
		{"in the lines a cut definition shows", similar("cut.go", 10, 20), false},
		{"past the lines a cut definition shows", similar("cut.go", 150, 170), true},
		{"the line after a cut at a newline", similar("cut.go", 61, 61), true},
		{"the line a cut shows in part", similar("part.go", 61, 61), false},
		{"the line after one a cut shows in part", similar("part.go", 62, 62), true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := withoutOverlap([]contextpack.Chunk{tt.in}, others); (len(got) == 1) != tt.kept {
				t.Fatalf("withoutOverlap = %+v, want kept %t", got, tt.kept)
			}
		})
	}
}

// TestSimilarShown: a prompt shows at most tuning.Get().Runner.MaxSimilarShown of the chunks
// found, counted after it drops those that the head no longer has or that
// other stages show.
func TestSimilarShown(t *testing.T) {
	files := map[string]string{}
	found := make([]contextpack.Chunk, 0, tuning.Get().ContextPack.SimilarLimit)
	for i := range tuning.Get().ContextPack.SimilarLimit {
		path := fmt.Sprintf("f%02d.go", i)
		files[path] = fmt.Sprintf("func f%d() {}\n", i)
		found = append(found, contextpack.Chunk{Stage: contextpack.StageSimilar, Path: path, StartLine: 1, EndLine: 1, Text: fmt.Sprintf("func f%d() {}", i)})
	}
	found[0].Text = "func gone() {}"
	others := []contextpack.Chunk{{Stage: contextpack.StageDefinition, Path: "f01.go", StartLine: 1, EndLine: 1}}
	got := similarShown(t.Context(), tree(t, files), nil, found, others, slog.New(slog.DiscardHandler))
	if want := found[2 : 2+tuning.Get().Runner.MaxSimilarShown]; !slices.Equal(got, want) {
		t.Fatalf("shown %+v, want %+v", got, want)
	}
	t.Run("a tail that a similar chunk the other stages show overlaps", func(t *testing.T) {
		lines := make([]string, 70)
		for i := range lines {
			lines[i] = fmt.Sprintf("line %d", i+1)
		}
		text := func(start, end int) string { return strings.Join(lines[start-1:end], "\n") }
		chunk := func(stage string, start, end int) contextpack.Chunk {
			return contextpack.Chunk{Stage: stage, Path: "tail.go", StartLine: start, EndLine: end, Text: text(start, end)}
		}
		head := tree(t, map[string]string{"tail.go": strings.Join(lines, "\n") + "\n"})
		found := []contextpack.Chunk{chunk(contextpack.StageSimilar, 1, 60), chunk(contextpack.StageSimilar, 51, 61)}
		others := []contextpack.Chunk{chunk(contextpack.StageDefinition, 1, 40)}
		if got := similarShown(t.Context(), head, nil, found, others, slog.New(slog.DiscardHandler)); !slices.Equal(got, found[1:]) {
			t.Fatalf("shown %+v, want the tail", got)
		}
	})
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
	most := indexer.DefaultOptions().MaxChunkBytes
	long := "var x = \"" + strings.Repeat("x", most) + "\""
	// big's text, as the index cuts it, ends in its second line; comment's
	// ends with the newline after its first.
	big := "func big() {\n\ts := \"" + strings.Repeat("x", most) + "\"\n}"
	comment := "// " + strings.Repeat("x", most-4)
	near := "var y = " + strings.Repeat("y", most-10)
	head := tree(t, map[string]string{
		"lib.go": "package lib\n\nfunc a() {\n\treturn\n}\n\nfunc b() {}\n", "long.go": "package lib\n" + long + "\n",
		"big.go": "package lib\n" + big + "\n", "comment.go": "package lib\n" + comment + "\nfunc f() {}\n",
		"comment_more.go": "package lib\n" + comment + " more\nfunc f() {}\n",
		"more.go":         "package lib\n\nfunc a() {\n\treturn\n} // a\n\nfunc b() {}\n", "near.go": "package lib\n" + near + " + y\n",
		"tight.go": "package lib\n\nfunc a() {\n\treturn\n}\n",
	})
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
		{"cut short", chunk("big.go", 2, 4, textcut.Prefix(big, most)), []contextpack.Chunk{chunk("big.go", 2, 3, textcut.Prefix(big, most))}},
		{"cut at a newline", chunk("comment.go", 2, 3, comment+"\n"), []contextpack.Chunk{chunk("comment.go", 2, 2, comment+"\n")}},
		{"cut at a newline after a line the head has more of", chunk("comment_more.go", 2, 3, comment+"\n"), nil},
		{"ending on a blank line", chunk("lib.go", 3, 6, "func a() {\n\treturn\n}\n"), []contextpack.Chunk{chunk("lib.go", 3, 6, "func a() {\n\treturn\n}\n")}},
		{"ending on a blank line after a line the head has more of", chunk("more.go", 3, 6, "func a() {\n\treturn\n}\n"), nil},
		{"ending on a blank line past the head's last", chunk("tight.go", 3, 6, "func a() {\n\treturn\n}\n"), nil},
		{"a whole line near the bound that the head has more of", chunk("near.go", 2, 2, near), nil},
		{
			"one line cut short", chunk("long.go", 2, 2, long[:indexer.DefaultOptions().MaxChunkBytes]),
			[]contextpack.Chunk{chunk("long.go", 2, 2, long[:indexer.DefaultOptions().MaxChunkBytes])},
		},
		{"a last line the head has more of", chunk("lib.go", 3, 4, "func a() {\n\tret"), nil},
		{"changed", chunk("lib.go", 3, 5, "func a() {\n\tpanic(1)\n}"), nil},
		{"past the file's end", chunk("lib.go", 7, 9, "func b() {}\n\nfunc c() {}"), nil},
		{"gone", chunk("old.go", 1, 1, "func b() {}"), nil},
		{"empty", chunk("lib.go", 1, 1, ""), nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := atHeadOf(t, head, []contextpack.Chunk{tt.in}); !slices.Equal(got, tt.want) {
				t.Fatalf("atHead = %+v, want %+v", got, tt.want)
			}
		})
	}
	t.Run("found at lines an earlier one mostly shows", func(t *testing.T) {
		a := "func a() {\n\treturn\n}"
		got := distinct(atHeadOf(t, head, []contextpack.Chunk{chunk("lib.go", 3, 5, a), chunk("lib.go", 1, 3, a), chunk("lib.go", 5, 7, "}\n\nfunc b() {}")}))
		if want := []contextpack.Chunk{chunk("lib.go", 3, 5, a), chunk("lib.go", 5, 7, "}\n\nfunc b() {}")}; !slices.Equal(got, want) {
			t.Fatalf("atHead = %+v, want %+v", got, want)
		}
	})
}

// TestAtHeadChecksDeclarations: a chunk of a declaration is shown only where
// the head still declares it. Every chunk that the indexer cuts from a file
// the head has unchanged is kept, while a window of a long declaration's body
// that the head has under another name or receiver is left out, though its
// text is unchanged.
func TestAtHeadChecksDeclarations(t *testing.T) {
	var body strings.Builder
	for i := range 200 {
		fmt.Fprintf(&body, "\t_ = %d\n", i)
	}
	long := func(head string) string {
		return "package lib\n\n// Long is long.\n" + head + " {\n" + body.String() + "}\n"
	}
	var methods strings.Builder
	for i := range 60 {
		fmt.Fprintf(&methods, "    void m%d() {\n        count++;\n    }\n", i)
	}
	base := map[string]string{
		"long.go":   long("func Long()"),
		"method.go": long("func (s *Server) handle()"),
		"small.go":  "package lib\n\ntype Server struct{}\n\nfunc (s *Server) small() {\n\treturn\n}\n",
		"lib.py":    "class C:\n    def m(self):\n        return 1\n\n\ndef f():\n    return 2\n",
		// The indexer joins a short gap of a block or an oversized class,
		// such as an opening line or a field, to the declaration next to it.
		"blocks.cpp": "#include <cstdio>\n\nnamespace {\nint helper(int x) { return x + 1; }\n}\n\nextern \"C\" {\nint exported(int x) { return helper(x); }\n}\n",
		"compat.py":  "if os.name == \"nt\":\n    def path_sep():\n        return \"\\\\\"\nelse:\n    def path_sep():\n        return \"/\"\n",
		"Big.java":   "class Big {\n    int count;\n" + methods.String() + "}\n",
	}
	built, _, _, err := indexer.Build(t.Context(), tree(t, base), nil, nil, indexer.Options{})
	if err != nil {
		t.Fatal(err)
	}
	index := make([]contextpack.Chunk, 0, len(built))
	for _, c := range built {
		index = append(index, contextpack.Chunk{
			Stage: contextpack.StageSimilar, Path: c.Path, Language: c.Language, Symbol: c.Symbol, Kind: c.Kind, Scope: c.Scope,
			StartLine: c.StartLine, EndLine: c.EndLine, Text: c.Text,
		})
	}
	// bodies is the windows of a long declaration's body that do not show
	// the line that names it.
	bodies := func(path string) []contextpack.Chunk {
		var out []contextpack.Chunk
		for _, c := range index {
			if c.Path == path && c.Symbol != "" && c.StartLine > 4 {
				out = append(out, c)
			}
		}
		return out
	}
	if len(bodies("long.go")) == 0 || len(bodies("method.go")) == 0 || bodies("method.go")[0].Scope != "Server" {
		t.Fatalf("index %+v has no window of a long body under its declaration", index)
	}
	for _, want := range []struct {
		path, symbol string
		start        int
	}{{"blocks.cpp", "helper", 3}, {"blocks.cpp", "exported", 7}, {"compat.py", "path_sep", 1}, {"Big.java", "m0", 1}} {
		if !slices.ContainsFunc(index, func(c contextpack.Chunk) bool {
			return c.Path == want.path && c.Symbol == want.symbol && c.StartLine == want.start
		}) {
			t.Fatalf("index %+v has no chunk of %s from line %d in %s", index, want.symbol, want.start, want.path)
		}
	}
	if got := atHeadOf(t, tree(t, base), index); !slices.Equal(got, index) {
		for _, c := range index {
			if !slices.Contains(got, c) {
				t.Errorf("atHead of an unchanged tree left out %s:%d-%d under %q", c.Path, c.StartLine, c.EndLine, c.Symbol)
			}
		}
		t.FailNow()
	}
	renamed := maps.Clone(base)
	renamed["long.go"] = long("func Longer()")
	renamed["method.go"] = long("func (c *Client) handle()")
	for _, path := range []string{"long.go", "method.go"} {
		if got := atHeadOf(t, tree(t, renamed), bodies(path)); len(got) != 0 {
			t.Errorf("atHead under another declaration = %+v, want none", got)
		}
	}
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

// TestAtHeadLooksPastAnotherDeclaration: a window of a long body that moved
// down, whose old lines the head now fills with the same text in another
// function, is found farther away in its own function, rather than left
// out for the nearer copy's name.
func TestAtHeadLooksPastAnotherDeclaration(t *testing.T) {
	var body strings.Builder
	for i := range 200 {
		fmt.Fprintf(&body, "\t_ = %d\n", i)
	}
	fn := func(name string) string { return "func " + name + "() {\n" + body.String() + "}\n" }
	built, _, _, err := indexer.Build(t.Context(), tree(t, map[string]string{"lib.go": "package lib\n\n" + fn("A")}), nil, nil, indexer.Options{})
	if err != nil {
		t.Fatal(err)
	}
	var window contextpack.Chunk
	for _, c := range built {
		if c.Symbol == "A" && c.StartLine > 10 {
			window = contextpack.Chunk{
				Stage: contextpack.StageSimilar, Path: c.Path, Language: c.Language, Symbol: c.Symbol, Kind: c.Kind,
				StartLine: c.StartLine, EndLine: c.EndLine, Text: c.Text,
			}
			break
		}
	}
	if window.Symbol == "" {
		t.Fatalf("index %+v has no window of A's body", built)
	}
	// B, as long as A, now sits above it: the window's old lines are B's.
	moved := len(strings.Split(fn("B"), "\n"))
	got := atHeadOf(t, tree(t, map[string]string{"lib.go": "package lib\n\n" + fn("B") + "\n" + fn("A")}), []contextpack.Chunk{window})
	want := window
	want.StartLine, want.EndLine = window.StartLine+moved, window.EndLine+moved
	if !slices.Equal(got, []contextpack.Chunk{want}) {
		t.Fatalf("atHead = %+v, want the window at lines %d-%d of A", got, want.StartLine, want.EndLine)
	}
}

// TestAtHeadFindsALineIndentedPastTheBound: a line that the index stores
// from its first word, its indentation being over the bound, is found at
// the head's lines, where the code above it moved too.
func TestAtHeadFindsALineIndentedPastTheBound(t *testing.T) {
	line := strings.Repeat(" ", indexer.DefaultOptions().MaxChunkBytes+10) + "callSomething()"
	src := "func F() {\n" + line + "\n}\n"
	built, _, _, err := indexer.Build(t.Context(), tree(t, map[string]string{"deep.go": "package lib\n\n" + src}), nil, nil, indexer.Options{})
	if err != nil {
		t.Fatal(err)
	}
	var stored contextpack.Chunk
	for _, c := range built {
		if c.StartLine == 4 {
			stored = contextpack.Chunk{
				Stage: contextpack.StageSimilar, Path: c.Path, Language: c.Language, Symbol: c.Symbol, Kind: c.Kind,
				StartLine: c.StartLine, EndLine: c.EndLine, Text: c.Text,
			}
		}
	}
	if stored.Text != "callSomething()" {
		t.Fatalf("index %+v stores no chunk of line 4 from its first word", built)
	}
	got := atHeadOf(t, tree(t, map[string]string{"deep.go": "package lib\n\n// moved\n" + src}), []contextpack.Chunk{stored})
	want := stored
	want.StartLine, want.EndLine = 5, 5
	if !slices.Equal(got, []contextpack.Chunk{want}) {
		t.Fatalf("atHead = %+v, want %+v", got, want)
	}
}

// TestSimilarContextLeavesOutFilesTooLargeToIndex: a change that adds files
// over the indexer's MaxFileBytes spends none of its queries on them, so a
// smaller file that adds fewer lines is still asked about.
func TestSimilarContextLeavesOutFilesTooLargeToIndex(t *testing.T) {
	var diff strings.Builder
	files := map[string]string{"small.go": "func small() {}\n"}
	// The large files share one blob, which the tree stores once.
	large := strings.Repeat("x", indexer.DefaultOptions().MaxFileBytes+1)
	changed := make([]string, 0, tuning.Get().ContextPack.SimilarQueries+1)
	for i := range tuning.Get().ContextPack.SimilarQueries {
		path := fmt.Sprintf("big%d.go", i)
		files[path], changed = large, append(changed, path)
		diff.WriteString(fileDiff("", path, added(fmt.Sprintf("func big%d() {", i), "\treturn", "}")))
	}
	diff.WriteString(fileDiff("", "small.go", added("func small() {}")))
	res := &gitfetch.Result{Diff: diff.String(), Changed: append(changed, "small.go")}
	gw := &fakeSimilarGateway{status: http.StatusNoContent, answer: contextpack.SimilarResponse{Indexed: true, Chunks: []contextpack.Chunk{}}}
	s := reviewSpec()
	s.GatewayURL = gw.serve(t).URL
	similarContext(t.Context(), s, runspec.Secrets{GatewayToken: "run", ReportToken: "report"}, tree(t, files), res, nil,
		slog.New(slog.DiscardHandler))
	gw.mu.Lock()
	defer gw.mu.Unlock()
	if len(gw.requests) != 2 || !slices.Equal(gw.requests[1].Queries, []string{"func small() {}\n"}) {
		t.Fatalf("similar requests %+v, want a probe and a search of small.go alone", gw.requests)
	}
}

// TestSimilarLeavesOutPathsTheRunWouldNotIndex: an index built before the
// run's rules can hold a file that they now leave out, ignored by the run or
// Unindexed, which the head still has unchanged. Neither automatic context
// nor search_code shows it.
func TestSimilarLeavesOutPathsTheRunWouldNotIndex(t *testing.T) {
	const kept = "func kept() {}"
	files := map[string]string{
		"lib/kept.go": kept + "\n", "vendor/dep.go": "func dep() {}\n", "secret.sops.yaml": "password: ENC[AES256_GCM,data:x]\n",
		"a.go": "func a() {}\n",
	}
	found := func(path, text string) contextpack.Chunk {
		return contextpack.Chunk{Stage: contextpack.StageSimilar, Path: path, StartLine: 1, EndLine: 1, Text: text, Ref: "similarity 0.9"}
	}
	answer := contextpack.SimilarResponse{Indexed: true, Chunks: []contextpack.Chunk{
		found("vendor/dep.go", "func dep() {}"), found("secret.sops.yaml", "password: ENC[AES256_GCM,data:x]"), found("lib/kept.go", kept),
	}}
	gw := &fakeSimilarGateway{status: http.StatusNoContent, answer: answer}
	s := reviewSpec()
	s.GatewayURL, s.Ignore = gw.serve(t).URL, []string{"vendor/**"}
	res := &gitfetch.Result{Diff: fileDiff("", "a.go", added("func a() {}")), Changed: []string{"a.go"}}
	chunks, search := similarContext(t.Context(), s, runspec.Secrets{GatewayToken: "run", ReportToken: "report"}, tree(t, files), res, nil,
		slog.New(slog.DiscardHandler))
	if len(chunks) != 1 || chunks[0].Path != "lib/kept.go" || search == nil {
		t.Fatalf("similar code %+v, search_code offered %t; want lib/kept.go alone and search_code", chunks, search != nil)
	}
	out, err := search.Run(t.Context(), json.RawMessage(`{"query":"kept"}`))
	if want := "lib/kept.go:1-1 similarity 0.9\n" + kept; err != nil || out != want {
		t.Fatalf("search_code = %q, %v; want %q", out, err, want)
	}
}

// atHeadOf is atHead without ignore globs, with a context that does not end.
func atHeadOf(t *testing.T, head *object.Tree, chunks []contextpack.Chunk) []contextpack.Chunk {
	t.Helper()
	out, err := atHead(t.Context(), head, nil, nil, chunks)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// TestSearchToolAsksForSpares: search_code asks the gateway for more chunks
// than it shows, so a found chunk that the head no longer has, or that the
// run now ignores, leaves its place to the next one rather than leaving it
// empty. It shows no more than tuning.Get().Runner.SearchShown however many are found.
func TestSearchToolAsksForSpares(t *testing.T) {
	files := map[string]string{"vendor/dep.go": "func dep() {}\n"}
	found := func(path, text string) contextpack.Chunk {
		return contextpack.Chunk{Stage: contextpack.StageSimilar, Path: path, StartLine: 1, EndLine: 1, Text: text, Ref: "similarity 0.9"}
	}
	stale := []contextpack.Chunk{
		found("gone.go", "func gone() {}"), found("vendor/dep.go", "func dep() {}"), found("old.go", "func old() {}"),
		found("vendor/other.go", "func other() {}"),
	}
	valid := make([]contextpack.Chunk, 0, tuning.Get().Runner.SearchShown+2)
	for i := range tuning.Get().Runner.SearchShown + 2 {
		path, text := fmt.Sprintf("lib/f%d.go", i), fmt.Sprintf("func f%d() {}", i)
		files[path] = text + "\n"
		valid = append(valid, found(path, text))
	}
	files["old.go"] = "func old() { changed() }\n"
	head := tree(t, files)
	for _, tt := range []struct {
		name  string
		found []contextpack.Chunk
		want  []string
	}{
		{"the fifth past four left out", append(slices.Clone(stale), valid[0]), []string{"lib/f0.go"}},
		{"more than it shows", valid, []string{"lib/f0.go", "lib/f1.go", "lib/f2.go", "lib/f3.go"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			// As the gateway does, one query's answer holds similarPerQuery
			// chunks unless the request asks for more.
			gw := &fakeSimilarGateway{answer: contextpack.SimilarResponse{Indexed: true, Chunks: tt.found}, perQuery: 4}
			tool := &searchTool{gatewayURL: gw.serve(t).URL, token: "run", head: head, ignore: []string{"vendor/**"}, maxBytes: 1 << 10}
			out, err := tool.Run(t.Context(), json.RawMessage(`{"query":"f"}`))
			if err != nil {
				t.Fatal(err)
			}
			var shown []string
			for block := range strings.SplitSeq(out, "\n\n") {
				if path, _, ok := strings.Cut(block, ":"); ok {
					shown = append(shown, path)
				}
			}
			if !slices.Equal(shown, tt.want) {
				t.Fatalf("search_code = %q, want %q", out, tt.want)
			}
		})
	}
}
