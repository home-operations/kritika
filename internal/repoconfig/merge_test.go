package repoconfig

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/review"
	"github.com/home-operations/kritika/internal/webhook"
)

func adminSettings() configfile.Settings {
	return configfile.Settings{
		Enabled: true, Ignore: []string{"vendor/**"}, Settle: 2 * time.Minute,
		Models:     configfile.Models{Review: "p/big"},
		Confidence: configfile.Confidence{Threshold: 5, Risk: review.RiskMedium},
		Filters: configfile.Filters{
			Include: []configfile.Filter{{Name: "wanted", Expr: `pr.labels.exists(l, l.name == "needs-review")`}},
			Exclude: []configfile.Filter{{Name: "drafts", Expr: "pr.draft"}},
		},
		Agent: configfile.AgentSettings{MaxSteps: 30, MaxToolOutputBytes: 1000, MaxTokens: 5000, Timeout: 10 * time.Minute, Commands: []string{"rg"}},
		Review: configfile.Review{
			RequireSuggestedFix: true,
			Templates:           configfile.ReviewTemplates{Summary: "docs/summary.tmpl"}, InlineComments: true,
			Rules: []configfile.Rule{{ID: "wrap-errors", Rule: "Wrap errors."}, {ID: "house-style", File: "docs/rules.md"}},
		},
		Providers: []string{"own", "p"},
	}
}

