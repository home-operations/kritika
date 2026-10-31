package runner

import (
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/repoconfig"
	"github.com/home-operations/kritika/internal/runspec"
)

// ruleDoc is a rule file with the paths frontmatter given, none when "".
func ruleDoc(paths, body string) string {
	if paths == "" {
		return body
	}
	return "---\npaths: " + paths + "\n---\n" + body
}

func TestDiscoverRuleFiles(t *testing.T) {
	files := map[string]string{
		"main.go":                                 "package main\n",
		".claude/rules/error-handling.md":         ruleDoc(`["**/*.rs"]`, "No bare let _ =.\n"),
		".claude/rules/authz.md":                  ruleDoc("", "Check every tenant.\n"),
		".claude/rules/backend/API.md":            ruleDoc(`"api/**"`, "Validate input.\n"),
		".claude/rules/notes.txt":                 "Not markdown.\n",
		".claude/rules/blank.md":                  ruleDoc(`["**/*.go"]`, ""),
		".claude/rules/bad-glob.md":               ruleDoc(`["photos [2024/**"]`, "Matches nothing.\n"),
		".claude/rules/huge.md":                   strings.Repeat("x", repoconfig.MaxRuleFileBytes),
		".claude/rules/___.md":                    "No id.\n",
		".agents/rules/error-handling.md":         "Another one.\n",
		".agents/rules/style.md":                  "Short names.\n",
		".claude/rules/configured.md":             "A configured rule's file.\n",
		".claude/rules/taken.md":                  "An id a configured rule has.\n",
		"docs/rules/release.md":                   "Tag it.\n",
		".claude/rules/a/b/c/d/e/f/g/h/i/deep.md": "Below the depth.\n",
	}
	const (
		authz   = ".claude/rules/authz.md"
		api     = ".claude/rules/backend/API.md"
		errs    = ".claude/rules/error-handling.md"
		style   = ".agents/rules/style.md"
		agErrs  = ".agents/rules/error-handling.md"
		badNote = ".claude/rules/bad-glob.md: skipped, none of its paths is a valid glob"
		hugNote = ".claude/rules/huge.md: skipped, it is longer than the 32 KiB of rule files a review is given; split it"
		idNote  = ".claude/rules/___.md: skipped, its name makes no rule id"
		tkNote  = ".claude/rules/taken.md: skipped, a configured rule has the id taken"
	)
	taken := &runspec.RuleFiles{
		Paths: configfile.DefaultRuleFilePaths, IDs: []string{"taken", "own"}, Files: []string{".claude/rules/configured.md"},
	}
	many, capped, cappedTexts := map[string]string{}, []configfile.Rule{}, repoconfig.Files{}
	blanks := map[string]string{"r/z.md": "Read past the cap.\n"}
	manyMore := map[string]string{"r/rule-101.md": "One of many.\n", "r/rule-102.md": "One of many.\n"}
	manyMoreTexts := repoconfig.Files{"r/rule-101.md": "One of many."}
	nested, nestedTexts := map[string]string{"r/z.md": "The hundredth.\n"}, repoconfig.Files{"r/z.md": "The hundredth."}
	var nestedRules []configfile.Rule
	for i := range repoconfig.MaxRuleFiles + 1 {
		id := fmt.Sprintf("rule-%03d", i)
		many["r/"+id+".md"] = "One of many.\n"
		if i < repoconfig.MaxRuleFiles {
			capped = append(capped, configfile.Rule{ID: id, File: "r/" + id + ".md"})
			cappedTexts["r/"+id+".md"] = "One of many."
		}
		if i < repoconfig.MaxRuleFiles {
			blanks["r/"+id+".md"] = ""
		}
		manyMore["r/"+id+".md"] = "One of many.\n"
		if i >= 2 {
			manyMoreTexts["r/"+id+".md"] = "One of many."
		}
		if i < repoconfig.MaxRuleFiles-1 {
			nested["r/sub/"+id+".md"] = "One of many.\n"
			nestedRules = append(nestedRules, configfile.Rule{ID: id, File: "r/sub/" + id + ".md"})
			nestedTexts["r/sub/"+id+".md"] = "One of many."
		}
	}
	nestedRules = append(nestedRules, configfile.Rule{ID: "z", File: "r/z.md"})
	tests := []struct {
		name      string
		files     map[string]string
		rf        *runspec.RuleFiles
		want      []configfile.Rule
		wantTexts repoconfig.Files
		wantNotes []string
	}{
		{
			// .agents/rules is read first, so its error-handling.md is kept
			// and .claude/rules' is the duplicate.
			name: "the default directories, scoped first", files: files, rf: taken,
			want: []configfile.Rule{
				{ID: "backend-api", File: api, Paths: []string{"api/**"}},
				{ID: "error-handling", File: agErrs}, {ID: "style", File: style}, {ID: "authz", File: authz},
			},
			wantTexts: repoconfig.Files{
				agErrs: "Another one.", style: "Short names.", authz: "Check every tenant.", api: "Validate input.",
			},
			wantNotes: []string{idNote, badNote, errs + ": skipped, another rule file has the id error-handling", hugNote, tkNote},
		},
		{name: "a missing directory", files: files, rf: &runspec.RuleFiles{Paths: []string{"nope"}}},
		{
			name: "a directory that is not clean", files: files, rf: &runspec.RuleFiles{Paths: []string{"./docs//rules/"}},
			want:      []configfile.Rule{{ID: "release", File: "docs/rules/release.md"}},
			wantTexts: repoconfig.Files{"docs/rules/release.md": "Tag it."},
		},
		{
			name: "past the cap", files: many, rf: &runspec.RuleFiles{Paths: []string{"r"}}, want: capped, wantTexts: cappedTexts,
			wantNotes: []string{"more than 100 rule files found; the rest were left out"},
		},
		{
			name: "the cap counts the files read, kept or not", files: blanks, rf: &runspec.RuleFiles{Paths: []string{"r"}},
			wantNotes: []string{"more than 100 rule files found; the rest were left out"},
		},
		{
			name: "files a configured rule names take no room past the cap", files: manyMore,
			rf:        &runspec.RuleFiles{Paths: []string{"r"}, Files: []string{"r/rule-000.md", "r/rule-001.md"}},
			want:      append(slices.Clone(capped[2:]), configfile.Rule{ID: "rule-100", File: "r/rule-100.md"}, configfile.Rule{ID: "rule-101", File: "r/rule-101.md"}),
			wantTexts: manyMoreTexts,
			wantNotes: []string{"more than 100 rule files found; the rest were left out"},
		},
		{
			name: "files an earlier directory held take no room", files: nested,
			rf: &runspec.RuleFiles{Paths: []string{"r/sub", "r"}}, want: nestedRules, wantTexts: nestedTexts,
		},
		{
			name: "the root directory", files: map[string]string{"rules.md": "Tag it.\n", "a/b.md": "Deep.\n", "main.go": ""},
			rf:        &runspec.RuleFiles{Paths: []string{"."}},
			want:      []configfile.Rule{{ID: "a-b", File: "a/b.md"}, {ID: "rules", File: "rules.md"}},
			wantTexts: repoconfig.Files{"a/b.md": "Deep.", "rules.md": "Tag it."},
		},
		{
			name: "directories that overlap read a file once", files: files,
			rf:        &runspec.RuleFiles{Paths: []string{"docs", "docs/rules"}},
			want:      []configfile.Rule{{ID: "rules-release", File: "docs/rules/release.md"}},
			wantTexts: repoconfig.Files{"docs/rules/release.md": "Tag it."},
		},
		{
			name: "a configured file named with a dot", files: files,
			rf: &runspec.RuleFiles{Paths: []string{"docs/rules"}, Files: []string{"./docs/rules/release.md"}},
		},
		{
			name: "a file too large to read", files: map[string]string{"r/big.md": strings.Repeat("x", repoconfig.MaxFileBytes+1)},
			rf: &runspec.RuleFiles{Paths: []string{"r"}}, wantNotes: []string{repoconfig.TooLarge("r/big.md")},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rules, texts, notes, err := discoverRuleFiles(tree(t, tt.files), tt.rf)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(rules, tt.want) {
				t.Fatalf("rules = %+v\nwant    %+v", rules, tt.want)
			}
			if tt.wantTexts == nil {
				tt.wantTexts = repoconfig.Files{}
			}
			if !maps.Equal(texts, tt.wantTexts) {
				t.Fatalf("texts = %q\nwant    %q", texts, tt.wantTexts)
			}
			if !slices.Equal(notes, tt.wantNotes) {
				t.Fatalf("notes = %q\nwant    %q", notes, tt.wantNotes)
			}
		})
	}
}

