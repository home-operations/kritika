package configfile

import (
	"fmt"
	"maps"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/home-operations/kritika/internal/jobtimeout"
	"github.com/home-operations/kritika/internal/model"
)

// fixture sets the variables testdata/full.yaml's secrets name and returns
// its content.
func fixture(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile("testdata/full.yaml")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("TEST_OPENROUTER_API_KEY", "sk-or-test")
	t.Setenv("TEST_LOCAL_KEY", "sk-ant-test\n")
	t.Setenv("TEST_PRIVATE_KEY", "-----BEGIN RSA PRIVATE KEY-----\nabc\n-----END RSA PRIVATE KEY-----\n")
	t.Setenv("TEST_WEBHOOK_SECRET", "whsec")
	t.Setenv("TEST_CLIENT_ID", "Iv1.fromenv")
	return string(raw)
}

func TestLoadFull(t *testing.T) {
	f, err := load(t, fixture(t))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	t.Run("providers resolve from env", func(t *testing.T) {
		if got := f.Providers["openrouter"].APIKeyValue().Value(); got != "sk-or-test" {
			t.Fatalf("openrouter key = %q", got)
		}
		if got := f.Providers["local"].APIKeyValue().Value(); got != "sk-ant-test" {
			t.Fatalf("local key = %q (trailing newline must be trimmed)", got)
		}
	})

	t.Run("records the variables its secrets came from", func(t *testing.T) {
		want := []string{"TEST_CLIENT_ID", "TEST_LOCAL_KEY", "TEST_OPENROUTER_API_KEY", "TEST_PRIVATE_KEY", "TEST_WEBHOOK_SECRET"}
		if got := f.SecretEnv(); !slices.Equal(got, want) {
			t.Fatalf("SecretEnv = %q, want %q", got, want)
		}
	})

	t.Run("secrets redact when formatted", func(t *testing.T) {
		s := f.Providers["openrouter"].APIKeyValue()
		for _, out := range []string{s.String(), fmt.Sprint(s), fmt.Sprintf("%v", s), fmt.Sprintf("%#v", s), fmt.Sprintf("%+v", s)} {
			if strings.Contains(out, "sk-or") {
				t.Fatalf("secret leaked through formatting: %q", out)
			}
		}
		if (Secret{}).String() != "" {
			t.Fatal("an empty secret should format as empty, so absence stays visible")
		}
	})

	t.Run("settings layer defaults, account, repository", func(t *testing.T) {
		ho, _ := f.Account(ForgeGitHub, "home-operations")
		od, _ := f.Account(ForgeGitHub, "onedr0p")
		tests := []struct {
			name     string
			account  *Account
			repo     string
			enabled  bool
			review   ModelRef
			conc     int
			perDay   int
			settle   time.Duration
			filterOK map[string]any   // a PR the effective filter must accept
			filterNo []map[string]any // PRs the effective filter must reject
		}{
			{
				name: "unlisted repo inherits account", account: ho, repo: "home-operations/other",
				enabled: true, review: "openrouter/openai/gpt-6-sol", conc: 3, perDay: 200,
				filterOK: SamplePR(), filterNo: []map[string]any{with(SamplePR(), "draft", true)},
			},
			{
				name: "listed repo applies its own settle", account: ho, repo: "home-operations/flate",
				enabled: true, review: "openrouter/openai/gpt-6-sol", conc: 3, perDay: 200,
				settle:   30 * time.Second,
				filterOK: SamplePR(),
			},
			{
				name: "repo exclusion adds to the inherited one", account: ho, repo: "home-operations/kopiur",
				enabled: true, review: "openrouter/openai/gpt-6-sol", conc: 3, perDay: 200,
				filterOK: SamplePR(), filterNo: []map[string]any{with(SamplePR(), "draft", true), with(SamplePR(), "labels", []any{map[string]any{"name": "skip-review", "color": "0"}})},
			},
			{
				name: "listed repo inherits enabled", account: ho, repo: "home-operations/charts-mirror",
				enabled: true, review: "openrouter/openai/gpt-6-sol", conc: 3, perDay: 200,
			},
			{
				name: "account overrides review model", account: od, repo: "onedr0p/home-ops",
				enabled: true, review: "local/claude-opus-5", conc: 3,
				settle:   2 * time.Minute,
				filterOK: SamplePR(),
			},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				s := f.Settings(tt.account, tt.repo)
				if s.Enabled != tt.enabled || s.Models.Review != tt.review ||
					s.Limits.Concurrency != tt.conc || s.Limits.ReviewsPerDay != tt.perDay ||
					s.Settle != tt.settle {
					t.Fatalf("Settings = %+v", s)
				}
				if s.Models.Fallback != "local/claude-sonnet-5" {
					t.Fatalf("fallback should inherit from defaults, got %q", s.Models.Fallback)
				}
				if tt.filterOK != nil {
					if skip, by, err := s.Filters.Skips(tt.filterOK, nil); err != nil || skip {
						t.Fatalf("filter should accept: by=%v err=%v", by, err)
					}
				}
				for _, pr := range tt.filterNo {
					if skip, _, err := s.Filters.Skips(pr, nil); err != nil || !skip {
						t.Fatalf("filter should reject %v: skip=%v err=%v", pr, skip, err)
					}
				}
			})
		}
	})

	t.Run("concurrency falls back to the default when unset everywhere", func(t *testing.T) {
		g := &File{Accounts: []Account{{Forge: ForgeGitHub, Name: "x"}}}
		if got := g.Settings(&g.Accounts[0], "x/y").Limits.Concurrency; got != DefaultConcurrency {
			t.Fatalf("concurrency = %d, want %d", got, DefaultConcurrency)
		}
	})
}

