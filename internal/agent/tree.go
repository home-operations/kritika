// Package agent is a bounded tool loop over a git commit's tree. A Stepper
// reads the tree through read_file, grep, and list_files. When a repository
// allows commands, the Stepper also executes allowlisted commands over a
// checkout of the tree through run. The Stepper must then call submit_review
// to end the run.
package agent

import (
	"errors"
	"fmt"
	"path"
	"strings"

	"github.com/go-git/go-git/v6/plumbing/object"

	"github.com/home-operations/kritika/internal/chunk"
)

// binarySniffBytes is how many bytes at the start of a file isBinary searches
// for a NUL byte to decide that the file is binary.
const binarySniffBytes = 8 << 10

// Tree is a read-only view over a commit's tree for the agent's tools. The
// tools read this tree's git objects directly, never a working tree, so
// there is no path or symlink escape.
type Tree struct {
	root   *object.Tree
	ignore []string
}

// NewTree wraps t. ignore is a set of doublestar globs that grep and
// list_files skip. read_file can still read an ignored path.
func NewTree(t *object.Tree, ignore []string) *Tree {
	return &Tree{root: t, ignore: ignore}
}

// ignored reports whether p matches one of the tree's ignore globs.
func (t *Tree) ignored(p string) bool { return chunk.Matches(t.ignore, p) }

// file returns the tree's file at p. It first cleans p and makes sure that p
// is valid. The returned path is the cleaned form of p.
func (t *Tree) file(p string) (*object.File, string, error) {
	cleaned, err := cleanPath(p)
	if err != nil {
		return nil, "", err
	}
	f, err := t.root.File(cleaned)
	if err != nil {
		return nil, "", fmt.Errorf("%s: %w", cleaned, err)
	}
	return f, cleaned, nil
}

// cleanPath rejects an empty, absolute, or tree-escaping path, and returns
// the cleaned, slash-separated form otherwise.
func cleanPath(p string) (string, error) {
	if p == "" {
		return "", errors.New("path is empty")
	}
	if path.IsAbs(p) {
		return "", fmt.Errorf("path %q is absolute", p)
	}
	cleaned := path.Clean(p)
	if cleaned == "." {
		return "", fmt.Errorf("path %q is empty", p)
	}
	if cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", fmt.Errorf("path %q escapes the tree", p)
	}
	return cleaned, nil
}

// isBinary reports whether content has a NUL byte in its first 8 KiB. git
// itself uses the same heuristic to skip a file in a text diff.
func isBinary(content string) bool {
	n := min(len(content), binarySniffBytes)
	return strings.IndexByte(content[:n], 0) >= 0
}

// splitLines splits content into lines. It strips a trailing empty line that
// a final newline leaves, so that a file's line count matches what an editor
// shows.
func splitLines(content string) []string {
	lines := strings.Split(content, "\n")
	if n := len(lines); n > 0 && lines[n-1] == "" {
		return lines[:n-1]
	}
	return lines
}
