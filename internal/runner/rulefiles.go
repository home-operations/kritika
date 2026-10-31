package runner

import (
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"path"
	"slices"
	"strings"

	"github.com/go-git/go-git/v6/plumbing/filemode"
	"github.com/go-git/go-git/v6/plumbing/object"

	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/repoconfig"
	"github.com/home-operations/kritika/internal/runspec"
)

// Notes the runner adds to the pack about the repository's rule files.
const (
	noteRuleFileSkipped = "%s: skipped, %s"
	noteRuleFilesCapped = "more than %d rule files found; the rest were left out"
)

// maxRuleFileDepth bounds how deep below a rule file directory files are
// looked for, against a tree that nests without end.
const maxRuleFileDepth = 8

// withRuleFiles is p with the rule files that its spec has discovered added
// after its rules, and files with their text, from the merge-base tree. A
// file that cannot be a rule is left out with a note. p and files are
// returned as they are when the spec discovers none.
func withRuleFiles(base *object.Tree, p runspec.Spec, files repoconfig.Files) (runspec.Spec, repoconfig.Files, []string, error) {
	rf := p.Prompt.RuleFiles
	if rf == nil || len(rf.Paths) == 0 {
		return p, files, nil, nil
	}
	rules, texts, notes, err := discoverRuleFiles(base, rf)
	if err != nil || len(rules) == 0 {
		return p, files, notes, err
	}
	prompt := *p.Prompt
	prompt.Rules = slices.Concat(prompt.Rules, rules)
	p.Prompt = &prompt
	out := maps.Clone(files)
	if out == nil {
		out = repoconfig.Files{}
	}
	maps.Copy(out, texts)
	return p, out, notes, nil
}

// discoverRuleFiles reads the markdown files that the merge-base tree keeps
// under rf's directories, at any depth, as file rules. Each one's id is
// its path below the directory, and its paths are its frontmatter's: a
// file without them applies to every change. A file that a configured rule
// names is that rule's, and one whose id a configured rule or an earlier
// file has is left out with a note, as is one too long to ever fit a
// review's rule files. At most MaxRuleFiles files are read, kept or not.
// The rules come scoped ones first, then the rest, each by path: so a long
// file that applies everywhere does not take the room of ones narrowed to
// the change.
func discoverRuleFiles(
	base *object.Tree, rf *runspec.RuleFiles,
) (rules []configfile.Rule, texts repoconfig.Files, notes []string, err error) {
	readFile := treeReader(base)
	texts = repoconfig.Files{}
	configured := make([]string, len(rf.Files))
	for i, f := range rf.Files {
		configured[i] = path.Clean(f)
	}
	seen := map[string]bool{}
	read := 0
	for _, dir := range rf.Paths {
		dir = path.Clean(dir)
		t := base
		if dir != "." {
			var terr error
			if t, terr = base.Tree(dir); terr != nil {
				continue
			}
		}
		// A file a configured rule names, or an earlier directory held, is
		// not read, so it takes none of the room to list the ones that are.
		wanted := func(rel string) bool {
			file := path.Join(dir, rel)
			return !slices.Contains(configured, file) && !seen[file]
		}
		var names []string
		markdownFiles(t, "", 0, &names, repoconfig.MaxRuleFiles+1-read, wanted)
		slices.Sort(names)
		for _, rel := range names {
			file := path.Join(dir, rel)
			if read == repoconfig.MaxRuleFiles {
				notes = append(notes, fmt.Sprintf(noteRuleFilesCapped, repoconfig.MaxRuleFiles))
				return orderRuleFiles(rules), texts, notes, nil
			}
			seen[file], read = true, read+1
			data, rerr := readFile(file)
			switch {
			case errors.Is(rerr, fs.ErrNotExist):
				continue
			case rerr != nil:
				return nil, nil, nil, rerr
			case len(data) > repoconfig.MaxFileBytes:
				notes = append(notes, repoconfig.TooLarge(file))
				continue
			}
			id := repoconfig.RuleFileID(rel)
			parsed := repoconfig.ParseRuleFile(data)
			var skip string
			switch {
			case parsed.Text == "":
				continue
			case id == "":
				skip = "its name makes no rule id"
			case slices.Contains(rf.IDs, id):
				skip = "a configured rule has the id " + id
			case slices.ContainsFunc(rules, func(r configfile.Rule) bool { return r.ID == id }):
				skip = "another rule file has the id " + id
			case parsed.Scoped && len(parsed.Paths) == 0:
				skip = "none of its paths is a valid glob"
			case len(id)+len(parsed.Text) > repoconfig.MaxRuleFileBytes:
				skip = fmt.Sprintf("it is longer than the %d KiB of rule files a review is given; split it", repoconfig.MaxRuleFileBytes>>10)
			}
			if skip != "" {
				notes = append(notes, fmt.Sprintf(noteRuleFileSkipped, file, skip))
				continue
			}
			rules = append(rules, configfile.Rule{ID: id, File: file, Paths: parsed.Paths})
			texts[file] = parsed.Text
		}
	}
	return orderRuleFiles(rules), texts, notes, nil
}

// orderRuleFiles puts the rules with paths first, keeping the order within
// each.
func orderRuleFiles(rules []configfile.Rule) []configfile.Rule {
	slices.SortStableFunc(rules, func(a, b configfile.Rule) int {
		switch {
		case len(a.Paths) > 0 == (len(b.Paths) > 0):
			return 0
		case len(a.Paths) > 0:
			return -1
		default:
			return 1
		}
	})
	return rules
}

// markdownFiles appends to names the paths below t, joined to prefix, of
// the files t holds with the rule file extension that wanted keeps, at
// most maxRuleFileDepth directories down and at most limit of them in all.
// A symlink is passed over: the tree holds its target's path, not that
// file's text.
func markdownFiles(t *object.Tree, prefix string, depth int, names *[]string, limit int, wanted func(string) bool) {
	for _, e := range t.Entries {
		if len(*names) >= limit {
			return
		}
		name := path.Join(prefix, e.Name)
		if e.Mode == filemode.Symlink {
			continue
		}
		if e.Mode.IsFile() {
			if strings.HasSuffix(e.Name, repoconfig.RuleFileExt) && wanted(name) {
				*names = append(*names, name)
			}
			continue
		}
		if depth == maxRuleFileDepth {
			continue
		}
		if sub, err := t.Tree(e.Name); err == nil {
			markdownFiles(sub, name, depth+1, names, limit, wanted)
		}
	}
}
