package config

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/home-operations/kritika/internal/jobtimeout"
)

func TestSettingsValidation(t *testing.T) {
	for _, tt := range []struct{ name, doc, want string }{
		{"invalid log level", "logLevel: loud", "logLevel: KRITIKA_LOG_LEVEL must be debug, info, warn or error"},
		{"invalid log format", "logFormat: xml", "logFormat: KRITIKA_LOG_FORMAT must be json or text"},
		{"case insensitive logging", "logLevel: DEBUG\nlogFormat: TEXT", ""},
		{"warning alias", "logLevel: warning", ""},
		{"zero workers", "reviewWorkers: 0", "reviewWorkers: KRITIKA_REVIEW_WORKERS must be positive"},
		{"zero gateway lifetime", "gatewayTokenTtl: 0s", "gatewayTokenTtl: KRITIKA_GATEWAY_TOKEN_TTL must be positive"},
		{"zero cache size", "gatewayCacheMaxSize: 0", "gatewayCacheMaxSize: KRITIKA_GATEWAY_CACHE_MAX_SIZE must be positive"},
		{"zero cache age", "gatewayCacheMaxAge: 0s", "gatewayCacheMaxAge: KRITIKA_GATEWAY_CACHE_MAX_AGE must be positive"},
		{"gateway cache", "gatewayCacheDir: /var/cache/kritika\ngatewayCacheMaxSize: 512Mi\ngatewayCacheMaxAge: 6h", ""},
		{"zero runner lifetime", "runner: {ttl: 0s}", "runner.ttl: KRITIKA_RUNNER_TTL must be positive"},
		{"zero runner deadline", "runner: {deadline: 0s}", "runner.deadline: KRITIKA_RUNNER_DEADLINE must be positive"},
		{"zero onboarding window", "onboardWindow: 0", "onboardWindow: KRITIKA_ONBOARD_WINDOW must be positive"},
		{"negative polling", "pollInterval: -1ns", "pollInterval: KRITIKA_POLL_INTERVAL must not be negative"},
		{"short retention", "diffRetention: 23h59m59.999999999s", "diffRetention: KRITIKA_DIFF_RETENTION must be at least 24h"},
		{"long deadline", fmt.Sprintf("runner: {deadline: %s}", jobtimeout.MaxRunnerDeadline+time.Nanosecond), "runner.deadline: KRITIKA_RUNNER_DEADLINE must not exceed"},
		{"empty public address", "addr: ''", "addr: KRITIKA_ADDR must be set"},
		{"empty metrics address", "metricsAddr: ''", "metricsAddr: KRITIKA_METRICS_ADDR must be set"},
		{"empty gateway address", "gatewayAddr: ''", "gatewayAddr: KRITIKA_GATEWAY_ADDR must be set"},
		{"empty role", "databaseAppRole: ''", "databaseAppRole: KRITIKA_DATABASE_APP_ROLE must be set"},
		{"same database roles", "databaseRunnerRole: kritika_app", "databaseRunnerRole: KRITIKA_DATABASE_RUNNER_ROLE must be distinct"},
		{"unknown executor", "executor: docker", "executor: KRITIKA_EXECUTOR must be kubernetes or local"},
		{"URL replacement without a to", "runner: {miseUrlReplacements: [{from: 'https://github.com/'}]}",
			"runner.miseUrlReplacements: KRITIKA_RUNNER_MISE_URL_REPLACEMENTS must give every rule a from of its own and a to"},
		{"URL replacement without a from", "runner: {miseUrlReplacements: [{to: 'https://mirror.example.com/'}]}",
			"KRITIKA_RUNNER_MISE_URL_REPLACEMENTS must give every rule a from of its own and a to"},
		{"URL replacements sharing a from", "runner: {miseUrlReplacements: [{from: a, to: b}, {from: a, to: c}]}",
			"KRITIKA_RUNNER_MISE_URL_REPLACEMENTS must give every rule a from of its own and a to"},
		{"URL replacements", "runner: {miseUrlReplacements: [{from: 'regex:^https://(.+)/', to: 'https://mirror.example.com/$1/'}]}", ""},
		{"allowed bounds", fmt.Sprintf("pollInterval: 0s\npollLookback: 0s\nindexGrace: 0s\ntranscriptRetention: 24h\ndiffRetention: 24h\nrunner: {deadline: %s}", jobtimeout.MaxRunnerDeadline), ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := LoadSettings([]byte(tt.doc))
			if tt.want == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("LoadSettings = %v, want %s", err, tt.want)
			}
		})
	}
}

func TestSettingsValidationCollectsErrors(t *testing.T) {
	_, err := LoadSettings([]byte("reviewWorkers: 0\nindexWorkers: -1\nrunner: {ttl: 0s}\nlogLevel: loud\nlogFormat: xml"))
	if err == nil {
		t.Fatal("invalid settings accepted")
	}
	for _, path := range []string{"reviewWorkers", "indexWorkers", "runner.ttl", "logLevel", "logFormat"} {
		if !strings.Contains(err.Error(), path) {
			t.Errorf("missing %s in %v", path, err)
		}
	}
}

func TestValidationErrorsAreStable(t *testing.T) {
	const doc = "reviewWorkers: 0\nlogLevel: loud\nrunner: {ttl: 0s}"
	const want = "config: logLevel: KRITIKA_LOG_LEVEL must be debug, info, warn or error; reviewWorkers: KRITIKA_REVIEW_WORKERS must be positive; runner.ttl: KRITIKA_RUNNER_TTL must be positive"
	for range 20 {
		_, err := LoadSettings([]byte(doc))
		if err == nil || err.Error() != want {
			t.Fatalf("LoadSettings = %v, want %s", err, want)
		}
	}
}

func TestServeRequiresRunnerSecret(t *testing.T) {
	for _, key := range []string{"databaseSecret", "databaseSecretKey"} {
		t.Run(key, func(t *testing.T) {
			s, err := LoadSettings([]byte("webUrl: https://example.com\ngatewayUrl: http://gateway:8082\nrunner: {image: runner:test, " + key + ": ''}"))
			if err != nil {
				t.Fatal(err)
			}
			cfg := Config{Settings: s, ConfigFile: "config.yaml"}
			if err := cfg.ValidateServe(); err == nil || !strings.Contains(err.Error(), "runner."+key) {
				t.Fatalf("ValidateServe = %v", err)
			}
		})
	}
}
