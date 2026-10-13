package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFileSettings(t *testing.T) {
	for _, tt := range []struct {
		name, doc, env, value, wantErr string
	}{
		{name: "file", doc: "webUrl: https://kritika.example.com/\nlogLevel: debug\nreviewWorkers: 4\npollInterval: 0s\nrunner: {runtimeClass: gvisor, deadline: 20m}"},
		{name: "conflicting scalar", doc: "logLevel: debug", env: "KRITIKA_LOG_LEVEL", value: "debug", wantErr: "logLevel conflicts with KRITIKA_LOG_LEVEL"},
		{name: "conflicting zero", doc: "pollInterval: 0s", env: "KRITIKA_POLL_INTERVAL", value: "1h", wantErr: "pollInterval conflicts"},
		{name: "conflicting runner", doc: "runner: {runtimeClass: gvisor}", env: "KRITIKA_RUNNER_RUNTIME_CLASS", value: "", wantErr: "runner.runtimeClass conflicts"},
		{name: "invalid workers", doc: "reviewWorkers: 0", wantErr: "must be positive"},
		{name: "invalid log level", doc: "logLevel: loud", wantErr: "KRITIKA_LOG_LEVEL"},
		{name: "invalid duration", doc: "runner: {ttl: tomorrow}", wantErr: "time.Duration"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, []byte(tt.doc), 0o600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("KRITIKA_CONFIG_FILE", path)
			t.Setenv("KRITIKA_DATABASE_URL", "postgres://app@db/kritika")
			if tt.env != "" {
				t.Setenv(tt.env, tt.value)
			}
			c, err := Load()
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("Load = %v, want %s", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if c.WebURL != "https://kritika.example.com" || c.LogLevel != "debug" || c.ReviewWorkers != 4 ||
				c.PollInterval != 0 || c.RunnerRuntimeClass != "gvisor" || c.RunnerDeadline != 20*time.Minute || c.IndexWorkers != 1 {
				t.Fatalf("file settings not applied: %+v", c.Settings)
			}
			for _, e := range c.Env() {
				if e.Name == "KRITIKA_LOG_LEVEL" && (!e.File || e.Set || e.Value != "debug") {
					t.Fatalf("file provenance = %+v", e)
				}
			}
		})
	}
}
