package worker

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"maps"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/forge"
	"github.com/home-operations/kritika/internal/repoconfig"
	"github.com/home-operations/kritika/internal/review"
)

func adminSettings(t *testing.T) configfile.Settings {
	t.Helper()
	filters := configfile.Filters{Exclude: []configfile.Filter{{Name: "drafts", Expr: "pr.draft"}}}
	if err := filters.Compile(); err != nil {
		t.Fatal(err)
	}
	return configfile.Settings{
		Enabled: true, Filters: filters, Ignore: []string{"vendor/**"},
		Review: configfile.Review{
			Rules: []configfile.Rule{{ID: "ops", File: "ops/rules.md"}}, RequireSuggestedFix: true,
			Templates: configfile.ReviewTemplates{Summary: "ops/summary.tmpl", Inline: "ops/inline.tmpl"},
		},
	}
}

func TestEffective(t *testing.T) {
	adminFiles := repoconfig.Files{"ops/rules.md": "admin rules", "ops/summary.tmpl": "op summary", "ops/inline.tmpl": "op inline"}
	with := func(extra repoconfig.Files) repoconfig.Files {
		files := maps.Clone(adminFiles)
		maps.Copy(files, extra)
		return files
	}
	adminDefaults := review.Templates{Summary: "op summary", Inline: "op inline"}
	adminPaths := []string{"ops/rules.md", "ops/summary.tmpl", "ops/inline.tmpl"}

	tests := []struct {
		name string
		// doc is the merge-base .kritika.yaml, none when empty. files are
		// what the runner read.
		doc          string
		files        repoconfig.Files
		enabled      bool
		inRepoFilter bool
		ignore       []string
		repoFiles    []string
		templates    review.Templates
		strict       bool
		notes        []string
	}{
		{
			name: "no file keeps the admin's settings", files: adminFiles, enabled: true, ignore: []string{"vendor/**"},
			repoFiles: adminPaths, templates: adminDefaults, strict: true,
		},
		{
			name: "disable", doc: "enabled: false\n", files: adminFiles, ignore: []string{"vendor/**"},
			repoFiles: append(adminPaths, repoconfig.FileName), templates: adminDefaults, strict: true,
		},
		{
			name: "the file's conditions are kept apart from the admin's", doc: "trigger:\n  exclude: [{ expr: 'pr.body.contains(\"[skip-review]\")' }]\n", files: adminFiles,
			enabled: true, inRepoFilter: true, ignore: []string{"vendor/**"}, repoFiles: append(adminPaths, repoconfig.FileName),
			templates: adminDefaults, strict: true,
		},
		{
			name: "ignore globs add to the admin's", doc: "ignore: [gen/**, vendor/**]\n",
			files: adminFiles, enabled: true, ignore: []string{"vendor/**", "gen/**"},
			repoFiles: append(adminPaths, repoconfig.FileName), templates: adminDefaults, strict: true,
		},
		{
			name: "review.fixes may only turn on", doc: "review: { fixes: false }\n", files: adminFiles,
			enabled: true, ignore: []string{"vendor/**"}, repoFiles: append(adminPaths, repoconfig.FileName),
			templates: adminDefaults, strict: true,
			notes: []string{".kritika.yaml: review.fixes false was dropped; allowed: true, since an admin requires a suggested fix"},
		},
		{
			name:    "repository file rules follow the admin's, and its summary template replaces the admin's",
			doc:     "rules: [{ id: repo, file: .kritika/rules.md }]\ncomments:\n  summary: .kritika/summary.tmpl\n",
			files:   with(repoconfig.Files{".kritika/rules.md": "repo rules", ".kritika/summary.tmpl": "repo summary"}),
			enabled: true, ignore: []string{"vendor/**"},
			repoFiles: []string{"ops/rules.md", ".kritika/rules.md", ".kritika/summary.tmpl", "ops/inline.tmpl", repoconfig.FileName},
			templates: review.Templates{Summary: "repo summary", Inline: "op inline"}, strict: true,
		},
		{
			name: "a template the runner could not read leaves the built-in one", doc: "comments:\n  summary: .kritika/gone.tmpl\n",
			files: adminFiles, enabled: true, ignore: []string{"vendor/**"},
			repoFiles: []string{"ops/rules.md", ".kritika/gone.tmpl", "ops/inline.tmpl", repoconfig.FileName},
			templates: review.Templates{Inline: "op inline"}, strict: true,
		},
		{
			name: "invalid yaml is noted and the admin's settings apply", doc: "enabled: false\nunknown: 1\n", files: adminFiles,
			enabled: true, ignore: []string{"vendor/**"}, repoFiles: append(adminPaths, repoconfig.FileName),
			templates: adminDefaults, strict: true,
			notes: []string{".kritika.yaml was ignored: repoconfig: parse: yaml: construct errors: line 2: field unknown not found in type repoconfig.File"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			settings := adminSettings(t)
			var doc []byte
			if tt.doc != "" {
				doc = []byte(tt.doc)
			}
			e, notes := effective(settings, doc)
			hasInRepoFilter := len(e.InRepoFilters.Include)+len(e.InRepoFilters.Exclude) > 0
			if e.Enabled != tt.enabled || hasInRepoFilter != tt.inRepoFilter || !reflect.DeepEqual(e.Filters, settings.Filters) {
				t.Fatalf("enabled=%v inRepoFilters=%v admin filters=%v", e.Enabled, e.InRepoFilters, e.Filters)
			}
			if !slices.Equal(e.Ignore, tt.ignore) {
				t.Fatalf("ignore=%v", e.Ignore)
			}
			if got := e.repoFiles(); !slices.Equal(got, tt.repoFiles) {
				t.Fatalf("repoFiles = %v, want %v", got, tt.repoFiles)
			}
			if got := e.templates(tt.files); got != tt.templates || e.Review.RequireSuggestedFix != tt.strict {
				t.Fatalf("templates=%+v strict=%v", got, e.Review.RequireSuggestedFix)
			}
			if !slices.Equal(notes, tt.notes) {
				t.Fatalf("notes = %q, want %q", notes, tt.notes)
			}
			if !slices.Equal(settings.Ignore, []string{"vendor/**"}) || !reflect.DeepEqual(settings.Review.Rules, adminSettings(t).Review.Rules) {
				t.Fatalf("the admin's settings were modified: %v %v", settings.Ignore, settings.Review.Rules)
			}
		})
	}
}

