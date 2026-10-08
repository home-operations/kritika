package review

import (
	"cmp"
	"path"
	"slices"

	"github.com/home-operations/kritika/internal/chunk"
)

// PartBytes is the most diff one part of a split review holds: about
// thirty files of an ordinary change, few enough for one agent to read each
// with care. A review whose diff is larger is split into parts.
const PartBytes = 64 << 10

// SplitDiff cuts the files of a unified diff that no ignore glob matches
// into the parts a review is split into, each the paths of its files: nil
// when they fit in one part of size bytes, or maxParts is under two. Files
// go by directory, a directory's own before its subdirectories', and a part
// ends where a directory starts when the directory fits a part of its own;
// a larger directory is cut between its files, and a file larger than a
// part is a part alone. When the files need more than maxParts parts, the
// parts grow until maxParts hold them, so no file is left out.
func SplitDiff(diff string, ignore []string, size, maxParts int) [][]string {
	if diff == "" || maxParts < 2 {
		return nil
	}
	var files []fileSection
	total := 0
	for _, s := range splitFiles(diff) {
		if !chunk.Ignored(ignore, s.path) {
			files = append(files, s)
			total += len(s.text)
		}
	}
	if total <= size {
		return nil
	}
	slices.SortFunc(files, func(a, b fileSection) int {
		return cmp.Or(cmp.Compare(path.Dir(a.path), path.Dir(b.path)), cmp.Compare(a.path, b.path))
	})
	for {
		if parts := packParts(files, size); len(parts) <= maxParts {
			return parts
		}
		size += max(size/8, 1)
	}
}

// packParts packs files, sorted by directory, into parts of at most size
// bytes, starting a part at a directory that would fit one of its own
// rather than cutting it.
func packParts(files []fileSection, size int) [][]string {
	var parts [][]string
	var part []string
	used := 0
	next := func() {
		parts, part, used = append(parts, part), nil, 0
	}
	for i := 0; i < len(files); {
		dir, group, j := path.Dir(files[i].path), 0, i
		for ; j < len(files) && path.Dir(files[j].path) == dir; j++ {
			group += len(files[j].text)
		}
		if used > 0 && used+group > size && group <= size {
			next()
		}
		for _, f := range files[i:j] {
			if used > 0 && used+len(f.text) > size {
				next()
			}
			part, used = append(part, f.path), used+len(f.text)
		}
		i = j
	}
	if len(part) > 0 {
		next()
	}
	return parts
}