func TestMerge(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		doc  string
		want func(*configfile.Settings)
		// include and exclude are the labels of the file's own conditions.
		include []string
		exclude []string
		dropped []string
		wantErr string
	}{
		{name: "no file"},
		{
			name: "the file narrows, appends file rules and replaces presentation",
			doc: "enabled: false\ntrigger: { exclude: [{ expr: pr.draft }] }\nignore: [gen/**, vendor/**]\n" +
				"rules: [{ id: repo-style, file: .kritika/rules.md }, { id: sql, file: .kritika/sql.md, paths: ['**/*.sql'] }]\n" +
				"comments:\n  finding: .kritika/inline.tmpl\n",
			want: func(s *configfile.Settings) {
				s.Enabled, s.Ignore = false, []string{"vendor/**", "gen/**"}
				s.Review.Rules = append(s.Review.Rules, configfile.Rule{ID: "repo-style", File: ".kritika/rules.md"},
					configfile.Rule{ID: "sql", File: ".kritika/sql.md", Paths: []string{"**/*.sql"}})
				s.Review.Templates.Inline = ".kritika/inline.tmpl"
			},
			exclude: []string{"pr.draft"},
		},
		{
			name: "an admin's file rule stays as the admin wrote it", doc: "rules: [{ id: house-style, file: docs/rules.md, paths: ['**/*.sql'] }]\n",
			dropped: []string{".kritika.yaml: rules house-style was dropped: an admin's rule has that id"},
		},
		{
			name: "context files follow the admin's", doc: "context: [{ path: db/schema.sql, description: the schema, paths: ['**/*.sql'] }]\n",
			want: func(s *configfile.Settings) {
				s.Review.Context = append(s.Review.Context, configfile.ContextFile{Path: "db/schema.sql", Description: "the schema", Paths: []string{"**/*.sql"}})
			},
		},
		{name: "a context file without a description", doc: "context: [{ path: db/schema.sql }]\n", wantErr: "description is required"},
		{
			name: "rules follow the admin's, and one with an admin's id is dropped",
			doc:  "rules:\n  - { id: wrap-errors, rule: Anything goes. }\n  - { id: no-tokens, rule: Never log a token., paths: ['**/*.go'] }\n",
			want: func(s *configfile.Settings) {
				s.Review.Rules = append(s.Review.Rules, configfile.Rule{ID: "no-tokens", Rule: "Never log a token.", Paths: []string{"**/*.go"}})
			},
			dropped: []string{".kritika.yaml: rules wrap-errors was dropped: an admin's rule has that id"},
		},
		{name: "a rule without an id", doc: "rules: [{ rule: Never log a token. }]\n", wantErr: `rules[0].id "" must be`},
		{
			name: "a rule may say when it applies",
			doc:  "rules: [{ id: renovate, rule: Say what breaks., whenExpr: 'pr.headRef.startsWith(\"renovate/\")' }]\n",
			want: func(s *configfile.Settings) {
				s.Review.Rules = append(s.Review.Rules, configfile.Rule{ID: "renovate", Rule: "Say what breaks.", WhenExpr: `pr.headRef.startsWith("renovate/")`})
			},
		},
		{name: "a rule whose whenExpr fails the smoke test", doc: "rules: [{ id: a, rule: x, whenExpr: 'pr.labels[5].name == \"x\"' }]\n", wantErr: "rules[0].whenExpr: smoke test"},
		{name: "enabled true cannot widen", doc: "enabled: true\n"},
		{
			name: "presentation replaces the admin's", doc: "comments: { inline: false, summary: .kritika/summary.tmpl }\n",
			want: func(s *configfile.Settings) {
				s.Review.InlineComments, s.Review.Templates.Summary = false, ".kritika/summary.tmpl"
			},
		},
		{
			name: "feedback replaces the admin's", doc: "review: { feedback: minimal }\n",
			want: func(s *configfile.Settings) { s.Review.Feedback = configfile.FeedbackMinimal },
		},
		{
			name: "an unknown feedback level is dropped", doc: "review: { feedback: exhaustive }\n",
			dropped: []string{`.kritika.yaml: review.feedback "exhaustive" was dropped; allowed: detailed, standard or minimal`},
		},
		{
			name: "approve replaces the admin's", doc: "review: { approve: true }\n",
			want: func(s *configfile.Settings) { s.Review.Approve = true },
		},
		{
			name: "review.fixes may only turn on", doc: "review: { fixes: false }\n",
			dropped: []string{".kritika.yaml: review.fixes false was dropped; allowed: true, since an admin requires a suggested fix"},
		},
		{
			name: "a model of a provider the account may use",
			doc:  "review: { model: own/small, fallback: p/big }\n",
			want: func(s *configfile.Settings) {
				s.Models = configfile.Models{Review: "own/small", Fallback: "p/big"}
			},
		},
		{
			name: "a model of another provider, or no model, is dropped", doc: "review: { model: q/big, fallback: p }\n",
			dropped: []string{
				`.kritika.yaml: review.model "q/big" was dropped; allowed: a model of own, p`,
				`.kritika.yaml: review.fallback "p" was dropped; allowed: a model of own, p`,
			},
		},
		{
			name: "confidence replaces the admin's", doc: "confidence: { model: own/judge, threshold: 0 }\n",
			want: func(s *configfile.Settings) {
				s.Confidence.Model, s.Confidence.Threshold = "own/judge", 0
			},
		},
		{
			name: "a confidence model of another provider and a threshold off the scale are dropped",
			doc:  "confidence: { model: q/judge, threshold: 6 }\n",
			dropped: []string{
				`.kritika.yaml: confidence.threshold 6 was dropped; allowed: 0 to 5`,
				`.kritika.yaml: confidence.model "q/judge" was dropped; allowed: a model of own, p`,
			},
		},
		{
			name: "confidence.risk may only lower the admin's", doc: "confidence: { risk: low }\n",
			want: func(s *configfile.Settings) { s.Confidence.Risk = review.RiskLow },
		},
		{
			name: "a confidence.risk above the admin's, or no level, is dropped", doc: "confidence: { risk: critical }\n",
			dropped: []string{
				`.kritika.yaml: confidence.risk "critical" was dropped; allowed: medium or lower`,
			},
		},
		{name: "the risk instructions are the admin's alone", doc: "confidence: { instructions: all low }\n", wantErr: "field instructions not found"},
		{
			name: "an inclusion under an admin's name is dropped", include: []string{"mine", "pr.open"},
			doc:     "trigger: { include: [{ name: wanted, expr: 'true' }, { name: mine, expr: 'true' }, { expr: pr.open }] }\n",
			dropped: []string{`.kritika.yaml: trigger.include wanted was dropped: an admin's condition has that name`},
		},
		{
			name: "an exclusion under an admin's name is dropped", exclude: []string{"mine", "pr.fork"},
			doc:     "trigger: { exclude: [{ name: drafts, expr: 'false' }, { name: mine, expr: 'false' }, { expr: pr.fork }] }\n",
			dropped: []string{`.kritika.yaml: trigger.exclude drafts was dropped: an admin's condition has that name`},
		},
		{
			name: "a name of the admin's other list is dropped too", include: []string{"mine"}, exclude: []string{"mine"},
			doc: "trigger: { include: [{ name: drafts, expr: 'true' }, { name: mine, expr: 'true' }], " +
				"exclude: [{ name: mine, expr: 'false' }, { name: wanted, expr: 'false' }] }\n",
			dropped: []string{
				`.kritika.yaml: trigger.include drafts was dropped: an admin's condition has that name`,
				`.kritika.yaml: trigger.exclude wanted was dropped: an admin's condition has that name`,
			},
		},
		{name: "a mode is no longer a key", doc: "mode: agentic\n", wantErr: "field mode not found"},
		{name: "agent limits are the admin's alone", doc: "agent: { steps: 5 }\n", wantErr: "field agent not found"},
		{name: "settle is the admin's alone", doc: "trigger: { settle: 1m }\n", wantErr: "field settle not found"},
		{name: "a secret reference does not decode", doc: "review: { model: { env: KEY } }\n", wantErr: "cannot unmarshal"},
		{name: "a file that does not parse leaves the admin's settings", doc: "unknown: 1\n", wantErr: "unknown"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var doc []byte
			if tt.doc != "" {
				doc = []byte(tt.doc)
			}
			op, want := adminSettings(), adminSettings()
			if tt.want != nil {
				tt.want(&want)
			}
			m, err := Merge(doc, op)
			if (err != nil) != (tt.wantErr != "") || (err != nil && !strings.Contains(err.Error(), tt.wantErr)) {
				t.Fatalf("err = %v, want %q", err, tt.wantErr)
			}
			if !reflect.DeepEqual(m.Settings, want) {
				t.Fatalf("settings = %+v\nwant       %+v", m.Settings, want)
			}
			if !slices.Equal(labels(m.InRepoFilters.Include), tt.include) || !slices.Equal(labels(m.InRepoFilters.Exclude), tt.exclude) ||
				!slices.Equal(m.Dropped, tt.dropped) {
				t.Fatalf("filters=%v dropped=%q", m.InRepoFilters, m.Dropped)
			}
			if !reflect.DeepEqual(op, adminSettings()) {
				t.Fatal("Merge changed the admin's settings")
			}
		})
	}
}

