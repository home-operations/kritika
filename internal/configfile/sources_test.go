package configfile

import (
	"testing"
	"time"
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
  deadline: 20m
  resources: {limits: {memory: 1Gi}}
  tools: [{name: helm, image: example.com/helm:test, commands: [helm]}]
`)
	deadline, resources := f.RunnerFor()
	if f.PollInterval() != 0 || f.PollLookback() != 2*time.Hour || f.OnboardWindow() != 8 ||
		f.DisabledIndexGrace() != 48*time.Hour || f.TranscriptRetention() != 72*time.Hour || f.DiffRetention() != 96*time.Hour ||
		deadline != 20*time.Minute || resources["limits"] == nil || len(f.ToolsFor([]string{"helm"})) != 1 {
		t.Fatalf("runtime settings = %+v", f.Run)
	}
	for _, doc := range []string{
		"runner: {deadlien: 20m}",
		"runner: {tools: [{name: helm, image: example.com/helm:test, typo: x}]}",
		"runner: {deadline: 0s}",
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

func TestEnvironmentDoesNotDeclarePolicy(t *testing.T) {
	setInstanceEnv(t)
	for name, value := range map[string]string{
		"KRITIKA_AUTH_ADMIN_PASSWORD": "secret", "KRITIKA_AUTH_SESSION_TTL": "1h",
		"KRITIKA_APPS_NAME": "other", "KRITIKA_PROVIDERS_TYPE": "anthropic",
		"KRITIKA_REVIEW_APPROVE": "true", "KRITIKA_REVIEW_MODEL": "unknown/model",
		"KRITIKA_CONFIDENCE_THRESHOLD": "0", "KRITIKA_TRIGGER_LIMIT": "5", "KRITIKA_EMBEDDING_DIMS": "16",
	} {
		t.Setenv(name, value)
	}
	empty := mustLoad(t, "")
	if empty.Auth.Configured() || len(empty.Connections) != 0 || len(empty.Providers) != 0 || empty.Embedding != nil || empty.Defaults.Review.Model != nil {
		t.Fatal("environment declared application configuration")
	}
	t.Setenv("KRITIKA_REVIEW_WORKERS", "5")
	f := mustLoad(t, fileWithDefaults)
	if f.Run.ReviewWorkers != 5 {
		t.Fatal("operational environment setting not applied")
	}
	if f.Auth.Configured() || len(f.Connections) != 1 || len(f.Providers) != 1 || f.Embedding.Dims != 8 || f.Defaults.Review.Approve != nil || f.Defaults.Trigger.Limit != nil {
		t.Fatal("environment changed file settings")
	}
	if f.FileLayer().Review.Value != "openrouter/big" {
		t.Fatal("environment changed model")
	}
	// A variable is still available when explicitly referenced by the file.
	f = mustLoad(t, fileWithDefaults+"auth: {admin: {password: {env: KRITIKA_AUTH_ADMIN_PASSWORD}}}\n")
	_, password, ok := f.Auth.AdminUser()
	if !ok || password.Value() != "secret" {
		t.Fatal("explicit secret reference not resolved")
	}
}
