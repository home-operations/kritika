package repoconfig

import (
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/review"
)

func TestParseSkill(t *testing.T) {
	t.Parallel()
	const dir = ".agents/skills/review-go"
	tests := []struct {
		name    string
		dir     string
		data    string
		want    Skill
		wantErr string
	}{
		{
			name: "the name from the frontmatter", data: "---\nname: go-style\ndescription: How Go is reviewed here.\n---\n# Go\n",
			want: Skill{Name: "go-style", Description: "How Go is reviewed here.", Dir: dir},
		},
		{
			name: "the name defaults to the folder", data: "---\ndescription: How Go is reviewed here.\n---\n",
			want: Skill{Name: "review-go", Description: "How Go is reviewed here.", Dir: dir},
		},
		{
			name: "a description over several lines is one", data: "---\ndescription: |\n  How Go\n    is  reviewed\n\n  here.\n---\n",
			want: Skill{Name: "review-go", Description: "How Go is reviewed here.", Dir: dir},
		},
		{
			name: "other keys are ignored",
			data: "---\nname: review-go\ndescription: Go.\nallowed-tools: Bash(rm:*)\nlicense: MIT\nmetadata: { version: 2 }\n---\nRun rm.\n",
			want: Skill{Name: "review-go", Description: "Go.", Dir: dir},
		},
		{
			name: "a byte order mark and blank lines before the frontmatter", data: "\ufeff\r\n\n \t---\ndescription: Go.\n---\n",
			want: Skill{Name: "review-go", Description: "Go.", Dir: dir},
		},
		{
			name: "a frontmatter closed at the end of the file", data: "---\ndescription: Go.\n---",
			want: Skill{Name: "review-go", Description: "Go.", Dir: dir},
		},
		{
			name: "a description of the most characters allowed", data: "---\ndescription: " + strings.Repeat("x", MaxSkillDescription) + "\n---\n",
			want: Skill{Name: "review-go", Description: strings.Repeat("x", MaxSkillDescription), Dir: dir},
		},
		{name: "no frontmatter", data: "# Reviewing Go\n\nname: review-go\n", wantErr: "no frontmatter"},
		{name: "an empty file", wantErr: "no frontmatter"},
		{name: "a frontmatter that is not closed", data: "---\nname: review-go\ndescription: Go.\n", wantErr: "frontmatter is not closed"},
		{name: "a frontmatter that is not YAML", data: "---\ndescription: [\n---\n", wantErr: "frontmatter: "},
		{name: "no description", data: "---\nname: review-go\n---\nGo.\n", wantErr: "description is required"},
		{name: "a blank description", data: "---\ndescription: \" \\n \"\n---\n", wantErr: "description is required"},
		{name: "a bad name", data: "---\nname: Review_Go\ndescription: Go.\n---\n", wantErr: `name "Review_Go" must be lowercase letters, digits and hyphens`},
		{name: "a name ending in a hyphen", data: "---\nname: review-\ndescription: Go.\n---\n", wantErr: `name "review-" must be`},
		{
			name: "a name over 64 characters", data: "---\nname: " + strings.Repeat("a", 65) + "\ndescription: Go.\n---\n",
			wantErr: "at most 64 characters",
		},
		{name: "a folder that is no name", dir: ".agents/skills/Review Go", data: "---\ndescription: Go.\n---\n", wantErr: `name "Review Go" must be`},
		{
			name: "an overlong description", data: "---\ndescription: " + strings.Repeat("x", MaxSkillDescription+1) + "\n---\n",
			wantErr: "description is over 1024 characters",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if tt.dir == "" {
				tt.dir = dir
			}
			got, err := ParseSkill(tt.dir, []byte(tt.data))
			if (err != nil) != (tt.wantErr != "") || (err != nil && !strings.Contains(err.Error(), tt.wantErr)) {
				t.Fatalf("err = %v, want %q", err, tt.wantErr)
			}
			if got != tt.want {
				t.Fatalf("skill = %+v, want %+v", got, tt.want)
			}
		})
	}
}

// TestParseSkillCountsCharacters: the description's bound is in
// characters, so one in a script of multibyte characters fits as one in
// ASCII does.
func TestParseSkillCountsCharacters(t *testing.T) {
	for n, wantErr := range map[int]bool{MaxSkillDescription: false, MaxSkillDescription + 1: true} {
		_, err := ParseSkill(".agents/skills/go", []byte("---\ndescription: "+strings.Repeat("語", n)+"\n---\n"))
		if (err != nil) != wantErr {
			t.Errorf("a description of %d characters: err = %v, want an error %v", n, err, wantErr)
		}
	}
}

