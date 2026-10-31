package worker

import (
	"reflect"
	"testing"

	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/runspec"
)

// TestRuleFiles: the spec carries every configured rule, its when
// conditions holding or not, so the runner leaves their files and ids to
// them, and it discovers nothing without directories.
func TestRuleFiles(t *testing.T) {
	t.Parallel()
	rules := []configfile.Rule{
		{ID: "no-tokens", Rule: "Never log a token."},
		{ID: "errors", File: ".claude/rules/errors.md", When: []configfile.When{{Expr: "false"}}},
	}
	tests := []struct {
		name  string
		dirs  configfile.RuleFiles
		rules []configfile.Rule
		want  *runspec.RuleFiles
	}{
		{name: "no directories", rules: rules},
		{name: "written empty", dirs: configfile.RuleFiles{Paths: []string{}}, rules: rules},
		{
			name: "every rule is taken", dirs: configfile.RuleFiles{Paths: configfile.DefaultRuleFilePaths}, rules: rules,
			want: &runspec.RuleFiles{
				Paths: configfile.DefaultRuleFilePaths, IDs: []string{"no-tokens", "errors"}, Files: []string{".claude/rules/errors.md"},
			},
		},
		{
			name: "no rules", dirs: configfile.RuleFiles{Paths: []string{"docs/rules"}},
			want: &runspec.RuleFiles{Paths: []string{"docs/rules"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := ruleFiles(tt.dirs, tt.rules); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("ruleFiles = %+v, want %+v", got, tt.want)
			}
		})
	}
}
