package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/home-operations/kritika/internal/agent"
	"github.com/home-operations/kritika/internal/gitfetch"
	"github.com/home-operations/kritika/internal/model"
	"github.com/home-operations/kritika/internal/upstream"
)

// What fetch_repo may take: a run makes at most fetchMax fetches and writes
// at most fetchWriteBytes of their files, beside its checkout; one fetch
// takes at most fetchWireBytes from the server and fetchTimeout of wall
// time, and keeps at most fetchDiffBytes of a diff. A run also lists tags
// at most listMax times, counting the listing a fetch that names a missing
// ref makes to name the closest; an answer shows at most tagsShown names,
// and such an error at most closestMax.
const (
	fetchMax        = 8
	fetchWriteBytes = 256 << 20
	fetchWireBytes  = 128 << 20
	fetchTimeout    = 2 * time.Minute
	fetchDiffBytes  = 4 << 20
	listMax         = 8
	tagsShown       = 200
	closestMax      = 10
)

// upstreamDir is the scratch directory fetch_repo writes to, beside the
// checkout, and upstreamRel the same directory as the run tool's commands
// see it from the checkout.
const (
	upstreamDir = "upstream"
	upstreamRel = "../" + upstreamDir
)

var fetchRepoSchema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"url": {"type": "string", "description": "The repository's https:// clone URL, such as https://github.com/cert-manager/cert-manager."},
		"ref": {
			"type": "string",
			"description": "The tag, branch or full 40-character commit SHA to fetch; HEAD for the default branch. Leave out with tags."
		},
		"from": {"type": "string", "description": "An earlier tag, branch or full commit SHA: the result then includes the diff from it to ref."},
		"paths": {
			"type": "array",
			"items": {"type": "string"},
			"description": "Files and directories to fetch, relative to the root; only their files are downloaded. Omit for all."
		},
		"tags": {
			"type": "string",
			"description": "Instead of ref: list the repository's tags whose names contain this, such as 0.37 or gpu-; empty for all."
		}
	},
	"required": ["url"],
	"additionalProperties": false
}`)

// fetchRepoTool is fetch_repo: it fetches a repository other than the one
// under review at a ref, and writes its files under dir, beside the
// checkout, for read_file to read and the run tool's commands to search,
// with the diff from an earlier ref when asked.
type fetchRepoTool struct {
	dir string
	// transport carries the fetches; nil is upstream's default, through the
	// pod's proxy.
	transport http.RoundTripper

	fetches  int
	listings int
	written  int64
	sources  []string
}

func (t *fetchRepoTool) Def() model.ToolDef {
	return model.ToolDef{
		Name: "fetch_repo",
		Description: "Fetch a repository other than the one under review, such as the upstream of a dependency, at a tag, " +
			"branch or commit, and write its files to a directory read_file reads and the run tool's commands search. With from, it also " +
			"returns the diff between the two. With tags instead of ref, it lists the repository's tags, to find the tag " +
			fmt.Sprintf("of a version rather than guess its name. A review may fetch %d times and list tags %d times.", fetchMax, listMax),
		InputSchema: fetchRepoSchema,
	}
}

func (t *fetchRepoTool) Run(ctx context.Context, input json.RawMessage) (string, error) {
	var in struct {
		URL   string   `json:"url"`
		Ref   string   `json:"ref"`
		From  string   `json:"from"`
		Paths []string `json:"paths"`
		// Tags is set, to "" for every tag, when the call lists tags. A
		// model may send every property, the ones it does not mean empty,
		// so beside a ref an empty one asks for nothing.
		Tags *string `json:"tags"`
	}
	if len(input) > 0 {
		if err := json.Unmarshal(input, &in); err != nil {
			return "", fmt.Errorf("agent: fetch_repo: %w", err)
		}
	}
	switch {
	case in.Tags != nil && in.Ref == "":
		if in.From != "" || len(in.Paths) > 0 {
			return "", errors.New("agent: fetch_repo: give ref to fetch, or tags without from or paths to list tags")
		}
		return t.listTags(ctx, in.URL, *in.Tags)
	case in.Tags != nil && *in.Tags != "":
		return "", errors.New("agent: fetch_repo: give ref to fetch or tags to list tags, not both")
	}
	req := upstream.Request{URL: in.URL, Ref: in.Ref, From: in.From, Paths: in.Paths}
	if t.fetches >= fetchMax {
		return "", fmt.Errorf("agent: fetch_repo: this review has made its %d fetches", fetchMax)
	}
	left := fetchWriteBytes - t.written
	if left <= 0 {
		return "", fmt.Errorf("agent: fetch_repo: this review has written its %d MiB of fetched files", fetchWriteBytes>>20)
	}
	t.fetches++
	name := dirName(t.fetches, req.URL, req.Ref)
	dest := filepath.Join(t.dir, name)
	f := &upstream.Fetcher{Transport: t.transport, Limits: upstream.Limits{
		WireBytes: fetchWireBytes, WriteBytes: left, BlobBytes: agent.MaxBlobBytes, DiffBytes: fetchDiffBytes,
	}}
	fctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()
	res, err := f.Fetch(fctx, req, dest)
	if err != nil {
		// What a failed fetch wrote is not counted, so it is not kept.
		_ = os.RemoveAll(dest)
		if errors.Is(err, upstream.ErrTooLarge) {
			if len(req.Paths) == 0 {
				return "", fmt.Errorf("agent: fetch_repo: %w; give paths to fetch only the files under them", err)
			}
			return "", fmt.Errorf("agent: fetch_repo: %w; give fewer paths", err)
		}
		if errors.Is(err, upstream.ErrNoRef) {
			hint := ""
			if t.listings < listMax {
				t.listings++
				hint = closest(fctx, f, req)
			}
			return "", fmt.Errorf("agent: fetch_repo: %w.%s", err, hint)
		}
		return "", fmt.Errorf("agent: fetch_repo: %w", err)
	}
	t.written += res.Bytes
	page := "tree/" + req.Ref
	if req.From != "" {
		page = "compare/" + req.From + "..." + req.Ref
	}
	t.record(req.URL, page)

	rel := upstreamRel + "/" + name
	var b strings.Builder
	fmt.Fprintf(&b, "Fetched %s at %s, commit %s. Its files are under %s, from the checkout the run tool's commands run in: "+
		"%d files, %d KiB.", req.URL, req.Ref, res.Commit[:12], rel, res.Files, (res.Bytes+1023)>>10)
	if res.Skipped > 0 {
		fmt.Fprintf(&b, " Left out: %d symlinks, submodules or files over %d MiB.", res.Skipped, agent.MaxBlobBytes>>20)
	}
	if res.Truncated {
		b.WriteString(" The writing stopped at this review's limit, so the paths that sort last are missing.")
	}
	for _, m := range res.Missing {
		fmt.Fprintf(&b, " Nothing is under %s", m.Path)
		if len(m.Near) > 0 {
			fmt.Fprintf(&b, "; these paths end in it: %s", strings.Join(m.Near, ", "))
		}
		b.WriteString(".")
	}
	if req.From == "" {
		return b.String(), nil
	}
	fmt.Fprintf(&b, "\n\nThe diff from %s, commit %s, changes %d files", req.From, res.FromCommit[:12], len(res.Changed))
	if res.DiffCut > 0 {
		fmt.Fprintf(&b, ", %d of them left out of it as too large", res.DiffCut)
	}
	if res.Diff == "" {
		b.WriteString(".")
		return b.String(), nil
	}
	if t.written+int64(len(res.Diff)) > fetchWriteBytes {
		fmt.Fprintf(&b, ". It is not kept in a file, past what this review may write, and follows, cut to fit when long:\n\n%s", res.Diff)
		return b.String(), nil
	}
	diffFile := name + ".diff"
	if err := os.WriteFile(filepath.Join(t.dir, diffFile), []byte(res.Diff), 0o644); err != nil {
		return "", fmt.Errorf("agent: fetch_repo: %w", err)
	}
	t.written += int64(len(res.Diff))
	fmt.Fprintf(&b, ". It is in %s/%s, and follows, cut to fit when long:\n\n%s", upstreamRel, diffFile, res.Diff)
	return b.String(), nil
}

// listTags answers a call that lists the tags of the repository at rawURL
// whose names contain match.
func (t *fetchRepoTool) listTags(ctx context.Context, rawURL, match string) (string, error) {
	if t.listings >= listMax {
		return "", fmt.Errorf("agent: fetch_repo: this review has listed tags %d times", listMax)
	}
	t.listings++
	lctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()
	tags, err := (&upstream.Fetcher{Transport: t.transport, Limits: upstream.Limits{WireBytes: fetchWireBytes}}).Tags(lctx, rawURL)
	if err != nil {
		return "", fmt.Errorf("agent: fetch_repo: %w", err)
	}
	t.record(rawURL, "tags")
	return tagList(rawURL, match, tags), nil
}

// tagList is the answer to a listing of tags, newest first: those whose
// names contain match, at most tagsShown of them.
func tagList(rawURL, match string, tags []string) string {
	if len(tags) == 0 {
		return rawURL + " has no tags."
	}
	matched := withText(tags, match)
	if len(matched) == 0 {
		return fmt.Sprintf("None of the %d tags of %s contain %q. The newest: %s.", len(tags), rawURL, match,
			strings.Join(tags[:min(len(tags), closestMax)], ", "))
	}
	var b strings.Builder
	if match == "" {
		fmt.Fprintf(&b, "The %d tags of %s, newest first", len(tags), rawURL)
	} else {
		fmt.Fprintf(&b, "%d of the %d tags of %s contain %q, newest first", len(matched), len(tags), rawURL, match)
	}
	if len(matched) > tagsShown {
		fmt.Fprintf(&b, "; the first %d, so narrow tags for the rest", tagsShown)
		matched = matched[:tagsShown]
	}
	b.WriteString(":\n" + strings.Join(matched, "\n"))
	return b.String()
}

// closest names the tags of req's repository nearest the refs it named,
// for the error of a fetch that named one the repository lacks: those with
// the same version in them, or else the newest. It is "" when the tags
// cannot be listed.
func closest(ctx context.Context, f *upstream.Fetcher, req upstream.Request) string {
	tags, err := f.Tags(ctx, req.URL)
	if err != nil || len(tags) == 0 {
		return ""
	}
	var b strings.Builder
	for _, ref := range []string{req.Ref, req.From} {
		v := versionOf(ref)
		if v == "" || slices.Contains(tags, ref) {
			continue
		}
		if near := withText(tags, v); len(near) > 0 {
			fmt.Fprintf(&b, " Tags with %s: %s.", v, strings.Join(near[:min(len(near), closestMax)], ", "))
		}
	}
	if b.Len() == 0 {
		fmt.Fprintf(&b, " Its newest tags: %s.", strings.Join(tags[:min(len(tags), closestMax)], ", "))
	}
	return b.String()
}

// versionOf is the version in a ref's name, from its first digit on:
// 0.37.0 in v0.37.0 or descheduler-0.37.0. It is "" for HEAD, a commit SHA
// or a name without one.
func versionOf(ref string) string {
	if ref == "HEAD" || gitfetch.IsSHA(ref) {
		return ""
	}
	if i := strings.IndexAny(ref, "0123456789"); i >= 0 {
		return ref[i:]
	}
	return ""
}

// withText is the tags whose names contain text, in any case.
func withText(tags []string, text string) []string {
	text = strings.ToLower(text)
	return slices.DeleteFunc(slices.Clone(tags), func(tag string) bool { return !strings.Contains(strings.ToLower(tag), text) })
}

// Sources are the pages of what the tool fetched, in first-fetch order,
// never nil.
func (t *fetchRepoTool) Sources() []string { return append([]string{}, t.sources...) }

// record keeps a page of what the tool read as a source: on GitHub page
// under the repository, such as tree/<ref>, compare/<from>...<ref> or
// tags, and elsewhere the repository.
func (t *fetchRepoTool) record(rawURL, page string) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return
	}
	u.Path = strings.TrimSuffix(strings.TrimSuffix(u.Path, "/"), ".git")
	if strings.EqualFold(u.Hostname(), "github.com") {
		u.Path += "/" + page
	}
	u.RawPath = ""
	if page := u.String(); !slices.Contains(t.sources, page) {
		t.sources = append(t.sources, page)
	}
}

// dirNameMax bounds a directory's name, well inside a file system's 255
// bytes.
const dirNameMax = 100

// dirName names fetch n's directory after the repository and the ref, in
// characters a command takes as given.
func dirName(n int, rawURL, ref string) string {
	repo := "repo"
	if u, err := url.Parse(rawURL); err == nil {
		if base := strings.TrimSuffix(path.Base(strings.TrimSuffix(u.Path, "/")), ".git"); base != "" && base != "." && base != "/" {
			repo = base
		}
	}
	name := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', strings.ContainsRune("._@-", r):
			return r
		}
		return '-'
	}, fmt.Sprintf("%d-%s@%s", n, repo, ref))
	return name[:min(len(name), dirNameMax)]
}

// fetchedReadFile is read_file that also reads the files fetch_repo wrote
// to dir, at the paths it names under upstreamRel, and hands every other
// path to the read_file it wraps, over the head commit.
type fetchedReadFile struct {
	agent.Tool
	dir      string
	maxBytes int
}

func (r fetchedReadFile) Def() model.ToolDef {
	def := r.Tool.Def()
	def.Description += " It also reads the files fetch_repo wrote, at the paths it names under " + upstreamRel + "/."
	return def
}

func (r fetchedReadFile) Run(ctx context.Context, input json.RawMessage) (string, error) {
	var req struct {
		Path      string `json:"path"`
		StartLine int    `json:"start_line"`
		EndLine   int    `json:"end_line"`
	}
	if len(input) > 0 {
		if err := json.Unmarshal(input, &req); err != nil {
			return "", fmt.Errorf("agent: read_file: %w", err)
		}
	}
	rel, ok := strings.CutPrefix(path.Clean(req.Path), upstreamRel+"/")
	if !ok {
		return r.Tool.Run(ctx, input)
	}
	content, err := readFetched(r.dir, rel)
	if err != nil {
		return "", fmt.Errorf("agent: read_file: %s: %w", req.Path, err)
	}
	out, err := agent.NumberLines(req.Path, content, req.StartLine, req.EndLine, r.maxBytes)
	if err != nil {
		return "", fmt.Errorf("agent: read_file: %w", err)
	}
	return out, nil
}

// readFetched reads the file at rel under dir. The read stays inside dir
// whatever rel or a link in it says. A fetch writes no file over
// MaxBlobBytes, but keeps a diff of up to fetchDiffBytes, which is read in
// line ranges.
func readFetched(dir, rel string) (string, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return "", err
	}
	defer func() { _ = root.Close() }()
	f, err := root.Open(rel)
	if errors.Is(err, fs.ErrNotExist) {
		return "", errors.New("no such file: fetch_repo names the paths it wrote")
	}
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	switch {
	case err != nil:
		return "", err
	case info.IsDir():
		return "", errors.New("a directory: list it with the run tool's commands")
	case info.Size() > fetchDiffBytes:
		return "", fmt.Errorf("%d bytes, over the %d byte limit", info.Size(), fetchDiffBytes)
	}
	b, err := io.ReadAll(f)
	return string(b), err
}
