// Package upstream fetches a repository other than the one under review,
// such as the upstream of a dependency the change bumps, at one ref into a
// throwaway bare repository, and writes its files to a directory the run
// tool's commands can search. Given a second ref it also diffs the two.
// Like gitfetch it is pure go-git, and every fetch is bounded: what the
// server may send and what is written are both capped.
package upstream

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync/atomic"

	git "github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/config"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/client"
	"github.com/go-git/go-git/v6/plumbing/filemode"
	"github.com/go-git/go-git/v6/plumbing/object"
	"github.com/go-git/go-git/v6/plumbing/protocol"
	"github.com/go-git/go-git/v6/plumbing/protocol/packp"
	"github.com/go-git/go-git/v6/plumbing/transport"

	"github.com/home-operations/kritika/internal/gitfetch"
)

const remoteName = "origin"

// Refs the fetched commits are kept under, private to kritika.
const (
	toRef   = plumbing.ReferenceName("refs/kritika/to")
	fromRef = plumbing.ReferenceName("refs/kritika/from")
)

// ErrTooLarge is what a fetch fails with when the server sends more than
// Limits.WireBytes.
var ErrTooLarge = errors.New("upstream: the repository sends more than a fetch may take")

// ErrNoRef is what a fetch fails with when the repository has no tag or
// branch by the name asked for.
var ErrNoRef = errors.New("upstream: no such tag or branch")

// Request is one fetch.
type Request struct {
	// URL is the repository's https:// clone URL.
	URL string
	// Ref is what to fetch: a tag or branch name, HEAD, or a full commit
	// SHA.
	Ref string
	// From, when set, is an earlier ref of the same kinds; the result then
	// carries the diff from it to Ref.
	From string
	// Paths, when set, are the files and directories, relative to the
	// repository root, that are written and diffed. From a server that can
	// filter, only their files are downloaded.
	Paths []string
}

// Limits bound one fetch.
type Limits struct {
	// WireBytes caps what the server may send, across every request the
	// fetch makes.
	WireBytes int64
	// WriteBytes caps the files written. They are written in the tree's
	// order, so past the cap the paths that sort last are left out.
	WriteBytes int64
	// BlobBytes is the largest file written or diffed; a larger one is
	// left out of both, and not read.
	BlobBytes int64
	// DiffBytes caps the diff: a file whose diff would take it past the
	// cap is left out of it.
	DiffBytes int
}

// Result is what a fetch found and wrote.
type Result struct {
	// Commit is the commit Ref names, and FromCommit the one From names.
	Commit, FromCommit string
	// Filtered says the server sent only the files asked for; it sends
	// the whole tree when no paths are given or it cannot filter.
	Filtered bool
	// Files and Bytes are what was written. Skipped counts the symlinks,
	// submodules and files over BlobBytes left out, and Truncated says
	// WriteBytes stopped the writing early.
	Files     int
	Bytes     int64
	Skipped   int
	Truncated bool
	// Diff is the unified diff from FromCommit to Commit, kept to Paths.
	// Changed lists every path the diff touches, and DiffCut how many of
	// those files Diff leaves out: those over BlobBytes, and those that
	// would take it past DiffBytes.
	Diff    string
	Changed []string
	DiffCut int
	// Missing are the Paths the tree has nothing under.
	Missing []MissingPath
}

// MissingPath is a path a fetch asked for that the tree has nothing under,
// with the tree's paths that end in it, at most nearMax: where a path
// given by its last segments is.
type MissingPath struct {
	Path string
	Near []string
}

// nearMax bounds MissingPath.Near.
const nearMax = 5

// Fetcher fetches repositories within its Limits.
type Fetcher struct {
	// Transport carries the fetch's HTTP requests. Nil is a clone of
	// http.DefaultTransport: a client of kritika's own takes the place of
	// go-git's, so the transport has to read the proxy, the egress
	// gateway, from the environment as go-git's does.
	Transport http.RoundTripper
	Limits    Limits
}

