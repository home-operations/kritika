package runner

import (
	"log/slog"
	"reflect"
	"testing"
	"time"

	"github.com/go-git/go-billy/v6/memfs"
	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing/object"
	"github.com/go-git/go-git/v6/storage/memory"

	"github.com/home-operations/kritika/internal/gittest"
	"github.com/home-operations/kritika/internal/indexer"
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