func TestEffectiveSkip(t *testing.T) {
	vars := func(body string) map[string]any {
		return map[string]any{"title": "t", "body": body, "draft": false, "labels": []any{}}
	}
	tests := []struct {
		name    string
		doc     string
		body    string
		changed []string
		want    repoconfig.SkipReason
	}{
		{"nothing to skip", "", "", []string{"main.go"}, ""},
		{"disabled", "enabled: false\n", "", []string{"main.go"}, repoconfig.SkipDisabled},
		{"filtered", "trigger:\n  exclude: [{ expr: 'pr.body.contains(\"[skip-review]\")' }]\n", "please [skip-review]", []string{"main.go"}, repoconfig.SkipFiltered},
		{"filter allows", "trigger:\n  exclude: [{ expr: 'pr.body.contains(\"[skip-review]\")' }]\n", "normal", []string{"main.go"}, ""},
		{"filter that fails to evaluate skips", "trigger:\n  include: [{ expr: 'pr.number > 0' }]\n", "", []string{"main.go"}, repoconfig.SkipFiltered},
		{"only ignored paths", "ignore: [docs/**]\n", "", []string{"docs/a.md", "docs/b/c.md"}, repoconfig.SkipOnlyPaths},
		{"a path outside the ignore globs", "ignore: [docs/**]\n", "", []string{"docs/a.md", "main.go"}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e, _ := effective(configfile.Settings{Enabled: true}, []byte(tt.doc))
			got, _, _ := e.Check(vars(tt.body), tt.changed)
			if got != tt.want {
				t.Fatalf("skip = %q, want %q", got, tt.want)
			}
			if got != "" && !got.Valid() {
				t.Fatalf("reason %q is not valid", got)
			}
		})
	}
	for r, want := range map[repoconfig.SkipReason]string{
		repoconfig.SkipDisabled: "disabled in .kritika.yaml", repoconfig.SkipFiltered: "filtered", repoconfig.SkipOnlyPaths: "only ignored paths changed",
	} {
		if r.Description() != want {
			t.Fatalf("%q.Description() = %q, want %q", r, r.Description(), want)
		}
	}
	if repoconfig.SkipReason("other").Valid() {
		t.Fatal("an unknown reason must not be valid")
	}
}

// fileForge is a forge whose only call is FileAt. It answers from files, or
// with err when set.
type fileForge struct {
	forge.Client
	files map[string]string
	err   error
}

func (f fileForge) FileAt(_ context.Context, _, _, _, path string) ([]byte, error) {
	if f.err != nil {
		return nil, f.err
	}
	content, ok := f.files[path]
	if !ok {
		return nil, fmt.Errorf("fake: %s: %w", path, fs.ErrNotExist)
	}
	return []byte(content), nil
}

