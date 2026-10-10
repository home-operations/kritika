package configfile

import (
	"fmt"
	"maps"
	"slices"
)

// DefaultSkillPaths are the directories a repository's skills are looked
// for in unless a scope names others: the ones agents read theirs from.
var DefaultSkillPaths = []string{".agents/skills", ".claude/skills"}

// SkillsSpec sets a repository's skills at one scope. Paths, the
// directories whose folders each hold a skill, replace the broader scope's
// when written, and written empty look nowhere. Scope adds to the broader
// scope's, a skill named in both taking the narrower one's.
type SkillsSpec struct {
	Paths *[]string             `yaml:"paths,omitempty"`
	Scope map[string]SkillScope `yaml:"scope,omitempty"`
}

// SkillScope narrows when a skill is offered to a review: with Paths only
// when a changed path matches one of them, and with When only to a pull
// request one of those conditions holds for. Load, when set, says whether
// a review the skill applies to is given its instructions in the system
// prompt, rather than its name to read with load_skill; unset, Loads
// decides from Paths and When.
type SkillScope struct {
	Paths []string `yaml:"paths,omitempty" json:"paths,omitempty"`
	When  []When   `yaml:"when,omitempty" json:"when,omitempty"`
	Load  *bool    `yaml:"load,omitempty" json:"load,omitempty"`
}

// Loads reports whether a review the skill applies to is given it whole:
// as Load says when it is set, and otherwise when Paths or When already
// decide which pull requests the skill applies to.
func (s SkillScope) Loads() bool {
	if s.Load != nil {
		return *s.Load
	}
	return len(s.Paths) > 0 || len(s.When) > 0
}

// Skills are a repository's skills as resolved: the directories they are
// looked for in, at the merge base, and the scope of those that have one,
// by name.
type Skills struct {
	Paths []string              `json:"paths"`
	Scope map[string]SkillScope `json:"scope"`
}

// WithSkills is s with one scope's skills laid over it.
func WithSkills(s Skills, o SkillsSpec) Skills { return s.overlay(o) }

func (s Skills) overlay(o SkillsSpec) Skills {
	if o.Paths != nil {
		s.Paths = slices.Clone(*o.Paths)
	}
	if len(o.Scope) > 0 {
		s.Scope = maps.Clone(s.Scope)
		if s.Scope == nil {
			s.Scope = map[string]SkillScope{}
		}
		maps.Copy(s.Scope, o.Scope)
	}
	return s
}

// CheckSkills rejects skill directories outside the repository and scopes
// with a glob that is not valid or a condition a rule's could not be. The
// error starts with the key, as skills.paths[i] or skills.scope.name.
func CheckSkills(paths []string, scope map[string]SkillScope) error {
	for i, p := range paths {
		if err := CheckRepoPath(p); err != nil {
			return fmt.Errorf("skills.paths[%d]: %w", i, err)
		}
	}
	for _, name := range slices.Sorted(maps.Keys(scope)) {
		sc := scope[name]
		for i, g := range sc.Paths {
			if !ValidGlob(g) {
				return fmt.Errorf("skills.scope.%s.paths[%d] %q is not a valid glob", name, i, g)
			}
		}
		if err := checkWhen(sc.When); err != nil {
			return fmt.Errorf("skills.scope.%s.%w", name, err)
		}
	}
	return nil
}
