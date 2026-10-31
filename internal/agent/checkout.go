package agent

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/go-git/go-git/v6/plumbing/filemode"
	"github.com/go-git/go-git/v6/plumbing/object"
	"github.com/go-git/go-git/v6/plumbing/storer"
)

// MaxCheckoutBytes bounds a checkout's total size on the pod's scratch
// volume, beside the fetched pack.
const MaxCheckoutBytes = 256 << 20

// CheckoutStats is what Checkout wrote and what it left out.
type CheckoutStats struct {
	Files int
	Bytes int64
	// Skipped counts ignored paths, symlinks and files over the size cap.
	Skipped int
	// Truncated is set when the total cap stopped the checkout early.
	Truncated bool
}

// Checkout writes the tree's files under dir, an empty directory, for the
// commands that the run tool executes. It skips ignored paths, files over the
// read_file size cap, and symlinks, which can point out of dir. It stops
// before the total passes maxBytes. It writes every file through an os.Root
// on dir, which refuses a path that leads out of it. It writes every file as
// 0644, whatever mode git recorded: the commands read the checkout, and
// nothing in it is meant to run.
func (t *Tree) Checkout(ctx context.Context, dir string, maxBytes int64) (CheckoutStats, error) {
	var stats CheckoutStats
	dst, err := os.OpenRoot(dir)
	if err != nil {
		return stats, fmt.Errorf("agent: checkout: %w", err)
	}
	defer func() { _ = dst.Close() }()
	err = t.root.Files().ForEach(func(f *object.File) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if f.Mode == filemode.Symlink || t.ignored(f.Name) || f.Size > MaxBlobBytes {
			stats.Skipped++
			return nil
		}
		if stats.Bytes+f.Size > maxBytes {
			stats.Truncated = true
			return storer.ErrStop
		}
		if err := writeFile(dst, filepath.FromSlash(f.Name), f); err != nil {
			return fmt.Errorf("%s: %w", f.Name, err)
		}
		stats.Files++
		stats.Bytes += f.Size
		return nil
	})
	if err != nil {
		return stats, fmt.Errorf("agent: checkout: %w", err)
	}
	return stats, nil
}

func writeFile(dst *os.Root, name string, f *object.File) error {
	if err := dst.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		return err
	}
	r, err := f.Reader()
	if err != nil {
		return err
	}
	defer func() { _ = r.Close() }()
	// O_EXCL: a path is written once, never through whatever is there.
	out, err := dst.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, r); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}