func TestOfferedSkills(t *testing.T) {
	t.Parallel()
	found := []Skill{{Name: "review-go", Description: "Go."}, {Name: "db", Description: "Migrations."}, {Name: "renovate", Description: "Bumps."}}
	scope := map[string]configfile.SkillScope{
		"db":      {Paths: []string{"db/**", "**/*.sql"}},
		"unknown": {Paths: []string{"nothing/**"}},
	}
	// Each of the big skills takes 1000 bytes of the listing, so four fit.
	big := make([]Skill, 0, 6)
	for i := range 6 {
		name := fmt.Sprintf("big-%d", i)
		big = append(big, Skill{Name: name, Description: strings.Repeat("x", 1000-len(name))})
	}
	small := Skill{Name: "small", Description: strings.Repeat("y", MaxSkillListingBytes-4000-len("small"))}
	tests := []struct {
		name     string
		found    []Skill
		off      []string
		changed  []string
		want     []string
		wantLeft int
	}{
		{name: "none found", changed: []string{"main.go"}},
		{name: "a scope's paths match no changed path", found: found, changed: []string{"main.go"}, want: []string{"review-go", "renovate"}},
		{name: "a scope's paths match one changed path", found: found, changed: []string{"main.go", "db/0001.up"}, want: []string{"review-go", "db", "renovate"}},
		{name: "a later glob matches", found: found, changed: []string{"q/a.sql"}, want: []string{"review-go", "db", "renovate"}},
		{name: "nothing changed", found: found, want: []string{"review-go", "renovate"}},
		{name: "the skills that are off", found: found, off: []string{"renovate", "review-go"}, changed: []string{"db/x"}, want: []string{"db"}},
		{name: "off before paths", found: found, off: []string{"db"}, changed: []string{"db/x"}, want: []string{"review-go", "renovate"}},
		{name: "the listing's budget", found: big, want: []string{"big-0", "big-1", "big-2", "big-3"}, wantLeft: 2},
		{
			name: "a skill that fits after one that did not", found: append(slices.Clone(big), small),
			want: []string{"big-0", "big-1", "big-2", "big-3", "small"}, wantLeft: 2,
		},
		{
			name: "a skill that does not apply takes no room and is not counted", found: append([]Skill{{Name: "db", Description: strings.Repeat("z", 1000)}}, big...),
			changed: []string{"main.go"}, want: []string{"big-0", "big-1", "big-2", "big-3"}, wantLeft: 2,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			out, left := OfferedSkills(tt.found, scope, tt.off, tt.changed)
			var got []string
			size := 0
			for _, s := range out {
				got = append(got, s.Name)
				size += len(s.Name) + len(s.Description)
			}
			if !slices.Equal(got, tt.want) || left != tt.wantLeft {
				t.Fatalf("offered %q, %d left; want %q, %d left", got, left, tt.want, tt.wantLeft)
			}
			if size > MaxSkillListingBytes {
				t.Fatalf("the listing takes %d bytes, over %d", size, MaxSkillListingBytes)
			}
		})
	}
}

func TestSkillsOff(t *testing.T) {
	t.Parallel()
	scope := map[string]configfile.SkillScope{
		"paths-only": {Paths: []string{"db/**"}},
		"renovate":   {When: []configfile.When{{Expr: `pr.headRef.startsWith("renovate/")`}}},
		"bots": {When: []configfile.When{
			{Name: "deps", Expr: `pr.headRef.startsWith("deps/")`}, {Expr: `pr.headRef.startsWith("renovate/")`},
		}},
		"broken": {When: []configfile.When{{Expr: "pr.nope"}}},
		"always": {When: []configfile.When{{Expr: "true"}}},
	}
	tests := []struct {
		name    string
		scope   map[string]configfile.SkillScope
		headRef string
		want    []string
	}{
		{name: "no scope", headRef: "feat/x"},
		{name: "no condition holds", scope: scope, headRef: "feat/x", want: []string{"bots", "broken", "renovate"}},
		{name: "one of two holds", scope: scope, headRef: "deps/go", want: []string{"broken", "renovate"}},
		{name: "each holds", scope: scope, headRef: "renovate/go", want: []string{"broken"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := SkillsOff(tt.scope, map[string]any{"headRef": tt.headRef}); !slices.Equal(got, tt.want) {
				t.Fatalf("off = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestPromptSkills(t *testing.T) {
	t.Parallel()
	got := PromptSkills([]Skill{{Name: "a", Description: "A.", Dir: "x/a"}, {Name: "b", Description: "B.", Dir: "x/b"}})
	if want := []review.Skill{{Name: "a", Description: "A."}, {Name: "b", Description: "B."}}; !slices.Equal(got, want) {
		t.Fatalf("prompt skills = %+v", got)
	}
}

func TestParseSkills(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		doc  string
		want configfile.SkillsSpec
	}{
		{name: "unset"},
		{name: "paths", doc: "skills: { paths: [docs/skills, .claude/skills] }\n", want: configfile.SkillsSpec{Paths: &[]string{"docs/skills", ".claude/skills"}}},
		{name: "no paths", doc: "skills: { paths: [] }\n", want: configfile.SkillsSpec{Paths: &[]string{}}},
		{
			name: "scope",
			doc: "skills:\n  scope:\n    review-renovate-pr:\n      when:\n        - expr: pr.headRef.startsWith(\"renovate/\")\n" +
				"    migrations:\n      paths: [\"db/migrations/**\"]\n",
			want: configfile.SkillsSpec{Scope: map[string]configfile.SkillScope{
				"review-renovate-pr": {When: []configfile.When{{Expr: `pr.headRef.startsWith("renovate/")`}}},
				"migrations":         {Paths: []string{"db/migrations/**"}},
			}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f, err := Parse([]byte(tt.doc))
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(f.Skills, tt.want) {
				t.Fatalf("skills = %+v, want %+v", f.Skills, tt.want)
			}
		})
	}
}
