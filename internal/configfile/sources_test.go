package configfile

import (
	"strings"
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

func TestPolicyEnvironmentRequiresMigration(t *testing.T) {
	for _, name := range []string{
		"KRITIKA_AUTH_SESSION_TTL", "KRITIKA_APPS_NAME", "KRITIKA_PROVIDERS_TYPE",
		"KRITIKA_REVIEW_APPROVE", "KRITIKA_CONFIDENCE_GATE", "KRITIKA_TRIGGER_LIMIT", "KRITIKA_EMBEDDING_DIMS",
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv(name, "private-value")
			_, err := Parse(nil)
			if err == nil || !strings.Contains(err.Error(), name) || !strings.Contains(err.Error(), "configuration file") || strings.Contains(err.Error(), "private-value") {
				t.Fatalf("Parse = %v", err)
			}
		})
	}
	t.Run("explicit secret and operational env", func(t *testing.T) {
		t.Setenv("KRITIKA_AUTH_ADMIN_PASSWORD", "secret")
		t.Setenv("KRITIKA_REVIEW_WORKERS", "5")
		f := mustLoad(t, "auth: {admin: {password: {env: KRITIKA_AUTH_ADMIN_PASSWORD}}}\n")
		_, password, ok := f.Auth.AdminUser()
		if !ok || password.Value() != "secret" || f.Run.ReviewWorkers != 5 {
			t.Fatal("referenced secret or operational env was not applied")
		}
	})
}
