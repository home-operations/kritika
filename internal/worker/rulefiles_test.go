package worker

import (
	"reflect"
	"testing"

	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/repoconfig"
	"github.com/home-operations/kritika/internal/runspec"
)

// TestRuleFiles: the spec carries every configured rule, its when
// conditions holding or not, so the runner leaves their files and ids to
// them.
func TestRuleFiles(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		rules []configfile.Rule
		want  *runspec.RuleFiles
	}{
		{name: "no rules", want: &runspec.RuleFiles{Paths: repoconfig.RuleFileDirs}},
		{
			name: "every rule is taken",
			rules: []configfile.Rule{
				{ID: "no-tokens", Rule: "Never log a token."},
				{ID: "errors", File: ".claude/rules/errors.md", When: []configfile.When{{Expr: "false"}}},
			},
			want: &runspec.RuleFiles{
				Paths: repoconfig.RuleFileDirs, IDs: []string{"no-tokens", "errors"}, Files: []string{".claude/rules/errors.md"},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := ruleFiles(tt.rules); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("ruleFiles = %+v, want %+v", got, tt.want)
			}
		})
	}
}
