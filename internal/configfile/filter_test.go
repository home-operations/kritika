package configfile

import (
	"strings"
	"testing"
)

func TestFiltersSkips(t *testing.T) {
	const (
		draft    = "pr.draft"
		renovate = `pr.author.startsWith("renovate")`
		wanted   = `pr.labels.exists(l, l.name == "needs-review")`
		// broken passes the smoke test against SamplePR, which has one
		// label, and fails on a pull request with none.
		broken = `pr.labels[0].name == "x"`
	)
	sample := SamplePR()
	isDraft := with(sample, "draft", true)
	byRenovate := with(sample, "author", "renovate[bot]")
	labelled := with(sample, "labels", []any{map[string]any{"name": "needs-review", "color": "0"}})
	unlabelled := with(sample, "labels", []any{})

	tests := []struct {
		name    string
		filters Filters
		vars    map[string]any
		skip    bool
		// by is the label of the condition returned, "" for none.
		by      string
		wantErr bool
	}{
		{name: "empty lists review everything", vars: isDraft},
		{name: "an exclude that holds", filters: Filters{Exclude: []Filter{{Expr: "false"}, {Name: "drafts", Expr: draft}}},
			vars: isDraft, skip: true, by: "drafts"},
		{name: "an exclude without a name that holds", filters: Filters{Exclude: []Filter{{Expr: draft}}},
			vars: isDraft, skip: true, by: draft},
		{name: "no exclude holds", filters: Filters{Exclude: []Filter{{Name: "drafts", Expr: draft}}}, vars: sample},
		{name: "the first include holds", filters: Filters{Include: []Filter{{Expr: renovate}, {Name: "wanted", Expr: wanted}}},
			vars: byRenovate},
		{name: "a later include holds", filters: Filters{Include: []Filter{{Expr: renovate}, {Name: "wanted", Expr: wanted}}},
			vars: labelled},
		{name: "no include holds", filters: Filters{Include: []Filter{{Expr: renovate}, {Name: "wanted", Expr: wanted}}},
			vars: sample, skip: true},
		{name: "exclude wins over include", filters: Filters{Include: []Filter{{Expr: renovate}}, Exclude: []Filter{{Name: "drafts", Expr: draft}}},
			vars: with(byRenovate, "draft", true), skip: true, by: "drafts"},
		{name: "an include holds and no exclude does", filters: Filters{Include: []Filter{{Expr: renovate}}, Exclude: []Filter{{Name: "drafts", Expr: draft}}},
			vars: byRenovate},
		{name: "an exclude that fails to evaluate skips", filters: Filters{Exclude: []Filter{{Name: "broken", Expr: broken}}},
			vars: unlabelled, skip: true, by: "broken", wantErr: true},
		{name: "an include that fails to evaluate skips", filters: Filters{Include: []Filter{{Name: "broken", Expr: broken}, {Expr: "true"}}},
			vars: unlabelled, skip: true, by: "broken", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.filters.Compile(); err != nil {
				t.Fatalf("Compile: %v", err)
			}
			skip, by, err := tt.filters.Skips(tt.vars)
			var label string
			if by != nil {
				label = by.Label()
			}
			if skip != tt.skip || label != tt.by || (err != nil) != tt.wantErr {
				t.Fatalf("Skips = %v, %q, %v; want %v, %q, error %v", skip, label, err, tt.skip, tt.by, tt.wantErr)
			}
		})
	}
}

func TestFiltersCompile(t *testing.T) {
	tests := []struct {
		name    string
		filters Filters
		want    string // substring of the error; empty means it compiles
	}{
		{name: "empty lists"},
		{name: "the same name in both lists", filters: Filters{Include: []Filter{{Name: "a", Expr: "true"}}, Exclude: []Filter{{Name: "a", Expr: "true"}}}},
		{name: "conditions without a name", filters: Filters{Exclude: []Filter{{Expr: "true"}, {Expr: "true"}}}},
		{name: "include without an expression", filters: Filters{Include: []Filter{{Expr: "true"}, {Name: "a"}}}, want: "include[1]: expr is required"},
		{name: "exclude with a blank expression", filters: Filters{Exclude: []Filter{{Expr: " \n"}}}, want: "exclude[0]: expr is required"},
		{name: "include syntax error", filters: Filters{Include: []Filter{{Expr: "pr.draft &&"}}}, want: "include[0]: "},
		{name: "exclude not a bool", filters: Filters{Exclude: []Filter{{Expr: "true"}, {Expr: "pr.title"}}}, want: "exclude[1]: "},
		{name: "exclude fails the smoke test", filters: Filters{Exclude: []Filter{{Expr: `pr.labels[5].name == "x"`}}},
			want: "exclude[0]: smoke test against a sample pull request"},
		{name: "include name given twice", filters: Filters{Include: []Filter{{Name: "a", Expr: "true"}, {Expr: "true"}, {Name: "a", Expr: "false"}}},
			want: `include[2]: name "a" is given twice`},
		{name: "exclude name given twice", filters: Filters{Exclude: []Filter{{Name: "a", Expr: "true"}, {Name: "a", Expr: "false"}}},
			want: `exclude[1]: name "a" is given twice`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.filters.Compile()
			if tt.want == "" {
				if err != nil {
					t.Fatalf("Compile: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Compile = %v, want it to mention %q", err, tt.want)
			}
		})
	}
}

