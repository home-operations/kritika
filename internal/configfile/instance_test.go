package configfile

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/home-operations/kritika/internal/model"
	"github.com/home-operations/kritika/internal/review"
)

// fileWithDefaults is minimal's app with instance defaults: a provider,
// both default models and an embedder of the provider, and acme's entry.
const fileWithDefaults = `apps:
  acme-bot: { accounts: [acme], clientId: x, privateKey: { env: TEST_PRIVATE_KEY }, webhookSecret: { env: TEST_WEBHOOK_SECRET } }
providers:
  openrouter: { type: openrouter, apiKey: { env: TEST_PROVIDER_KEY } }
review: { model: openrouter/big, fallback: openrouter/small }
embedding: { model: openrouter/e1, dims: 8 }
accounts:
  acme: {}
`

func setInstanceEnv(t *testing.T) {
	t.Helper()
	t.Setenv("TEST_PRIVATE_KEY", "tok")
	t.Setenv("TEST_WEBHOOK_SECRET", "whsec")
	t.Setenv("TEST_PROVIDER_KEY", "sk-file")
}

// TestFileInstanceDefaults: the file's providers, default models and
// embedder run, and every account inherits them.
func TestFileInstanceDefaults(t *testing.T) {
	setInstanceEnv(t)
	f, err := Parse([]byte(fileWithDefaults))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	a := &f.Accounts[0]
	if p, ok := f.Provider(a, "openrouter"); !ok || p.APIKeyValue().Value() != "sk-file" {
		t.Fatalf("provider = %+v, %v; want the file's", p, ok)
	}
	s := f.Settings(a, "acme/x")
	if s.Models.Review != "openrouter/big" || s.Models.Fallback != "openrouter/small" {
		t.Fatalf("models = %+v; want the file's", s.Models)
	}
	if src := f.Sources(a, "acme/x"); src["review.model"] != SourceDefaults || src["review.fallback"] != SourceDefaults {
		t.Fatalf("sources = %v; want the models from the defaults", src)
	}
	if f.Embedding == nil || f.Embedding.Model != "e1" || f.Embedding.APIKeyValue().Value() != "sk-file" || f.Embedding.BaseURL != "https://openrouter.ai/api/v1" {
		t.Fatalf("embedding = %+v; want the file's", f.Embedding)
	}
	layer := f.FileLayer()
	if p := layer.Providers["openrouter"]; p.Source != SourceFile || layer.Review != (FileValue{Value: "openrouter/big", Source: SourceFile}) ||
		layer.Embedding == nil || layer.Embedding.Model != "openrouter/e1" {
		t.Fatalf("file layer = %+v", layer)
	}

	// An account's own provider may not take a name the instance's use.
	mine := strings.Replace(fileWithDefaults, "  acme: {}",
		"  acme: { providers: { openrouter: { type: openai, apiKey: { env: TEST_PROVIDER_KEY } } } }", 1)
	if _, err := Parse([]byte(mine)); err == nil || !strings.Contains(err.Error(), "the instance declares a provider by that name") {
		t.Fatalf("Parse with an account provider named like the instance's = %v", err)
	}
}

// TestProviderRetries checks the file's retry bounds.
func TestProviderRetries(t *testing.T) {
	setInstanceEnv(t)
	withRetries := strings.Replace(fileWithDefaults, "apiKey: { env: TEST_PROVIDER_KEY } }", "apiKey: { env: TEST_PROVIDER_KEY }, retries: 2 }", 1)
	f, err := Parse([]byte(withRetries))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if p, _ := f.Provider(&f.Accounts[0], "openrouter"); p.Retries != 2 {
		t.Fatalf("retries = %d, want 2", p.Retries)
	}
	if _, err := Parse([]byte(strings.Replace(withRetries, "retries: 2", "retries: -1", 1))); err == nil ||
		!strings.Contains(err.Error(), "providers.openrouter.retries must be between 0 and 5") {
		t.Fatalf("Parse with negative retries = %v", err)
	}
}

