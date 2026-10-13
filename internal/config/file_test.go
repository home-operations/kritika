package config

import (
	"os"
	"path/filepath"
	"reflect"
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
		{name: "invalid duration", doc: "runner: {ttl: tomorrow}", wantErr: "runner.ttl"},
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

func TestSettingsSources(t *testing.T) {
	t.Setenv("KRITIKA_REVIEW_WORKERS", "5")
	t.Setenv("KRITIKA_RUNNER_IMAGE", "example.com/runner:test")
	s, err := LoadSettings([]byte(`pollInterval: 0s
runner:
  serviceAccount: ""
  runtimeClass: gvisor
  resources:
    requests:
      cpu: 10m
    limits:
      example.com/device: "1"
  tools:
    - name: flux
      image: example.com/flux:test
      commands: [flux, flate]
`))
	if err != nil {
		t.Fatal(err)
	}
	if s.ReviewWorkers != 5 || s.IndexWorkers != 1 || s.PollInterval != 0 ||
		s.RunnerImage != "example.com/runner:test" || s.RunnerServiceAccount != "" ||
		s.RunnerRuntimeClass != "gvisor" || s.RunnerTTL != 10*time.Minute {
		t.Fatalf("merged settings = %+v", s)
	}
	wantResources := jsonObject{
		"requests": map[string]any{"cpu": "10m"},
		"limits":   map[string]any{"example.com/device": "1"},
	}
	wantTools := toolList{{Name: "flux", Image: "example.com/flux:test", Commands: []string{"flux", "flate"}}}
	if !reflect.DeepEqual(s.RunnerResources, wantResources) || !reflect.DeepEqual(s.Tools, wantTools) {
		t.Fatalf("resources = %#v, tools = %#v", s.RunnerResources, s.Tools)
	}
	if !s.fileEnv["KRITIKA_POLL_INTERVAL"] || !s.fileEnv["KRITIKA_RUNNER_TOOLS"] || s.fileEnv["KRITIKA_REVIEW_WORKERS"] {
		t.Fatalf("file provenance = %v", s.fileEnv)
	}
}

func TestSettingsTypes(t *testing.T) {
	for _, tt := range []struct{ name, doc, want string }{
		{"duration requires units", "runner: {ttl: 600}", "runner.ttl"},
		{"duration must be scalar", "pollInterval: [10m]", "pollInterval"},
		{"workers must be numeric", "reviewWorkers: '4'", "reviewWorkers"},
		{"no boolean coercion", "logLevel: true", "logLevel"},
		{"tools must be a list", "runner: {tools: {name: flux}}", "runner.tools"},
		{"resources must be a map", "runner: {resources: [1]}", "runner.resources"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := LoadSettings([]byte(tt.doc)); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("LoadSettings = %v, want %s", err, tt.want)
			}
		})
	}
}

func TestNullSettingsAreOmitted(t *testing.T) {
	for _, doc := range []string{"pollInterval: null", "pollInterval:", "runner: null", "runner: {deadline: null}"} {
		t.Run(doc, func(t *testing.T) {
			t.Setenv("KRITIKA_POLL_INTERVAL", "5m")
			t.Setenv("KRITIKA_RUNNER_DEADLINE", "20m")
			s, err := LoadSettings([]byte(doc))
			if err != nil {
				t.Fatal(err)
			}
			if s.PollInterval != 5*time.Minute || s.RunnerDeadline != 20*time.Minute || len(s.fileEnv) != 0 {
				t.Fatalf("null changed settings or provenance: %+v", s)
			}
		})
	}
	s, err := LoadSettings([]byte("pollInterval: null\nrunner: {deadline: null}"))
	if err != nil || s.PollInterval != 10*time.Minute || s.RunnerDeadline != 15*time.Minute || len(s.fileEnv) != 0 {
		t.Fatalf("null changed defaults: %+v, %v", s, err)
	}
}

func TestEnvFormatsStructuredFileSettings(t *testing.T) {
	s, err := LoadSettings([]byte(`runner:
  resources: {requests: {cpu: 10m}}
  tools: [{name: flux, image: example.com/flux:test}]
`))
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{Settings: s}
	for _, e := range cfg.Env() {
		switch e.Name {
		case "KRITIKA_RUNNER_RESOURCES":
			if e.Value != `{"requests":{"cpu":"10m"}}` || !e.File || e.Path != "runner.resources" {
				t.Fatalf("resources = %+v", e)
			}
		case "KRITIKA_RUNNER_TOOLS":
			if e.Value != `[{"name":"flux","image":"example.com/flux:test"}]` || !e.File || e.Path != "runner.tools" {
				t.Fatalf("tools = %+v", e)
			}
		}
	}
}
