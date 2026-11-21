package runner

import (
	"bytes"
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
	"github.com/home-operations/kritika/internal/runspec"
	"github.com/home-operations/kritika/internal/textcut"
)

// similarTimeout bounds one similar-code request to the gateway, which
// can wait for the account's embedding slot.
const similarTimeout = time.Minute

// maxSimilarBody bounds the gateway's answer: contextpack.SimilarLimit chunks.
const maxSimilarBody = 1 << 20

// searchCalls is how many times one run can call search_code.
const searchCalls = 10

// similarCode asks the gateway for the chunks of the repository's index
// nearest each query, outside the paths that the run reported. It asks on
// runspec.SimilarPath, which only a gateway that leaves them out serves.
func similarCode(ctx context.Context, gatewayURL, token string, req contextpack.SimilarRequest) (contextpack.SimilarResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, similarTimeout)
	defer cancel()
	body, err := json.Marshal(req)
	if err != nil {
		return contextpack.SimilarResponse{}, fmt.Errorf("runner: similar code: %w", err)
	}
	url := strings.TrimSuffix(gatewayURL, "/") + runspec.SimilarPath
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
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
	chunks := distinct(atHead(s.head, out.Chunks))
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

// withoutOverlap is the similar chunks of which the other stages' chunks
// among others show no more than half the lines. A similar chunk is a whole
// declaration or window: one that shares a line or two with a definition
// that another stage shows still shows code that the prompt otherwise lacks,
// while one mostly shown already adds little but its lines again. An other
// chunk shows only the lines of its text. One that the pack, built with
// contextpack.DefaultOptions, cut at MaxChunkBytes shows fewer than its
// range, and its text ends in a line of the pack's own that says so; only a
// cut text is longer than MaxChunkBytes.
func withoutOverlap(similar, others []contextpack.Chunk) []contextpack.Chunk {
	var shown []contextpack.Chunk
	for _, o := range others {
		if o.Stage == contextpack.StageSimilar {
			continue
		}
		text := o.Text
		if len(text) > contextpack.DefaultOptions.MaxChunkBytes {
			// A cut that ends at a newline shows none of the line after it.
			text = strings.TrimSuffix(strings.TrimSuffix(text, "\n… (truncated)"), "\n")
		}
		n := strings.Count(text, "\n") + 1
		o.EndLine = min(o.EndLine, o.StartLine+n-1)
		shown = append(shown, o)
	}
	return slices.DeleteFunc(slices.Clone(similar), func(c contextpack.Chunk) bool { return mostlyShown(c, shown) })
}

// mostlyShown reports whether the chunks of shown cover more than half of
// c's lines.
func mostlyShown(c contextpack.Chunk, shown []contextpack.Chunk) bool {
	covered := 0
	for l := c.StartLine; l <= c.EndLine; l++ {
		if slices.ContainsFunc(shown, func(o contextpack.Chunk) bool { return o.Path == c.Path && o.StartLine <= l && l <= o.EndLine }) {
			covered++
		}
	}
	return 2*covered > c.EndLine-c.StartLine+1
}

// atHead is the similar chunks as the pull request's head has them. The
// index is of the default branch at some commit, which can be older or newer
// than the head, and a chunk's lines there can hold other code at the head.
// So a chunk is found by its text, at its own lines or, when code above it
// moved, at the nearest place the head holds it, and shown at those lines. A
// chunk whose text the head no longer holds is left out rather than shown
// by its old lines: those now hold code that its similarity was not measured
// for. A chunk is found where the head's lines, cut as the indexer cuts a
// chunk, give its text, and claims the lines its text shows. A chunk of a
// declaration is also left out unless the head, parsed as the indexer parses
// it, declares the chunk's symbol, kind and scope around those lines, or cut
// as the indexer cuts it, holds them in a piece under that symbol, kind and
// scope, as the indexer joins a short gap of an oversized declaration to the
// nested one next to it: a window of a long declaration's body need not hold
// the line that names it, which a rename changes, and the symbol is what the
// chunk is shown under and what a match by name matched. The nearest place
// that gives the text and is declared so is the one shown, as a body that
// moved down can leave the same lines in another function at its old place.
func atHead(head *object.Tree, chunks []contextpack.Chunk) []contextpack.Chunk {
	files := map[string]*headFile{}
	parser := &chunk.Parser{Limits: chunk.Limits{MaxBytes: indexer.DefaultOptions.MaxFileBytes}}
	var out []contextpack.Chunk
	for _, c := range chunks {
		f, ok := files[c.Path]
		if !ok {
			f = readHeadFile(head, c.Path)
			files[c.Path] = f
		}
		start, end, ok := findText(f.lines, c.Text, c.StartLine-1, c.EndLine-c.StartLine+1, func(start, end int) bool {
			at := c
			at.StartLine, at.EndLine = start+1, end+1
			return at.Symbol == "" || f.declares(parser, at)
		})
		if !ok {
			continue
		}
		c.StartLine, c.EndLine = start+1, end+1
		out = append(out, c)
	}
	return out
}

