package runner

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/repoconfig"
	"github.com/home-operations/kritika/internal/review"
)

// skillDoc is a SKILL.md with the frontmatter keys given, one per line.
func skillDoc(front, body string) string { return "---\n" + front + "\n---\n" + body }

func TestDiscoverSkills(t *testing.T) {
	files := map[string]string{
		"main.go":                           "package main\n",
		".agents/skills/README.md":          skillDoc("description: Not a skill.", ""),
		".agents/skills/review-go/SKILL.md": skillDoc("description: How Go is reviewed here.", "# Go\n"),
		".agents/skills/review-go/notes.md": "Notes.\n",
		".agents/skills/db/SKILL.md":        skillDoc("name: migrations\ndescription: What a migration must keep.", ""),
		".agents/skills/empty/notes.md":     "No SKILL.md here.\n",
		".agents/skills/deep/er/SKILL.md":   skillDoc("description: Too deep.", ""),
		".claude/skills/renovate/SKILL.md":  skillDoc("name: renovate\ndescription: Dependency bumps.\nallowed-tools: Bash", ""),
		".claude/skills/broken/SKILL.md":    "# No frontmatter\n",
		".claude/skills/dup/SKILL.md":       skillDoc("name: review-go\ndescription: Another one.", ""),
		".claude/skills/huge/SKILL.md":      skillDoc("description: Huge.", strings.Repeat("x", repoconfig.MaxFileBytes)),
		"docs/skills/release/SKILL.md":      skillDoc("description: Cutting a release.", ""),
	}
	goSkill := repoconfig.Skill{Name: "review-go", Description: "How Go is reviewed here.", Dir: ".agents/skills/review-go", Text: "# Go"}
	migrations := repoconfig.Skill{Name: "migrations", Description: "What a migration must keep.", Dir: ".agents/skills/db"}
	renovate := repoconfig.Skill{Name: "renovate", Description: "Dependency bumps.", Dir: ".claude/skills/renovate"}
	const (
		brokenNote = ".claude/skills/broken/SKILL.md: skipped, no frontmatter"
		dupNote    = ".claude/skills/dup/SKILL.md: skipped, another skill is named review-go"
		hugeNote   = ".claude/skills/huge/SKILL.md: skipped, it exceeds the 262144 byte per-file limit"
	)

	many := func(n int) map[string]string {
		out := map[string]string{}
		for i := range n {
			out[fmt.Sprintf("s/skill-%02d/SKILL.md", i)] = skillDoc("description: One of many.", "")
		}
		return out
	}
	capped := func(n int) []repoconfig.Skill {
		out := make([]repoconfig.Skill, 0, n)
		for i := range n {
			name := fmt.Sprintf("skill-%02d", i)
			out = append(out, repoconfig.Skill{Name: name, Description: "One of many.", Dir: "s/" + name})
		}
		return out
	}
	overCap := many(repoconfig.MaxSkills + 2)
	maps.Copy(overCap, map[string]string{"z/late/SKILL.md": skillDoc("description: Past the cap.", "")})

	tests := []struct {
		name      string
		files     map[string]string
		dirs      []string
		want      []repoconfig.Skill
		wantNotes []string
	}{
		{
			name: "both default directories, in order", files: files, dirs: configfile.DefaultSkillPaths,
			want: []repoconfig.Skill{migrations, goSkill, renovate}, wantNotes: []string{brokenNote, dupNote, hugeNote},
		},
		{
			name: "the directories in the order given", files: files, dirs: []string{".claude/skills", ".agents/skills"},
			want: []repoconfig.Skill{
				{Name: "review-go", Description: "Another one.", Dir: ".claude/skills/dup"}, renovate, migrations,
			},
			wantNotes: []string{brokenNote, hugeNote, ".agents/skills/review-go/SKILL.md: skipped, another skill is named review-go"},
		},
		{name: "a missing directory", files: files, dirs: []string{"nope/skills", ".agents/skills"}, want: []repoconfig.Skill{migrations, goSkill}},
		{
			name: "a directory that is not clean", files: files, dirs: []string{"./docs//skills/"},
			want: []repoconfig.Skill{{Name: "release", Description: "Cutting a release.", Dir: "docs/skills/release"}},
		},
		{name: "a file named as a directory", files: files, dirs: []string{"main.go"}},
		{name: "a skill's own folder is not a directory of skills", files: files, dirs: []string{".agents/skills/review-go"}},
		{name: "no directories", files: files},
		{name: "as many as are read", files: many(repoconfig.MaxSkills), dirs: []string{"s"}, want: capped(repoconfig.MaxSkills)},
		{
			name: "past the cap", files: overCap, dirs: []string{"s", "z"}, want: capped(repoconfig.MaxSkills),
			wantNotes: []string{"more than 50 skills found; the rest were left out"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, notes, err := discoverSkills(tree(t, tt.files), tt.dirs)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(got, tt.want) {
				t.Fatalf("skills = %+v\nwant     %+v", got, tt.want)
			}
			if !slices.Equal(notes, tt.wantNotes) {
				t.Fatalf("notes = %q, want %q", notes, tt.wantNotes)
			}
		})
	}
}