func TestConnectionCredentials(t *testing.T) {
	f, err := load(t, fixture(t))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	in, ok := f.Connection("sticky-gecko")
	if !ok || !in.Serves("Home-Operations") {
		t.Fatalf("Connection(sticky-gecko) = %v, %v", in, ok)
	}
	if in.App.PrivateKeyValue().Value() == "" || in.WebhookSecretValue().Value() != "whsec" || in.App.ClientIDValue() != "Iv1.xxxxxxxx" {
		t.Fatal("github app credentials not resolved")
	}
	br, _ := f.Connection("bot-ross")
	if br.App.ClientIDValue() != "Iv1.fromenv" {
		t.Fatalf("clientIdFrom not resolved: %q", br.App.ClientIDValue())
	}
	if _, ok := f.Connection("nope"); ok {
		t.Fatal("unknown connection should not resolve")
	}
}

func TestHashAndConnectionLookup(t *testing.T) {
	f, err := load(t, fixture(t))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(f.Hash()) != 64 {
		t.Fatalf("hash = %q", f.Hash())
	}
	// The environment overlay is part of the configuration, so a replica
	// with another one does not hash alike.
	t.Setenv("KRITIKA_TRIGGER_SETTLE", "90s")
	if g, err := load(t, fixture(t)); err != nil || g.Hash() == f.Hash() {
		t.Fatalf("hash unchanged by an overlay variable: %v", err)
	}
	ho, ok := f.Account(ForgeGitHub, "Home-Operations")
	if !ok || ho.Name != "home-operations" || ho.ID() != AccountID(ForgeGitHub, "home-operations") || ho.Slug() != "github/home-operations" {
		t.Fatalf("Account = %+v, %v", ho, ok)
	}
	if in := f.ConnectionFor(ho); in == nil || in.Name != "sticky-gecko" {
		t.Fatalf("ConnectionFor = %v", in)
	}
	if f.ConnectionFor(&Account{Forge: ForgeGitHub, Name: "someone-else"}) != nil {
		t.Fatal("an account no connection serves must not resolve")
	}
	if _, ok := f.Account(ForgeGitHub, "someone-else"); ok {
		t.Fatal("an account no connection serves must not run")
	}
}

func TestIgnore(t *testing.T) {
	f, err := load(t, fixture(t))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	ho, _ := f.Account(ForgeGitHub, "home-operations")
	got := f.Settings(ho, "home-operations/flate").Ignore
	if len(got) != len(DefaultIgnore)+1 || got[len(got)-1] != "**/testdata/**" {
		t.Fatalf("ignore = %v", got)
	}
	if n := len(f.Settings(ho, "home-operations/other").Ignore); n != len(DefaultIgnore) {
		t.Fatalf("unlisted repo ignore = %d globs, want defaults only", n)
	}
}

func TestDefaultIgnore(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"vendor/x/y.go", true},
		{"web/node_modules/a/index.js", true},
		{"Cargo.lock", true},
		{"web/pnpm-lock.yaml", true},
		{"infra/.terraform.lock.hcl", true},
		{"go.work.sum", true},
		{"api/v1/zz_generated.deepcopy.go", true},
		{"proto/api.pb.go", true},
		{"proto/api.pb.gw.go", true},
		{"web/dist/app.min.js", true},
		{"web/dist/app.js.map", true},
		{"internal/generated/schema.go", true},
		{"logs/run.log", true},
		{"go.mod", false},
		{"package.json", false},
		{"tsconfig.json", false},
		{".sops.yaml", false},
		{"kubernetes/apps/foo/secret.sops.yaml", false},
		{"web/dist/index.html", false},
		{"build/ci.sh", false},
		{"charts/kritika/Chart.yaml", false},
		{"CHANGELOG.md", false},
		{"kubernetes/apps/foo/crds/bar.yaml", false},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			got := false
			for _, g := range DefaultIgnore {
				if ok, err := doublestar.Match(g, tc.path); err != nil {
					t.Fatalf("Match(%q): %v", g, err)
				} else if ok {
					got = true
				}
			}
			if got != tc.want {
				t.Fatalf("ignored = %v, want %v", got, tc.want)
			}
		})
	}
}

// filterExprs lists the expressions of fs, in order.
func filterExprs(fs []Filter) string {
	exprs := make([]string, len(fs))
	for i, f := range fs {
		exprs[i] = f.Expr
	}
	return strings.Join(exprs, ", ")
}

func TestFilterOnBody(t *testing.T) {
	prg, err := compileFilter(`pr.body.contains("[skip-review]")`)
	if err != nil {
		t.Fatalf("compileFilter: %v", err)
	}
	marked := with(SamplePR(), "body", "please review\n\n[skip-review]")
	if ok, err := prg.Eval(marked); err != nil || !ok {
		t.Fatalf("filter should accept a body containing the marker: ok=%v err=%v", ok, err)
	}
	if ok, err := prg.Eval(SamplePR()); err != nil || ok {
		t.Fatalf("filter should reject the sample body: ok=%v err=%v", ok, err)
	}
}

func with(pr map[string]any, k string, v any) map[string]any {
	out := make(map[string]any, len(pr))
	maps.Copy(out, pr)
	out[k] = v
	return out
}

// minimal is the smallest valid file; cases mutate it.
var minimal = githubMinimal("clientId: Iv1.acme, ")

// githubMinimal is the smallest configuration: one app serving acme;
// clientFields is spliced into the app's entry.
func githubMinimal(clientFields string) string {
	return `
apps:
  acme-bot: { accounts: [acme], ` + clientFields + `privateKey: { env: TEST_PRIVATE_KEY }, webhookSecret: { env: TEST_WEBHOOK_SECRET } }
`
}

// acme is minimal with entries, lines of the repositories map, such as
// "  acme/*: { trigger: { settle: 1m } }\n".
func acme(entries string) string {
	if entries == "" {
		return minimal
	}
	return minimal + "repositories:\n" + entries
}

// acmeAccount is minimal with keys, lines of acme's accounts entry.
func acmeAccount(keys string) string { return minimal + "accounts:\n  acme:\n" + keys }

