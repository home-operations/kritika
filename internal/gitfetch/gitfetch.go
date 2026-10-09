// Package gitfetch fetches exactly the commits a review needs, the head and
// the merge-base and, for a re-review, the head and merge base of the last
// review, at depth one into a throwaway bare repository, and diffs their
// trees. Trees are enough: `git diff` compares trees and needs no history.
// It is pure go-git; the runner image has no git binary.
package gitfetch

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"

	git "github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/config"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/client"
	"github.com/go-git/go-git/v6/plumbing/object"
	githttp "github.com/go-git/go-git/v6/plumbing/transport/http"
)

// Refs the commits are fetched into. They are private to kritika so the bare
// repository never gains a branch a later fetch could confuse.
const (
	headRef      = "refs/kritika/head"
	baseRef      = "refs/kritika/base"
	priorRef     = "refs/kritika/prior"
	priorBaseRef = "refs/kritika/prior-base"
)

const remoteName = "origin"

// Fetch describes what to fetch.
type Fetch struct {
	// CloneURL is the HTTPS clone URL, or a local path for tests.
	CloneURL string
	// Token, when set, authenticates as x-access-token, which is what a GitHub
	// App installation token expects.
	Token string
	// Head and Base are full commit SHAs.
	Head, Base string
	// Prior, when set, is the full SHA of the head the last review saw. It
	// is fetched best effort: a force-push may have made it unreachable.
	// PriorChanged are the paths the change touched at Prior, and PriorBase
	// the merge base it was reviewed against, also fetched best effort.
	Prior        string
	PriorChanged []string
	PriorBase    string
}

// Result is the two fetched commits and the diff between them.
type Result struct {
	Head *object.Commit
	Base *object.Commit
	// Diff is the unified diff from base to head.
	Diff string
	// PatchID is a stable identity of the change, independent of line
	// numbers and of which commits carry it, in the spirit of
	// `git patch-id --stable`: a rebase that leaves the change untouched
	// keeps its patch id.
	PatchID string
	// Changed lists the paths the diff touches, head-side names.
	Changed []string
	// Prior is the fetched prior head, nil when none was asked for or it
	// could not be fetched, in which case PriorErr says why. DeltaDiff and
	// DeltaChanged are the diff from it to head and the paths that diff
	// touches, kept to the paths the change touches at either end, less
	// the hunks the base gained in them between the two merge bases: what
	// else moved between the two heads is the base, under a merge or a
	// rebase. PriorBaseErr is why the prior merge base could not be
	// fetched, in which case those hunks stay in.
	Prior        *object.Commit
	PriorErr     error
	PriorBaseErr error
	DeltaDiff    string
	DeltaChanged []string
	// Dir is the bare repository on disk; the caller removes it.
	Dir string

	repo *git.Repository
}

// Close closes and removes the bare repository. go-git keeps the fetched
// pack open, and a removed file stays on disk while it is.
func (r *Result) Close() error { return errors.Join(r.repo.Close(), os.RemoveAll(r.Dir)) }

// Run fetches head and base at depth one and diffs them. The temp dir is
// removed on error; on success the caller owns it through Result.Close.
func Run(ctx context.Context, f Fetch) (*Result, error) {
	if !IsSHA(f.Head) || (f.Base != "" && !IsSHA(f.Base)) || (f.Prior != "" && !IsSHA(f.Prior)) || (f.PriorBase != "" && !IsSHA(f.PriorBase)) {
		return nil, fmt.Errorf("gitfetch: head %q, base %q, prior %q and prior base %q must be full commit SHAs",
			f.Head, f.Base, f.Prior, f.PriorBase)
	}
	dir, err := os.MkdirTemp("", "kritika-fetch-")
	if err != nil {
		return nil, fmt.Errorf("gitfetch: temp dir: %w", err)
	}
	res, err := run(ctx, f, dir)
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	return res, nil
}

