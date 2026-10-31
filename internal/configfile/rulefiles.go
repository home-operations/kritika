package configfile

import (
	"fmt"
	"slices"
)

// DefaultRuleFilePaths are the directories a repository's rule files are
// looked for in unless a configuration names others: the ones coding agents
// read path-scoped conventions from.
var DefaultRuleFilePaths = []string{".agents/rules", ".claude/rules"}

// RuleFilesSpec sets where a repository's rule files are discovered. Paths,
// the directories whose markdown files are each a rule, replace the default
// ones when written, and written empty look nowhere.
type RuleFilesSpec struct {
	Paths *[]string `yaml:"paths,omitempty"`
}

// RuleFiles are where a repository's rule files are discovered, as
// resolved: directories of the merge base.
type RuleFiles struct {
	Paths []string `json:"paths"`
}

// WithRuleFiles is r with a repository's rule file directories laid over it.
func WithRuleFiles(r RuleFiles, o RuleFilesSpec) RuleFiles { return r.overlay(o) }

func (r RuleFiles) overlay(o RuleFilesSpec) RuleFiles {
	if o.Paths != nil {
		r.Paths = slices.Clone(*o.Paths)
	}
	return r
}

// CheckRuleFiles rejects rule file directories outside the repository. The
// error starts with the key, as ruleFiles.paths[i].
func CheckRuleFiles(paths []string) error {
	for i, p := range paths {
		if err := CheckRepoPath(p); err != nil {
			return fmt.Errorf("ruleFiles.paths[%d]: %w", i, err)
		}
	}
	return nil
}
