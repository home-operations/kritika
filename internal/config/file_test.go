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
		{name: "file", doc: "webUrl: https://kritika.example.com/\nlogLevel: debug\nreviewWorkers: 4\npollInterval: 0s\n" +
			"runner: {jobTemplate: {spec: {activeDeadlineSeconds: 1200}}, podTemplate: {spec: {runtimeClassName: gvisor}}}"},
		{name: "conflicting scalar", doc: "logLevel: debug", env: "KRITIKA_LOG_LEVEL", value: "debug", wantErr: "logLevel conflicts with KRITIKA_LOG_LEVEL"},
		{name: "conflicting zero", doc: "pollInterval: 0s", env: "KRITIKA_POLL_INTERVAL", value: "1h", wantErr: "pollInterval conflicts"},
		{name: "conflicting runner", doc: "runner: {podTemplate: {spec: {runtimeClassName: gvisor}}}", env: "KRITIKA_RUNNER_POD_TEMPLATE", value: "{}",
			wantErr: "runner.podTemplate conflicts"},
		{name: "invalid workers", doc: "reviewWorkers: 0", wantErr: "must be positive"},
		{name: "invalid log level", doc: "logLevel: loud", wantErr: "KRITIKA_LOG_LEVEL"},
		{name: "invalid duration", doc: "gatewayTokenTtl: tomorrow", wantErr: "gatewayTokenTtl"},
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
			spec, _ := c.RunnerPodTemplate["spec"].(map[string]any)
			if c.WebURL != "https://kritika.example.com" || c.LogLevel != "debug" || c.ReviewWorkers != 4 ||
				c.PollInterval != 0 || spec["runtimeClassName"] != "gvisor" || c.RunnerDeadline() != 20*time.Minute || c.IndexWorkers != 1 {
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

func TestRunnerTemplatesFromEnvironment(t *testing.T) {
	t.Setenv("KRITIKA_RUNNER_JOB_TEMPLATE", `{"spec":{"ttlSecondsAfterFinished":60}}`)
	t.Setenv("KRITIKA_RUNNER_POD_TEMPLATE", `{"spec":{"priorityClassName":"kritika-runner","hostUsers":false}}`)
	s, _, err := LoadSettings(nil)
	if err != nil {
		t.Fatal(err)
	}
	if want := (jsonObject{"spec": map[string]any{"ttlSecondsAfterFinished": float64(60)}}); !reflect.DeepEqual(s.RunnerJobTemplate, want) {
		t.Fatalf("jobTemplate = %#v, want %#v", s.RunnerJobTemplate, want)
	}
	if want := (jsonObject{"spec": map[string]any{"priorityClassName": "kritika-runner", "hostUsers": false}}); !reflect.DeepEqual(s.RunnerPodTemplate, want) {
		t.Fatalf("podTemplate = %#v, want %#v", s.RunnerPodTemplate, want)
	}
}

func TestSettingsSources(t *testing.T) {
	t.Setenv("KRITIKA_REVIEW_WORKERS", "5")
	t.Setenv("KRITIKA_RUNNER_JOB_TEMPLATE", `{"spec":{"activeDeadlineSeconds":1200}}`)
	s, _, err := LoadSettings([]byte(`pollInterval: 0s
runner:
  serviceAccount: ""
  podTemplate:
    spec:
      resources:
        requests:
          cpu: 10m
      nodeSelector:
        node-role.example.com/runner: "true"
      hostUsers: false
`))
	if err != nil {
		t.Fatal(err)
	}
	if s.ReviewWorkers != 5 || s.IndexWorkers != 1 || s.PollInterval != 0 || s.RunnerServiceAccount != "" || s.RunnerDeadline() != 20*time.Minute {
		t.Fatalf("merged settings = %+v", s)
	}
	wantPod := jsonObject{"spec": map[string]any{
		"resources":    map[string]any{"requests": map[string]any{"cpu": "10m"}},
		"nodeSelector": map[string]any{"node-role.example.com/runner": "true"},
		"hostUsers":    false,
	}}
	if !reflect.DeepEqual(s.RunnerPodTemplate, wantPod) {
		t.Fatalf("podTemplate = %#v", s.RunnerPodTemplate)
	}
	if !s.fileEnv["KRITIKA_POLL_INTERVAL"] || !s.fileEnv["KRITIKA_RUNNER_POD_TEMPLATE"] || s.fileEnv["KRITIKA_REVIEW_WORKERS"] ||
		s.fileEnv["KRITIKA_RUNNER_JOB_TEMPLATE"] {
		t.Fatalf("file provenance = %v", s.fileEnv)
	}
}

func TestSettingsTypes(t *testing.T) {
	for _, tt := range []struct{ name, doc, want string }{
		{"duration requires units", "gatewayTokenTtl: 600", "gatewayTokenTtl"},
		{"duration must be scalar", "pollInterval: [10m]", "pollInterval"},
		{"workers must be numeric", "reviewWorkers: '4'", "reviewWorkers"},
		{"workers must be whole", "reviewWorkers: 4.5", "reviewWorkers"},
		{"workers must fit", "reviewWorkers: 18446744073709551616", "reviewWorkers"},
		{"unsigned workers must fit", "reviewWorkers: 18446744073709551615", "reviewWorkers"},
		{"workers cannot be NaN", "reviewWorkers: .nan", "reviewWorkers"},
		{"no boolean coercion", "logLevel: true", "logLevel"},
		{"duration zero requires units", "pollInterval: 0", "pollInterval"},
		{"days are not duration units", "pollInterval: 1d", "pollInterval"},
		{"nested keys are case sensitive", "runner: {ServiceAccount: named}", "ServiceAccount"},
		{"omitted keys are case sensitive", "runner: {PodTemplate: {}}", "PodTemplate"},
		{"unknown null is not omitted", "runner: {unknown: null}", "unknown"},
		{"templates must be maps", "runner: {podTemplate: [1]}", "runner.podTemplate"},
		{"cache size in decimal units", "gatewayCacheMaxSize: 2GB", "gatewayCacheMaxSize"},
		{"cache size in a fraction", "gatewayCacheMaxSize: 1.5Gi", "gatewayCacheMaxSize"},
		{"cache size must be whole bytes", "gatewayCacheMaxSize: 1.5", "gatewayCacheMaxSize"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if _, _, err := LoadSettings([]byte(tt.doc)); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("LoadSettings = %v, want %s", err, tt.want)
			}
		})
	}
}