// TestFilterScopes: each list adds up from the file's root through owner/*
// to owner/name, a condition under a name the list already has taking that
// one's place.
func TestFilterScopes(t *testing.T) {
	t.Setenv("TEST_PRIVATE_KEY", "tok")
	t.Setenv("TEST_WEBHOOK_SECRET", "whsec")

	tests := []struct {
		name              string
		root, owner, repo string
		want              string
		// sibling is what a repository without an entry gets.
		sibling string
	}{
		{name: "only the root", root: "{ name: a, expr: pr.draft }", want: "pr.draft", sibling: "pr.draft"},
		{name: "each scope adds", root: "{ expr: pr.draft }", owner: "{ expr: pr.fork }", repo: "{ name: a, expr: pr.open }",
			want: "pr.draft, pr.fork, pr.open", sibling: "pr.draft, pr.fork"},
		{name: "owner/* replaces the root's by name", root: "{ name: a, expr: pr.draft }, { expr: pr.fork }", owner: "{ name: a, expr: pr.open }",
			want: "pr.open, pr.fork", sibling: "pr.open, pr.fork"},
		{name: "owner/name replaces the root's by name", root: "{ name: a, expr: pr.draft }, { expr: pr.fork }", repo: "{ expr: pr.open }, { name: a, expr: 'false' }",
			want: "false, pr.fork, pr.open", sibling: "pr.draft, pr.fork"},
		{name: "owner/name replaces what owner/* added", root: "{ expr: pr.draft }", owner: "{ name: a, expr: pr.fork }", repo: "{ name: a, expr: pr.open }",
			want: "pr.draft, pr.open", sibling: "pr.draft, pr.fork"},
		{name: "owner/name replaces what owner/* replaced", root: "{ name: a, expr: pr.draft }", owner: "{ name: a, expr: pr.fork }", repo: "{ name: a, expr: pr.open }",
			want: "pr.open", sibling: "pr.fork"},
		{name: "another name is added", root: "{ name: a, expr: pr.draft }", repo: "{ name: b, expr: pr.open }",
			want: "pr.draft, pr.open", sibling: "pr.draft"},
	}
	for _, list := range []string{"include", "exclude"} {
		other := map[string]string{"include": "exclude", "exclude": "include"}[list]
		for _, tt := range tests {
			t.Run(list+"/"+tt.name, func(t *testing.T) {
				trigger := func(conditions string) string { return "trigger: { " + list + ": [" + conditions + "] }" }
				// The other list holds the name the cases replace, which
				// must stay as it is: a name stands for a condition of
				// one list.
				f, err := loadBytes(t, []byte("trigger: { "+list+": ["+tt.root+"], "+other+": [{ name: a, expr: 'true' }] }\n"+
					acme("  acme/*: { "+trigger(tt.owner)+" }\n  acme/x: { "+trigger(tt.repo)+" }\n  acme/y: {}\n")))
				if err != nil {
					t.Fatalf("Parse: %v", err)
				}
				s := f.Settings(&f.Accounts[0], "acme/x").Filters
				got, kept := s.Include, s.Exclude
				if list == "exclude" {
					got, kept = kept, got
				}
				if filterExprs(got) != tt.want || filterExprs(kept) != "true" {
					t.Fatalf("%s = %q, %s = %q; want %q and the other list untouched", list, filterExprs(got), other, filterExprs(kept), tt.want)
				}
				// The merged conditions are the compiled ones.
				if _, _, err := s.Skips(SamplePR()); err != nil {
					t.Fatalf("Skips: %v", err)
				}
				// A repository's conditions stay its own.
				sibling := f.Settings(&f.Accounts[0], "acme/y").Filters
				if list == "exclude" {
					sibling.Include = sibling.Exclude
				}
				if got := filterExprs(sibling.Include); got != tt.sibling {
					t.Fatalf("acme/y's %s = %q, want %q", list, got, tt.sibling)
				}
			})
		}
	}
}