// headFile is a file as the head has it. Its declarations, and the pieces
// that the indexer cuts it into, are worked out once, when a chunk of a
// declaration is found in it.
type headFile struct {
	path, src     string
	lines         []string
	decls, pieces []chunk.Decl
	parsed        bool
}

// readHeadFile is path at head, empty when the head has no such file, it is
// empty, or it is larger than any file the index holds. A final newline ends
// the last line rather than starting an empty one, as the indexer counts
// lines.
func readHeadFile(head *object.Tree, path string) *headFile {
	f := &headFile{path: path}
	file, err := head.File(path)
	if err != nil || file.Size > int64(indexer.DefaultOptions.MaxFileBytes) {
		return f
	}
	src, err := file.Contents()
	if err != nil || src == "" {
		return f
	}
	f.src, f.lines = src, strings.Split(strings.TrimSuffix(src, "\n"), "\n")
	return f
}

// declares reports whether the head declares c's symbol, with c's kind and
// scope, around c's lines, or the indexer stores c's lines of the head in a
// piece under them. The declaration alone does not hold a piece that the
// indexer widened over a short gap beside it, and the pieces alone do not
// hold a declaration that the indexer cut otherwise when it was indexed, as
// it does once the declaration around it grows past MaxDeclLines.
func (f *headFile) declares(parser *chunk.Parser, c contextpack.Chunk) bool {
	if !f.parsed {
		f.parsed = true
		pf := parser.Parse(f.path, []byte(f.src))
		f.decls, f.pieces = pf.Decls, indexer.Pieces(pf, indexer.DefaultOptions)
		pf.Close()
	}
	holds := func(d chunk.Decl) bool {
		return d.Symbol == c.Symbol && d.Kind == c.Kind && d.Scope == c.Scope && d.StartLine <= c.StartLine && c.EndLine <= d.EndLine
	}
	return slices.ContainsFunc(f.decls, holds) || slices.ContainsFunc(f.pieces, holds)
}

// distinct is chunks without those whose lines an earlier one mostly shows:
// two chunks of the index can be found at the same head lines. It runs on
// the chunks that are shown, so that one left out for what the other stages
// show does not take a chunk with it that they do not.
func distinct(chunks []contextpack.Chunk) []contextpack.Chunk {
	var out []contextpack.Chunk
	for _, c := range chunks {
		if !mostlyShown(c, out) {
			out = append(out, c)
		}
	}
	return out
}

// findText is the 0-based first and last of lines that text shows, where
// text is what the index holds of n lines: them joined and cut at
// MaxChunkBytes, as indexer.ChunkText cuts a chunk. When text is in more than one
// place it is the one nearest near, false when it is in none. The lines past
// a cut need not be the index's, and the line after a cut that ends a line
// is not shown. A whole text of exactly MaxChunkBytes is also what a cut of a
// longer one gives, so the head can have more of its last line. A place
// counts only when accept takes its first and last line.
func findText(lines []string, text string, near, n int, accept func(first, last int) bool) (int, int, bool) {
	if text == "" || n < 1 {
		return 0, 0, false
	}
	most := indexer.DefaultOptions.MaxChunkBytes
	at := func(i int) (int, bool) {
		if i < 0 {
			return 0, false
		}
		end := min(i+n, len(lines))
		rest := text
		for j := i; j < end; j++ {
			line, tail, more := strings.Cut(rest, "\n")
			if more {
				if lines[j] != line {
					return 0, false
				}
				rest = tail
				continue
			}
			if !strings.HasPrefix(lines[j], line) {
				// A line indented past the bound is stored from its first
				// word, and so is a chunk of its own.
				if j == i && len(lines[j]) > most && indexer.ChunkText(lines[j], most) == text {
					return j, true
				}
				return 0, false
			}
			var b strings.Builder
			b.WriteString(text[:len(text)-len(line)])
			for k := j; k < end && b.Len() <= most; k++ {
				if k > j {
					b.WriteByte('\n')
				}
				b.WriteString(lines[k])
			}
			if textcut.Prefix(b.String(), most) != text {
				return 0, false
			}
			if line == "" && b.Len() > len(text) {
				return j - 1, true
			}
			return j, true
		}
		return 0, false
	}
	for d := 0; near-d >= 0 || near+d < len(lines); d++ {
		if last, ok := at(near - d); ok && accept(near-d, last) {
			return near - d, last, true
		}
		if last, ok := at(near + d); ok && accept(near+d, last) {
			return near + d, last, true
		}
	}
	return 0, 0, false
}

// maxSimilarShown is the most similar chunks a prompt shows.
const maxSimilarShown = 10

// similarShown is the similar chunks found that a prompt shows: as head has
// them, none that others, the other stages' chunks, mostly show or that an
// earlier one shown mostly shows, and the first maxSimilarShown of the rest.
func similarShown(head *object.Tree, found, others []contextpack.Chunk, logger *slog.Logger) []contextpack.Chunk {
	current := atHead(head, found)
	shown := distinct(withoutOverlap(current, others))
	logger.Info("similar chunks shown", "found", len(found), "at_head", len(current), "apart", len(shown),
		"shown", min(len(shown), maxSimilarShown))
	return shown[:min(len(shown), maxSimilarShown)]
}
