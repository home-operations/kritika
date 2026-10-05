package repoconfig

import (
	"bytes"
	"errors"
	"fmt"
	"path"
	"regexp"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/home-operations/kritika/internal/chunk"
	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/review"
)

// SkillFile is the file that makes a folder a skill: frontmatter with the
// skill's name and description, then its instructions.
const SkillFile = "SKILL.md"

// Bounds on a repository's skills: how many are read, how long a
// description may be, and how much of the system prompt their names and
// descriptions may take together.
const (
	MaxSkills            = 50
	MaxSkillDescription  = 1024
	MaxSkillListingBytes = 4 << 10
)

// skillNameRe is what a skill's name may be, as the format has it.
var skillNameRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,62}[a-z0-9])?$`)

// Skill is a skill a repository keeps: the folder it lives in, and the
// name and description its SkillFile gives.
type Skill struct {
	Name, Description, Dir string
}

// ParseSkill reads the frontmatter of the SkillFile in dir. The name
// defaults to the folder's; a file with no frontmatter, no description or a
// name that is not one is an error. Every other key, allowed-tools among
// them, is left unread: a skill guides a review and grants it nothing.
func ParseSkill(dir string, data []byte) (Skill, error) {
	rest, ok := bytes.CutPrefix(bytes.TrimLeft(data, "\ufeff \t\r\n"), []byte("---"))
	if !ok {
		return Skill{}, errors.New("no frontmatter")
	}
	front, _, ok := bytes.Cut(rest, []byte("\n---"))
	if !ok {
		return Skill{}, errors.New("frontmatter is not closed")
	}
	var meta struct {
		Name        string `yaml:"name"`
		Description string `yaml:"description"`
	}
	if err := yaml.Unmarshal(front, &meta); err != nil {
		return Skill{}, fmt.Errorf("frontmatter: %w", err)
	}
	s := Skill{Name: strings.TrimSpace(meta.Name), Description: strings.Join(strings.Fields(meta.Description), " "), Dir: dir}
	if s.Name == "" {
		s.Name = path.Base(dir)
	}
	switch {
	case !skillNameRe.MatchString(s.Name):
		return Skill{}, fmt.Errorf("name %q must be lowercase letters, digits and hyphens, at most 64 characters", s.Name)
	case s.Description == "":
		return Skill{}, errors.New("description is required")
	case len(s.Description) > MaxSkillDescription:
		return Skill{}, fmt.Errorf("description is over %d characters", MaxSkillDescription)
	}
	return s, nil
}

// OfferedSkills is the skills of found a review of a change of the changed
// paths is offered, in order: each that scope does not keep from it, by
// its paths or because the worker found none of its conditions to hold
// (off), while their names and descriptions fit MaxSkillListingBytes. left
// is how many applied and did not fit.
func OfferedSkills(found []Skill, scope map[string]configfile.SkillScope, off, changed []string) (out []Skill, left int) {
	room := MaxSkillListingBytes
	for _, s := range found {
		sc := scope[s.Name]
		if slices.Contains(off, s.Name) {
			continue
		}
		if len(sc.Paths) > 0 && !slices.ContainsFunc(changed, func(c string) bool { return chunk.Ignored(sc.Paths, c) }) {
			continue
		}
		if size := len(s.Name) + len(s.Description); size <= room {
			room -= size
			out = append(out, s)
		} else {
			left++
		}
	}
	return out, left
}

// SkillsOff names the skills of scope none of whose when conditions holds
// for the pull request with the filter variables vars, sorted: the ones a
// review of it is not offered, as RulesFor leaves a rule out.
func SkillsOff(scope map[string]configfile.SkillScope, vars map[string]any) []string {
	var off []string
	for name, sc := range scope {
		if len(sc.When) > 0 && len(RulesFor([]configfile.Rule{{ID: name, When: sc.When}}, vars)) == 0 {
			off = append(off, name)
		}
	}
	slices.Sort(off)
	return off
}

// PromptSkills is skills as the system prompt lists them.
func PromptSkills(skills []Skill) []review.Skill {
	out := make([]review.Skill, len(skills))
	for i, s := range skills {
		out[i] = review.Skill{Name: s.Name, Description: s.Description}
	}
	return out
}