// loadBytes is load for a document in bytes.
func loadBytes(t *testing.T, raw []byte) (*File, error) {
	t.Helper()
	return load(t, string(raw))
}

// TestEnabledDefault checks enabled resolves like the other overrides: the
// defaults and an account set where a repository starts, entry or not,
// until an admin turns it on or off.
func TestEnabledDefault(t *testing.T) {
	t.Setenv("TEST_PRIVATE_KEY", "tok")
	t.Setenv("TEST_WEBHOOK_SECRET", "whsec")
	tests := []struct {
		name, doc string
		want      map[string]bool
	}{
		{"on unless turned off", acme("  acme/listed: { trigger: { settle: 1m } }\n"),
			map[string]bool{"acme/new": true, "acme/listed": true}},
		{"off at the defaults", "enabled: false\n" + acme("  acme/listed: {}\n"),
			map[string]bool{"acme/new": false, "acme/listed": false}},
		{"owner/* over the defaults", "enabled: false\n" + acme("  acme/*: { enabled: true }\n"),
			map[string]bool{"acme/new": true}},
		{"off at owner/*", acme("  acme/*: { enabled: false }\n  acme/listed: {}\n"),
			map[string]bool{"acme/new": false, "acme/listed": false}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := mustLoad(t, tt.doc)
			for repo, want := range tt.want {
				if got := f.Settings(&f.Accounts[0], repo).Enabled; got != want {
					t.Errorf("%s enabled = %v, want %v", repo, got, want)
				}
			}
		})
	}
}

// TestRuns checks which repositories run once the forge says what they
// are: an archived one never, one an admin turned on or off as they chose,
// a fork not otherwise, and the rest as enabled resolves.
func TestRuns(t *testing.T) {
	t.Setenv("TEST_PRIVATE_KEY", "tok")
	t.Setenv("TEST_WEBHOOK_SECRET", "whsec")
	archived, fork := RepoTraits{Archived: true}, RepoTraits{Fork: true}
	tests := []struct {
		name, doc, repo string
		traits          RepoTraits
		want            bool
	}{
		{"a source repository", acme(""), "acme/app", RepoTraits{}, true},
		{"a source repository turned off", acme("  acme/*: { enabled: false }\n"), "acme/app", RepoTraits{}, false},
		{"a fork", acme(""), "acme/copy", fork, false},
		{"a fork owner/* turns on", acme("  acme/*: { enabled: true }\n"), "acme/copy", fork, false},
		{"a fork with an entry", acme("  acme/copy: { trigger: { settle: 1m } }\n"), "acme/copy", fork, false},
		{"an archived repository", acme(""), "acme/old", archived, false},
		{"a repository an admin turned off", acme("  acme/*: { enabled: true }\n"), "acme/app", RepoTraits{TurnedOn: new(false)}, false},
		{"a repository an admin turned on", acme("  acme/*: { enabled: false }\n"), "acme/app", RepoTraits{TurnedOn: new(true)}, true},
		{"a fork an admin turned on", acme(""), "acme/copy", RepoTraits{Fork: true, TurnedOn: new(true)}, true},
		{"an archived repository an admin turned on", acme(""), "acme/old", RepoTraits{Archived: true, TurnedOn: new(true)}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := mustLoad(t, tt.doc)
			if got := f.Runs(&f.Accounts[0], tt.repo, tt.traits); got != tt.want {
				t.Errorf("Runs(%s, %+v) = %v, want %v", tt.repo, tt.traits, got, tt.want)
			}
		})
	}
}

func TestProviders(t *testing.T) {
	t.Setenv("TEST_PRIVATE_KEY", "tok")
	t.Setenv("TEST_WEBHOOK_SECRET", "whsec")
	tests := []struct {
		name    string
		yaml    string
		want    Provider
		pricing model.Pricing
	}{
		{
			name: "anthropic with pricing",
			yaml: "  p:\n    type: anthropic\n    apiKey: { env: TEST_WEBHOOK_SECRET }\n" +
				"    pricing:\n      acme-large: { input: 3, output: 15, cacheRead: 0.3, cacheWrite: 3.75 }\n",
			want:    Provider{Type: ProviderAnthropic},
			pricing: model.Pricing{"acme-large": {Input: 3, Output: 15, CacheRead: 0.3, CacheWrite: 3.75}},
		},
		{
			name: "anthropic behind a gateway",
			yaml: "  p:\n    type: anthropic\n    baseUrl: https://gw.example.com/\n    apiKey: { env: TEST_WEBHOOK_SECRET }\n",
			want: Provider{Type: ProviderAnthropic, BaseURL: "https://gw.example.com/"},
		},
		{
			name: "openai at the SDK default url",
			yaml: "  p:\n    type: openai\n    apiKey: { env: TEST_WEBHOOK_SECRET }\n",
			want: Provider{Type: ProviderOpenAI},
		},
		{
			name: "openrouter behind a proxy",
			yaml: "  p:\n    type: openrouter\n    baseUrl: https://proxy.example.com/api/v1\n    apiKey: { env: TEST_WEBHOOK_SECRET }\n",
			want: Provider{Type: ProviderOpenRouter, BaseURL: "https://proxy.example.com/api/v1"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, err := loadBytes(t, []byte("providers:\n"+tt.yaml+minimal))
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			p := f.Providers["p"]
			if p.Type != tt.want.Type || p.BaseURL != tt.want.BaseURL || p.APIKeyValue().Value() != "whsec" {
				t.Fatalf("provider = %+v", p)
			}
			if !maps.Equal(p.Pricing, tt.pricing) {
				t.Fatalf("pricing = %v, want %v", p.Pricing, tt.pricing)
			}
		})
	}
}