func TestNullSettingsAreOmitted(t *testing.T) {
	for _, doc := range []string{"pollInterval: null", "pollInterval:", "runner: null", "runner: {jobTemplate: null}"} {
		t.Run(doc, func(t *testing.T) {
			t.Setenv("KRITIKA_POLL_INTERVAL", "5m")
			t.Setenv("KRITIKA_RUNNER_JOB_TEMPLATE", `{"spec":{"activeDeadlineSeconds":1200}}`)
			s, _, err := LoadSettings([]byte(doc))
			if err != nil {
				t.Fatal(err)
			}
			if s.PollInterval != 5*time.Minute || s.RunnerDeadline() != 20*time.Minute || len(s.fileEnv) != 0 {
				t.Fatalf("null changed settings or provenance: %+v", s)
			}
		})
	}
	s, _, err := LoadSettings([]byte("pollInterval: null\nrunner: {jobTemplate: null}"))
	if err != nil || s.PollInterval != 10*time.Minute || s.RunnerDeadline() != 15*time.Minute || len(s.fileEnv) != 0 {
		t.Fatalf("null changed defaults: %+v, %v", s, err)
	}
}

func TestEnvFormatsStructuredFileSettings(t *testing.T) {
	s, _, err := LoadSettings([]byte(`runner:
  podTemplate: {spec: {resources: {requests: {cpu: 10m}}}}
`))
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{Settings: s}
	for _, e := range cfg.Env() {
		switch e.Name {
		case "KRITIKA_RUNNER_POD_TEMPLATE":
			if e.Value != `{"spec":{"resources":{"requests":{"cpu":"10m"}}}}` || !e.File || e.Path != "runner.podTemplate" {
				t.Fatalf("podTemplate = %+v", e)
			}
		}
	}
}

func TestByteSize(t *testing.T) {
	for _, tt := range []struct {
		in   string
		want ByteSize
		ok   bool
	}{
		{"0", 0, true},
		{"1048576", 1 << 20, true},
		{"512Ki", 512 << 10, true},
		{"2Gi", 2 << 30, true},
		{"1Ti", 1 << 40, true},
		{"", 0, false},
		{"Gi", 0, false},
		{"-1Gi", 0, false},
		{"2G", 0, false},
		{"2gi", 0, false},
		{"8388608Ti", 0, false},
	} {
		var got ByteSize
		err := got.UnmarshalText([]byte(tt.in))
		if (err == nil) != tt.ok || got != tt.want {
			t.Errorf("ByteSize(%q) = %d, %v; want %d, ok %v", tt.in, got, err, tt.want, tt.ok)
		}
	}
	s, _, err := LoadSettings([]byte("gatewayCacheMaxSize: 1048576"))
	if err != nil || s.GatewayCacheMaxSize != 1<<20 {
		t.Fatalf("a size in bytes from the file = %d, %v", s.GatewayCacheMaxSize, err)
	}
	if s, _, err = LoadSettings(nil); err != nil || s.GatewayCacheMaxSize != 2<<30 || s.GatewayCacheMaxAge != 24*time.Hour || s.GatewayCacheDir != "" {
		t.Fatalf("defaults: size %d, age %s, dir %q, %v", s.GatewayCacheMaxSize, s.GatewayCacheMaxAge, s.GatewayCacheDir, err)
	}
}
