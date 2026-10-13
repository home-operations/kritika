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

func TestConflictingSources(t *testing.T) {
	for _, tt := range []struct{ name, value, doc, path string }{
		{"KRITIKA_AUTH_ADMIN_PASSWORD", "secret", "auth: {admin: {password: {env: PASSWORD}}}", "auth.admin.password"},
		{"KRITIKA_AUTH_SESSION_TTL", "1h", "auth: {sessionTTL: 2h}", "auth.sessionTTL"},
		{"KRITIKA_REVIEW_APPROVE", "true", "review: {approve: false}", "review.approve"},
		{"KRITIKA_CONFIDENCE_THRESHOLD", "3", "confidence: {threshold: 0}", "confidence.threshold"},
		{"KRITIKA_EMBEDDING_DIMS", "16", "embedding: {dims: 8}", "embedding.dims"},
		{"KRITIKA_PROVIDERS_API_KEY", "secret", "providers: {openrouter: {type: openrouter}}", "providers.openrouter"},
		{"KRITIKA_APPS_CLIENT_ID", "id", "apps: {github: {clientId: other}}", "apps.github"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(tt.name, tt.value)
			_, err := Parse([]byte(tt.doc))
			if err == nil || !strings.Contains(err.Error(), tt.path+" conflicts with "+tt.name) {
				t.Fatalf("Parse = %v", err)
			}
		})
	}
}
