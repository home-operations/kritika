package gitfetch

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCloseReleasesTheRepository: go-git keeps a fetched pack open, so
// Close must close the repository before removing it, or the process keeps
// the deleted pack's descriptor, and its disk space, until it exits.
func TestCloseReleasesTheRepository(t *testing.T) {
	r := build(t)
	res, err := Run(t.Context(), Fetch{CloneURL: r.dir, Head: r.head, Base: r.base})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if err := res.Close(); err != nil {
		t.Fatal(err)
	}
	fds, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatal(err)
	}
	for _, fd := range fds {
		if target, err := os.Readlink(filepath.Join("/proc/self/fd", fd.Name())); err == nil && strings.HasPrefix(target, res.Dir) {
			t.Fatalf("descriptor %s is still open on %s", fd.Name(), target)
		}
	}
}
