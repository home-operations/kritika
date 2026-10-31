package configfile

import (
	"reflect"
	"strings"
	"testing"
)

// TestRuleFilesResolve: the root's paths replace the default ones, and
// written empty they look nowhere.
func TestRuleFilesResolve(t *testing.T) {
	t.Setenv("TEST_PRIVATE_KEY", "tok")
	t.Setenv("TEST_WEBHOOK_SECRET", "whsec")
	tests := []struct {
		name string
		yaml string
		want RuleFiles
	}{
		{name: "the default paths", yaml: minimal, want: RuleFiles{Paths: []string{".agents/rules", ".claude/rules"}}},
		{
			name: "the root's paths replace the default",
			yaml: "ruleFiles: { paths: [docs/rules] }\n" + minimal,
			want: RuleFiles{Paths: []string{"docs/rules"}},
		},
		{name: "empty paths turn rule files off", yaml: "ruleFiles: { paths: [] }\n" + minimal, want: RuleFiles{Paths: []string{}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := mustLoad(t, tt.yaml)
			if got := f.Settings(&f.Accounts[0]).RuleFiles; !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("rule files = %+v, want %+v", got, tt.want)
			}
			if !reflect.DeepEqual(DefaultRuleFilePaths, []string{".agents/rules", ".claude/rules"}) {
				t.Fatalf("resolving changed the default paths: %q", DefaultRuleFilePaths)
			}
		})
	}
}

func TestCheckRuleFiles(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		paths   []string
		wantErr string
	}{
		{name: "none"},
		{name: "in the repository", paths: []string{".claude/rules", "docs/rules"}},
		{name: "outside it", paths: []string{"docs/rules", "../rules"}, wantErr: "ruleFiles.paths[1]"},
		{name: "absolute", paths: []string{"/rules"}, wantErr: "ruleFiles.paths[0]"},
		{name: "empty", paths: []string{""}, wantErr: "ruleFiles.paths[0]"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := CheckRuleFiles(tt.paths)
			if (err != nil) != (tt.wantErr != "") || err != nil && !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err = %v, want %q", err, tt.wantErr)
			}
		})
	}
}