// TestAccountProviders: an account's own provider serves its models, and only
// its.
func TestAccountProviders(t *testing.T) {
	t.Setenv("TEST_PRIVATE_KEY", "tok")
	t.Setenv("TEST_WEBHOOK_SECRET", "whsec")
	withOwn := minimal + "accounts:\n  acme:\n    providers:\n" +
		"      own: { type: openai, baseUrl: http://llm.internal:4000/v1, apiKey: { env: TEST_WEBHOOK_SECRET } }\n" +
		"repositories:\n  acme/*: { review: { model: own/big } }\n"
	f, err := loadBytes(t, []byte(withOwn))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	ten := &f.Accounts[0]
	if p, ok := f.Provider(ten, "own"); !ok || p.Type != ProviderOpenAI || p.APIKeyValue().Value() != "whsec" {
		t.Fatalf("Provider(acme, own) = %+v, %v", p, ok)
	}
	if _, ok := f.Provider(nil, "own"); ok {
		t.Fatal("an account's provider must not be the instance's")
	}
	refused := []struct{ name, yaml, want string }{
		{"a name a model reference cannot carry", strings.Replace(withOwn, "      own:", "      Own:", 1), "a provider name must be lowercase"},
		{"a name the instance already uses", "providers:\n  own: { type: anthropic, apiKey: { env: TEST_WEBHOOK_SECRET } }\n" + withOwn,
			"the instance declares a provider by that name"},
		{"the defaults naming an account's provider", "review: { model: own/big }\n" + withOwn, "not declared under providers"},
		{"an invalid provider", strings.Replace(withOwn, "type: openai", "type: gemini", 1), "accounts.acme.providers.own.type must be"},
	}
	for _, tt := range refused {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := loadBytes(t, []byte(tt.yaml)); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Parse = %v, want an error containing %q", err, tt.want)
			}
		})
	}
}