func TestReadRepoConfig(t *testing.T) {
	down := errors.New("forge down")
	tests := []struct {
		name    string
		client  fileForge
		doc     string
		notes   []string
		wantErr error
	}{
		{name: "none", client: fileForge{}},
		{name: "read", client: fileForge{files: map[string]string{repoconfig.FileName: "enabled: false\n"}}, doc: "enabled: false\n"},
		{
			name:   "over the file cap",
			client: fileForge{files: map[string]string{repoconfig.FileName: strings.Repeat("x", repoconfig.MaxFileBytes+1)}},
			notes:  []string{repoconfig.TooLarge(repoconfig.FileName)},
		},
		{
			name:   "over what the forge reads",
			client: fileForge{err: fmt.Errorf("fake: %w", forge.ErrFileTooLarge)},
			notes:  []string{repoconfig.TooLarge(repoconfig.FileName)},
		},
		{name: "a forge error fails the job for a retry", client: fileForge{err: down}, wantErr: down},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			doc, notes, err := readRepoConfig(t.Context(), tt.client, "o", "r", "base")
			if !errors.Is(err, tt.wantErr) || string(doc) != tt.doc || (doc == nil) != (tt.doc == "") || !slices.Equal(notes, tt.notes) {
				t.Fatalf("readRepoConfig = %q, %q, %v", doc, notes, err)
			}
		})
	}
}

func (f fileForge) MergeBase(context.Context, string, string, int, string, string) (string, error) {
	return "base", nil
}

func TestFollowUpRepoConfig(t *testing.T) {
	files := map[string]string{"ops/rules.md": "admin rules", ".kritika/rules.md": "repo rules"}
	with := func(doc string) map[string]string {
		m := maps.Clone(files)
		m[repoconfig.FileName] = doc
		return m
	}
	tests := []struct {
		name   string
		files  map[string]string
		reason string
		model  configfile.ModelRef
		// read are the files the follow-up's runner reads from the merge
		// base, sorted.
		read []string
	}{
		{name: "no file", files: files, model: "p/big", read: []string{"ops/inline.tmpl", "ops/rules.md", "ops/summary.tmpl"}},
		{
			name: "the repository's model and file rules", files: with("review: { model: p/small }\nrules: [{ id: repo, file: .kritika/rules.md }]\n"),
			model: "p/small", read: []string{".kritika.yaml", ".kritika/rules.md", "ops/inline.tmpl", "ops/rules.md", "ops/summary.tmpl"},
		},
		{
			name: "a model of a provider the account may not use is dropped", files: with("review: { model: q/huge }\n"), model: "p/big",
			read: []string{".kritika.yaml", "ops/inline.tmpl", "ops/rules.md", "ops/summary.tmpl"},
		},
		{name: "disabled", files: with("enabled: false\n"), reason: "disabled in .kritika.yaml", model: "p/big"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			settings := adminSettings(t)
			settings.Models.Review = "p/big"
			settings.Providers = []string{"p"}
			f := &followUp{client: fileForge{files: tt.files}, owner: "o", repo: "r", pr: &pullRequest{number: 1}, settings: settings}
			reason, err := f.repoConfig(t.Context())
			if err != nil || reason != tt.reason {
				t.Fatalf("repoConfig = %q, %v; want %q", reason, err, tt.reason)
			}
			read := f.eff.repoFiles()
			slices.Sort(read)
			if f.settings.Models.Review != tt.model || !slices.Equal(read, tt.read) {
				t.Fatalf("model = %s, files read = %q", f.settings.Models.Review, read)
			}
			if tt.reason == "" && f.mergeBase != "base" {
				t.Fatalf("merge base = %q", f.mergeBase)
			}
		})
	}
}