func run(ctx context.Context, f Fetch, dir string) (_ *Result, err error) {
	repo, err := git.PlainInit(dir, true)
	if err != nil {
		return nil, fmt.Errorf("gitfetch: init: %w", err)
	}
	defer func() {
		if err != nil {
			_ = repo.Close()
		}
	}()
	if _, err := repo.CreateRemote(&config.RemoteConfig{Name: remoteName, URLs: []string{f.CloneURL}}); err != nil {
		return nil, fmt.Errorf("gitfetch: remote: %w", err)
	}
	var opts []client.Option
	if f.Token != "" {
		opts = append(opts, client.WithHTTPAuth(&githttp.BasicAuth{Username: "x-access-token", Password: f.Token}))
	}
	// Fetching a bare SHA needs the server to allow it; GitHub does for
	// reachable commits. Both refspecs in one fetch so the server can send
	// one pack.
	err = repo.FetchContext(ctx, &git.FetchOptions{
		RemoteName:    remoteName,
		ClientOptions: opts,
		Depth:         1,
		Tags:          git.NoTags,
		RefSpecs:      refSpecs(f),
	})
	if err != nil && !errors.Is(err, git.NoErrAlreadyUpToDate) {
		return nil, fmt.Errorf("gitfetch: fetch: %w", err)
	}
	head, err := repo.CommitObject(plumbing.NewHash(f.Head))
	if err != nil {
		return nil, fmt.Errorf("gitfetch: head %s: %w", f.Head, err)
	}
	if f.Base == "" {
		// Head only: no diff, the caller walks the tree.
		return &Result{Head: head, Dir: dir, repo: repo}, nil
	}
	base, err := repo.CommitObject(plumbing.NewHash(f.Base))
	if err != nil {
		return nil, fmt.Errorf("gitfetch: base %s: %w", f.Base, err)
	}
	changes, err := treeChanges(ctx, base, head)
	if err != nil {
		return nil, err
	}
	diff, changed, err := renderChanges(ctx, changes)
	if err != nil {
		return nil, err
	}
	res := &Result{Head: head, Base: base, Diff: diff, PatchID: PatchID(diff), Changed: changed, Dir: dir, repo: repo}
	if f.Prior == "" {
		return res, nil
	}
	if res.Prior, res.PriorErr = fetchCommit(ctx, repo, opts, f.Prior, priorRef); res.Prior == nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("gitfetch: prior: %w", ctx.Err())
		}
		return res, nil
	}
	// The change's own paths, both names of a rename, at either head.
	own := make(map[string]bool, 2*len(changes)+len(f.PriorChanged))
	for _, c := range changes {
		for _, name := range []string{c.From.Name, c.To.Name} {
			if name != "" {
				own[name] = true
			}
		}
	}
	for _, name := range f.PriorChanged {
		own[name] = true
	}
	delta, err := treeChanges(ctx, res.Prior, head)
	if err != nil {
		return nil, err
	}
	delta = slices.DeleteFunc(delta, func(c *object.Change) bool { return !own[c.From.Name] && !own[c.To.Name] })
	var gained map[string]bool
	if f.PriorBase != "" && f.PriorBase != f.Base {
		var priorBase *object.Commit
		if priorBase, res.PriorBaseErr = fetchCommit(ctx, repo, opts, f.PriorBase, priorBaseRef); priorBase != nil {
			if gained, err = baseGained(ctx, priorBase, base, own); err != nil {
				return nil, err
			}
		} else if ctx.Err() != nil {
			return nil, fmt.Errorf("gitfetch: prior base: %w", ctx.Err())
		}
	}
	if res.DeltaDiff, res.DeltaChanged, err = renderDelta(ctx, delta, gained); err != nil {
		return nil, err
	}
	return res, nil
}

// fetchCommit fetches one commit into ref in a fetch of its own, so that an
// unreachable commit cannot fail the fetch of head and base. The error says
// why it could not be had; the caller decides whether that matters.
func fetchCommit(ctx context.Context, repo *git.Repository, opts []client.Option, sha, ref string) (*object.Commit, error) {
	if c, err := repo.CommitObject(plumbing.NewHash(sha)); err == nil {
		return c, nil
	}
	err := repo.FetchContext(ctx, &git.FetchOptions{
		RemoteName:    remoteName,
		ClientOptions: opts,
		Depth:         1,
		Tags:          git.NoTags,
		RefSpecs:      []config.RefSpec{config.RefSpec(sha + ":" + ref)},
	})
	if err != nil && !errors.Is(err, git.NoErrAlreadyUpToDate) {
		return nil, fmt.Errorf("gitfetch: fetch %s: %w", sha, err)
	}
	c, err := repo.CommitObject(plumbing.NewHash(sha))
	if err != nil {
		return nil, fmt.Errorf("gitfetch: %s: %w", sha, err)
	}
	return c, nil
}