// TestSkillTool runs its calls in order on one tool: the skills it has
// opened are each listed once, in the order first read, and a call that
// failed opened none.
func TestSkillTool(t *testing.T) {
	goBody := skillDoc("description: How Go is reviewed here.", "# Go\n\nSee checklist.md.\n")
	base := tree(t, map[string]string{
		".agents/skills/review-go/SKILL.md":     goBody,
		".agents/skills/review-go/checklist.md": "- wrap errors\n",
		".agents/skills/review-go/refs/long.md": strings.Repeat("é", 100),
		".agents/skills/db/SKILL.md":            skillDoc("name: migrations\ndescription: Migrations.", "Reversible.\n"),
		".agents/skills/secret/SKILL.md":        skillDoc("description: Not offered.", "Hidden.\n"),
		"main.go":                               "package main\n",
	})
	tool := &skillTool{base: base, maxBytes: 64, skills: []repoconfig.Skill{
		{Name: "review-go", Description: "How Go is reviewed here.", Dir: ".agents/skills/review-go"},
		{Name: "migrations", Description: "Migrations.", Dir: ".agents/skills/db"},
	}, opened: []string{"migrations"}}
	if def := tool.Def(); def.Name != "load_skill" || !json.Valid(def.InputSchema) {
		t.Fatalf("def = %+v", def)
	}
	if got := tool.names(); !slices.Equal(got, []string{"review-go", "migrations"}) {
		t.Fatalf("names = %q", got)
	}
	if got := tool.Opened(); !slices.Equal(got, []string{"migrations"}) {
		t.Fatalf("opened before any call = %#v, want the skill given whole", got)
	}
	// Not nil either: the run's row takes no NULL for the list.
	if got := (&skillTool{}).Opened(); got == nil || len(got) != 0 {
		t.Fatalf("opened with none given = %#v, want an empty list", got)
	}
	tests := []struct {
		name    string
		input   string
		want    string
		wantErr string
	}{
		{name: "an unknown skill", input: `{"name":"nope"}`, wantErr: `agent: load_skill: no skill is named "nope"`},
		{name: "a skill the review was not offered", input: `{"name":"secret"}`, wantErr: `no skill is named "secret"`},
		{name: "a skill by its folder, not its name", input: `{"name":"db"}`, wantErr: `no skill is named "db"`},
		{name: "no name", input: `{}`, wantErr: `no skill is named ""`},
		{name: "no input", wantErr: `no skill is named ""`},
		{name: "input that is not JSON", input: `{"name":`, wantErr: "agent: load_skill: "},
		{name: "a path above the folder", input: `{"name":"migrations","file":"../secret/SKILL.md"}`, wantErr: `"../secret/SKILL.md" is outside the skill's folder`},
		{name: "the parent folder", input: `{"name":"migrations","file":".."}`, wantErr: `".." is outside the skill's folder`},
		{name: "a path that climbs out midway", input: `{"name":"migrations","file":"refs/../../../main.go"}`, wantErr: "is outside the skill's folder"},
		{name: "an absolute path", input: `{"name":"migrations","file":"/main.go"}`, wantErr: `"/main.go" is outside the skill's folder`},
		{name: "a missing file", input: `{"name":"migrations","file":"gone.md"}`, wantErr: `agent: load_skill: migrations has no file "gone.md"`},
		{name: "a folder named as the file", input: `{"name":"review-go","file":"refs"}`, wantErr: `review-go has no file "refs"`},
		{name: "the body", input: `{"name":"migrations"}`, want: skillDoc("name: migrations\ndescription: Migrations.", "Reversible.\n")},
		{name: "a file inside the folder", input: `{"name":"review-go","file":"checklist.md"}`, want: "- wrap errors\n"},
		{name: "a path cleaned to inside the folder", input: `{"name":" review-go ","file":"./refs/../checklist.md"}`, want: "- wrap errors\n"},
		{name: "the folder itself is the body, cut to the limit", input: `{"name":"review-go","file":"."}`, want: goBody[:38] + "\n[cut: the file is longer]"},
		{
			name: "a cut that keeps whole characters", input: `{"name":"review-go","file":"refs/long.md"}`,
			want: strings.Repeat("é", 19) + "\n[cut: the file is longer]",
		},
		{name: "the body again", input: `{"name":"migrations","file":"SKILL.md"}`, want: skillDoc("name: migrations\ndescription: Migrations.", "Reversible.\n")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before := tool.Opened()
			got, err := tool.Run(t.Context(), json.RawMessage(tt.input))
			if (err != nil) != (tt.wantErr != "") || (err != nil && !strings.Contains(err.Error(), tt.wantErr)) {
				t.Fatalf("err = %v, want %q", err, tt.wantErr)
			}
			if got != tt.want {
				t.Fatalf("output = %q, want %q", got, tt.want)
			}
			if err != nil && !slices.Equal(tool.Opened(), before) {
				t.Fatalf("a failed call opened a skill: %q", tool.Opened())
			}
		})
	}
	if got := tool.Opened(); !slices.Equal(got, []string{"migrations", "review-go"}) {
		t.Fatalf("opened = %q, want the one given whole, then each once in the order first read", got)
	}
}

