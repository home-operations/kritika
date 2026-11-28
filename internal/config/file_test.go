package config

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/home-operations/kritika/internal/tuning"
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
	if s, _, err = LoadSettings(nil); err != nil || s.GatewayCacheMaxSize != 2<<30 || s.GatewayCacheDir != "" {
		t.Fatalf("defaults: size %d, dir %q, %v", s.GatewayCacheMaxSize, s.GatewayCacheDir, err)
	}
}

func TestTuning(t *testing.T) {
	const name = "KRITIKA_TUNING_GATEWAY_SIMILAR_PER_QUERY"
	for _, tt := range []struct {
		name, doc, value, wantErr string
		want                      int
	}{
		{name: "default", want: 4},
		{name: "environment", value: "6", want: 6},
		{name: "file", doc: "tuning: {gateway: {similarPerQuery: 6}}", want: 6},
		{name: "file beside other settings", doc: "reviewWorkers: 3\ntuning:\n  gateway:\n    similarPerQuery: 6\n", want: 6},
		{name: "null", doc: "tuning: {gateway: {similarPerQuery: null}}", want: 4},
		{name: "both", doc: "tuning: {gateway: {similarPerQuery: 6}}", value: "6",
			wantErr: "tuning.gateway.similarPerQuery conflicts with " + name},
		{name: "unknown key", doc: "tuning: {gateway: {bogus: 1}}", wantErr: "bogus"},
		{name: "unknown area", doc: "tuning: {nowhere: {similarPerQuery: 1}}", wantErr: "nowhere"},
		{name: "zero from the file", doc: "tuning: {gateway: {similarPerQuery: 0}}",
			wantErr: "tuning.gateway.similarPerQuery: " + name + " must be positive"},
		{name: "zero from the environment", value: "0", wantErr: "tuning.gateway.similarPerQuery: " + name + " must be positive"},
		{name: "not a number", value: "many", wantErr: "config: environment variable " + name + ": "},
		{name: "fraction", doc: "tuning: {gateway: {similarPerQuery: 1.5}}", wantErr: "whole number"},
		{name: "fraction of an int64", doc: "tuning: {agent: {defaultMaxTokens: 1000.5}}", wantErr: "whole number"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, []byte(tt.doc), 0o600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("KRITIKA_CONFIG_FILE", path)
			t.Setenv("KRITIKA_DATABASE_URL", "postgres://app@db/kritika")
			if tt.value != "" {
				t.Setenv(name, tt.value)
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
			if c.Tuning.Gateway.SimilarPerQuery != tt.want || c.Tuning.Gateway.SimilarMax != 10 {
				t.Fatalf("tuning.gateway = %+v, want similarPerQuery %d and the other defaults", c.Tuning.Gateway, tt.want)
			}
		})
	}
}

// TestTuningFromFile follows a file's tuning block from LoadFile, as
// kritika config check and serve read it, to tuning.Get and Env.
func TestTuningFromFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("tuning: {gateway: {similarPerQuery: 6}}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	tuning.Override(t, func(v *tuning.Values) { *v = cfg.Tuning })
	if got := tuning.Get().Gateway.SimilarPerQuery; got != 6 {
		t.Fatalf("tuning.Get().Gateway.SimilarPerQuery = %d, want 6", got)
	}
	var found bool
	for _, e := range cfg.Env() {
		switch e.Name {
		case "KRITIKA_TUNING_GATEWAY_SIMILAR_PER_QUERY":
			found = true
			if !e.File || e.Set || e.Path != "tuning.gateway.similarPerQuery" || e.Value != "6" {
				t.Fatalf("Env = %+v, want the file's 6 at tuning.gateway.similarPerQuery", e)
			}
		case "KRITIKA_TUNING_GATEWAY_SIMILAR_MAX":
			if e.File || e.Value != "10" {
				t.Fatalf("Env = %+v, want the default 10", e)
			}
		}
	}
	if !found {
		t.Fatal("Env does not list KRITIKA_TUNING_GATEWAY_SIMILAR_PER_QUERY")
	}
	if got := tuning.Env(cfg.Tuning); len(got) != 1 || got[0] != (tuning.EnvPair{Name: "KRITIKA_TUNING_GATEWAY_SIMILAR_PER_QUERY", Value: "6"}) {
		t.Fatalf("tuning.Env = %v, want the one file setting", got)
	}
}

// TestTuningEmptyList: an empty token lifetime list, which turns token
// creation off, reads from the file's [] and from the environment's [], and
// serve hands it to its runners as [], which reads back empty.
func TestTuningEmptyList(t *testing.T) {
	const name = "KRITIKA_TUNING_WEB_API_TOKEN_LIFETIMES"
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("tuning: {webApi: {tokenLifetimes: []}}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Tuning.WebAPI.TokenLifetimes; len(got) != 0 {
		t.Fatalf("from the file, TokenLifetimes = %v, want none", got)
	}
	pairs := tuning.Env(cfg.Tuning)
	if want := []tuning.EnvPair{{Name: name, Value: tuning.EmptyList}}; !slices.Equal(pairs, want) {
		t.Fatalf("tuning.Env = %v, want %v", pairs, want)
	}
	// A runner reads what serve hands it.
	t.Setenv(name, pairs[0].Value)
	runner, err := LoadRunner()
	if err != nil {
		t.Fatal(err)
	}
	if got := runner.Tuning.WebAPI.TokenLifetimes; got == nil || len(got) != 0 {
		t.Fatalf("from the environment, TokenLifetimes = %#v, want none", got)
	}
}

// TestSettingsValidateTuning: the shared settings loader, which
// configfile.Parse reads through, refuses what tuning's Validate does.
func TestSettingsValidateTuning(t *testing.T) {
	_, _, err := LoadSettings([]byte("tuning: {gateway: {similarPerQuery: 0}}"))
	if want := "tuning.gateway.similarPerQuery: KRITIKA_TUNING_GATEWAY_SIMILAR_PER_QUERY must be positive"; err == nil ||
		!strings.Contains(err.Error(), want) {
		t.Fatalf("LoadSettings = %v, want %q", err, want)
	}
}

// TestRunnerSkipsServesRetention: serve raises the retention minimum with
// its retentions and hands its runners only the tuning. The runner, at the
// default retentions, does not refuse them: it reads no retention.
func TestRunnerSkipsServesRetention(t *testing.T) {
	t.Setenv("KRITIKA_TUNING_WORKER_MIN_RETENTION", "1000h")
	// Load unsets the DSN once read.
	t.Setenv("KRITIKA_DATABASE_URL", "postgres://app@db/kritika")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "must be at least 1000h0m0s") {
		t.Fatalf("serve at the default retentions: Load = %v, want them refused", err)
	}
	if _, err := LoadRunner(); err != nil {
		t.Fatalf("LoadRunner = %v, want the retentions left to serve", err)
	}
	t.Setenv("KRITIKA_TRANSCRIPT_RETENTION", "1000h")
	t.Setenv("KRITIKA_DIFF_RETENTION", "1000h")
	t.Setenv("KRITIKA_DATABASE_URL", "postgres://app@db/kritika")
	if _, err := Load(); err != nil {
		t.Fatalf("serve with its retentions raised: Load = %v", err)
	}
}