func TestParseRejects(t *testing.T) {
	t.Setenv("TEST_PRIVATE_KEY", "tok")
	t.Setenv("TEST_WEBHOOK_SECRET", "whsec")
	t.Setenv("TEST_EMPTY", "")

	if _, err := loadBytes(t, []byte(minimal)); err != nil {
		t.Fatalf("minimal fixture must parse: %v", err)
	}

	tests := []struct {
		name string
		yaml string
		want string // substring of the error
	}{
		{"unknown top-level key", minimal + "account: []\n", "field account not found"},
		{"unknown nested key", strings.Replace(minimal, "accounts: [acme]", "accounts: [acme], owner: acme", 1), "field owner not found"},
		{"a forge key", strings.Replace(minimal, "acme-bot: { ", "acme-bot: { forge: github, ", 1), "field forge not found"},
		{"bad app name", strings.Replace(minimal, "acme-bot:", "Acme Bot:", 1), "lowercase"},
		{"duplicate app", minimal + "  acme-bot: { accounts: [other], clientId: x, " +
			"privateKey: { env: TEST_PRIVATE_KEY }, webhookSecret: { env: TEST_WEBHOOK_SECRET } }\n", "already defined"},
		{"an account two apps serve", minimal + "  acme-two: { accounts: [ACME], clientId: x, " +
			"privateKey: { env: TEST_PRIVATE_KEY }, webhookSecret: { env: TEST_WEBHOOK_SECRET } }\n", "an account is served by one app"},
		{"duplicate account entry", minimal + "accounts:\n  acme: {}\n  Acme: {}\n", "duplicates accounts.Acme"},
		{"an account entry with its owner", minimal + "accounts:\n  acme/x: {}\n", "must be the account's name"},
		{"an account entry with an unknown key", minimal + "accounts:\n  acme: { mode: single }\n", "field mode not found"},
		{"missing accounts", strings.Replace(minimal, "accounts: [acme], ", "", 1), "accounts must list at least one account"},
		{"blank account", strings.Replace(minimal, "accounts: [acme]", "accounts: [acme, ' ']", 1), `accounts[1] " " must be the account's name`},
		{"a served account with its owner", strings.Replace(minimal, "accounts: [acme]", "accounts: [acme/x]", 1), `accounts[0] "acme/x" must be the account's name`},
		{"account listed twice", strings.Replace(minimal, "accounts: [acme]", "accounts: [acme, ACME]", 1), `accounts[1] "ACME" is listed twice`},
		{"an app without a client id", githubMinimal(""), "apps.acme-bot.clientId is required"},
		{"a client id reference with an unknown key", githubMinimal("clientId: { vault: x }, "), "field vault not found"},
		{"missing private key", strings.Replace(minimal, "privateKey: { env: TEST_PRIVATE_KEY }, ", "", 1), "apps.acme-bot.privateKey: reference must set env"},
		{"unset env reference", strings.Replace(minimal, "TEST_PRIVATE_KEY", "TEST_DOES_NOT_EXIST", 1), "is not set"},
		{"empty env reference", strings.Replace(minimal, "TEST_PRIVATE_KEY", "TEST_EMPTY", 1), "privateKey is required"},
		{"a file reference", strings.Replace(minimal, "{ env: TEST_PRIVATE_KEY }", "{ file: /var/run/secrets/token }", 1), "field file not found"},
		{"empty reference", strings.Replace(minimal, "{ env: TEST_PRIVATE_KEY }", "{}", 1), "apps.acme-bot.privateKey: reference must set env"},
		{"unknown provider type", "providers:\n  p:\n    type: cohere\n    apiKey: { env: TEST_WEBHOOK_SECRET }\n" + minimal, "type must be"},
		{"negative pricing", "providers:\n  p:\n    type: anthropic\n    apiKey: { env: TEST_WEBHOOK_SECRET }\n" +
			"    pricing: { acme-large: { input: 3, output: -1 } }\n" + minimal, "providers.p.pricing.acme-large"},
		{"unknown pricing field", "providers:\n  p:\n    type: anthropic\n    apiKey: { env: TEST_WEBHOOK_SECRET }\n" +
			"    pricing: { acme-large: { prompt: 3 } }\n" + minimal, "field prompt not found"},
		{"relative base url", "providers:\n  p:\n    type: openai\n    baseUrl: gw.example.com/v1\n    apiKey: { env: TEST_WEBHOOK_SECRET }\n" + minimal,
			"must be an absolute URL"},
		{"anthropic without key", "providers:\n  p:\n    type: anthropic\n    apiKey: { env: TEST_EMPTY }\n" + minimal, "apiKey resolved to an empty value"},
		{"model without provider", "review:\n  model: gpt\n" + minimal, "<provider>/<model>"},
		{"model referencing undeclared provider", "review:\n  model: nope/gpt\n" + minimal, "not declared under providers"},
		{"owner/* model referencing undeclared provider", acme("  acme/*: { review: { model: nope/gpt } }\n"), "not declared under providers"},
		{"negative limit", "limits:\n  reviewsPerDay: -1\n" + minimal, "must not be negative"},
		{"negative account limit", acmeAccount("    limits: { tokensPerMonth: -1 }\n"), "accounts.acme.limits: limits must not be negative"},
		{"negative settle default", "trigger:\n  settle: -1s\n" + minimal, "configfile: trigger.settle must not be negative"},
		{"negative trigger limit", acme("  acme/x: { trigger: { limit: -1 } }\n"), "repositories.acme/x.trigger.limit must not be negative"},
		{"trigger.lines is not a key", acme("  acme/x: { trigger: { lines: 500 } }\n"), "field lines not found"},
		{"trigger condition with a bad glob", acme("  acme/x: { trigger: { include: [{ paths: ['['] }] } }\n"), `include[0]: paths[0] "[" is not a valid glob`},
		{"unknown feedback", "review:\n  feedback: exhaustive\n" + minimal, "configfile: review.feedback must be detailed, standard or minimal"},
		{"context without a description", "context: [{ path: db/schema.sql }]\n" + minimal, "configfile: context[0]: description is required"},
		{"context outside the repository", "context: [{ path: ../x, description: x }]\n" + minimal, "escapes the repository"},
		{"context with a bad glob", "context: [{ path: x, description: x, paths: ['['] }]\n" + minimal, "paths[0] \"[\" is not a valid glob"},
		{"rule with a bad id", "rules: [{ id: Wrap_Errors, rule: x }]\n" + minimal, `configfile: rules[0].id "Wrap_Errors" must be`},
		{"rule listed twice", acme("  acme/*: { rules: [{ id: a, rule: x }, { id: a, rule: y }] }\n"), `repositories.acme/*.rules[1].id "a" is listed twice`},
		{"blank rule", acme("  acme/x: { rules: [{ id: a, rule: ' ' }] }\n"), "repositories.acme/x.rules[0]: set one of rule or file"},
		{"overlong rule", "rules: [{ id: a, rule: " + strings.Repeat("x", MaxRuleChars+1) + " }]\n" + minimal, "over the 2000 allowed"},
		{"rule with a bad glob", "rules: [{ id: a, rule: x, paths: ['['] }]\n" + minimal, `rules[0].paths[0] "[" is not a valid glob`},
		{"rule whenExpr syntax error", acme("  acme/x: { rules: [{ id: a, rule: x, whenExpr: 'pr.draft &&' }] }\n"), "repositories.acme/x.rules[0].whenExpr"},
		{"rule whenExpr on pr.lines", "rules: [{ id: a, rule: x, whenExpr: pr.lines > 10 }]\n" + minimal,
			"configfile: rules[0].whenExpr: pr.lines is known to a trigger condition alone"},
		{"rule whenExpr not a bool", "rules: [{ id: a, rule: x, whenExpr: pr.title }]\n" + minimal, "configfile: rules[0].whenExpr"},
		{"negative settle at owner/*", acme("  acme/*: { trigger: { settle: -1s } }\n"), "repositories.acme/*.trigger.settle must not be negative"},
		{"negative settle repository", acme("  acme/x: { trigger: { settle: -1s } }\n"), "repositories.acme/x.trigger.settle must not be negative"},
		{"indexing role removed", "review:\n  indexing: p/m\n" + minimal, "field indexing not found"},
		{"bad ignore glob", acme("  acme/x: { ignore: ['['] }\n"), "not a valid glob"},
		{"include syntax error", "trigger:\n  include: [{ expr: 'pr.draft &&' }]\n" + minimal, "configfile: trigger.include[0]"},
		{"exclude syntax error", "trigger:\n  exclude: [{ expr: 'true' }, { expr: 'pr.draft &&' }]\n" + minimal, "configfile: trigger.exclude[1]"},
		{"include with no expression", "trigger:\n  include: [{ name: empty }]\n" + minimal, "configfile: trigger.include[0]: expr or paths is required"},
		{"exclude with a blank expression", "trigger:\n  exclude: [{ name: empty, expr: ' ' }]\n" + minimal, "configfile: trigger.exclude[0]: expr or paths is required"},
		{"include name given twice", "trigger:\n  include: [{ name: a, expr: 'true' }, { name: a, expr: 'true' }]\n" + minimal,
			`configfile: trigger.include[1]: name "a" is given twice`},
		{"exclude name given twice", "trigger:\n  exclude: [{ name: a, expr: 'true' }, { name: a, expr: 'true' }]\n" + minimal,
			`configfile: trigger.exclude[1]: name "a" is given twice`},
		{"include as a bare expression", "trigger:\n  include: 'true'\n" + minimal, "cannot unmarshal"},
		{"include fails smoke test", "trigger:\n  include: [{ expr: 'pr.labels[5].name == \"x\"' }]\n" + minimal, "smoke test"},
		{"repository include error", acme("  acme/x: { trigger: { include: [{ expr: 'pr.title' }] } }\n"), "repositories.acme/x.trigger.include[0]"},
		{"repository exclude error", acme("  acme/x: { trigger: { exclude: [{ expr: 'pr.title' }] } }\n"), "repositories.acme/x.trigger.exclude[0]"},
		{"owner exclude error", acme("  acme/*: { trigger: { exclude: [{ expr: 'pr.title' }] } }\n"), "repositories.acme/*.trigger.exclude[0]"},
		{"a single filter expression", "trigger:\n  filterExpr: 'true'\n" + minimal, "field filterExpr not found"},
		{"a forks switch", "trigger:\n  forks: true\n" + minimal, "field forks not found"},
		{"a repository key without an owner", acme("  x: {}\n"), "keyed owner/* or owner/name"},
		{"a repository key too deep", acme("  acme/x/y: {}\n"), "keyed owner/* or owner/name"},
		{"duplicate repository", acme("  acme/x: {}\n  ACME/x: {}\n"), "duplicates repositories.ACME/x"},
		{"a repository entry that says where it starts", acme("  acme/x: { enabled: false }\n"), "repositories.acme/x.enabled: turn a repository on or off in the dashboard"},
		{"a defaults wrapper", "defaults: { enabled: false }\n" + minimal, "field defaults not found"},
		{"a models block at the root", "models: { review: p/m }\n" + minimal, "field models not found"},
		{"a flat settle on an entry", acme("  acme/x: { settle: 1m }\n"), "field settle not found"},
		{"a file key that moved to the environment", "polling: { interval: 5m }\n" + minimal, "field polling not found"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := loadBytes(t, []byte(tt.yaml))
			if err == nil {
				t.Fatal("expected an error")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error %q does not mention %q", err, tt.want)
			}
		})
	}
}

