package runner

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-git/go-billy/v6/memfs"
	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing/object"
	"github.com/go-git/go-git/v6/storage/memory"

	"github.com/home-operations/kritika/internal/gitfetch"
	"github.com/home-operations/kritika/internal/gittest"
	"github.com/home-operations/kritika/internal/indexer"
	"github.com/home-operations/kritika/internal/runspec"
	"github.com/home-operations/kritika/internal/store"
)

func TestBuildIndexFallsBackOnTruncation(t *testing.T) {
	fs := memfs.New()
	r, err := git.Init(memory.NewStorage(), git.WithWorkTree(fs))
	if err != nil {
		t.Fatal(err)
	}
	gittest.Unsigned(t, r)
	wt, err := r.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	commit := func(content string) *object.Tree {
		t.Helper()
		for _, name := range []string{"a.yaml", "b.yaml", "c.yaml"} {
			f, err := fs.Create(name)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := f.Write([]byte(content)); err != nil {
				t.Fatal(err)
			}
			if err := f.Close(); err != nil {
				t.Fatal(err)
			}
			if _, err := wt.Add(name); err != nil {
				t.Fatal(err)
			}
		}
		h, err := wt.Commit(content, &git.CommitOptions{Author: &object.Signature{Name: "t", Email: "t@t", When: time.Now()}})
		if err != nil {
			t.Fatal(err)
		}
		c, err := r.CommitObject(h)
		if err != nil {
			t.Fatal(err)
		}
		tree, err := c.Tree()
		if err != nil {
			t.Fatal(err)
		}
		return tree
	}
	base, head := commit("key: old\n"), commit("key: new\n")
	for _, tc := range []struct {
		name                 string
		files, bytes, chunks int
	}{
		{"files", 1, indexer.DefaultOptions.MaxBytes, indexer.DefaultOptions.MaxChunks},
		{"bytes", indexer.DefaultOptions.MaxFiles, len("key: new\n"), indexer.DefaultOptions.MaxChunks},
		{"chunks", indexer.DefaultOptions.MaxFiles, indexer.DefaultOptions.MaxBytes, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opts := indexer.DefaultOptions
			opts.MaxFiles, opts.MaxBytes, opts.MaxChunks = tc.files, tc.bytes, tc.chunks
			got, changed, stats, mode, err := buildIndex(t.Context(), head, base, nil, opts, slog.New(slog.DiscardHandler))
			if err != nil || mode != store.IndexModeFull || changed != nil || !stats.Truncated {
				t.Fatalf("buildIndex = %v, %v, %+v, %s, %v", got, changed, stats, mode, err)
			}
			want, _, _, err := indexer.Build(t.Context(), head, nil, nil, opts)
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("full fallback = %+v, want %+v, %v", got, want, err)
			}
			if baseFor(mode, "base") != "" {
				t.Fatal("full fallback retained the base")
			}
		})
	}
	_, changed, stats, mode, err := buildIndex(t.Context(), head, base, nil, indexer.DefaultOptions, slog.New(slog.DiscardHandler))
	if err != nil || mode != store.IndexModeIncremental || len(changed) != 3 || stats.Truncated {
		t.Fatalf("unlimited build = %v, %+v, %s, %v", changed, stats, mode, err)
	}
}

// TestFetchForIndex: an incremental step whose base the remote does not
// serve falls back to a full build. Any other failure to fetch is the run's
// error, so the job is tried again rather than re-embedding the repository.
func TestFetchForIndex(t *testing.T) {
	head, base := strings.Repeat("a", 40), strings.Repeat("b", 40)
	for _, tc := range []struct {
		name     string
		base     string
		incr     error
		wantMode string
		wantErr  bool
		fetches  int
	}{
		{name: "incremental", base: base, wantMode: store.IndexModeIncremental, fetches: 1},
		{name: "no base", wantMode: store.IndexModeFull, fetches: 1},
		{name: "base unavailable", base: base, incr: fmt.Errorf("gitfetch: fetch: %w", gitfetch.ErrCommitUnavailable),
			wantMode: store.IndexModeFull, fetches: 2},
		{name: "fetch failed", base: base, incr: errors.New("gitfetch: fetch: connection reset"), wantErr: true, fetches: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var fetches []gitfetch.Fetch
			trees := func(_ context.Context, f gitfetch.Fetch) (*gitfetch.Result, error) {
				fetches = append(fetches, f)
				if f.Base != "" && tc.incr != nil {
					return nil, tc.incr
				}
				return &gitfetch.Result{}, nil
			}
			_, mode, err := fetchForIndex(t.Context(), runspec.Spec{Head: head, Base: tc.base}, "", trees, slog.New(slog.DiscardHandler))
			if (err != nil) != tc.wantErr || mode != tc.wantMode || len(fetches) != tc.fetches {
				t.Fatalf("fetchForIndex = %q, %v after %d fetches, want %q, error %v after %d", mode, err, len(fetches),
					tc.wantMode, tc.wantErr, tc.fetches)
			}
		})
	}
}

// TestFetchForIndexFromARemote: through the real fetch, a base that the
// remote does not have falls back to a full build of the head. A remote that
// fails for any other reason, as a forge that answers 503 does, fails the run
// after the one fetch, rather than fetching the repository again to embed it
// all anew.
func TestFetchForIndexFromARemote(t *testing.T) {
	dir := t.TempDir()
	r, err := git.PlainInit(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	gittest.Unsigned(t, r)
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	wt, err := r.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := wt.Add("main.go"); err != nil {
		t.Fatal(err)
	}
	h, err := wt.Commit("main", &git.CommitOptions{Author: &object.Signature{Name: "t", Email: "t@t", When: time.Now()}})
	if err != nil {
		t.Fatal(err)
	}
	head, missing := h.String(), strings.Repeat("0", 40)

	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(srv.Close)
	unavailable := srv.URL + "/acme/webapp.git"
	// What one fetch asks of the failing remote.
	if _, err := gitfetch.Trees(t.Context(), gitfetch.Fetch{CloneURL: unavailable, Head: head}); err == nil {
		t.Fatal("a fetch from a remote that answers 503 succeeded")
	}
	perFetch := requests.Swap(0)
	if perFetch == 0 {
		t.Fatal("a fetch asked nothing of the remote")
	}

	for _, tc := range []struct {
		name     string
		cloneURL string
		wantMode string
		wantErr  bool
		// wantRequests is what the failing remote is asked, 0 for the local one.
		wantRequests int32
	}{
		{name: "base the remote does not have", cloneURL: dir, wantMode: store.IndexModeFull},
		{name: "remote unavailable", cloneURL: unavailable, wantErr: true, wantRequests: perFetch},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requests.Store(0)
			res, mode, err := fetchForIndex(t.Context(), runspec.Spec{CloneURL: tc.cloneURL, Head: head, Base: missing}, "",
				gitfetch.Trees, slog.New(slog.DiscardHandler))
			if res != nil {
				t.Cleanup(func() { _ = res.Close() })
			}
			if (err != nil) != tc.wantErr || mode != tc.wantMode || requests.Load() != tc.wantRequests {
				t.Fatalf("fetchForIndex = %q, %v after %d requests to the remote, want %q, error %v after %d",
					mode, err, requests.Load(), tc.wantMode, tc.wantErr, tc.wantRequests)
			}
		})
	}
}