// TestPromptInputsSkills: a review is offered the skills found only when
// its spec has skills, less the ones its scope keeps from this change, and
// given whole the ones its scope loads.
func TestPromptInputsSkills(t *testing.T) {
	found := []repoconfig.Skill{
		{Name: "review-go", Description: "Go.", Dir: ".agents/skills/review-go", Text: "Wrap errors."},
		{Name: "migrations", Description: "Migrations.", Dir: ".agents/skills/db", Text: "Reversible."},
		{Name: "renovate", Description: "Bumps.", Dir: ".claude/skills/renovate", Text: "Read the release notes."},
	}
	big := make([]repoconfig.Skill, 0, 6)
	for i := range 6 {
		big = append(big, repoconfig.Skill{Name: fmt.Sprintf("big-%d", i), Description: strings.Repeat("x", 1000)})
	}
	huge := repoconfig.Skill{Name: "huge", Description: "Big.", Text: strings.Repeat("h", repoconfig.MaxSkillLoadedBytes+1)}
	tests := []struct {
		name       string
		skills     *Skills
		found      []repoconfig.Skill
		want       []string
		wantLoaded []string
		wantNotes  []string
	}{
		{name: "a spec without skills offers none", found: found},
		{name: "every skill found", skills: &Skills{Paths: configfile.DefaultSkillPaths}, found: found, want: []string{"review-go", "migrations", "renovate"}},
		{name: "none found", skills: &Skills{Paths: configfile.DefaultSkillPaths}},
		{
			name: "the scope's paths and the skills that are off",
			skills: &Skills{
				Paths: configfile.DefaultSkillPaths, Off: []string{"renovate"},
				Scope: map[string]configfile.SkillScope{"migrations": {Paths: []string{"db/**"}}, "review-go": {Paths: []string{"**/*.go"}}},
			},
			found: found, want: []string{"review-go"},
		},
		{
			name: "the skills past the listing's budget are noted", skills: &Skills{Paths: configfile.DefaultSkillPaths}, found: big,
			want:      []string{"big-0", "big-1", "big-2", "big-3"},
			wantNotes: []string{"2 skill(s) left out, past the 4 KiB their names and descriptions are given"},
		},
		{
			name: "the skills the scope loads are given whole",
			skills: &Skills{
				Paths: configfile.DefaultSkillPaths, Off: []string{"migrations"},
				Scope: map[string]configfile.SkillScope{"renovate": {Load: true}, "migrations": {Load: true}},
			},
			found: found, want: []string{"review-go"}, wantLoaded: []string{"renovate"},
		},
		{
			name:   "a skill past the loaded skills' budget is listed and noted",
			skills: &Skills{Paths: configfile.DefaultSkillPaths, Scope: map[string]configfile.SkillScope{"huge": {Load: true}}},
			found:  append(slices.Clone(found[:1]), huge), want: []string{"review-go", "huge"},
			wantNotes: []string{"1 skill(s) offered by name rather than loaded, past the 32 KiB loaded skills are given"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := agentPromptSpec()
			s.Prompt.Skills = tt.skills
			in := newPromptInputs(s, repoconfig.Files{}, tt.found, []string{"main.go"})
			var got, loaded []string
			for _, sk := range in.skills {
				got = append(got, sk.Name)
			}
			for _, sk := range in.loaded {
				loaded = append(loaded, sk.Name)
			}
			if !slices.Equal(got, tt.want) || !slices.Equal(loaded, tt.wantLoaded) || !slices.Equal(in.notes, tt.wantNotes) {
				t.Fatalf("skills = %q, loaded = %q, notes = %q; want %q, %q, %q", got, loaded, in.notes, tt.want, tt.wantLoaded, tt.wantNotes)
			}
			system := newAgentPrompt(s, in, packView{Diff: agentDiff, Changed: []string{"main.go"}}, nil, false, false).system
			if want := review.SystemPrompt(nil, repoconfig.PromptSkills(in.skills, in.loaded), nil, nil, false, false, false); system != want {
				t.Fatalf("system prompt:\n%s", system)
			}
			if strings.Contains(system, "## Skills") != (len(tt.want)+len(tt.wantLoaded) > 0) {
				t.Fatalf("system prompt lists skills = %v:\n%s", len(tt.want) == 0, system)
			}
			for _, name := range tt.wantLoaded {
				if !strings.Contains(system, "\n\n### "+name+"\n\n") {
					t.Fatalf("system prompt does not give %s whole:\n%s", name, system)
				}
			}
		})
	}
}
