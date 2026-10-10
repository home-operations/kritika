package configfile

import (
	"reflect"
	"strings"
	"testing"
)

// TestSkillsResolve: a narrower scope's paths replace the broader one's,
// written empty they look nowhere, and the scopes add up by name with the
// narrower one's taking a name both give.
func TestSkillsResolve(t *testing.T) {
	t.Setenv("TEST_PRIVATE_KEY", "tok")
	t.Setenv("TEST_WEBHOOK_SECRET", "whsec")
	renovate := SkillScope{When: []When{{Expr: `pr.headRef.startsWith("renovate/")`}}}
	tests := []struct {
		name string
		yaml string
		repo string
		want Skills
	}{
		{name: "the default paths", yaml: minimal, repo: "acme/x", want: Skills{Paths: []string{".agents/skills", ".claude/skills"}}},
		{
			name: "the root's paths replace the default",
			yaml: "skills: { paths: [docs/skills] }\n" + minimal, repo: "acme/x",
			want: Skills{Paths: []string{"docs/skills"}},
		},
		{
			name: "a repository's paths replace the root's",
			yaml: "skills: { paths: [docs/skills] }\n" + acme("  acme/x: { skills: { paths: [a, b] } }\n"), repo: "acme/x",
			want: Skills{Paths: []string{"a", "b"}},
		},
		{
			name: "another repository keeps the root's",
			yaml: "skills: { paths: [docs/skills] }\n" + acme("  acme/x: { skills: { paths: [a, b] } }\n"), repo: "acme/y",
			want: Skills{Paths: []string{"docs/skills"}},
		},
		{
			name: "empty paths turn skills off",
			yaml: acme("  acme/x: { skills: { paths: [] } }\n"), repo: "acme/x",
			want: Skills{Paths: []string{}},
		},
		{
			name: "a scope alone keeps the paths",
			yaml: acme("  acme/*: { skills: { scope: { db: { paths: ['db/**'] } } } }\n"), repo: "acme/x",
			want: Skills{Paths: []string{".agents/skills", ".claude/skills"}, Scope: map[string]SkillScope{"db": {Paths: []string{"db/**"}}}},
		},
		{
			name: "scopes add up, the narrower winning a name",
			yaml: "skills:\n  scope:\n    renovate: { when: [{ expr: 'pr.headRef.startsWith(\"renovate/\")' }] }\n    db: { paths: ['db/**'] }\n" +
				acme("  acme/*: { skills: { scope: { go: { paths: ['**/*.go'] } } } }\n  acme/x: { skills: { scope: { db: { paths: ['migrations/**'] } } } }\n"),
			repo: "acme/x",
			want: Skills{Paths: []string{".agents/skills", ".claude/skills"}, Scope: map[string]SkillScope{
				"renovate": renovate, "go": {Paths: []string{"**/*.go"}}, "db": {Paths: []string{"migrations/**"}},
			}},
		},
		{
			name: "the broader scope is left as written",
			yaml: "skills: { scope: { db: { paths: ['db/**'] } } }\n" + acme("  acme/x: { skills: { scope: { db: { paths: ['migrations/**'] } } } }\n"),
			repo: "acme/y",
			want: Skills{Paths: []string{".agents/skills", ".claude/skills"}, Scope: map[string]SkillScope{"db": {Paths: []string{"db/**"}}}},
		},
		{
			name: "a written load is kept, false apart from unset",
			yaml: "skills: { scope: { db: { paths: ['db/**'], load: false }, docs: { load: true } } }\n" + minimal, repo: "acme/x",
			want: Skills{Paths: []string{".agents/skills", ".claude/skills"}, Scope: map[string]SkillScope{
				"db": {Paths: []string{"db/**"}, Load: new(false)}, "docs": {Load: new(true)},
			}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := mustLoad(t, tt.yaml)
			if got := f.Settings(&f.Accounts[0], tt.repo).Skills; !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("skills = %+v, want %+v", got, tt.want)
			}
			if !reflect.DeepEqual(DefaultSkillPaths, []string{".agents/skills", ".claude/skills"}) {
				t.Fatalf("resolving changed the default paths: %q", DefaultSkillPaths)
			}
		})
	}
}

func TestSkillScopeLoads(t *testing.T) {
	t.Parallel()
	when := []When{{Expr: `pr.headRef.startsWith("renovate/")`}}
	tests := []struct {
		name  string
		scope SkillScope
		want  bool
	}{
		{"no scope", SkillScope{}, false},
		{"paths", SkillScope{Paths: []string{"db/**"}}, true},
		{"conditions", SkillScope{When: when}, true},
		{"paths and conditions", SkillScope{Paths: []string{"db/**"}, When: when}, true},
		{"load: false over paths", SkillScope{Paths: []string{"db/**"}, Load: new(false)}, false},
		{"load: false over conditions", SkillScope{When: when, Load: new(false)}, false},
		{"load: true with neither", SkillScope{Load: new(true)}, true},
		{"load: false with neither", SkillScope{Load: new(false)}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := tt.scope.Loads(); got != tt.want {
				t.Fatalf("Loads() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSkillsRejects(t *testing.T) {
	t.Setenv("TEST_PRIVATE_KEY", "tok")
	t.Setenv("TEST_WEBHOOK_SECRET", "whsec")
	tests := []struct {
		name string
		yaml string
		want string
	}{
		{"absolute path", "skills: { paths: [/x] }\n" + minimal, `configfile: skills.paths[0]: path "/x" must be relative`},
		{"blank path", "skills: { paths: [a, ' '] }\n" + minimal, "configfile: skills.paths[1]: path must not be empty"},
		{"path outside the repository", acme("  acme/x: { skills: { paths: [../x] } }\n"),
			`configfile: repositories.acme/x.skills.paths[0]: path "../x" escapes the repository`},
		{"scope with a bad glob", "skills: { scope: { db: { paths: ['db/**', '['] } } }\n" + minimal,
			`configfile: skills.scope.db.paths[1] "[" is not a valid glob`},
		{"scope with a bad glob on an entry", acme("  acme/*: { skills: { scope: { db: { paths: ['['] } } } }\n"),
			`configfile: repositories.acme/*.skills.scope.db.paths[0] "[" is not a valid glob`},
		{"scope condition syntax error", acme("  acme/x: { skills: { scope: { db: { when: [{ expr: 'pr.draft &&' }] } } } }\n"),
			"configfile: repositories.acme/x.skills.scope.db.when[0]"},
		{"scope condition on pr.lines", "skills: { scope: { db: { when: [{ expr: pr.lines > 10 }] } } }\n" + minimal,
			"configfile: skills.scope.db.when[0]: pr.lines is known to a trigger condition alone"},
		{"scope condition with no expression", "skills: { scope: { db: { when: [{ name: empty }] } } }\n" + minimal,
			"configfile: skills.scope.db.when[0]: expr is required"},
		{"scope condition name given twice", "skills: { scope: { db: { when: [{ name: n, expr: 'true' }, { name: n, expr: 'true' }] } } }\n" + minimal,
			`configfile: skills.scope.db.when[1]: name "n" is given twice`},
		{"unknown skills key", "skills: { dirs: [a] }\n" + minimal, "field dirs not found"},
		{"unknown scope key", "skills: { scope: { db: { allowed-tools: [x] } } }\n" + minimal, "field allowed-tools not found"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := load(t, tt.yaml)
			if err == nil {
				t.Fatal("expected an error")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error %q does not mention %q", err, tt.want)
			}
		})
	}
}