// TestFileDefaultModelNeedsAProvider: a default model naming a provider
// the file does not declare is refused.
func TestFileDefaultModelNeedsAProvider(t *testing.T) {
	setInstanceEnv(t)
	_, err := Parse([]byte(strings.Replace(fileWithDefaults, "model: openrouter/big", "model: nowhere/big", 1)))
	if err == nil || !strings.Contains(err.Error(), "configfile: review.model") {
		t.Fatalf("Parse = %v; want the default model refused", err)
	}
}

func TestFileReviewDefaults(t *testing.T) {
	setInstanceEnv(t)
	doc := strings.Replace(fileWithDefaults, "review: { model: openrouter/big, fallback: openrouter/small }",
		"review: {model: openrouter/big, fallback: openrouter/small, effort: low, approve: true, fixes: true, cost: true, incremental: 7, diagram: true}", 1)
	f := mustLoad(t, doc+"trigger: {limit: 3}\n")
	a := &f.Accounts[0]
	s := f.Settings(a, "acme/x")
	if s.MaxAutoReviews != 3 || !s.Review.Approve || s.Models.Effort != model.EffortLow ||
		!s.Review.RequireSuggestedFix || s.Incremental.MaxDeltaFiles != 7 || !s.Review.Diagram || !s.Review.Cost {
		t.Fatalf("settings = %+v", s)
	}
	for _, d := range f.FileLayer().Defaults {
		if d.Source != SourceFile || f.Sources(a, "acme/x")[d.Key] != SourceDefaults {
			t.Fatalf("source = %+v", d)
		}
	}
}

func TestRiskFloors(t *testing.T) {
	floors := RiskFloors{
		review.RiskCritical: {"db/migrations/**"},
		review.RiskHigh:     {"charts/**", ".github/workflows/*.yaml"},
		review.RiskLow:      {"**"},
	}
	for _, tt := range []struct {
		name    string
		floors  RiskFloors
		changed []string
		want    review.Risk
		at      string
	}{
		{name: "no floors", changed: []string{"db/migrations/1.sql"}, want: review.RiskLow},
		{name: "no change under a floor", floors: floors, changed: []string{"README.md", "internal/a.go"}, want: review.RiskLow},
		{name: "the highest level a path matches", floors: floors, changed: []string{"charts/x/values.yaml", "db/migrations/1.sql"},
			want: review.RiskCritical, at: "db/migrations/1.sql"},
		{name: "the first path at that level", floors: floors, changed: []string{".github/workflows/ci.yaml", "charts/x/values.yaml"},
			want: review.RiskHigh, at: ".github/workflows/ci.yaml"},
		{name: "the same path however the change is listed", floors: floors, changed: []string{"charts/x/values.yaml", ".github/workflows/ci.yaml"},
			want: review.RiskHigh, at: ".github/workflows/ci.yaml"},
		{name: "a low floor names no path", floors: floors, changed: []string{"README.md"}, want: review.RiskLow},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got, at := tt.floors.Floor(tt.changed); got != tt.want || at != tt.at {
				t.Fatalf("Floor = %s at %q, want %s at %q", got, at, tt.want, tt.at)
			}
		})
	}
}

