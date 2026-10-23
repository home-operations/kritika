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
		large  = "pr.lines > 100"
	)
	locks, source := []string{"**/*.lock"}, []string{"src/**", "internal/**"}
	sample := SamplePR()
	isDraft := with(sample, "draft", true)
	byRenovate := with(sample, "author", "renovate[bot]")
	labelled := with(sample, "labels", []any{map[string]any{"name": "needs-review", "color": "0"}})
	unlabelled := with(sample, "labels", []any{})

	tests := []struct {
		name    string
		filters Filters
		vars    map[string]any
		// diff is the fetched diff, nil before it is.
		diff *Diff
		skip bool
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

		{name: "before the diff an exclude with paths does not hold", filters: Filters{Exclude: []Filter{{Name: "locks", Paths: locks}}}, vars: sample},
		{name: "before the diff an exclude on pr.lines does not hold", filters: Filters{Exclude: []Filter{{Name: "large", Expr: large}}}, vars: sample},
		{name: "before the diff an include that needs it may hold", filters: Filters{Include: []Filter{{Paths: source}, {Expr: "pr.lines < 3"}}},
			vars: sample},
		{name: "before the diff a failed include waits for one that needs it", filters: Filters{Include: []Filter{{Expr: renovate}, {Paths: source}}},
			vars: sample},
		{name: "before the diff an exclude that needs none still holds", filters: Filters{Include: []Filter{{Paths: source}},
			Exclude: []Filter{{Name: "large", Expr: large}, {Name: "drafts", Expr: draft}}}, vars: isDraft, skip: true, by: "drafts"},
		{name: "before the diff no include holds and none waits", filters: Filters{Include: []Filter{{Expr: renovate}, {Expr: wanted}},
			Exclude: []Filter{{Name: "locks", Paths: locks}}}, vars: sample, skip: true},

		{name: "pr.lines over the diff's", filters: Filters{Exclude: []Filter{{Name: "large", Expr: large}}},
			vars: sample, diff: &Diff{Lines: 101}, skip: true, by: "large"},
		{name: "pr.lines at the diff's", filters: Filters{Exclude: []Filter{{Name: "large", Expr: large}}}, vars: sample, diff: &Diff{Lines: 100}},
		{name: "an exclude's paths match a changed path", filters: Filters{Exclude: []Filter{{Name: "locks", Paths: locks}}},
			vars: sample, diff: &Diff{Changed: []string{"main.go", "web/pnpm.lock"}}, skip: true, by: "locks"},
		{name: "an exclude's paths match no changed path", filters: Filters{Exclude: []Filter{{Name: "locks", Paths: locks}}},
			vars: sample, diff: &Diff{Changed: []string{"main.go"}}},
		{name: "an include's paths match a changed path", filters: Filters{Include: []Filter{{Paths: source}}},
			vars: sample, diff: &Diff{Changed: []string{"docs/a.md", "src/a.go"}}},
		{name: "an include's paths match no changed path", filters: Filters{Include: []Filter{{Paths: source}}},
			vars: sample, diff: &Diff{Changed: []string{"docs/a.md"}}, skip: true},
		{name: "expr and paths both hold", filters: Filters{Exclude: []Filter{{Name: "bot-locks", Expr: renovate, Paths: locks}}},
			vars: byRenovate, diff: &Diff{Changed: []string{"go.lock"}}, skip: true, by: "bot-locks"},
		{name: "expr holds and paths do not", filters: Filters{Exclude: []Filter{{Name: "bot-locks", Expr: renovate, Paths: locks}}},
			vars: byRenovate, diff: &Diff{Changed: []string{"main.go"}}},
		{name: "paths hold and expr does not", filters: Filters{Exclude: []Filter{{Name: "bot-locks", Expr: renovate, Paths: locks}}},
			vars: sample, diff: &Diff{Changed: []string{"go.lock"}}},
		{name: "with the diff exclude wins over include", filters: Filters{Include: []Filter{{Paths: source}}, Exclude: []Filter{{Name: "large", Expr: large}}},
			vars: sample, diff: &Diff{Lines: 500, Changed: []string{"src/a.go"}}, skip: true, by: "large"},
		{name: "with the diff a failed include and one that holds", filters: Filters{Include: []Filter{{Expr: renovate}, {Paths: source}}},
			vars: sample, diff: &Diff{Lines: 1, Changed: []string{"src/a.go"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.filters.Compile(); err != nil {
				t.Fatalf("Compile: %v", err)
			}
			skip, by, err := tt.filters.Skips(tt.vars, tt.diff)
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

func TestFilterCompile(t *testing.T) {
	tests := []struct {
		name   string
		filter Filter
		want   string // the error; empty means it compiles
	}{
		{name: "paths only", filter: Filter{Paths: []string{"src/**", "*.go"}}},
		{name: "expr only", filter: Filter{Expr: "pr.draft"}},
		{name: "expr and paths", filter: Filter{Expr: "pr.lines > 2000", Paths: []string{"**/*.lock"}}},
		{name: "neither", filter: Filter{Name: "a"}, want: "expr or paths is required"},
		{name: "a blank expr and no paths", filter: Filter{Expr: " \n"}, want: "expr or paths is required"},
		{name: "a bad glob", filter: Filter{Paths: []string{"["}}, want: `paths[0] "[" is not a valid glob`},
		{name: "a bad glob after a good one", filter: Filter{Expr: "true", Paths: []string{"src/**", "a[b"}}, want: `paths[1] "a[b" is not a valid glob`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.filter.Compile()
			if tt.want == "" {
				if err != nil {
					t.Fatalf("Compile: %v", err)
				}
				return
			}
			if err == nil || err.Error() != tt.want {
				t.Fatalf("Compile = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestFiltersNeedsDiff(t *testing.T) {
	tests := []struct {
		name    string
		filters Filters
		want    bool
	}{
		{name: "empty lists"},
		{name: "expressions over the pull request alone", filters: Filters{Include: []Filter{{Expr: "pr.open"}}, Exclude: []Filter{{Expr: "pr.draft"}}}},
		{name: "an include with paths", filters: Filters{Include: []Filter{{Expr: "pr.open"}, {Paths: []string{"src/**"}}}}, want: true},
		{name: "an exclude with paths", filters: Filters{Exclude: []Filter{{Expr: "pr.draft", Paths: []string{"src/**"}}}}, want: true},
		{name: "an exclude on pr.lines", filters: Filters{Exclude: []Filter{{Expr: "pr.draft"}, {Expr: "pr.lines > 2000"}}}, want: true},
		{name: "an include on has(pr.lines)", filters: Filters{Include: []Filter{{Expr: "has(pr.lines) && pr.open"}}}, want: true},
		{name: "an exclude indexing pr by lines", filters: Filters{Exclude: []Filter{{Expr: `pr["lines"] > 2000`}}}, want: true},
		{name: "another field named in a string", filters: Filters{Exclude: []Filter{{Expr: `pr.title.contains("lines")`}}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.filters.Compile(); err != nil {
				t.Fatalf("Compile: %v", err)
			}
			if got := tt.filters.NeedsDiff(); got != tt.want {
				t.Fatalf("NeedsDiff = %v, want %v", got, tt.want)
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
		{name: "include without an expression", filters: Filters{Include: []Filter{{Expr: "true"}, {Name: "a"}}}, want: "include[1]: expr or paths is required"},
		{name: "exclude with a blank expression", filters: Filters{Exclude: []Filter{{Expr: " \n"}}}, want: "exclude[0]: expr or paths is required"},
		{name: "include with paths alone", filters: Filters{Include: []Filter{{Name: "a", Paths: []string{"src/**"}}}}},
		{name: "exclude with a bad glob", filters: Filters{Exclude: []Filter{{Expr: "true", Paths: []string{"["}}}}, want: `exclude[0]: paths[0] "[" is not a valid glob`},
		{name: "include syntax error", filters: Filters{Include: []Filter{{Expr: "pr.draft &&"}}}, want: "include[0]: "},
		{name: "exclude not a bool", filters: Filters{Exclude: []Filter{{Expr: "true"}, {Expr: "pr.title"}}}, want: "exclude[1]: "},
		{name: "exclude fails the smoke test", filters: Filters{Exclude: []Filter{{Expr: `pr.labels[5].name == "x"`}}},
			want: "exclude[0]: smoke test against a sample pull request"},
		{name: "exclude with a malformed regex the smoke test does not reach", filters: Filters{Exclude: []Filter{{Expr: `pr.draft && pr.title.matches("(")`}}},
			want: "exclude[0]: prfilter: program: error parsing regexp"},
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

// TestFilterLists: each list resolves from the file's root as written,
// compiled, the other list left as it is.
func TestFilterLists(t *testing.T) {
	t.Setenv("TEST_PRIVATE_KEY", "tok")
	t.Setenv("TEST_WEBHOOK_SECRET", "whsec")

	for _, list := range []string{"include", "exclude"} {
		other := map[string]string{"include": "exclude", "exclude": "include"}[list]
		t.Run(list, func(t *testing.T) {
			f := mustLoad(t, "trigger: { "+list+": [{ name: a, expr: pr.draft }, { expr: pr.fork }], "+other+": [{ name: a, expr: 'true' }] }\n"+minimal)
			s := f.Settings(&f.Accounts[0]).Filters
			got, kept := s.Include, s.Exclude
			if list == "exclude" {
				got, kept = kept, got
			}
			if filterExprs(got) != "pr.draft, pr.fork" || filterExprs(kept) != "true" {
				t.Fatalf("%s = %q, %s = %q; want the root's and the other list untouched", list, filterExprs(got), other, filterExprs(kept))
			}
			// The resolved conditions are the compiled ones.
			if _, _, err := s.Skips(SamplePR(), nil); err != nil {
				t.Fatalf("Skips: %v", err)
			}
		})
	}
}