// TestNarrowTo: a pull request confidence.when does not hold for is
// reviewed as if no confidence model were set.
func TestNarrowTo(t *testing.T) {
	t.Parallel()
	scoped := func() Effective {
		var e Effective
		e.Confidence = configfile.Confidence{Model: "p/judge", Fallback: "p/other", When: []configfile.When{{Expr: "pr.bot"}}}
		return e
	}
	e := scoped()
	e.narrowTo(configfile.SamplePR())
	if e.Confidence.Model != "" || e.Confidence.Fallback != "" {
		t.Fatalf("confidence = %+v, want no model for a person's pull request", e.Confidence)
	}
	e = scoped()
	bot := configfile.SamplePR()
	bot["bot"] = true
	e.narrowTo(bot)
	if e.Confidence.Model != "p/judge" || e.Confidence.RepoUnscored {
		t.Fatalf("confidence = %+v, want the model kept for a bot's", e.Confidence)
	}
	if e.Confidence.RepoUnscored {
		t.Fatal("the admin's conditions leaving a pull request out is not the repository's doing")
	}
	// A repository's file narrowing scoring leaves the pull request
	// unscored and unapproved, never approved on its findings alone.
	e = scoped()
	e.Confidence.WhenInRepo = []configfile.When{{Expr: "pr.draft"}}
	e.narrowTo(bot)
	if e.Confidence.Model != "" || !e.Confidence.RepoUnscored {
		t.Fatalf("confidence = %+v, want it unscored by the repository", e.Confidence)
	}
}

// TestNarrowToGate: a pull request confidence.gate's conditions do not hold
// for is scored but not gated.
func TestNarrowToGate(t *testing.T) {
	t.Parallel()
	gated := func() Effective {
		var e Effective
		e.Confidence = configfile.Confidence{Model: "p/judge", Gate: true, GateWhen: []configfile.When{{Expr: "pr.bot"}}}
		return e
	}
	e := gated()
	e.narrowTo(configfile.SamplePR())
	if e.Confidence.Gate || e.Confidence.Model != "p/judge" {
		t.Fatalf("confidence = %+v, want a person's pull request scored and not gated", e.Confidence)
	}
	e = gated()
	bot := configfile.SamplePR()
	bot["bot"] = true
	e.narrowTo(bot)
	if !e.Confidence.Gate {
		t.Fatalf("confidence = %+v, want a bot's pull request gated", e.Confidence)
	}
}

// TestPickRoute: model routes read the forge's diff only when a condition
// needs it. They see a deleted file by its base-side path, and pr.lines
// without the ignored paths. They leave a pull request whose diff the forge
// will not give on the default model.
func TestPickRoute(t *testing.T) {
	t.Parallel()
	const diff = "diff --git a/db/0042.sql b/db/0042.sql\n--- a/db/0042.sql\n+++ b/db/0042.sql\n@@ -1 +1,2 @@\n x\n+y\n" +
		"diff --git a/vendor/big.go b/vendor/big.go\n--- a/vendor/big.go\n+++ b/vendor/big.go\n@@ -1 +1,3 @@\n x\n+y\n+z\n" +
		"diff --git a/old.go b/old.go\ndeleted file mode 100644\n--- a/old.go\n+++ /dev/null\n@@ -1 +0,0 @@\n-x\n"
	routed := func(t *testing.T, c configfile.Filter) Effective {
		t.Helper()
		if err := c.Compile(); err != nil {
			t.Fatal(err)
		}
		var e Effective
		e.Ignore = []string{"vendor/**"}
		e.Models = configfile.Models{Review: "p/default", Routes: []configfile.ModelRoute{{Model: "p/routed", When: []configfile.Filter{c}}}}
		return e
	}
	logger := slog.New(slog.DiscardHandler)
	served := func() (string, error) { return diff, nil }
	for _, tt := range []struct {
		name string
		when configfile.Filter
		want configfile.ModelRef
	}{
		{"a changed path", configfile.Filter{Paths: []string{"db/**"}}, "p/routed"},
		{"a deleted file", configfile.Filter{Paths: []string{"old.go"}}, "p/routed"},
		{"lines without the ignored paths", configfile.Filter{Expr: "pr.lines == 2"}, "p/routed"},
		{"a path the diff does not change", configfile.Filter{Paths: []string{"api/**"}}, "p/default"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			e := routed(t, tt.when)
			pickRoute(logger, &e, configfile.SamplePR(), served)
			if e.Models.Review != tt.want {
				t.Fatalf("model = %s, want %s", e.Models.Review, tt.want)
			}
		})
	}
	e := routed(t, configfile.Filter{Expr: "pr.bot"})
	pickRoute(logger, &e, configfile.SamplePR(), func() (string, error) {
		t.Fatal("a condition on the pull request alone read the diff")
		return "", nil
	})
	e = routed(t, configfile.Filter{Paths: []string{"db/**"}})
	pickRoute(logger, &e, configfile.SamplePR(), func() (string, error) { return "", errors.New("forge: diff too large") })
	if e.Models.Review != "p/default" || e.Models.Route != "" {
		t.Fatalf("models = %+v, want the default without the diff", e.Models)
	}
}