// Fetch fetches req at depth one and writes the files under its paths, or
// the whole tree, to dest, a directory it creates. A ref that names an
// annotated tag is peeled to the tag's commit.
func (f *Fetcher) Fetch(ctx context.Context, req Request, dest string) (*Result, error) {
	paths, err := req.check()
	if err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp("", "kritika-upstream-")
	if err != nil {
		return nil, fmt.Errorf("upstream: temp dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	repo, err := git.PlainInit(dir, true)
	if err != nil {
		return nil, fmt.Errorf("upstream: init: %w", err)
	}
	// go-git keeps the fetched pack open, and a removed file stays on disk
	// while it is.
	defer func() { _ = repo.Close() }()
	return f.fetch(ctx, repo, req, paths, dest)
}

func (f *Fetcher) fetch(ctx context.Context, repo *git.Repository, req Request, paths []string, dest string) (*Result, error) {
	if _, err := repo.CreateRemote(&config.RemoteConfig{Name: remoteName, URLs: []string{req.URL}}); err != nil {
		return nil, fmt.Errorf("upstream: remote: %w", err)
	}
	wire, done := f.wire()
	defer done()
	opts := []client.Option{client.WithHTTPClient(&http.Client{Transport: wire})}
	specs := []config.RefSpec{config.RefSpec(req.Ref + ":" + toRef.String())}
	if req.From != "" {
		specs = append(specs, config.RefSpec(req.From+":"+fromRef.String()))
	}
	res := &Result{Filtered: len(paths) > 0}
	var filter packp.Filter
	if res.Filtered {
		filter = packp.FilterBlobNone()
	}
	err := fetch(ctx, repo, opts, specs, 1, filter)
	if errors.Is(err, transport.ErrFilterNotSupported) {
		res.Filtered = false
		err = fetch(ctx, repo, opts, specs, 1, "")
	}
	if err != nil {
		return nil, f.fetchErr(wire, err)
	}

	to, err := commitAt(repo, toRef)
	if err != nil {
		return nil, err
	}
	res.Commit = to.Hash.String()
	toTree, err := to.Tree()
	if err != nil {
		return nil, fmt.Errorf("upstream: tree of %s: %w", to.Hash, err)
	}
	var changes object.Changes
	if req.From != "" {
		from, err := commitAt(repo, fromRef)
		if err != nil {
			return nil, err
		}
		res.FromCommit = from.Hash.String()
		fromTree, err := from.Tree()
		if err != nil {
			return nil, fmt.Errorf("upstream: tree of %s: %w", from.Hash, err)
		}
		// Renames are detected once the blobs are in: detecting them reads
		// the files, which a filtered fetch has not got yet.
		if changes, err = object.DiffTreeWithOptions(ctx, fromTree, toTree, &object.DiffTreeOptions{}); err != nil {
			return nil, fmt.Errorf("upstream: diff: %w", err)
		}
		changes = slices.DeleteFunc(changes, func(c *object.Change) bool {
			return !under(c.From.Name, paths) && !under(c.To.Name, paths)
		})
	}
	files, skipped, err := listFiles(toTree, paths)
	if err != nil {
		return nil, err
	}
	res.Skipped = skipped
	if res.Missing, err = missingPaths(toTree, paths); err != nil {
		return nil, err
	}
	if res.Filtered {
		if err := backfill(ctx, repo, opts, blobs(files, changes)); err != nil {
			return nil, f.fetchErr(wire, err)
		}
	}
	if err := f.write(ctx, repo, files, dest, res); err != nil {
		return nil, err
	}
	if req.From != "" {
		if res.Diff, res.Changed, res.DiffCut, err = render(ctx, changes, f.Limits); err != nil {
			return nil, err
		}
	}
	return res, nil
}

// wire is the transport a fetch or a listing goes through, capped at
// WireBytes, and what to call once it is done.
func (f *Fetcher) wire() (*cappedTransport, func()) {
	if f.Transport != nil {
		return &cappedTransport{base: f.Transport, max: f.Limits.WireBytes}, func() {}
	}
	t := http.DefaultTransport.(*http.Transport).Clone()
	// Nothing reuses t once the fetch is done, and it would keep its idle
	// connections open until they time out.
	return &cappedTransport{base: t, max: f.Limits.WireBytes}, t.CloseIdleConnections
}

// Tags lists the tags of the repository at rawURL, an https:// clone URL,
// in newerFirst order. It asks the server for refs/tags/ only: GitHub
// otherwise lists every pull request's refs too, which can outnumber the
// tags many times over. A server too old to take the prefix lists every
// ref, and the rest are dropped here.
func (f *Fetcher) Tags(ctx context.Context, rawURL string) ([]string, error) {
	if err := checkURL(rawURL); err != nil {
		return nil, err
	}
	u, err := transport.ParseURL(rawURL)
	if err != nil {
		return nil, fmt.Errorf("upstream: url: %w", err)
	}
	wire, done := f.wire()
	defer done()
	cl := client.New(client.WithHTTPClient(&http.Client{Transport: wire}))
	sess, err := cl.Handshake(ctx, &transport.Request{URL: u, Command: transport.UploadPackService, Protocol: protocol.V2})
	if err != nil {
		return nil, f.listErr(wire, err)
	}
	defer func() { _ = sess.Close() }()
	refs, err := sess.GetRemoteRefs(ctx, &transport.GetRemoteRefsOptions{RefPrefixes: []string{"refs/tags/"}})
	switch {
	// go-git takes a listing without refs for an empty repository, which is
	// what the prefix leaves of one without tags.
	case errors.Is(err, transport.ErrEmptyRemoteRepository) && !wire.over():
		return []string{}, nil
	case err != nil:
		return nil, f.listErr(wire, err)
	}
	tags := []string{}
	for _, ref := range refs.References {
		if name, ok := strings.CutPrefix(ref.Name().String(), "refs/tags/"); ok && !strings.HasSuffix(name, "^{}") {
			tags = append(tags, name)
		}
	}
	slices.SortFunc(tags, newerFirst)
	return tags, nil
}

func (f *Fetcher) listErr(wire *cappedTransport, err error) error {
	if wire.over() {
		return fmt.Errorf("%w: past %d MiB", ErrTooLarge, f.Limits.WireBytes>>20)
	}
	return fmt.Errorf("upstream: tags: %w", err)
}

// newerFirst orders tag names by the numbers in them, compared as
// numbers and largest first, so v1.10.0 comes before v1.9.0 and
// gpu-v0.12.1 before gpu-v0.12.0, whatever a name puts around its
// version. A pre-release is listed just above its release.
func newerFirst(a, b string) int {
	for a != "" && b != "" {
		da, db := digitRun(a), digitRun(b)
		if da == 0 || db == 0 {
			if a[0] != b[0] {
				return cmp.Compare(b[0], a[0])
			}
			a, b = a[1:], b[1:]
			continue
		}
		x, y := strings.TrimLeft(a[:da], "0"), strings.TrimLeft(b[:db], "0")
		if c := cmp.Or(cmp.Compare(len(y), len(x)), strings.Compare(y, x)); c != 0 {
			return c
		}
		a, b = a[da:], b[db:]
	}
	return cmp.Compare(len(b), len(a))
}

// digitRun is how many digits s starts with.
func digitRun(s string) int {
	n := 0
	for n < len(s) && s[n] >= '0' && s[n] <= '9' {
		n++
	}
	return n
}

// fetchErr says why a fetch over wire failed. A ref the server lacks fails
// as an empty repository when the server lists only the refs asked for, as
// protocol v2 lets it.
func (f *Fetcher) fetchErr(wire *cappedTransport, err error) error {
	switch {
	case wire.over():
		return fmt.Errorf("%w: past %d MiB", ErrTooLarge, f.Limits.WireBytes>>20)
	case errors.Is(err, git.ErrRemoteRefNotFound) || errors.Is(err, transport.ErrEmptyRemoteRepository):
		return fmt.Errorf("%w (%w); give one the repository has, or a commit's full SHA", ErrNoRef, err)
	}
	return fmt.Errorf("upstream: fetch: %w", err)
}

func fetch(ctx context.Context, repo *git.Repository, opts []client.Option, specs []config.RefSpec, depth int, filter packp.Filter) error {
	err := repo.FetchContext(ctx, &git.FetchOptions{
		RemoteName:    remoteName,
		ClientOptions: opts,
		Depth:         depth,
		Tags:          git.NoTags,
		RefSpecs:      specs,
		Filter:        filter,
	})
	if errors.Is(err, git.NoErrAlreadyUpToDate) {
		return nil
	}
	return err
}

// commitAt is the commit the fetched ref points at, through any annotated
// tags, one of which can name another.
func commitAt(repo *git.Repository, name plumbing.ReferenceName) (*object.Commit, error) {
	ref, err := repo.Reference(name, true)
	if err != nil {
		return nil, fmt.Errorf("upstream: %s: %w", name, err)
	}
	h := ref.Hash()
	for tag, err := repo.TagObject(h); err == nil; tag, err = repo.TagObject(h) {
		h = tag.Target
	}
	c, err := repo.CommitObject(h)
	if err != nil {
		return nil, fmt.Errorf("upstream: commit %s: %w", h, err)
	}
	return c, nil
}

type file struct {
	name string
	hash plumbing.Hash
}

// listFiles lists the regular files of tree under paths, every one when
// there are none, and counts the symlinks and submodules there it leaves
// out. It reads only trees, which a filtered fetch has.
func listFiles(tree *object.Tree, paths []string) ([]file, int, error) {
	w := object.NewTreeWalker(tree, true, nil)
	defer w.Close()
	var files []file
	skipped := 0
	for {
		name, e, err := w.Next()
		if errors.Is(err, io.EOF) {
			return files, skipped, nil
		}
		if err != nil {
			return nil, 0, fmt.Errorf("upstream: walk: %w", err)
		}
		if e.Mode == filemode.Dir || !under(name, paths) {
			continue
		}
		if e.Mode == filemode.Symlink || e.Mode == filemode.Submodule {
			skipped++
			continue
		}
		files = append(files, file{name: name, hash: e.Hash})
	}
}

// missingPaths are the paths tree has nothing under, each with the tree's
// paths that end in it. It reads only trees, which a filtered fetch has.
func missingPaths(tree *object.Tree, paths []string) ([]MissingPath, error) {
	if len(paths) == 0 {
		return nil, nil
	}
	found := make([]bool, len(paths))
	near := make([][]string, len(paths))
	w := object.NewTreeWalker(tree, true, nil)
	defer w.Close()
	for {
		name, _, err := w.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("upstream: walk: %w", err)
		}
		for i, p := range paths {
			switch {
			case name == p || strings.HasPrefix(name, p+"/"):
				found[i] = true
			case strings.HasSuffix(name, "/"+p) && len(near[i]) < nearMax:
				near[i] = append(near[i], name)
			}
		}
	}
	var out []MissingPath
	for i, p := range paths {
		if !found[i] {
			out = append(out, MissingPath{Path: p, Near: near[i]})
		}
	}
	return out, nil
}

// blobs are the blobs that writing files and rendering changes read, each
// once. A submodule's entry names a commit of another repository, which
// this one has no copy of.
func blobs(files []file, changes object.Changes) []plumbing.Hash {
	seen := make(map[plumbing.Hash]bool, len(files)+2*len(changes))
	var out []plumbing.Hash
	add := func(h plumbing.Hash) {
		if !seen[h] {
			seen[h] = true
			out = append(out, h)
		}
	}
	for _, f := range files {
		add(f.hash)
	}
	for _, c := range changes {
		for _, e := range []object.ChangeEntry{c.From, c.To} {
			if e.Name != "" && e.TreeEntry.Mode != filemode.Submodule {
				add(e.TreeEntry.Hash)
			}
		}
	}
	return out
}

// backfill fetches the blobs a filtered fetch left out. The refs to the
// fetched commits go first: go-git offers every local ref as a have, and a
// server told the client has a commit sends none of the blobs under it,
// wanted or not.
func backfill(ctx context.Context, repo *git.Repository, opts []client.Option, hashes []plumbing.Hash) error {
	var missing []plumbing.Hash
	for _, h := range hashes {
		if repo.Storer.HasEncodedObject(h) != nil {
			missing = append(missing, h)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	for _, name := range []plumbing.ReferenceName{toRef, fromRef} {
		if err := repo.Storer.RemoveReference(name); err != nil {
			return fmt.Errorf("remove %s: %w", name, err)
		}
	}
	specs := make([]config.RefSpec, len(missing))
	for i, h := range missing {
		specs[i] = config.RefSpec(fmt.Sprintf("%s:refs/kritika/blobs/%d", h, i))
	}
	if err := fetch(ctx, repo, opts, specs, 0, ""); err != nil {
		return err
	}
	left := 0
	for _, h := range missing {
		if repo.Storer.HasEncodedObject(h) != nil {
			left++
		}
	}
	if left > 0 {
		return fmt.Errorf("the server left out %d of the %d files asked for", left, len(missing))
	}
	return nil
}

// write writes files to dest, a directory it creates, within the limits,
// and records what it wrote in res. Every file is written 0644, whatever
// mode git recorded: nothing in it is meant to run.
func (f *Fetcher) write(ctx context.Context, repo *git.Repository, files []file, dest string, res *Result) error {
	if err := os.Mkdir(dest, 0o755); err != nil {
		return fmt.Errorf("upstream: %w", err)
	}
	for _, fl := range files {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("upstream: write: %w", err)
		}
		blob, err := repo.BlobObject(fl.hash)
		if err != nil {
			return fmt.Errorf("upstream: %s: %w", fl.name, err)
		}
		if blob.Size > f.Limits.BlobBytes {
			res.Skipped++
			continue
		}
		if res.Bytes+blob.Size > f.Limits.WriteBytes {
			res.Truncated = true
			return nil
		}
		name, err := relPath(fl.name)
		if err != nil {
			return fmt.Errorf("upstream: %w", err)
		}
		if err := writeBlob(filepath.Join(dest, filepath.FromSlash(name)), blob); err != nil {
			return fmt.Errorf("upstream: %s: %w", name, err)
		}
		res.Files++
		res.Bytes += blob.Size
	}
	return nil
}

func writeBlob(name string, blob *object.Blob) error {
	if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		return err
	}
	r, err := blob.Reader()
	if err != nil {
		return err
	}
	defer func() { _ = r.Close() }()
	// O_EXCL: a path is written once, never through whatever is there.
	out, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, r); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

// renameLimit is the most deleted or added files whose renames are found
// by content: go-git scores each deleted file against each added one,
// reading the added one whole every time. Past it, only exact renames are
// found.
const renameLimit = 50

// render is changes, with their renames found, as a unified diff, the
// paths they touch by their new names, and how many files the diff leaves
// out: those over l.BlobBytes, which are neither read nor diffed, and those
// that would take it past l.DiffBytes.
func render(ctx context.Context, changes object.Changes, l Limits) (string, []string, int, error) {
	var small, large object.Changes
	for _, c := range changes {
		from, to, err := c.Files()
		if err != nil {
			return "", nil, 0, fmt.Errorf("upstream: diff: %w", err)
		}
		if from != nil && from.Size > l.BlobBytes || to != nil && to.Size > l.BlobBytes {
			large = append(large, c)
		} else {
			small = append(small, c)
		}
	}
	opts := *object.DefaultDiffTreeOptions
	opts.RenameLimit = renameLimit
	small, err := object.DetectRenames(small, &opts)
	if err != nil {
		return "", nil, 0, fmt.Errorf("upstream: renames: %w", err)
	}
	all := slices.Concat(small, large)
	sort.Stable(all)
	changed := make([]string, len(all))
	for i, c := range all {
		changed[i] = cmp.Or(c.To.Name, c.From.Name)
	}
	var b strings.Builder
	cut := len(large)
	for _, c := range small {
		patch, err := object.Changes{c}.PatchContext(ctx)
		if err != nil {
			return "", nil, 0, fmt.Errorf("upstream: diff: %w", err)
		}
		s := patch.String()
		if b.Len()+len(s) > l.DiffBytes {
			cut++
			continue
		}
		b.WriteString(s)
	}
	return b.String(), changed, cut, nil
}

// check validates r and returns its paths cleaned.
func (r Request) check() ([]string, error) {
	if err := checkURL(r.URL); err != nil {
		return nil, err
	}
	if err := checkRef(r.Ref); err != nil {
		return nil, fmt.Errorf("upstream: ref: %w", err)
	}
	if r.From != "" {
		if err := checkRef(r.From); err != nil {
			return nil, fmt.Errorf("upstream: from: %w", err)
		}
	}
	paths := make([]string, 0, len(r.Paths))
	for _, p := range r.Paths {
		c, err := relPath(p)
		if err != nil {
			return nil, fmt.Errorf("upstream: paths: %w", err)
		}
		paths = append(paths, c)
	}
	return paths, nil
}

// checkURL accepts an https:// clone URL without credentials, a query or
// a fragment.
func checkURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" ||
		strings.Trim(u.Path, "/") == "" {
		return fmt.Errorf("upstream: url %q is not an https:// clone URL without credentials, a query or a fragment", raw)
	}
	return nil
}

