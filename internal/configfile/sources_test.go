package configfile

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/home-operations/kritika/internal/config"
)

func TestFileRuntime(t *testing.T) {
	setInstanceEnv(t)
	f := mustLoad(t, minimal+`
webUrl: https://kritika.example.com
pollInterval: 0s
pollLookback: 2h
onboardWindow: 8
indexGrace: 48h
transcriptRetention: 72h
diffRetention: 96h
runner:
  jobTemplate: {spec: {activeDeadlineSeconds: 1200}}
  podTemplate: {spec: {resources: {limits: {memory: 1Gi}}}}
`)
	spec, _ := f.Run.RunnerPodTemplate["spec"].(map[string]any)
	if f.PollInterval() != 0 || f.PollLookback() != 2*time.Hour || f.OnboardWindow() != 8 ||
		f.DisabledIndexGrace() != 48*time.Hour || f.TranscriptRetention() != 72*time.Hour || f.DiffRetention() != 96*time.Hour ||
		f.RunnerDeadline() != 20*time.Minute || spec["resources"] == nil {
		t.Fatalf("runtime settings = %+v", f.Run)
	}
	for _, doc := range []string{
		"runner: {deadlien: 20m}",
		"runner: {jobTemplate: {spec: {activeDeadlineSeconds: 0}}}",
		"onboardWindow: 0",
		"transcriptRetention: 1h",
		"webUrll: https://example.com",
		"databaseUrl: postgres://secret@db/kritika",
	} {
		t.Run(doc, func(t *testing.T) {
			if _, err := Parse([]byte(minimal + doc)); err == nil {
				t.Fatal("invalid file accepted")
			}
		})
	}
}

func TestDocumentStrictness(t *testing.T) {
	for _, tt := range []struct{ name, raw string }{
		{"duplicate setting", "pollInterval: 0s\npollInterval: 1m"},
		{"duplicate policy", "ignore: []\nignore: []"},
		{"duplicate nested setting", "runner: {serviceAccount: a, serviceAccount: b}"},
		{"duplicate nested policy", "review: {incremental: 1, incremental: 2}"},
		{"unknown setting", "runner: {ServiceAccount: a}"},
		{"unknown null setting", "runner: {typo: null}"},
		{"unknown policy", "review: {typo: null}"},
		{"unknown root", "typo: null"},
		{"numeric duration", "pollInterval: 0"},
		{"fractional workers", "reviewWorkers: 4.5"},
		{"fractional bytes", "gatewayCacheMaxSize: 1.5"},
		{"second document", "pollInterval: 0s\n---\nignore: []"},
		{"second empty document", "pollInterval: 0s\n---\n"},
		{"root sequence", "- pollInterval: 0s"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := Parse([]byte(tt.raw)); err == nil {
				t.Fatal("invalid document accepted")
			}
		})
	}
}

func TestDocumentAliases(t *testing.T) {
	for _, raw := range []string{
		`<<: {pollInterval: 0s, reviewWorkers: 2, ignore: ["**/*.md"]}
runner:
  podTemplate: {metadata: {labels: {example.com/name: &host example.com}}}
egress: {allow: [*host]}
`,
		`egress: {allow: [&host example.com]}
runner:
  podTemplate: {metadata: {labels: {example.com/name: *host}}}
pollInterval: 0s
reviewWorkers: 2
ignore: ["**/*.md"]
`,
	} {
		f, err := Parse([]byte(raw))
		if err != nil {
			t.Fatal(err)
		}
		if f.Run.PollInterval != 0 || f.Run.ReviewWorkers != 2 || !slices.Equal(f.Defaults.Ignore, []string{"**/*.md"}) ||
			!slices.Equal(f.Egress.Allow, []string{"example.com"}) {
			t.Fatalf("aliases or merged fields lost: run=%+v, ignore=%v, allow=%v", f.Run, f.Defaults.Ignore, f.Egress.Allow)
		}
		want := map[string]any{"metadata": map[string]any{"labels": map[string]any{"example.com/name": "example.com"}}}
		if !reflect.DeepEqual(map[string]any(f.Run.RunnerPodTemplate), want) {
			t.Fatalf("pod template = %#v, want %#v", f.Run.RunnerPodTemplate, want)
		}
	}
}

func TestFromConfigUsesStartupDocument(t *testing.T) {
	setInstanceEnv(t)
	raw := []byte(minimal + "pollInterval: 0s\nrunner: {serviceAccount: ''}\n")
	want, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KRITIKA_CONFIG_FILE", path)
	t.Setenv("KRITIKA_DATABASE_URL", "postgres://app@db/kritika")
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("invalid: ["), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KRITIKA_POLL_INTERVAL", "5m")
	for range 2 {
		got, err := FromConfig(cfg)
		if err != nil {
			t.Fatal(err)
		}
		if got.Run.PollInterval != 0 || got.Run.RunnerServiceAccount != "" || got.hash != want.hash ||
			!reflect.DeepEqual(got.Connections, want.Connections) {
			t.Fatal("startup settings, policy or hash changed")
		}
	}
}

func TestFromConfigWithoutFile(t *testing.T) {
	t.Setenv("KRITIKA_CONFIG_FILE", "")
	t.Setenv("KRITIKA_DATABASE_URL", "postgres://app@db/kritika")
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	f, err := FromConfig(cfg)
	if err != nil || f.Run.PollInterval != cfg.PollInterval {
		t.Fatalf("FromConfig = %+v, %v", f, err)
	}
}