// baseGained is the hunks the base gained in the change's own paths
// between the merge base the last review saw and this one's, keyed as
// hunkKeys keys them. A merge or a rebase carries them to the head without
// their being the change's, so the delta leaves them out.
func baseGained(ctx context.Context, priorBase, base *object.Commit, own map[string]bool) (map[string]bool, error) {
	changes, err := treeChanges(ctx, priorBase, base)
	if err != nil {
		return nil, err
	}
	changes = slices.DeleteFunc(changes, func(c *object.Change) bool { return !own[c.From.Name] && !own[c.To.Name] })
	diff, _, err := renderChanges(ctx, changes)
	if err != nil {
		return nil, err
	}
	return hunkKeys(diff), nil
}

// renderDelta is renderChanges less the hunks gained keys; a change left
// with none is not of the delta.
func renderDelta(ctx context.Context, changes object.Changes, gained map[string]bool) (string, []string, error) {
	if len(gained) == 0 {
		return renderChanges(ctx, changes)
	}
	var b strings.Builder
	var changed []string
	for _, c := range changes {
		patch, err := c.PatchContext(ctx)
		if err != nil {
			return "", nil, fmt.Errorf("gitfetch: patch: %w", err)
		}
		text := withoutHunks(patch.String(), gained)
		if text == "" {
			continue
		}
		b.WriteString(text)
		changed = append(changed, cmp.Or(c.To.Name, c.From.Name))
	}
	return b.String(), changed, nil
}

// treeChanges lists what changed between two commits, renames detected.
func treeChanges(ctx context.Context, from, to *object.Commit) (object.Changes, error) {
	fromTree, err := from.Tree()
	if err != nil {
		return nil, fmt.Errorf("gitfetch: tree of %s: %w", from.Hash, err)
	}
	toTree, err := to.Tree()
	if err != nil {
		return nil, fmt.Errorf("gitfetch: tree of %s: %w", to.Hash, err)
	}
	changes, err := object.DiffTreeWithOptions(ctx, fromTree, toTree, object.DefaultDiffTreeOptions)
	if err != nil {
		return nil, fmt.Errorf("gitfetch: diff: %w", err)
	}
	return changes, nil
}

// renderChanges is the changes as a unified diff, with the paths it
// touches by their head-side names.
func renderChanges(ctx context.Context, changes object.Changes) (string, []string, error) {
	patch, err := changes.PatchContext(ctx)
	if err != nil {
		return "", nil, fmt.Errorf("gitfetch: patch: %w", err)
	}
	var changed []string
	for _, c := range changes {
		name := cmp.Or(c.To.Name, c.From.Name)
		changed = append(changed, name)
	}
	return patch.String(), changed, nil
}

func refSpecs(f Fetch) []config.RefSpec {
	specs := []config.RefSpec{config.RefSpec(f.Head + ":" + headRef)}
	if f.Base != "" {
		specs = append(specs, config.RefSpec(f.Base+":"+baseRef))
	}
	return specs
}

// PatchID hashes a unified diff with everything positional stripped: hunk
// headers (line numbers move on a rebase), index lines (blob ids move with
// them) and trailing whitespace. What remains is the file names and the
// added and removed lines, which is what `git patch-id --stable` keys on.
func PatchID(diff string) string {
	h := sha256.New()
	for line := range strings.SplitSeq(diff, "\n") {
		if strings.HasPrefix(line, "@@") || strings.HasPrefix(line, "index ") {
			continue
		}
		h.Write([]byte(strings.TrimRight(line, " \t\r")))
		h.Write([]byte{'\n'})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// IsSHA reports whether s is a full lowercase SHA-1 commit id.
func IsSHA(s string) bool {
	if len(s) != 40 {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