// labels lists what the conditions fs are called.
func labels(fs []configfile.Filter) []string {
	out := make([]string, 0, len(fs))
	for _, f := range fs {
		out = append(out, f.Label())
	}
	return out
}

func TestMergedCheck(t *testing.T) {
	t.Parallel()
	pr := PullRequest{Number: 3, Title: "t", Body: "please [skip-review]", State: "open", Labels: []byte(`[{"name":"deps"}]`)}
	vars, err := pr.Vars()
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name    string
		doc     string
		changed []string
		want    SkipReason
		wantErr bool
		// filter is the label of the exclusion that held, or of a condition
		// that failed to evaluate.
		filter string
	}{
		{"nothing to skip", "", []string{"main.go"}, "", false, ""},
		{"disabled", "enabled: false\n", []string{"main.go"}, SkipDisabled, false, ""},
		{"the exclusion that holds is named", "trigger:\n  exclude: [{ expr: 'false' }, { name: marker, expr: 'pr.body.contains(\"[skip-review]\")' }]\n",
			[]string{"main.go"}, SkipFiltered, false, "marker"},
		{"an exclusion without a name", "trigger:\n  exclude: [{ expr: 'pr.body.contains(\"[skip-review]\")' }]\n", []string{"main.go"}, SkipFiltered, false, `pr.body.contains("[skip-review]")`},
		{"no exclusion holds", "trigger:\n  exclude: [{ name: drafts, expr: pr.draft }]\n", []string{"main.go"}, "", false, ""},
		{"no inclusion holds", "trigger:\n  include: [{ name: drafts, expr: pr.draft }, { expr: 'pr.number == 4' }]\n", []string{"main.go"}, SkipFiltered, false, ""},
		{"an inclusion holds", "trigger:\n  include: [{ expr: pr.draft }, { expr: 'pr.number == 3 && pr.open && pr.labels[0].name == \"deps\"' }]\n", []string{"main.go"}, "", false, ""},
		{"an exclusion wins over an inclusion", "trigger:\n  include: [{ expr: pr.open }]\n  exclude: [{ name: deps, expr: 'pr.labels.exists(l, l.name == \"deps\")' }]\n",
			[]string{"main.go"}, SkipFiltered, false, "deps"},
		{"filter that fails to evaluate skips", "trigger:\n  include: [{ expr: 'pr.number == 1 || pr.labels[9].name == \"x\"' }]\n", []string{"main.go"}, SkipFiltered, true, `pr.number == 1 || pr.labels[9].name == "x"`},
		{"only ignored paths", "ignore: [docs/**]\n", []string{"docs/a.md"}, SkipOnlyPaths, false, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m, err := Merge([]byte(tt.doc), configfile.Settings{Enabled: true})
			if err != nil {
				t.Fatal(err)
			}
			got, by, err := m.Check(vars, tt.changed)
			var filter string
			if by != nil {
				filter = by.Label()
			}
			if got != tt.want || (err != nil) != tt.wantErr || filter != tt.filter {
				t.Fatalf("Check = %q, %q, %v; want %q, %q", got, filter, err, tt.want, tt.filter)
			}
		})
	}
	for r, want := range map[SkipReason]string{
		SkipDisabled: "disabled in .kritika.yaml", SkipFiltered: "filtered by .kritika.yaml", SkipOnlyPaths: "only ignored paths changed",
	} {
		if !r.Valid() || r.Description() != want {
			t.Fatalf("%q.Description() = %q, want %q", r, r.Description(), want)
		}
	}
	if SkipReason("other").Valid() {
		t.Fatal("an unknown reason must not be valid")
	}
}

