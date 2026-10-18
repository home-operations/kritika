package config

import (
	"fmt"
	"math"
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
		{"zero runner deadline", "runner: {jobTemplate: {spec: {activeDeadlineSeconds: 0}}}",
			"runner.jobTemplate.spec.activeDeadlineSeconds: KRITIKA_RUNNER_JOB_TEMPLATE must be a whole number of seconds from 1 to 7200"},
		{"fractional runner deadline", "runner: {jobTemplate: {spec: {activeDeadlineSeconds: 1.5}}}", "must be a whole number of seconds"},
		{"negative runner deadline", "runner: {jobTemplate: {spec: {activeDeadlineSeconds: -900}}}", "must be a whole number of seconds"},
		// Past time.Duration's range, the seconds would wrap to 900.
		{"overflowing runner deadline", "runner: {jobTemplate: {spec: {activeDeadlineSeconds: 18446744974}}}", "must be a whole number of seconds"},
		{"runner deadline as a duration", "runner: {jobTemplate: {spec: {activeDeadlineSeconds: 15m}}}", "must be a number of seconds"},
		{"zero onboarding window", "onboardWindow: 0", "onboardWindow: KRITIKA_ONBOARD_WINDOW must be positive"},
		{"negative polling", "pollInterval: -1ns", "pollInterval: KRITIKA_POLL_INTERVAL must not be negative"},
		{"short retention", "diffRetention: 23h59m59.999999999s", "diffRetention: KRITIKA_DIFF_RETENTION must be at least 24h"},
		{"long deadline", fmt.Sprintf("runner: {jobTemplate: {spec: {activeDeadlineSeconds: %d}}}", int64(jobtimeout.MaxRunnerDeadline/time.Second)+1),
			"runner.jobTemplate.spec.activeDeadlineSeconds: KRITIKA_RUNNER_JOB_TEMPLATE must be a whole number of seconds from 1 to 7200"},
		{"empty public address", "addr: ''", "addr: KRITIKA_ADDR must be set"},
		{"empty metrics address", "metricsAddr: ''", "metricsAddr: KRITIKA_METRICS_ADDR must be set"},
		{"empty gateway address", "gatewayAddr: ''", "gatewayAddr: KRITIKA_GATEWAY_ADDR must be set"},
		{"empty role", "databaseAppRole: ''", "databaseAppRole: KRITIKA_DATABASE_APP_ROLE must be set"},
		{"same database roles", "databaseRunnerRole: kritika_app", "databaseRunnerRole: KRITIKA_DATABASE_RUNNER_ROLE must be distinct"},
		{"unknown executor", "executor: docker", "executor: KRITIKA_EXECUTOR must be kubernetes or local"},
		{"allowed bounds", fmt.Sprintf("pollInterval: 0s\npollLookback: 0s\nindexGrace: 0s\ntranscriptRetention: 24h\ndiffRetention: 24h\n"+
			"runner: {jobTemplate: {spec: {activeDeadlineSeconds: %d}}}", int64(jobtimeout.MaxRunnerDeadline/time.Second)), ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := LoadSettings([]byte(tt.doc))
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

// Parsing rejects a negative size, so the bounds are set directly.
func TestCacheSizeBounds(t *testing.T) {
	s, _, err := LoadSettings(nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		size ByteSize
		ok   bool
	}{{math.MinInt64, false}, {-1, false}, {0, false}, {1, true}, {math.MaxInt64, true}} {
		t.Run(fmt.Sprint(int64(tt.size)), func(t *testing.T) {
			s.GatewayCacheMaxSize = tt.size
			if err := s.Validate(); (err == nil) != tt.ok {
				t.Fatalf("Validate = %v, want ok %t", err, tt.ok)
			}
		})
	}
}

func TestSettingsValidationCollectsErrors(t *testing.T) {
	_, _, err := LoadSettings([]byte("reviewWorkers: 0\nindexWorkers: -1\nrunner: {jobTemplate: {spec: {activeDeadlineSeconds: 0}}}\nlogLevel: loud\nlogFormat: xml"))
	if err == nil {
		t.Fatal("invalid settings accepted")
	}
	for _, path := range []string{"reviewWorkers", "indexWorkers", "runner.jobTemplate.spec.activeDeadlineSeconds", "logLevel", "logFormat"} {
		if !strings.Contains(err.Error(), path) {
			t.Errorf("missing %s in %v", path, err)
		}
	}
}

func TestValidationErrorsAreStable(t *testing.T) {
	const doc = "reviewWorkers: 0\nlogLevel: loud\nrunner: {jobTemplate: {spec: {activeDeadlineSeconds: 0}}}"
	const want = "config: logLevel: KRITIKA_LOG_LEVEL must be debug, info, warn or error; reviewWorkers: KRITIKA_REVIEW_WORKERS must be positive; " +
		"runner.jobTemplate.spec.activeDeadlineSeconds: KRITIKA_RUNNER_JOB_TEMPLATE must be a whole number of seconds from 1 to 7200, " +
		"or River's 3h0m0s job timeout cap would cut the runner off early"
	for range 20 {
		_, _, err := LoadSettings([]byte(doc))
		if err == nil || err.Error() != want {
			t.Fatalf("LoadSettings = %v, want %s", err, want)
		}
	}
}

func TestServeRequiresRunnerSecret(t *testing.T) {
	for _, key := range []string{"databaseSecret", "databaseSecretKey"} {
		t.Run(key, func(t *testing.T) {
			s, _, err := LoadSettings([]byte("webUrl: https://example.com\ngatewayUrl: http://gateway:8082\nrunner: {" + key + ": ''}"))
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

func TestRunnerDeadline(t *testing.T) {
	for _, tt := range []struct {
		name, doc string
		want      time.Duration
	}{
		{"unset", "", DefaultRunnerDeadline},
		{"set", "runner: {jobTemplate: {spec: {activeDeadlineSeconds: 1200}}}", 20 * time.Minute},
		{"null", "runner: {jobTemplate: {spec: {activeDeadlineSeconds: null}}}", DefaultRunnerDeadline},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s, _, err := LoadSettings([]byte(tt.doc))
			if err != nil || s.RunnerDeadline() != tt.want {
				t.Fatalf("RunnerDeadline = %s, %v; want %s", s.RunnerDeadline(), err, tt.want)
			}
		})
	}
	t.Setenv("KRITIKA_RUNNER_JOB_TEMPLATE", `{"spec":{"activeDeadlineSeconds":600}}`)
	if s, _, err := LoadSettings(nil); err != nil || s.RunnerDeadline() != 10*time.Minute {
		t.Fatalf("from the environment: RunnerDeadline = %s, %v; want 10m", s.RunnerDeadline(), err)
	}
}

func TestS3CacheSettings(t *testing.T) {
	const base = "gatewayCacheStorage: s3\ngatewayCacheDir: /cache\ngatewayCacheS3Endpoint: https://s3.example.com\ngatewayCacheS3Region: garage\ngatewayCacheS3Bucket: kritika\n"
	for _, tt := range []struct{ name, doc, want string }{
		{"valid", base, ""},
		{"unknown backend", "gatewayCacheStorage: other", "must be filesystem or s3"},
		{"zero entry", "gatewayCacheMaxEntrySize: 0", "must be positive"},
		{"oversize entry", base + "gatewayCacheMaxEntrySize: 6Gi\n", "must not exceed 5Gi"},
		{"no staging", strings.ReplaceAll(base, "gatewayCacheDir: /cache\n", ""), "private local staging"},
		{"no bucket", strings.ReplaceAll(base, "gatewayCacheS3Bucket: kritika\n", ""), "S3 bucket"},
		{"no region", strings.ReplaceAll(base, "gatewayCacheS3Region: garage\n", ""), "S3 region"},
		{"endpoint credential", strings.ReplaceAll(base, "https://s3.example.com", "https://user:password@s3.example.com"), "without credentials"},
		{"endpoint path", strings.ReplaceAll(base, "https://s3.example.com", "https://s3.example.com/bucket"), "without credentials"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s, _, err := LoadSettings([]byte(tt.doc))
			if tt.want == "" {
				if err != nil {
					t.Fatal(err)
				}
				if !s.GatewayCacheS3ForcePathStyle || s.GatewayCacheMaxEntrySize != 256<<20 {
					t.Fatalf("defaults = %+v", s)
				}
			} else if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("got %v, want %q", err, tt.want)
			}
		})
	}
}