// TestJobTimeoutBounds checks that runner.activeDeadlineSeconds and
// agent.timeout are accepted up to the point where River's job timeout cap
// would otherwise cut the runner or the review short, and rejected past it.
func TestJobTimeoutBounds(t *testing.T) {
	t.Setenv("TEST_PRIVATE_KEY", "tok")
	t.Setenv("TEST_WEBHOOK_SECRET", "whsec")

	withTimeout := func(seconds int64) string {
		return acme(fmt.Sprintf("  acme/x: { agent: { timeout: %ds } }\n", seconds))
	}

	tests := []struct {
		name string
		yaml string
		want string // substring of the error; empty means the config must be accepted
	}{
		{"agent timeout at the cap", withTimeout(int64(jobtimeout.MaxAgentTimeout.Seconds())), ""},
		{"agent timeout past the cap", withTimeout(int64(jobtimeout.MaxAgentTimeout.Seconds()) + 1), "agent.timeout must not exceed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := loadBytes(t, []byte(tt.yaml))
			if tt.want == "" {
				if err != nil {
					t.Fatalf("expected no error, got %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want it to mention %q", err, tt.want)
			}
		})
	}
}

func TestScopePrecedence(t *testing.T) {
	t.Setenv("TEST_PRIVATE_KEY", "tok")
	t.Setenv("TEST_WEBHOOK_SECRET", "whsec")
	const head = `providers:
  p: { type: openai, apiKey: { env: TEST_WEBHOOK_SECRET } }
review: { model: p/big, fallback: p/small, incremental: 3 }
trigger:
  include: [{ name: bots, expr: 'pr.author.startsWith("renovate")' }]
  exclude: [{ name: drafts, expr: pr.draft }]
  settle: 2m
  limit: 4
ignore: ["defaults/**"]
agent: { steps: 9 }
rules: [{ id: ops, file: ops/rules.md }]
comments: { summary: ops/summary.tmpl }
limits: { tokensPerMonth: 1000, reviewsPerDay: 5 }
`
	// doc gives acme's owner/* and acme/x entries the flow keys owner and
	// repo, and its accounts entry limits when there are any.
	doc := func(limits, owner, repo string) string {
		out := head + acme("  acme/*: { "+owner+" }\n  acme/x: { "+repo+" }\n")
		if limits != "" {
			out += "accounts:\n  acme: { limits: { " + limits + " } }\n"
		}
		return out
	}
	parse := func(t *testing.T, doc string) *File {
		t.Helper()
		f, err := loadBytes(t, []byte(doc))
		if err != nil {
			t.Fatalf("Parse: %v", err)
		}
		return f
	}

	t.Run("an account inherits what it leaves out", func(t *testing.T) {
		f := parse(t, doc("", "", ""))
		s := f.Settings(&f.Accounts[0], "acme/x")
		if filterExprs(s.Filters.Include) != `pr.author.startsWith("renovate")` || filterExprs(s.Filters.Exclude) != "pr.draft" || s.Settle != 2*time.Minute || s.MaxAutoReviews != 4 || s.Agent.MaxSteps != 9 ||
			s.Incremental.MaxDeltaFiles != 3 || s.Models.Fallback != "p/small" || s.Limits.TokensPerMonth != 1000 ||
			!reflect.DeepEqual(s.Review.Rules, []Rule{{ID: "ops", File: "ops/rules.md"}}) || s.Review.Templates.Summary != "ops/summary.tmpl" {
			t.Fatalf("inherited settings = %+v", s)
		}
	})

	t.Run("an empty or zero value written at a narrower scope clears", func(t *testing.T) {
		f := parse(t, doc("tokensPerMonth: 0", `trigger: { include: [{ name: bots, expr: "true" }, { expr: pr.open }], exclude: [{ name: drafts, expr: "false" }, { expr: pr.fork }], settle: 0s }, review: { fallback: "" }`, `comments: { summary: "" }`))
		s := f.Settings(&f.Accounts[0], "acme/x")
		// A condition under a broader scope's name replaces it where it
		// stands, and one without a name is added.
		if filterExprs(s.Filters.Include) != "true, pr.open" || filterExprs(s.Filters.Exclude) != "false, pr.fork" || s.Settle != 0 || s.Models.Fallback != "" || s.Models.Review != "p/big" ||
			s.Limits.TokensPerMonth != 0 || s.Limits.ReviewsPerDay != 5 {
			t.Fatalf("cleared settings = %+v", s)
		}
		if s.Review.Templates.Summary != "" {
			t.Fatalf("cleared review = %+v", s.Review)
		}
	})

	t.Run("the narrowest scope written wins, field by field", func(t *testing.T) {
		f := parse(t, doc("", `agent: { steps: 7 }, review: { fixes: true }, ignore: ["account/**"]`,
			`review: { model: p/small }, ignore: ["repo/**"], agent: { tokens: 500 }`))
		s := f.Settings(&f.Accounts[0], "acme/x")
		if s.Agent.MaxSteps != 7 || s.Agent.MaxTokens != 500 || s.Models.Review != "p/small" {
			t.Fatalf("settings = %+v", s)
		}
		if !s.Review.RequireSuggestedFix || !reflect.DeepEqual(s.Review.Rules, []Rule{{ID: "ops", File: "ops/rules.md"}}) {
			t.Fatalf("review = %+v, want the account's strictness over the defaults' rules", s.Review)
		}
		want := append(append([]string(nil), DefaultIgnore...), "defaults/**", "account/**", "repo/**")
		if !slices.Equal(s.Ignore, want) {
			t.Fatalf("ignore = %v, want %v", s.Ignore, want)
		}
	})

	t.Run("an explicit concurrency must be positive", func(t *testing.T) {
		if _, err := loadBytes(t, []byte(doc("concurrency: 0", "", ""))); err == nil ||
			!strings.Contains(err.Error(), "concurrency must be positive") {
			t.Fatalf("Parse = %v", err)
		}
	})
}