func TestPullRequestVars(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	pr := PullRequest{Number: 7, Title: "Add b", Author: "octocat", State: "closed", Merged: true, Draft: true, Fork: true,
		HeadRef: "f", HeadSHA: "abc", BaseRef: "main", URL: "https://forge.example.com/acme/widgets/pulls/7", Body: "Adds b.",
		CreatedAt: at, Labels: []byte(`[{"name":"deps","color":"ededed"}]`), Event: "manual"}
	vars, err := pr.Vars()
	if err != nil {
		t.Fatal(err)
	}
	if vars["number"] != 7 || vars["open"] != false || vars["merged"] != true || vars["body"] != "Adds b." ||
		vars["createdAt"] != at || vars["headSha"] != "abc" || len(vars["labels"].([]any)) != 1 || vars["event"] != "manual" || len(vars) != 16 {
		t.Fatalf("vars = %v", vars)
	}
	// A pull request that crossed a JSON job document keeps its types.
	var back PullRequest
	if err := jsonRoundTrip(pr, &back); err != nil {
		t.Fatal(err)
	}
	again, err := back.Vars()
	if err != nil || again["number"] != 7 || again["createdAt"] != at {
		t.Fatalf("round trip vars = %v, %v", again, err)
	}
	if empty, err := (PullRequest{}).Vars(); err != nil || len(empty["labels"].([]any)) != 0 {
		t.Fatalf("empty labels = %v, %v", empty["labels"], err)
	}
}

func jsonRoundTrip(in, out any) error {
	b, err := json.Marshal(in)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, out)
}

// TestVarsMatchFilterVars: a filter sees the same pr variable when ingest
// judges a webhook's pull request as when the worker judges its stored row.
func TestVarsMatchFilterVars(t *testing.T) {
	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	hook := webhook.PullRequest{
		Number: 7, Title: "t", Author: "a", State: "open", Merged: true, Draft: true, Fork: true,
		HeadRef: "f", HeadSHA: "abc", BaseRef: "main", URL: "https://x", Body: "b", CreatedAt: at,
		Labels: []webhook.Label{{Name: "bug", Color: "f00"}},
	}
	labels, err := json.Marshal(hook.LabelVars())
	if err != nil {
		t.Fatal(err)
	}
	stored := PullRequest{
		Number: 7, Title: "t", Author: "a", State: "open", Merged: true, Draft: true, Fork: true,
		HeadRef: "f", HeadSHA: "abc", BaseRef: "main", URL: "https://x", Body: "b", CreatedAt: at,
		Labels: labels, Event: "opened",
	}
	got, err := stored.Vars()
	if err != nil {
		t.Fatal(err)
	}
	if want := hook.FilterVars("opened"); !reflect.DeepEqual(got, want) {
		t.Fatalf("Vars = %v\nFilterVars = %v", got, want)
	}
}
