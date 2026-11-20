package runner

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/go-git/go-git/v6/plumbing/object"

	"github.com/home-operations/kritika/internal/agent"
	"github.com/home-operations/kritika/internal/chunk"
	"github.com/home-operations/kritika/internal/contextpack"
	"github.com/home-operations/kritika/internal/indexer"
	"github.com/home-operations/kritika/internal/model"
	"github.com/home-operations/kritika/internal/textcut"
	"github.com/home-operations/kritika/internal/udiff"
)

// similarTimeout bounds one similar-code request to the gateway, which
// can wait for the account's embedding slot.
const similarTimeout = time.Minute

// maxSimilarBody bounds the gateway's answer: ten chunks.
const maxSimilarBody = 1 << 20

// searchCalls is how many times one run can call search_code.
const searchCalls = 10

// similarCode asks the gateway for the chunks of the repository's index
// nearest each query, outside the paths that the run reported.
func similarCode(ctx context.Context, gatewayURL, token string, req contextpack.SimilarRequest) (contextpack.SimilarResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, similarTimeout)
	defer cancel()
	body, err := json.Marshal(req)
	if err != nil {
		return contextpack.SimilarResponse{}, fmt.Errorf("runner: similar code: %w", err)
	}
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSuffix(gatewayURL, "/")+"/v1/similar", bytes.NewReader(body))
	if err != nil {
		return contextpack.SimilarResponse{}, fmt.Errorf("runner: similar code: %w", err)
	}
	hreq.Header.Set("Authorization", "Bearer "+token)
	hreq.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(hreq)
	if err != nil {
		return contextpack.SimilarResponse{}, fmt.Errorf("runner: similar code: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	answer, err := io.ReadAll(io.LimitReader(resp.Body, maxSimilarBody))
	if err != nil {
		return contextpack.SimilarResponse{}, fmt.Errorf("runner: similar code: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return contextpack.SimilarResponse{}, fmt.Errorf("runner: similar code: %s: %s", resp.Status, bytes.TrimSpace(answer))
	}
	var out contextpack.SimilarResponse
	if err := json.Unmarshal(answer, &out); err != nil {
		return contextpack.SimilarResponse{}, fmt.Errorf("runner: similar code: %w", err)
	}
	return out, nil
}

// queryContext is how many unchanged lines a query keeps on each side of a
// hunk's changed lines.
const queryContext = 3

// hunkQueries is what stage 4 embeds: the changed regions of the diff's
// hunks as the head has them, each its changed lines and a little context.
// It leaves out the files that the index holds no chunk of, ignored or of no
// language, and the hunks that add nothing, whose text is only context. The
// files take turns, the most changed first, so a query of each of the most
// changed files comes before a second of any. A region over the gateway's
// bound is asked about by the declarations its changes are in, as the index
// cuts a file, or else in windows of its lines, rather than by its start,
// which in a new file is its license and imports.
//
// A query is the code alone. The index leads each chunk with its path, so a
// query led by its own path scores the files beside it higher for their
// path alone, and stages 2 and 3 already show those that the change uses.
func hunkQueries(diff string, ignore []string) []string {
	var files []*queryFile
	for _, f := range udiff.Parse(diff) {
		if f.NewPath == "" || chunk.Matches(ignore, f.NewPath) {
			continue
		}
		if lang, _ := chunk.Language(f.NewPath); lang == "" {
			continue
		}
		qf := &queryFile{path: f.NewPath}
		for _, h := range f.Hunks {
			if region, n := changedRegion(h); n > 0 {
				qf.regions = append(qf.regions, region)
				qf.added += n
			}
		}
		if len(qf.regions) > 0 {
			files = append(files, qf)
		}
	}
	slices.SortStableFunc(files, func(a, b *queryFile) int { return cmp.Compare(b.added, a.added) })
	var parser chunk.Parser
	var out []string
	for len(files) > 0 && len(out) < contextpack.SimilarQueries {
		left := files[:0]
		for _, f := range files {
			if len(out) == contextpack.SimilarQueries {
				break
			}
			if q, ok := f.next(&parser); ok {
				out = append(out, q)
				left = append(left, f)
			}
		}
		files = left
	}
	return out
}

// queryFile is one file's regions still to ask about, and the queries left
// of the region in hand.
type queryFile struct {
	path    string
	added   int
	regions [][]udiff.Line
	pending []string
}

// next is the file's next query, false once it has none.
func (f *queryFile) next(parser *chunk.Parser) (string, bool) {
	for len(f.pending) == 0 {
		if len(f.regions) == 0 {
			return "", false
		}
		f.pending = regionQueries(parser, f.path, f.regions[0])
		f.regions = f.regions[1:]
	}
	q := f.pending[0]
	f.pending = f.pending[1:]
	return q, true
}

// changedRegion is the head-side lines of h from queryContext lines before
// its first added line to queryContext lines after its last, and how many
// lines it adds that are not blank. A hunk that only removes, or adds only
// blank lines, adds none.
func changedRegion(h udiff.Hunk) ([]udiff.Line, int) {
	var head []udiff.Line
	first, last, n := -1, -1, 0
	for _, l := range h.Lines {
		if l.Kind != '+' && l.Kind != ' ' {
			continue
		}
		if l.Kind == '+' && strings.TrimSpace(l.Text) != "" {
			if first < 0 {
				first = len(head)
			}
			last = len(head)
			n++
		}
		head = append(head, l)
	}
	if n == 0 {
		return nil, 0
	}
	return head[max(0, first-queryContext):min(len(head), last+queryContext+1)], n
}

// regionQueries is the queries of one changed region: the region itself when
// it fits the gateway's bound. Otherwise it is the outermost named
// declarations that hold an added line, each cut to the bound, as the index
// cuts a file. A declaration over the bound gives way to the ones nested in
// it that hold an added line. Without such a declaration, it is the windows
// of the region's lines that hold an added line.
func regionQueries(parser *chunk.Parser, path string, region []udiff.Line) []string {
	var b strings.Builder
	var added []int
	for i, l := range region {
		b.WriteString(l.Text)
		b.WriteByte('\n')
		if l.Kind == '+' && strings.TrimSpace(l.Text) != "" {
			added = append(added, i+1)
		}
	}
	text := b.String()
	if len(text) <= contextpack.SimilarQueryChars {
		return []string{text}
	}
	f := parser.Parse(path, []byte(text))
	defer f.Close()
	holds := func(d chunk.Decl) bool {
		return d.Symbol != "" && d.Kind != "import" && slices.ContainsFunc(added, d.Contains)
	}
	var out []string
	lastEnd := 0
	for i, d := range f.Decls {
		if d.StartLine <= lastEnd || !holds(d) {
			continue
		}
		body := f.Text(d.StartLine, d.EndLine) + "\n"
		if len(body) > contextpack.SimilarQueryChars && slices.ContainsFunc(f.Decls[i+1:], func(n chunk.Decl) bool {
			return n.StartLine > d.StartLine && n.EndLine <= d.EndLine && holds(n)
		}) {
			continue
		}
		out = append(out, textcut.Prefix(body, contextpack.SimilarQueryChars))
		lastEnd = d.EndLine
	}
	if len(out) > 0 {
		return out
	}
	return regionWindows(region)
}

// regionWindows cuts region into runs of whole lines within the gateway's
// bound, and keeps the runs that hold an added line.
func regionWindows(region []udiff.Line) []string {
	var out []string
	var b strings.Builder
	adds := false
	flush := func() {
		if adds {
			out = append(out, textcut.Prefix(b.String(), contextpack.SimilarQueryChars))
		}
		b.Reset()
		adds = false
	}
	for _, l := range region {
		if b.Len() > 0 && b.Len()+len(l.Text)+1 > contextpack.SimilarQueryChars {
			flush()
		}
		b.WriteString(l.Text)
		b.WriteByte('\n')
		adds = adds || l.Kind == '+' && strings.TrimSpace(l.Text) != ""
	}
	flush()
	return out
}

var searchSchema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"query": {"type": "string", "description": "What to look for: a description of the code, or a snippet like it."}
	},
	"required": ["query"],
	"additionalProperties": false
}`)

// searchTool is search_code: the agent's own similar-code search, over
// the same gateway route as stage 4, bounded to searchCalls per run.
type searchTool struct {
	gatewayURL, token string
	// head is the tree that the answer is shown as (atHead). headLock, when
	// set, is what a read of it takes: parts share it.
	head     *object.Tree
	headLock chan struct{}
	maxBytes int
	calls    int
}

func (s *searchTool) Def() model.ToolDef {
	return model.ToolDef{
		Name:        "search_code",
		Description: "Find the chunks of the repository most similar to a description or a snippet, outside the changed files.",
		InputSchema: searchSchema,
	}
}

func (s *searchTool) Run(ctx context.Context, input json.RawMessage) (string, error) {
	var req struct {
		Query string `json:"query"`
	}
	if len(input) > 0 {
		if err := json.Unmarshal(input, &req); err != nil {
			return "", fmt.Errorf("agent: search_code: %w", err)
		}
	}
	if strings.TrimSpace(req.Query) == "" {
		return "", errors.New("agent: search_code: query is empty")
	}
	if s.calls >= searchCalls {
		return "", fmt.Errorf("agent: search_code: the limit of %d searches per review is reached", searchCalls)
	}
	s.calls++
	out, err := similarCode(ctx, s.gatewayURL, s.token, contextpack.SimilarRequest{
		Queries: []string{textcut.Prefix(req.Query, contextpack.SimilarQueryChars)}, Kind: contextpack.SimilarKindSearch,
	})
	if err != nil {
		return "", fmt.Errorf("agent: search_code: %w", err)
	}
	release, err := acquire(ctx, s.headLock)
	if err != nil {
		return "", err
	}
	chunks := atHead(s.head, out.Chunks)
	release()
	if len(chunks) == 0 {
		return "No similar code found.", nil
	}
	var b strings.Builder
	for i, c := range chunks {
		if i > 0 {
			b.WriteString("\n\n")
		}
		fmt.Fprintf(&b, "%s:%d-%d", c.Path, c.StartLine, c.EndLine)
		if c.Symbol != "" {
			fmt.Fprintf(&b, " (%s %s)", c.Kind, c.Symbol)
		}
		fmt.Fprintf(&b, " %s\n%s", c.Ref, c.Text)
	}
	return textcut.Truncate(b.String(), s.maxBytes), nil
}

var _ agent.Tool = (*searchTool)(nil)

// withoutOverlap is the similar chunks that share no line with a chunk of
// another stage, which already shows that code.
func withoutOverlap(similar, others []contextpack.Chunk) []contextpack.Chunk {
	return slices.DeleteFunc(slices.Clone(similar), func(c contextpack.Chunk) bool {
		return slices.ContainsFunc(others, func(o contextpack.Chunk) bool {
			return o.Stage != contextpack.StageSimilar && o.Path == c.Path && o.StartLine <= c.EndLine && c.StartLine <= o.EndLine
		})
	})
}

// atHead is the similar chunks as the pull request's head has them. The
// index is of the default branch at some commit, which can be older or newer
// than the head, and a chunk's lines there can hold other code at the head.
// So a chunk is found by its text, at its own lines or, when code above it
// moved, at the nearest place the head holds it, and shown at those lines. A
// chunk whose text the head no longer holds is left out rather than shown
// by its old lines: those now hold code that its similarity was not measured
// for. The index cuts a long chunk's text short, so its last line need only
// begin the head's, and the chunk claims the lines its text covers.
func atHead(head *object.Tree, chunks []contextpack.Chunk) []contextpack.Chunk {
	files := map[string][]string{}
	var out []contextpack.Chunk
	for _, c := range chunks {
		lines, ok := files[c.Path]
		if !ok {
			lines = headLines(head, c.Path)
			files[c.Path] = lines
		}
		if start, ok := findText(lines, c.Text, c.StartLine-1); ok {
			c.EndLine = start + strings.Count(strings.TrimSuffix(c.Text, "\n"), "\n") + 1
			c.StartLine = start + 1
			out = append(out, c)
		}
	}
	return out
}

// headLines is the lines of path at head, none when the head has no such
// file or it is larger than any file the index holds.
func headLines(head *object.Tree, path string) []string {
	f, err := head.File(path)
	if err != nil || f.Size > int64(indexer.DefaultOptions.MaxFileBytes) {
		return nil
	}
	src, err := f.Contents()
	if err != nil {
		return nil
	}
	return strings.Split(src, "\n")
}

// findText is the 0-based line of lines where text starts, the one nearest
// near when it is in more than one place, false when it is in none.
func findText(lines []string, text string, near int) (int, bool) {
	text = strings.TrimSuffix(text, "\n")
	if text == "" {
		return 0, false
	}
	want := strings.Split(text, "\n")
	at := func(i int) bool {
		if i < 0 || i+len(want) > len(lines) {
			return false
		}
		last := len(want) - 1
		return slices.Equal(lines[i:i+last], want[:last]) && strings.HasPrefix(lines[i+last], want[last])
	}
	for d := 0; near-d >= 0 || near+d < len(lines); d++ {
		if at(near - d) {
			return near - d, true
		}
		if at(near + d) {
			return near + d, true
		}
	}
	return 0, false
}

// similarShown is the similar chunks found that a prompt shows: as head has
// them, and none whose lines others, the other stages' chunks, show.
func similarShown(head *object.Tree, found, others []contextpack.Chunk, logger *slog.Logger) []contextpack.Chunk {
	current := atHead(head, found)
	shown := withoutOverlap(current, others)
	logger.Info("similar chunks shown", "found", len(found), "at_head", len(current), "shown", len(shown))
	return shown
}