// TestWithRuleFiles: discovered rules come after the configured ones, their
// text reaches the prompt's files beside the configured files, and the
// spec and files it was given are left as they were.
func TestWithRuleFiles(t *testing.T) {
	base := tree(t, map[string]string{
		".claude/rules/error-handling.md": ruleDoc(`["**/*.rs"]`, "No bare let _ =.\n"),
		"docs/style.md":                   "Short names.\n",
	})
	own := configfile.Rule{ID: "style", File: "docs/style.md"}
	files := repoconfig.Files{"docs/style.md": "Short names.\n"}

	t.Run("discovered after the configured", func(t *testing.T) {
		p := agentPromptSpec()
		p.Prompt.Rules = []configfile.Rule{own}
		p.Prompt.RuleFiles = &runspec.RuleFiles{Paths: configfile.DefaultRuleFilePaths, IDs: []string{"style"}, Files: []string{"docs/style.md"}}
		got, gotFiles, notes, err := withRuleFiles(base, p, files)
		if err != nil || notes != nil {
			t.Fatalf("err = %v, notes = %q", err, notes)
		}
		want := []configfile.Rule{own, {ID: "error-handling", File: ".claude/rules/error-handling.md", Paths: []string{"**/*.rs"}}}
		if !reflect.DeepEqual(got.Prompt.Rules, want) {
			t.Fatalf("rules = %+v, want %+v", got.Prompt.Rules, want)
		}
		if gotFiles[".claude/rules/error-handling.md"] != "No bare let _ =." || gotFiles["docs/style.md"] != "Short names.\n" {
			t.Fatalf("files = %q", gotFiles)
		}
		if len(p.Prompt.Rules) != 1 || len(files) != 1 {
			t.Fatalf("the given spec or files changed: %+v %q", p.Prompt.Rules, files)
		}
		in := newPromptInputs(got, gotFiles, nil, []string{"src/main.rs"})
		if ids := in.ruleIDs(); !slices.Equal(ids, []string{"style", "error-handling"}) {
			t.Fatalf("prompt rules = %q", ids)
		}
		if in := newPromptInputs(got, gotFiles, nil, []string{"main.go"}); !slices.Equal(in.ruleIDs(), []string{"style"}) {
			t.Fatalf("prompt rules of a Go change = %q", in.ruleIDs())
		}
	})
	t.Run("none discovered when off", func(t *testing.T) {
		for _, rf := range []*runspec.RuleFiles{nil, {Paths: []string{}}} {
			p := agentPromptSpec()
			p.Prompt.RuleFiles = rf
			got, gotFiles, notes, err := withRuleFiles(base, p, files)
			if err != nil || notes != nil || len(got.Prompt.Rules) != 0 || len(gotFiles) != 1 {
				t.Fatalf("withRuleFiles(%+v) = %+v, %q, %q, %v", rf, got.Prompt.Rules, gotFiles, notes, err)
			}
		}
	})
}