// checkRef accepts HEAD, a full commit SHA, or a name a remote tag or
// branch could have, which also keeps it from changing the refspec it is
// put in.
func checkRef(ref string) error {
	if ref == "HEAD" || gitfetch.IsSHA(ref) {
		return nil
	}
	if ref == "" || strings.HasPrefix(ref, "-") || strings.HasPrefix(ref, "+") ||
		plumbing.ReferenceName("refs/"+ref).Validate() != nil {
		return fmt.Errorf("%q is not a tag, a branch, HEAD or a full commit SHA", ref)
	}
	return nil
}

// relPath rejects an empty, absolute or escaping path, and returns its
// cleaned, slash-separated form otherwise.
func relPath(p string) (string, error) {
	c := path.Clean(p)
	if p == "" || path.IsAbs(c) || c == "." || c == ".." || strings.HasPrefix(c, "../") {
		return "", fmt.Errorf("%q is not a path inside the repository", p)
	}
	return c, nil
}

// under reports whether name is one of paths or inside one; with no paths,
// every name is.
func under(name string, paths []string) bool {
	if len(paths) == 0 {
		return true
	}
	for _, p := range paths {
		if name == p || strings.HasPrefix(name, p+"/") {
			return true
		}
	}
	return false
}

// cappedTransport counts the bytes of every response body it carries and
// fails the read that passes max. That read returns only the bytes up to
// max: go-git reads a small response whole, in one read, and parses it from
// a buffer, so an error that came with every byte would never be seen.
type cappedTransport struct {
	base http.RoundTripper
	max  int64
	n    atomic.Int64
}

func (t *cappedTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	resp, err := t.base.RoundTrip(r)
	if err != nil {
		return nil, err
	}
	resp.Body = &cappedBody{ReadCloser: resp.Body, t: t}
	return resp, nil
}

// over reports whether the bodies have passed the cap.
func (t *cappedTransport) over() bool { return t.n.Load() > t.max }

type cappedBody struct {
	io.ReadCloser
	t *cappedTransport
}

func (b *cappedBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if total := b.t.n.Add(int64(n)); total > b.t.max {
		return max(n-int(total-b.t.max), 0), ErrTooLarge
	}
	return n, err
}