// TestConfidenceSettings: the confidence model and threshold resolve scope
// by scope like any setting, from the file, and are held
// to a declared provider and the score's scale.
func TestConfidenceSettings(t *testing.T) {
	setInstanceEnv(t)
	f, err := Parse([]byte(fileWithDefaults))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if c := f.Settings(&f.Accounts[0], "acme/x").Confidence; !reflect.DeepEqual(c, Confidence{Threshold: DefaultConfidenceThreshold, Risk: review.RiskLow}) {
		t.Fatalf("confidence = %+v; want no model, the provider's effort, the default threshold and the lowest risk", c)
	}
	for _, tt := range []struct{ name, doc, want string }{
		{name: "a threshold off the scale", doc: "confidence: { threshold: 6 }\n", want: "configfile: confidence.threshold must be between 0 and 5, got 6"},
		{name: "a negative threshold on an entry", doc: "repositories:\n  acme/x: { confidence: { threshold: -1 } }\n",
			want: "repositories.acme/x.confidence.threshold must be between 0 and 5"},
		{name: "a model of no declared provider", doc: "confidence: { model: nowhere/judge }\n",
			want: `configfile: confidence.model references provider "nowhere"`},
		{name: "a fallback of no declared provider", doc: "confidence: { model: openrouter/judge, fallback: nowhere/judge }\n",
			want: `configfile: confidence.fallback references provider "nowhere"`},
		{name: "a risk that is no level", doc: "confidence: { risk: none }\n",
			want: `configfile: confidence.risk must be low, medium, high or critical, got "none"`},
		{name: "an effort that is no level", doc: "confidence: { effort: turbo }\n",
			want: `configfile: confidence.effort must be none, minimal, low, medium, high, xhigh or max, got "turbo"`},
		{name: "a floor at no level", doc: "confidence: { floors: { none: ['**'] } }\n",
			want: `configfile: confidence.floors keys must be low, medium, high or critical, got "none"`},
		{name: "a floor glob that is none", doc: "repositories: { acme/x: { confidence: { floors: { high: ['['] } } } }\n",
			want: `configfile: repositories.acme/x.confidence.floors.high[0] "[" is not a valid glob`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := Parse([]byte(fileWithDefaults + tt.doc)); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want it to mention %q", err, tt.want)
			}
		})
	}
	doc := fileWithDefaults + `confidence: { model: openrouter/judge, fallback: openrouter/judge-small, threshold: 4, gate: true, effort: low, risk: medium, instructions: "Image bumps are low.", floors: { critical: ["db/migrations/**"] } }
repositories:
  acme/x: { confidence: { threshold: 3, gate: false, risk: high, fallback: "" } }
  acme/y: { confidence: { model: "", effort: "", instructions: "", floors: {} } }
`
	if f, err = Parse([]byte(doc)); err != nil {
		t.Fatalf("Parse: %v", err)
	}
	a := &f.Accounts[0]
	for repo, want := range map[string]Confidence{
		"acme/z": {Model: "openrouter/judge", Fallback: "openrouter/judge-small", Effort: model.EffortLow, Threshold: 4, Gate: true, Risk: review.RiskMedium,
			Instructions: "Image bumps are low.", Floors: RiskFloors{review.RiskCritical: {"db/migrations/**"}}},
		// A fallback written empty drops the root's.
		"acme/x": {Model: "openrouter/judge", Effort: model.EffortLow, Threshold: 3, Gate: false, Risk: review.RiskHigh, Instructions: "Image bumps are low.",
			Floors: RiskFloors{review.RiskCritical: {"db/migrations/**"}}},
		// An effort written empty leaves it to the provider, and floors
		// written empty drop the root's.
		"acme/y": {Fallback: "openrouter/judge-small", Threshold: 4, Gate: true, Risk: review.RiskMedium, Floors: RiskFloors{}},
	} {
		if got := f.Settings(a, repo).Confidence; !reflect.DeepEqual(got, want) {
			t.Errorf("confidence of %s = %+v, want %+v", repo, got, want)
		}
	}
	if src := f.Sources(a, "acme/z"); src["confidence.model"] != SourceDefaults || src["confidence.fallback"] != SourceDefaults ||
		src["confidence.threshold"] != SourceDefaults || src["confidence.gate"] != SourceDefaults {
		t.Fatalf("sources = %v", src)
	}
	defaults := f.FileLayer().Defaults
	layer := make([]string, 0, len(defaults))
	for _, d := range defaults {
		layer = append(layer, d.Key+"="+d.Value+" ("+string(d.Source)+")")
	}
	if want := []string{"confidence.model=openrouter/judge (file)", "confidence.fallback=openrouter/judge-small (file)", "confidence.effort=low (file)",
		"confidence.threshold=4 (file)", "confidence.gate=true (file)", "confidence.risk=medium (file)"}; !slices.Equal(layer, want) {
		t.Fatalf("file layer = %v, want %v", layer, want)
	}
}
