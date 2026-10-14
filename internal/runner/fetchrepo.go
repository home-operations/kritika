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
	"github.com/home-operations/kritika/internal/textcut"
	"github.com/home-operations/kritika/internal/upstream"
)

// What fetch_repo may take: a run makes at most the spec's fetches and writes
// at most fetchWriteBytes of their files, beside its checkout, a budget
// the run tool's kept outputs share; one fetch takes at most
// fetchWireBytes from the server and fetchTimeout of wall time, and keeps
// at most fetchDiffBytes of a diff, as a kept output is. A run also lists tags
// at most listMax times, counting the listing a fetch that names a missing
// ref makes to name the closest; an answer shows at most tagsShown names,
// such an error at most closestMax, and a diff's answer names at most
// changedShown of the paths it changes before the patches.
const (
	fetchWriteBytes = 256 << 20
	fetchWireBytes  = 128 << 20
	fetchTimeout    = 2 * time.Minute
	fetchDiffBytes  = 4 << 20
	listMax         = 8
	tagsShown       = 200
	closestMax      = 10
	changedShown    = 100
)

// upstreamDir is the scratch directory fetch_repo writes to, beside the
// checkout, where the run tool keeps a cut output too, and upstreamRel the
// same directory as the run tool's commands see it from the checkout.
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
			"description": "Files and directories to fetch, relative to the root, for part of a large repository such as a chart. Omit for all."
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
	// budget is what the review may still write beside the checkout.
	budget *agent.WriteBudget
	// fetchMax is how many fetches the review may make.
	fetchMax int
	// maxBytes is the loop's cap on a tool's answer, which a diff is cut to
	// here so the note after the cut, naming the file that holds it whole,
	// survives; zero leaves the cut to the loop.
	maxBytes int

	fetches  int
	listings int
	sources  []string
}

func (t *fetchRepoTool) Def() model.ToolDef {
	desc := "Fetch a repository other than the one under review, such as the upstream of a dependency, at a tag, " +
		"branch or commit, and write its files to a directory read_file reads and the run tool's commands search. With from, it also " +
		"returns the diff between the two, the paths it changes listed first. With tags instead of ref, it lists the repository's " +
		fmt.Sprintf("tags, to find the tag of a version rather than guess its name. A review may fetch %d times and list tags %d times.",
			t.fetchMax, listMax) +
		` For example, {"url": "https://github.com/cert-manager/cert-manager", "ref": "v1.17.0", "from": "v1.16.0", ` +
		`"paths": ["deploy/charts"]} reads a chart's changes between two versions, and ` +
		`{"url": "https://github.com/cert-manager/cert-manager", "tags": "1.17"} lists the tags with 1.17 in their names.`
	if t.maxBytes > 0 {
		desc += fmt.Sprintf(" A diff cut to %d KiB in the answer is whole in the file the answer names: read it with read_file in "+
			"line ranges, or search it with the commands, rather than fetch again.", t.maxBytes>>10)
	}
	return model.ToolDef{Name: "fetch_repo", Description: desc, InputSchema: fetchRepoSchema}
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
	// A model that sends every property may send paths as [""], which
	// asks for no path.
	in.Paths = slices.DeleteFunc(in.Paths, func(p string) bool { return strings.TrimSpace(p) == "" })
	switch {
	case in.Tags != nil && in.Ref == "":
		if in.From != "" || len(in.Paths) > 0 {
			return "", errors.New("agent: fetch_repo: give ref to fetch, or tags without from or paths to list tags")
		}
		return t.listTags(ctx, in.URL, *in.Tags)
	case in.Tags != nil && *in.Tags != "":
		return "", errors.New("agent: fetch_repo: give ref to fetch or tags to list tags, not both")
	case in.Ref == "":
		return "", errors.New("agent: fetch_repo: give ref, such as v1.17.0 or HEAD, to fetch, or tags to list the repository's tags")
	}
	req := upstream.Request{URL: in.URL, Ref: in.Ref, From: in.From, Paths: in.Paths}
	if t.fetches >= t.fetchMax {
		return "", fmt.Errorf("agent: fetch_repo: this review has made its %d fetches", t.fetchMax)
	}
	left := t.budget.Left()
	if left <= 0 {
		return "", fmt.Errorf("agent: fetch_repo: this review has written its %d MiB of fetched files", fetchWriteBytes>>20)
	}
	t.fetches++
	f := &upstream.Fetcher{Transport: t.transport, Limits: upstream.Limits{
		WireBytes: fetchWireBytes, WriteBytes: left, BlobBytes: agent.MaxBlobBytes, DiffBytes: fetchDiffBytes,
	}}
	fctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()
	asked := req
	res, req, err := t.fetch(fctx, f, req)
	if err != nil {
		if errors.Is(err, upstream.ErrTooLarge) {
			if len(req.Paths) == 0 {
				return "", fmt.Errorf("agent: fetch_repo: %w; give paths to fetch only the files under them", err)
			}
			return "", fmt.Errorf("agent: fetch_repo: %w; give fewer paths", err)
		}
		return "", fmt.Errorf("agent: fetch_repo: %w", err)
	}
	return t.answer(asked, req, res)
}

