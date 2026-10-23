package repoconfig

import (
	"encoding/json"
	"maps"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/model"
	"github.com/home-operations/kritika/internal/review"
	"github.com/home-operations/kritika/internal/webhook"
)

func adminSettings() configfile.Settings {
	return configfile.Settings{
		Enabled: true, Ignore: []string{"vendor/**"},
		Models:     configfile.Models{Review: "p/big", Effort: model.EffortHigh},
		Confidence: configfile.Confidence{Effort: model.EffortHigh, Threshold: 5, Risk: review.RiskMedium},
		Filters: configfile.Filters{
			Include: []configfile.Filter{{Name: "wanted", Expr: `pr.labels.exists(l, l.name == "needs-review")`}},
			Exclude: []configfile.Filter{{Name: "drafts", Expr: "pr.draft"}},
		},
		Agent: configfile.AgentSettings{MaxSteps: 30, MaxToolOutputBytes: 1000, MaxTokens: 5000, Timeout: 10 * time.Minute},
		Review: configfile.Review{
			RequireSuggestedFix: true,
			Templates:           configfile.ReviewTemplates{Summary: "docs/summary.tmpl"}, InlineComments: true,
			Rules: []configfile.Rule{{ID: "wrap-errors", Rule: "Wrap errors."}, {ID: "house-style", File: "docs/rules.md"}},
		},
		Skills: configfile.Skills{
			Paths: []string{".agents/skills", ".claude/skills"},
			Scope: map[string]configfile.SkillScope{"db": {Paths: []string{"db/**"}}, "go": {Paths: []string{"**/*.go"}}},
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
			doc:  "rules: [{ id: renovate, rule: Say what breaks., when: [{ expr: 'pr.headRef.startsWith(\"renovate/\")' }] }]\n",
			want: func(s *configfile.Settings) {
				s.Review.Rules = append(s.Review.Rules, configfile.Rule{
					ID: "renovate", Rule: "Say what breaks.", When: []configfile.When{{Expr: `pr.headRef.startsWith("renovate/")`}},
				})
			},
		},
		{name: "a rule whose condition fails the smoke test", doc: "rules: [{ id: a, rule: x, when: [{ expr: 'pr.labels[5].name == \"x\"' }] }]\n", wantErr: "rules[0].when[0]: smoke test"},
		{name: "enabled true cannot widen", doc: "enabled: true\n"},
		{
			name: "presentation replaces the admin's", doc: "comments: { inline: false, summary: .kritika/summary.tmpl }\n",
			want: func(s *configfile.Settings) {
				s.Review.InlineComments, s.Review.Templates.Summary = false, ".kritika/summary.tmpl"
			},
		},
		{
			name: "approve replaces the admin's", doc: "review: { approve: true }\n",
			want: func(s *configfile.Settings) { s.Review.Approve = true },
		},
		{
			name: "approval conditions hold beside the admin's", doc: "review: { approve: { when: [{ name: bumps, expr: pr.bot }] } }\n",
			want: func(s *configfile.Settings) {
				s.Review.Approve, s.Review.ApproveWhenInRepo = true, []configfile.When{{Name: "bumps", Expr: "pr.bot"}}
			},
		},
		{name: "an approval block with no conditions", doc: "review: { approve: {} }\n", wantErr: "a block must list when conditions"},
		{name: "an approval condition on the diff", doc: "review: { approve: { when: [{ expr: 'pr.lines > 1' }] } }\n",
			wantErr: "review.approve.when[0]: pr.lines"},
		{
			name: "confidence conditions hold beside the admin's", doc: "confidence: { when: [{ expr: pr.draft }] }\n",
			want: func(s *configfile.Settings) { s.Confidence.WhenInRepo = []configfile.When{{Expr: "pr.draft"}} },
		},
		{name: "a confidence condition that does not compile", doc: "confidence: { when: [{ expr: 'pr.(' }] }\n", wantErr: "confidence.when[0]"},
		{
			name: "a gate's conditions replace the admin's", doc: "confidence: { gate: { when: [{ expr: pr.bot }] } }\n",
			want: func(s *configfile.Settings) {
				s.Confidence.Gate, s.Confidence.GateWhen = true, []configfile.When{{Expr: "pr.bot"}}
			},
		},
		{name: "a gate condition on the diff", doc: "confidence: { gate: { when: [{ expr: 'pr.lines > 1' }] } }\n", wantErr: "confidence.gate.when[0]: pr.lines"},
		{
			name: "diagram replaces the admin's", doc: "review: { diagram: true }\n",
			want: func(s *configfile.Settings) { s.Review.Diagram = true },
		},
		{
			name: "review.fixes may only turn on", doc: "review: { fixes: false }\n",
			dropped: []string{".kritika.yaml: review.fixes false was dropped; allowed: true, since an admin requires a suggested fix"},
		},
		{
			name: "a model of a provider the account may use",
			doc:  "review: { model: own/small, fallback: p/big }\n",
			want: func(s *configfile.Settings) {
				s.Models = configfile.Models{Review: "own/small", Fallback: "p/big", Effort: model.EffortHigh}
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
			name: "an effort replaces the admin's", doc: "review: { effort: xhigh }\nconfidence: { effort: minimal }\n",
			want: func(s *configfile.Settings) {
				s.Models.Effort, s.Confidence.Effort = model.EffortXHigh, model.EffortMinimal
			},
		},
		{
			name: "an effort written empty leaves it to the provider", doc: "review: { effort: '' }\n",
			want: func(s *configfile.Settings) { s.Models.Effort = "" },
		},
		{
			name: "an effort that is no level is dropped", doc: "review: { effort: turbo }\n",
			dropped: []string{`.kritika.yaml: review.effort "turbo" was dropped; allowed: none, minimal, low, medium, high, xhigh or max`},
		},
		{
			name: "confidence replaces the admin's", doc: "confidence: { model: own/judge, fallback: p/judge, threshold: 0, gate: true }\n",
			want: func(s *configfile.Settings) {
				s.Confidence.Model, s.Confidence.Fallback, s.Confidence.Threshold, s.Confidence.Gate = "own/judge", "p/judge", 0, true
			},
		},
		{
			name: "a confidence model or fallback of another provider and a threshold off the scale are dropped",
			doc:  "confidence: { model: q/judge, fallback: q/small, threshold: 6 }\n",
			dropped: []string{
				`.kritika.yaml: confidence.threshold 6 was dropped; allowed: 0 to 5`,
				`.kritika.yaml: confidence.model "q/judge" was dropped; allowed: a model of own, p`,
				`.kritika.yaml: confidence.fallback "q/small" was dropped; allowed: a model of own, p`,
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
		{
			name: "skill paths replace the admin's", doc: "skills: { paths: [docs/skills] }\n",
			want: func(s *configfile.Settings) { s.Skills.Paths = []string{"docs/skills"} },
		},
		{
			name: "empty skill paths turn skills off", doc: "skills: { paths: [] }\n",
			want: func(s *configfile.Settings) { s.Skills.Paths = []string{} },
		},
		{
			name: "skill scopes add to the admin's, the file's taking a name both give",
			doc:  "skills: { scope: { db: { paths: ['migrations/**'] }, renovate: { when: [{ expr: 'pr.headRef.startsWith(\"renovate/\")' }] } } }\n",
			want: func(s *configfile.Settings) {
				s.Skills.Scope["db"] = configfile.SkillScope{Paths: []string{"migrations/**"}}
				s.Skills.Scope["renovate"] = configfile.SkillScope{When: []configfile.When{{Expr: `pr.headRef.startsWith("renovate/")`}}}
			},
		},
		{name: "an absolute skill path", doc: "skills: { paths: [/x] }\n", wantErr: `repoconfig: skills.paths[0]: path "/x" must be relative`},
		{name: "a skill path outside the repository", doc: "skills: { paths: [a, ../x] }\n", wantErr: `skills.paths[1]: path "../x" escapes the repository`},
		{name: "a skill scope with a bad glob", doc: "skills: { scope: { db: { paths: ['['] } } }\n", wantErr: `repoconfig: skills.scope.db.paths[0] "[" is not a valid glob`},
		{name: "a skill scope whose condition does not compile", doc: "skills: { scope: { db: { when: [{ expr: 'pr.draft &&' }] } } }\n", wantErr: "skills.scope.db.when[0]"},
		{
			name: "a skill scope whose condition reads pr.lines", doc: "skills: { scope: { db: { when: [{ expr: pr.lines > 10 }] } } }\n",
			wantErr: "skills.scope.db.when[0]: pr.lines is known to a trigger condition alone",
		},
		{name: "a skill scope takes no other key", doc: "skills: { scope: { db: { allowed-tools: [x] } } }\n", wantErr: "field allowed-tools not found"},
		{name: "agent limits are the admin's alone", doc: "agent: { steps: 5 }\n", wantErr: "field agent not found"},
		{name: "a secret reference does not decode", doc: "review: { model: { env: KEY } }\n", wantErr: "cannot construct"},
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
		SkipDisabled: "disabled in .kritika.yaml", SkipFiltered: "filtered", SkipOnlyPaths: "only ignored paths changed",
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
	pr := PullRequest{Number: 7, Title: "Add b", Author: "octocat", State: "closed", Merged: true, Draft: true, Fork: true, Bot: true,
		HeadRef: "f", HeadSHA: "abc", BaseRef: "main", URL: "https://forge.example.com/acme/widgets/pulls/7", Body: "Adds b.",
		CreatedAt: at, Labels: []byte(`[{"name":"deps","color":"ededed"}]`), Event: "manual"}
	vars, err := pr.Vars()
	if err != nil {
		t.Fatal(err)
	}
	if vars["number"] != 7 || vars["open"] != false || vars["merged"] != true || vars["body"] != "Adds b." ||
		vars["createdAt"] != at || vars["headSha"] != "abc" || len(vars["labels"].([]any)) != 1 || vars["event"] != "manual" || vars["bot"] != true ||
		len(vars) != 17 {
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

func TestMergeContextCollision(t *testing.T) {
	admin := adminSettings()
	admin.Review.Context = []configfile.ContextFile{{Path: "AGENTS.md", Description: "admin conventions"}}
	got, err := Merge([]byte("context:\n  - {path: AGENTS.md, description: repository conventions, paths: ['svc/**']}\n"), admin)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Review.Context, admin.Review.Context) || !slices.Equal(got.Dropped,
		[]string{".kritika.yaml: context AGENTS.md was dropped: an admin context entry has that path"}) {
		t.Fatalf("context = %+v, dropped = %v", got.Review.Context, got.Dropped)
	}
}

// TestPullRequestVarsMatchSample: the stored pull request's variables are
// the keys the sample conditions are checked against, but pr.lines.
func TestPullRequestVarsMatchSample(t *testing.T) {
	t.Parallel()
	vars, err := PullRequest{}.Vars()
	if err != nil {
		t.Fatal(err)
	}
	sample := configfile.SamplePR()
	delete(sample, "lines")
	if got, want := slices.Sorted(maps.Keys(vars)), slices.Sorted(maps.Keys(sample)); !slices.Equal(got, want) {
		t.Fatalf("Vars keys = %q, SamplePR keys = %q", got, want)
	}
}

// TestMergeKeepsAdminApprovalConditions: a file's approve: true turns
// approval on without dropping the admin's conditions.
func TestMergeKeepsAdminApprovalConditions(t *testing.T) {
	t.Parallel()
	op := configfile.Settings{Review: configfile.Review{ApproveWhen: []configfile.When{{Expr: "pr.bot"}}}}
	m, err := Merge([]byte("review: { approve: true }\n"), op)
	if err != nil || !m.Review.Approve || len(m.Review.ApproveWhen) != 1 || len(m.Review.ApproveWhenInRepo) != 0 {
		t.Fatalf("merged review = %+v, %v", m.Review, err)
	}
}

// TestMergeReplacesAdminGate: a file's gate replaces the admin's,
// conditions and all, where its approval conditions hold beside them.
func TestMergeReplacesAdminGate(t *testing.T) {
	t.Parallel()
	op := configfile.Settings{Confidence: configfile.Confidence{Gate: true, GateWhen: []configfile.When{{Expr: "pr.draft"}}}}
	m, err := Merge([]byte("confidence: { gate: { when: [{ expr: pr.bot }] } }\n"), op)
	if err != nil || !m.Confidence.Gate || len(m.Confidence.GateWhen) != 1 || m.Confidence.GateWhen[0].Expr != "pr.bot" {
		t.Fatalf("merged confidence = %+v, %v; want the file's conditions alone", m.Confidence, err)
	}
	if m, err = Merge([]byte("confidence: { gate: true }\n"), op); err != nil || !m.Confidence.Gate || len(m.Confidence.GateWhen) != 0 {
		t.Fatalf("merged confidence = %+v, %v; want gate: true to gate every pull request", m.Confidence, err)
	}
}

// TestMergeAgentRoutes: a file's agents that replace the default pass
// replace the admin's, whole, or are dropped whole when one of their
// models may not be taken, and a file's own model replaces the admin's
// routes too.
func TestMergeAgentRoutes(t *testing.T) {
	t.Parallel()
	doc := "review:\n  model: own/small\n  agents:\n" +
		"    - { name: deep, model: p/big, effort: max, fallback: own/small, when: [{ paths: ['db/**'] }] }\n"
	m, err := Merge([]byte(doc), adminSettings())
	if err != nil || len(m.Dropped) > 0 {
		t.Fatalf("Merge = %v, dropped %q", err, m.Dropped)
	}
	if m.Models.Review != "own/small" || m.Models.Effort != model.EffortHigh || len(m.Models.Routes) != 1 {
		t.Fatalf("models = %+v; the file's model and agent, the admin's effort", m.Models)
	}
	picked, err := m.Models.Pick(configfile.SamplePR(), &configfile.Diff{Changed: []string{"db/0042.sql"}})
	if err != nil || picked.Review != "p/big" || picked.Effort != model.EffortMax || picked.Fallback != "own/small" || picked.Route != "deep (db/**)" {
		t.Fatalf("Pick = %+v, %v", picked, err)
	}

	op := adminSettings()
	op.Models.Routes = m.Models.Routes
	if m, err := Merge([]byte("review: { model: own/small }\n"), op); err != nil || m.Models.Review != "own/small" || m.Models.Routes != nil {
		t.Fatalf("models = %+v, %v; a file's model replaces the admin's agents", m.Models, err)
	}
	if m, err := Merge([]byte("review: { agents: [{ name: light, model: own/small }] }\n"), op); err != nil ||
		len(m.Models.Routes) != 1 || m.Models.Routes[0].Name != "light" {
		t.Fatalf("routes = %+v, %v; a file's agents replace the admin's", m.Models.Routes, err)
	}

	for _, tt := range []struct {
		name, agent, dropped string
	}{
		{"a model of another provider", "{ name: deep, model: q/big, when: [{ expr: pr.bot }] }",
			`.kritika.yaml: review.agents that replace the default pass were dropped: deep's model "q/big" is not a model of own, p`},
		{"a fallback of another provider", "{ name: deep, model: p/big, fallback: q/small, when: [{ expr: pr.bot }] }",
			`.kritika.yaml: review.agents that replace the default pass were dropped: deep's fallback "q/small" is not a model of own, p`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			m, err := Merge([]byte("review: { agents: ["+tt.agent+", { name: light, model: own/small }] }\n"), adminSettings())
			if err != nil || len(m.Dropped) != 1 || m.Dropped[0] != tt.dropped {
				t.Fatalf("Merge = %v, dropped %q, want %q", err, m.Dropped, tt.dropped)
			}
			if !reflect.DeepEqual(m.Models, adminSettings().Models) {
				t.Fatalf("models = %+v; the admin's stand", m.Models)
			}
		})
	}
	if _, err := Merge([]byte("review: { agents: [{ name: deep, model: p/big, effort: loud }] }\n"), adminSettings()); err == nil ||
		!strings.Contains(err.Error(), "effort must be") {
		t.Fatalf("err = %v; an agent's effort that is no level does not parse", err)
	}
}

// TestMergeAgents: a file adds specialists of its own, their models among
// the account's, and only rules and skill scopes to an admin's specialist,
// whose other keys it cannot touch; an agent under the name of an admin's
// agent of the other kind is dropped.
func TestMergeAgents(t *testing.T) {
	t.Parallel()
	op := adminSettings()
	op.Reviewers = map[string]configfile.Reviewer{
		"security": {
			Name: "security", Instructions: "Admin's.", Categories: []review.Category{review.CategorySecurity}, Paths: []string{"auth/**"},
			Rules:  []configfile.Rule{{ID: "no-secrets", Rule: "No secrets."}},
			Skills: configfile.SkillsSpec{Scope: map[string]configfile.SkillScope{"gha": {Paths: []string{".github/**"}}}},
		},
	}
	op.Models.Routes = []configfile.ModelRoute{{Name: "deep", Model: "p/big"}}
	m, err := Merge([]byte(`review:
  agents:
    - name: security
      categories: [security]
      enabled: false
      paths: ['**']
      rules: [{ id: pin-actions, rule: Pin actions. }, { id: no-secrets, rule: Weaker. }, { id: wrap-errors, rule: Weaker. }]
      skills: { scope: { gha: { load: false }, sql: { paths: ['**/*.sql'] } } }
    - { name: perf, categories: [performance], instructions: Mind allocations., model: own/fast, fallback: nope/x }
    - { name: deep, categories: [tests] }
`), op)
	if err != nil {
		t.Fatal(err)
	}
	sec := m.Reviewers["security"]
	if !sec.On() || sec.Instructions != "Admin's." || !slices.Equal(sec.Paths, []string{"auth/**"}) {
		t.Errorf("security = %+v, want the admin's on, its keys untouched", sec)
	}
	ids := make([]string, 0, len(sec.Rules))
	for _, r := range sec.Rules {
		ids = append(ids, r.ID)
	}
	if !slices.Equal(ids, []string{"no-secrets", "pin-actions"}) {
		t.Errorf("security's rules = %q, want the admin's then the file's under an id of its own", ids)
	}
	if got := sec.Skills.Scope; len(got) != 2 || got["gha"].Load != nil || len(got["sql"].Paths) != 1 {
		t.Errorf("security's skill scopes = %+v, want the admin's gha kept and the file's sql added", got)
	}
	if perf := m.Reviewers["perf"]; perf.Model != "own/fast" || perf.Fallback != "" || perf.Instructions != "Mind allocations." {
		t.Errorf("perf = %+v, want the file's with its fallback of no provider dropped", perf)
	}
	if _, ok := m.Reviewers["deep"]; ok || len(m.Models.Routes) != 1 {
		t.Errorf("a specialist under the admin's replacing agent's name was kept")
	}
	if op.Reviewers["security"].Rules[0].ID != "no-secrets" || len(op.Reviewers["security"].Rules) != 1 {
		t.Error("merging changed the admin's settings")
	}
	for _, want := range []string{
		"review.agents[0] (security) was dropped but for its rules and skills", "(security).rules no-secrets was dropped",
		"(security).rules wrap-errors was dropped", "(security).skills.scope.gha was dropped", `(perf).fallback "nope/x" was dropped`,
		"review.agents[2] (deep) was dropped: an admin's agent of the other kind has that name",
	} {
		if !slices.ContainsFunc(m.Dropped, func(d string) bool { return strings.Contains(d, want) }) {
			t.Errorf("dropped %q lacks %q", m.Dropped, want)
		}
	}
	if m, _ := Merge([]byte("review: { agents: [{ name: security, categories: [security], rules: [{ id: x, rule: X. }] }] }\n"), op); slices.ContainsFunc(
		m.Dropped, func(d string) bool { return strings.Contains(d, "dropped but for") }) {
		t.Errorf("adding a rule alone noted a drop: %q", m.Dropped)
	}
}

func TestMergeAgentsCap(t *testing.T) {
	t.Parallel()
	op := adminSettings()
	op.Reviewers = map[string]configfile.Reviewer{}
	for _, n := range []string{"a", "b", "c", "d", "e", "f"} {
		op.Reviewers[n] = configfile.Reviewer{Name: n, Categories: []review.Category{review.CategoryTests}}
	}
	op.Models.Routes = []configfile.ModelRoute{{Name: "deep", Model: "p/big"}}
	m, err := Merge([]byte("review: { agents: [{ name: x, categories: [tests] }, { name: y, categories: [tests] }] }\n"), op)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := m.Reviewers["x"]; !ok || len(m.Reviewers)+len(m.Models.Routes) != configfile.MaxReviewers {
		t.Fatalf("agents = %d, want the cap of %d with x added", len(m.Reviewers)+len(m.Models.Routes), configfile.MaxReviewers)
	}
	if !slices.ContainsFunc(m.Dropped, func(d string) bool { return strings.Contains(d, "review.agents[1] (y) was dropped") }) {
		t.Errorf("dropped %q, want y past the cap", m.Dropped)
	}
}

// TestReferencedAgents: a specialist's files are read from the merge base
// with the rest, and one outside the repository is refused.
func TestReferencedAgents(t *testing.T) {
	t.Parallel()
	f, err := Parse([]byte("review: { agents: [{ name: sec, categories: [security], file: docs/sec.md, rules: [{ id: r, file: docs/r.md }] }] }\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got := f.Referenced(); !slices.Equal(got, []string{"docs/sec.md", "docs/r.md"}) {
		t.Errorf("referenced = %q", got)
	}
	if _, err := Parse([]byte("review: { agents: [{ name: sec, categories: [security], file: /etc/passwd }] }\n")); err == nil {
		t.Error("an agent's absolute file was accepted")
	}
}
