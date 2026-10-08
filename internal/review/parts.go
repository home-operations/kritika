package review

import (
	"cmp"
	"encoding/json"
	"fmt"
	"path"
	"slices"
	"strings"

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

// PartInput is one part of a split review as its prompt names it.
type PartInput struct {
	// Index counts the parts from 1 to Count.
	Index, Count int
	// Paths are the files the part reviews.
	Paths []string
}

// partLead tells a part of a split review what it reviews, given the
// parts and this part's index.
const partLead = "\nThis pull request is reviewed in %d parts, each by its own reviewer, and this is part %d: the files " +
	"marked (this part) above, whose diff below is all of the diff it shows. Report findings only on them, and on the files " +
	"of any earlier finding below that you are asked to verify; read any other file you need to judge yours, but leave its " +
	"findings to the part that reviews it.\n"

// mark is how the changed-files list marks a file the part reviews, "" for
// another file or a review that is not split.
func (p *PartInput) mark(file string) string {
	if p == nil || !slices.Contains(p.Paths, file) {
		return ""
	}
	return " (this part)"
}

// PartDiff is the sections of a unified diff for paths, in the diff's
// order.
func PartDiff(diff string, paths []string) string {
	var b strings.Builder
	for _, s := range splitFiles(diff) {
		if slices.Contains(paths, s.path) {
			b.WriteString(s.text)
		}
	}
	return b.String()
}

// CheckPart is Check for a part of a split review whose files are own: a
// finding on a file in others, which another part reviews, is refused with
// the part's own files named, so no two parts report one problem.
func CheckPart(own, others []string) func(json.RawMessage) error {
	return func(raw json.RawMessage) error {
		if err := Check(raw); err != nil {
			return err
		}
		var res Result
		// Check has decoded the same input.
		_ = json.Unmarshal(raw, &res)
		for _, f := range res.Findings {
			if p := strings.TrimSpace(f.Path); slices.Contains(others, p) {
				return fmt.Errorf("review: %s is another part's file; report findings only on this part's: %s", p, strings.Join(own, ", "))
			}
		}
		return nil
	}
}

// MergeParts joins what the parts of a split review submitted into one
// review: every part's findings, and a summary of the first headline, the
// takes in part order, praise up to its cap and every checked note, which
// Parse caps.
func MergeParts(parts []Result) Result {
	out := Result{Summary: Summary{Praise: []string{}}, Findings: []Finding{}}
	var takes []string
	for _, p := range parts {
		out.Summary.Headline = cmp.Or(out.Summary.Headline, p.Summary.Headline)
		if t := strings.TrimSpace(p.Summary.Take); t != "" {
			takes = append(takes, t)
		}
		for _, pr := range p.Summary.Praise {
			if len(out.Summary.Praise) < maxPraise {
				out.Summary.Praise = append(out.Summary.Praise, pr)
			}
		}
		out.Summary.Checked = append(out.Summary.Checked, p.Summary.Checked...)
		out.Findings = append(out.Findings, p.Findings...)
	}
	out.Summary.Take = strings.Join(takes, "\n\n")
	return out
}
