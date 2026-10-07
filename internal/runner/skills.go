package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"slices"
	"strings"
	"sync"

	"github.com/go-git/go-git/v6/plumbing/object"

	"github.com/home-operations/kritika/internal/model"
	"github.com/home-operations/kritika/internal/repoconfig"
	"github.com/home-operations/kritika/internal/textcut"
)

// Notes the runner adds to the pack about the repository's skills.
const (
	noteSkillSkipped = "%s: skipped, %s"
	noteSkillsCapped = "more than %d skills found; the rest were left out"
	noteSkillsLeft   = "%d skill(s) left out, past the 4 KiB their names and descriptions are given"
)

// discoverSkills reads the skills the merge-base tree keeps under dirs:
// each folder directly under one that holds a SkillFile, in the order of
// dirs and by name within one. A skill that cannot be read is left out
// with a note, as is one whose name an earlier one has.
func discoverSkills(base *object.Tree, dirs []string) (skills []repoconfig.Skill, notes []string, err error) {
	read := treeReader(base)
	for _, dir := range dirs {
		dir = path.Clean(dir)
		t, terr := base.Tree(dir)
		if terr != nil {
			continue
		}
		for _, e := range t.Entries {
			if e.Mode.IsFile() {
				continue
			}
			folder := path.Join(dir, e.Name)
			file := path.Join(folder, repoconfig.SkillFile)
			data, rerr := read(file)
			switch {
			case errors.Is(rerr, fs.ErrNotExist):
				continue
			case rerr != nil:
				return nil, nil, rerr
			case len(data) > repoconfig.MaxFileBytes:
				notes = append(notes, repoconfig.TooLarge(file))
				continue
			}
			s, perr := repoconfig.ParseSkill(folder, data)
			switch {
			case perr != nil:
				notes = append(notes, fmt.Sprintf(noteSkillSkipped, file, perr))
			case slices.ContainsFunc(skills, func(o repoconfig.Skill) bool { return o.Name == s.Name }):
				notes = append(notes, fmt.Sprintf(noteSkillSkipped, file, "another skill is named "+s.Name))
			case len(skills) == repoconfig.MaxSkills:
				return skills, append(notes, fmt.Sprintf(noteSkillsCapped, repoconfig.MaxSkills)), nil
			default:
				skills = append(skills, s)
			}
		}
	}
	return skills, notes, nil
}

var skillSchema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"name": {"type": "string", "description": "The skill's name, as the system prompt lists it."},
		"file": {
			"type": "string",
			"description": "A file the skill refers to, by its path inside the skill's folder; omit it for the skill itself."
		}
	},
	"required": ["name"],
	"additionalProperties": false
}`)

// skillTool is load_skill: it returns a skill the review was offered, or a
// file of the skill's folder, from the merge base. The agent's file tools
// read the head, where the pull request under review may have rewritten
// the skill.
type skillTool struct {
	base     *object.Tree
	skills   []repoconfig.Skill
	maxBytes int

	mu     sync.Mutex
	opened []string
}

func (s *skillTool) Def() model.ToolDef {
	return model.ToolDef{
		Name:        "load_skill",
		Description: "Read one of the repository's skills listed in the system prompt, or a file the skill refers to.",
		InputSchema: skillSchema,
	}
}

func (s *skillTool) Run(_ context.Context, input json.RawMessage) (string, error) {
	var req struct {
		Name string `json:"name"`
		File string `json:"file"`
	}
	if len(input) > 0 {
		if err := json.Unmarshal(input, &req); err != nil {
			return "", fmt.Errorf("agent: load_skill: %w", err)
		}
	}
	i := slices.IndexFunc(s.skills, func(o repoconfig.Skill) bool { return o.Name == strings.TrimSpace(req.Name) })
	if i < 0 {
		return "", fmt.Errorf("agent: load_skill: no skill is named %q", req.Name)
	}
	skill := s.skills[i]
	file := path.Clean(strings.TrimSpace(req.File))
	if req.File == "" || file == "." {
		file = repoconfig.SkillFile
	}
	if path.IsAbs(file) || file == ".." || strings.HasPrefix(file, "../") {
		return "", fmt.Errorf("agent: load_skill: %q is outside the skill's folder", req.File)
	}
	data, err := treeReader(s.base)(path.Join(skill.Dir, file))
	if errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("agent: load_skill: %s has no file %q", skill.Name, file)
	}
	if err != nil {
		return "", fmt.Errorf("agent: load_skill: %w", err)
	}
	s.mu.Lock()
	if !slices.Contains(s.opened, skill.Name) {
		s.opened = append(s.opened, skill.Name)
	}
	s.mu.Unlock()
	if len(data) > s.maxBytes {
		// Within maxBytes with its note, or the loop's cut to the same limit
		// would replace the note.
		const cut = "\n[cut: the file is longer]"
		return textcut.Prefix(string(data), max(s.maxBytes-len(cut), 0)) + cut, nil
	}
	return string(data), nil
}

// names lists the skills the tool offers, and Opened the ones it has
// returned, each in the order first seen, never nil: the run's row takes
// no NULL for either.
func (s *skillTool) names() []string {
	out := make([]string, len(s.skills))
	for i, o := range s.skills {
		out[i] = o.Name
	}
	return out
}

func (s *skillTool) Opened() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string{}, s.opened...)
}