// TestReviewPresentation checks every finding goes inline, from a
// detailed review, unless a scope sets another feedback level or turns
// inline comments off.
func TestReviewPresentation(t *testing.T) {
	t.Setenv("TEST_PRIVATE_KEY", "tok")
	t.Setenv("TEST_WEBHOOK_SECRET", "whsec")
	parse := func(t *testing.T, owner, repos string) *File {
		t.Helper()
		f, err := loadBytes(t, []byte(acme("  acme/*: { "+owner+" }\n"+repos)))
		if err != nil {
			t.Fatalf("Parse: %v", err)
		}
		return f
	}
	f := parse(t, "", "  acme/x: {}\n")
	if s := f.Settings(&f.Accounts[0], ""); !s.Review.InlineComments || s.Review.Feedback != FeedbackDetailed {
		t.Fatalf("review = %+v, want every finding inline, from a detailed review", s.Review)
	}
	f = parse(t, "comments: { inline: false }, review: { feedback: minimal }",
		"  acme/x: { comments: { inline: true } }\n  acme/y: { review: { feedback: standard } }\n")
	if s := f.Settings(&f.Accounts[0], "acme/x"); !s.Review.InlineComments || s.Review.Feedback != FeedbackMinimal {
		t.Fatalf("review = %+v, want the account's feedback with the repository's inline comments", s.Review)
	}
	if s := f.Settings(&f.Accounts[0], "acme/y"); s.Review.Feedback != FeedbackStandard {
		t.Fatalf("review = %+v, want the repository's feedback over the account's", s.Review)
	}
	f = parse(t, "review: { approve: true }", "  acme/x: { review: { approve: false } }\n  acme/y: {}\n")
	if s := f.Settings(&f.Accounts[0], "acme/x"); s.Review.Approve {
		t.Fatalf("review = %+v, want the repository's approve over the account's", s.Review)
	}
	if s := f.Settings(&f.Accounts[0], "acme/y"); !s.Review.Approve {
		t.Fatalf("review = %+v, want the account's approve", s.Review)
	}
}

// TestSettingsProviders: a repository's settings name the providers its
// account may use, the instance's and its own, sorted.
func TestSettingsProviders(t *testing.T) {
	t.Setenv("TEST_PRIVATE_KEY", "tok")
	t.Setenv("TEST_WEBHOOK_SECRET", "whsec")
	f := mustLoad(t, "providers:\n  p: { type: openai, apiKey: { env: TEST_WEBHOOK_SECRET } }\n"+
		acmeAccount("    providers:\n      own: { type: openai, apiKey: { env: TEST_WEBHOOK_SECRET } }\n"))
	if got := f.Settings(&f.Accounts[0], "acme/x").Providers; !slices.Equal(got, []string{"own", "p"}) {
		t.Fatalf("providers = %q, want [own p]", got)
	}
}

// TestRulesAddUp: each scope's rules follow the broader scope's, and one
// with an id already listed replaces that rule where it stands.
func TestRulesAddUp(t *testing.T) {
	t.Setenv("TEST_PRIVATE_KEY", "tok")
	t.Setenv("TEST_WEBHOOK_SECRET", "whsec")
	f := mustLoad(t, "rules: [{ id: a, rule: A }, { id: b, rule: B }]\n"+
		acme("  acme/*: { rules: [{ id: c, rule: C }] }\n  acme/x: { rules: [{ id: a, rule: A2, paths: ['**/*.go'] }] }\n"))
	for repo, want := range map[string][]Rule{
		"acme/x":        {{ID: "a", Rule: "A2", Paths: []string{"**/*.go"}}, {ID: "b", Rule: "B"}, {ID: "c", Rule: "C"}},
		"acme/unlisted": {{ID: "a", Rule: "A"}, {ID: "b", Rule: "B"}, {ID: "c", Rule: "C"}},
	} {
		if got := f.Settings(&f.Accounts[0], repo).Review.Rules; !reflect.DeepEqual(got, want) {
			t.Errorf("%s: rules = %+v, want %+v", repo, got, want)
		}
	}
	for repo, want := range map[string]map[string]Scope{
		"acme/x":        {"a": ScopeRepository, "b": ScopeDefaults, "c": ScopeAccount},
		"acme/unlisted": {"a": ScopeDefaults, "b": ScopeDefaults, "c": ScopeAccount},
	} {
		if got := f.RuleScopes(&f.Accounts[0], repo); !maps.Equal(got, want) {
			t.Errorf("%s: scopes = %v, want %v", repo, got, want)
		}
	}
}

