package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/home-operations/kritika/internal/agent"
	"github.com/home-operations/kritika/internal/model"
	"github.com/home-operations/kritika/internal/upstream"
)

// What fetch_repo may take: a run makes at most fetchMax fetches and writes
// at most fetchWriteBytes of their files, beside its checkout; one fetch
// takes at most fetchWireBytes from the server and fetchTimeout of wall
// time, and keeps at most fetchDiffBytes of a diff.
const (
	fetchMax        = 8
	fetchWriteBytes = 256 << 20
	fetchWireBytes  = 128 << 20
	fetchTimeout    = 2 * time.Minute
	fetchDiffBytes  = 4 << 20
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
		"ref": {"type": "string", "description": "The tag, branch or full 40-character commit SHA to fetch; HEAD for the default branch."},
		"from": {"type": "string", "description": "An earlier tag, branch or full commit SHA: the result then includes the diff from it to ref."},
		"paths": {
			"type": "array",
			"items": {"type": "string"},
			"description": "Files and directories to fetch, relative to the root; only their files are downloaded. Omit for all."
		}
	},
	"required": ["url", "ref"],
	"additionalProperties": false
}`)

// fetchRepoTool is fetch_repo: it fetches a repository other than the one
// under review at a ref, and writes its files under dir, beside the
// checkout, for the run tool's commands to search, with the diff from an
// earlier ref when asked.
type fetchRepoTool struct {
	dir string
	// transport carries the fetches; nil is upstream's default, through the
	// pod's proxy.
	transport http.RoundTripper

	fetches int
	written int64
	sources []string
}

func (t *fetchRepoTool) Def() model.ToolDef {
	return model.ToolDef{
		Name: "fetch_repo",
		Description: "Fetch a repository other than the one under review, such as the upstream of a dependency, at a tag, " +
			"branch or commit, and write its files to a directory the run tool's commands can search. With from, it also " +
			fmt.Sprintf("returns the diff between the two. A review may fetch %d times.", fetchMax),
		InputSchema: fetchRepoSchema,
	}
}

func (t *fetchRepoTool) Run(ctx context.Context, input json.RawMessage) (string, error) {
	var in struct {
		URL   string   `json:"url"`
		Ref   string   `json:"ref"`
		From  string   `json:"from"`
		Paths []string `json:"paths"`
	}
	if len(input) > 0 {
		if err := json.Unmarshal(input, &in); err != nil {
			return "", fmt.Errorf("agent: fetch_repo: %w", err)
		}
	}
	req := upstream.Request(in)
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
		return "", fmt.Errorf("agent: fetch_repo: %w", err)
	}
	t.written += res.Bytes
	t.record(req)

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
	if res.Files+res.Skipped == 0 && !res.Truncated && len(req.Paths) > 0 {
		b.WriteString(" No file is under the paths asked for.")
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

// Sources are the pages of what the tool fetched, in first-fetch order,
// never nil.
func (t *fetchRepoTool) Sources() []string { return append([]string{}, t.sources...) }

// record keeps what req fetched as a source: on GitHub the tree at the ref,
// or the compare view from the earlier one, and elsewhere the repository.
func (t *fetchRepoTool) record(req upstream.Request) {
	u, err := url.Parse(req.URL)
	if err != nil {
		return
	}
	u.Path = strings.TrimSuffix(strings.TrimSuffix(u.Path, "/"), ".git")
	if strings.EqualFold(u.Hostname(), "github.com") {
		if req.From != "" {
			u.Path += "/compare/" + req.From + "..." + req.Ref
		} else {
			u.Path += "/tree/" + req.Ref
		}
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