// answer is the answer to a fetch of req, asked for as asked, that found
// res: where the files are, what a ref stood in for, and the diff, cut to
// the tool's cap after the paths it changes, with the file that holds it
// whole named on both sides of the cut.
func (t *fetchRepoTool) answer(asked, req upstream.Request, res *upstream.Result) (string, error) {
	t.budget.Spend(res.Bytes)
	page := "tree/" + req.Ref
	if req.From != "" {
		page = "compare/" + req.From + "..." + req.Ref
	}
	// A fetch that found nothing under its paths read nothing.
	if res.Files > 0 || res.Diff != "" {
		t.record(req.URL, page)
	}

	name := dirName(t.fetches, req.URL, req.Ref)
	rel := upstreamRel + "/" + name
	var b strings.Builder
	for _, r := range [][2]string{{asked.Ref, req.Ref}, {asked.From, req.From}} {
		if r[0] != r[1] {
			fmt.Fprintf(&b, "The repository has no %s; its tag with that version, %s, stands in for it. ", r[0], r[1])
		}
	}
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
	if len(res.Changed) == 0 {
		b.WriteString(".")
		return b.String(), nil
	}
	// The paths come before the patches, so a cut answer still says what
	// the diff touches and the agent can ask for the files it needs.
	shown := res.Changed[:min(len(res.Changed), changedShown)]
	fmt.Fprintf(&b, ":\n%s\n", strings.Join(shown, "\n"))
	if rest := len(res.Changed) - len(shown); rest > 0 {
		fmt.Fprintf(&b, "and %d more, which the diff names.\n", rest)
	}
	if res.Diff == "" {
		return strings.TrimSuffix(b.String(), "\n"), nil
	}
	if int64(len(res.Diff)) > t.budget.Left() {
		fmt.Fprintf(&b, "The diff is not kept in a file, past what this review may write, and follows, cut to fit when long:\n\n%s", res.Diff)
		return b.String(), nil
	}
	diffFile := name + ".diff"
	if err := os.WriteFile(filepath.Join(t.dir, diffFile), []byte(res.Diff), 0o644); err != nil {
		return "", fmt.Errorf("agent: fetch_repo: %w", err)
	}
	t.budget.Spend(int64(len(res.Diff)))
	fmt.Fprintf(&b, "The diff is in %s/%s, and follows, cut to fit when long:\n\n%s", upstreamRel, diffFile, res.Diff)
	out := b.String()
	if t.maxBytes > 0 && len(out) > t.maxBytes {
		// As the run tool does for a kept output: the note after the cut
		// names the file, so it is read where the answer runs out.
		tail := fmt.Sprintf("\n[the whole diff is in %s/%s]", upstreamRel, diffFile)
		out = textcut.Truncate(out, t.maxBytes-len(tail)) + tail
	}
	return out, nil
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

// fetch fetches req to its directory under t.dir, as fetch n, and returns
// the request as fetched. When a ref names a version the repository tags
// under another name, v1.2.3 or chart-1.2.3 for 1.2.3, the fetch is made
// again with the one tag with that version in its place; the tags are
// listed once for that, counted among the listings, and name the closest
// in the error when no tag stands in.
func (t *fetchRepoTool) fetch(ctx context.Context, f *upstream.Fetcher, req upstream.Request) (*upstream.Result, upstream.Request, error) {
	res, err := t.fetchTo(ctx, f, req)
	if !errors.Is(err, upstream.ErrNoRef) {
		return res, req, err
	}
	hint := ""
	if t.listings < listMax {
		t.listings++
		if refs, lerr := f.Refs(ctx, req.URL, versioned(req)); lerr == nil && len(refs.Tags) > 0 {
			if fixed, ok := resolveRefs(req, refs); ok {
				res, rerr := t.fetchTo(ctx, f, fixed)
				if rerr == nil || !errors.Is(rerr, upstream.ErrNoRef) {
					return res, fixed, rerr
				}
				req, err = fixed, rerr
			}
			hint = closest(req, refs.Tags)
		}
	}
	return nil, req, fmt.Errorf("%w.%s", err, hint)
}

// versioned are the refs of req a tag could stand in for, those with a
// version in their names, which the listing asks the server for as
// branches: one the repository has as a branch is not missing.
func versioned(req upstream.Request) []string {
	var out []string
	for _, ref := range []string{req.Ref, req.From} {
		if versionOf(ref) != "" {
			out = append(out, ref)
		}
	}
	return out
}

// fetchTo is one fetch of req into the directory named for it, which a
// failed fetch leaves no trace of: what it wrote is not counted, so it is
// not kept.
func (t *fetchRepoTool) fetchTo(ctx context.Context, f *upstream.Fetcher, req upstream.Request) (*upstream.Result, error) {
	dest := filepath.Join(t.dir, dirName(t.fetches, req.URL, req.Ref))
	res, err := f.Fetch(ctx, req, dest)
	if err != nil {
		_ = os.RemoveAll(dest)
		return nil, err
	}
	return res, nil
}

// resolveRefs is req with each ref the repository has neither as a tag nor
// as a branch replaced by the one tag with its version, and whether any
// was. A ref without a version, a branch's name or HEAD, is kept as given.
func resolveRefs(req upstream.Request, refs upstream.Refs) (upstream.Request, bool) {
	changed := false
	for _, ref := range []*string{&req.Ref, &req.From} {
		if *ref == "" || slices.Contains(refs.Tags, *ref) || refs.Branches[*ref] {
			continue
		}
		if tag := tagFor(*ref, refs.Tags); tag != "" {
			*ref, changed = tag, true
		}
	}
	return req, changed
}

// tagFor is the one tag of tags with ref's version in it, "" when ref has
// no version or the tags have none or several with it: between v1.2.3 and
// chart-1.2.3 for 1.2.3 is the agent's to choose.
func tagFor(ref string, tags []string) string {
	v := versionOf(ref)
	if v == "" {
		return ""
	}
	found := ""
	for _, tag := range tags {
		if versionOf(tag) != v {
			continue
		}
		if found != "" {
			return ""
		}
		found = tag
	}
	return found
}

// closest names the tags of req's repository nearest the refs it named,
// for the error of a fetch that named one the repository lacks: those with
// the same version in them, or else the newest.
func closest(req upstream.Request, tags []string) string {
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
// under the repository, tree/<ref> or compare/<from>...<ref>, and
// elsewhere the repository.
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

// fetchedReadFile is read_file that also reads the files fetch_repo and
// the run tool wrote to dir, at the paths their notes name under
// upstreamRel, and hands every other path to the read_file it wraps, over
// the head commit.
type fetchedReadFile struct {
	agent.Tool
	dir      string
	maxBytes int
}

func (r fetchedReadFile) Def() model.ToolDef {
	def := r.Tool.Def()
	def.Description += " It also reads the files under " + upstreamRel + "/, at the paths fetch_repo and the run tool's notes name."
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
// MaxBlobBytes, but keeps a diff of up to fetchDiffBytes, as the run tool
// keeps an output, which is read in line ranges.
func readFetched(dir, rel string) (string, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return "", err
	}
	defer func() { _ = root.Close() }()
	f, err := root.Open(rel)
	if errors.Is(err, fs.ErrNotExist) {
		return "", errors.New("no such file: fetch_repo and the run tool's notes name the paths they wrote")
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