func TestRepositoryAgentReview(t *testing.T) {
	t.Setenv("TEST_PRIVATE_KEY", "tok")
	t.Setenv("TEST_WEBHOOK_SECRET", "whsec")
	withRepo := func(repo string) string {
		return acme("  acme/x: " + repo + "\n")
	}

	t.Run("defaults resolve when unset", func(t *testing.T) {
		f, err := loadBytes(t, []byte(withRepo("{}")))
		if err != nil {
			t.Fatal(err)
		}
		for _, repo := range []string{"acme/x", "acme/unlisted"} {
			s := f.Settings(&f.Accounts[0], repo)
			if !reflect.DeepEqual(s.Agent, DefaultAgent) || s.Incremental.MaxDeltaFiles != DefaultMaxDeltaFiles {
				t.Fatalf("%s: agent=%+v incremental=%+v", repo, s.Agent, s.Incremental)
			}
			if s.Review.RequireSuggestedFix || len(s.Review.Rules) != 0 || s.Review.Templates != (ReviewTemplates{}) {
				t.Fatalf("%s: review = %+v", repo, s.Review)
			}
		}
		if DefaultMaxDeltaFiles != 25 {
			t.Fatalf("DefaultMaxDeltaFiles = %d", DefaultMaxDeltaFiles)
		}
	})

	t.Run("repository values override the defaults", func(t *testing.T) {
		f, err := loadBytes(t, []byte(withRepo(`{
      agent: { steps: 12, output: 4096, tokens: 250000, timeout: 3m, commands: [curl, rg], commandTimeout: 10s },
      review: { incremental: 5, fixes: true },
      rules: [{ id: style, file: docs/rules.md }],
      comments: { summary: .kritika/summary.md.tmpl, finding: .kritika/inline.md.tmpl } }`)))
		if err != nil {
			t.Fatal(err)
		}
		s := f.Settings(&f.Accounts[0], "acme/x")
		want := AgentSettings{MaxSteps: 12, MaxToolOutputBytes: 4096, MaxTokens: 250_000, Timeout: 3 * time.Minute,
			Commands: []string{"curl", "rg"}, CommandTimeout: 10 * time.Second}
		if !reflect.DeepEqual(s.Agent, want) || s.Incremental.MaxDeltaFiles != 5 {
			t.Fatalf("agent=%+v incremental=%+v", s.Agent, s.Incremental)
		}
		if !s.Review.RequireSuggestedFix || len(s.Review.Rules) != 1 || s.Review.Rules[0].File != "docs/rules.md" ||
			s.Review.Templates.Summary != ".kritika/summary.md.tmpl" || s.Review.Templates.Inline != ".kritika/inline.md.tmpl" {
			t.Fatalf("review = %+v", s.Review)
		}
		if got := s.Review.Referenced(); strings.Join(got, ",") != "docs/rules.md,.kritika/summary.md.tmpl,.kritika/inline.md.tmpl" {
			t.Fatalf("referenced = %v", got)
		}
	})

	t.Run("a partial agent block keeps the other defaults", func(t *testing.T) {
		f, err := loadBytes(t, []byte(withRepo("{ agent: { steps: 7 } }")))
		if err != nil {
			t.Fatal(err)
		}
		want := DefaultAgent
		want.MaxSteps = 7
		if got := f.Settings(&f.Accounts[0], "acme/x").Agent; !reflect.DeepEqual(got, want) {
			t.Fatalf("agent = %+v, want %+v", got, want)
		}
	})

	rejects := []struct{ name, repo, want string }{
		{"a mode", "{ mode: agentic }", "field mode not found"},
		{"zero steps", "{ agent: { steps: 0 } }", "agent.steps must be positive"},
		{"negative steps", "{ agent: { steps: -1 } }", "agent.steps must be positive"},
		{"zero tool output", "{ agent: { output: 0 } }", "agent.output must be positive"},
		{"zero tokens", "{ agent: { tokens: 0 } }", "agent.tokens must be positive"},
		{"negative tokens", "{ agent: { tokens: -5 } }", "agent.tokens must be positive"},
		{"zero timeout", "{ agent: { timeout: 0s } }", "agent.timeout must be positive"},
		{"zero delta files", "{ review: { incremental: 0 } }", "review.incremental must be positive"},
		{"negative delta files", "{ review: { incremental: -3 } }", "review.incremental must be positive"},
		{"unknown agent key", "{ agent: { maxSteps: 3 } }", "field maxSteps not found"},
		{"zero command timeout", "{ agent: { commandTimeout: 0s } }", "agent.commandTimeout must be at least 1s"},
		{"sub-second command timeout", "{ agent: { commandTimeout: 500ms } }", "agent.commandTimeout must be at least 1s"},
		{"command path", "{ agent: { commands: [/usr/bin/curl] } }", "must be a bare command name"},
		{"relative command path", "{ agent: { commands: [./tool] } }", "must be a bare command name"},
		{"empty command", "{ agent: { commands: [''] } }", "must be a bare command name"},
		{"duplicate command", "{ agent: { commands: [rg, rg] } }", "is listed twice"},
		{"absolute rule file", "{ rules: [{ id: a, file: /etc/passwd }] }", "must be relative"},
		{"escaping template path", "{ comments: { summary: ../x.tmpl } }", "escapes the repository"},
		{"a rule with a file and text", "{ rules: [{ id: a, rule: Check., file: a.md }] }", "set one of rule or file"},
	}
	for _, tt := range rejects {
		t.Run("rejects "+tt.name, func(t *testing.T) {
			_, err := loadBytes(t, []byte(withRepo(tt.repo)))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want it to mention %q", err, tt.want)
			}
		})
	}
}
