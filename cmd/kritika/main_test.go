package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/riverqueue/river"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/home-operations/kritika/internal/config"
	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/configfile/configfiletest"
	"github.com/home-operations/kritika/internal/executor"
	"github.com/home-operations/kritika/internal/jobs"
	"github.com/home-operations/kritika/internal/server"
)

func parseAccount(t *testing.T, slug string) *configfile.File {
	t.Helper()
	t.Setenv("TEST_MAIN_TOKEN", "tok")
	return configfiletest.Load(t, `
apps:
  `+slug+`-bot:
    accounts: [`+slug+`]
    clientId: Iv1.test
    privateKey: { env: TEST_MAIN_TOKEN }
    webhookSecret: { env: TEST_MAIN_TOKEN }
`)
}

// recordApplied is an onApplied that reports applied and returns err.
func recordApplied(ch chan<- string, err error) func(context.Context) error {
	return func(context.Context) error {
		ch <- "applied"
		return err
	}
}

// applyStage is the kritika_config_error value on reg, -1 when absent.
func applyStage(reg *prometheus.Registry) float64 {
	families, _ := reg.Gather()
	for _, mf := range families {
		if mf.GetName() == "kritika_config_error" && len(mf.GetMetric()) == 1 {
			return mf.GetMetric()[0].GetGauge().GetValue()
		}
	}
	return -1
}

// TestApplyConfig: a refusal for the configuration's content is logged,
// raised on the gauge and recorded as an Event on the pod without ending
// leadership, a database error ends it, and the onApplied error is
// returned as is.
func TestApplyConfig(t *testing.T) {
	for _, tt := range []struct {
		name      string
		apply     error
		onApplied error
		wantErr   string
		wantGauge float64
		applied   bool
		// event is the reason of the Event recorded on the pod, if any.
		event string
	}{
		{name: "applied", wantGauge: 0, applied: true, event: "ConfigApplied"},
		{name: "refused for its content", apply: fmt.Errorf("store: account refused: %w", &pgconn.PgError{Code: "23514"}), wantGauge: 1,
			event: "ConfigRefused"},
		{name: "database error", apply: errors.New("connection reset"), wantErr: "connection reset", wantGauge: 0},
		{name: "onApplied error", onApplied: errors.New("enqueue failed"), wantErr: "enqueue failed", wantGauge: 0, applied: true,
			event: "ConfigApplied"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			reg := prometheus.NewRegistry()
			gauge := server.NewConfigErrorGauge(reg)
			applied := make(chan string, 1)
			client := fake.NewSimpleClientset()
			events := executor.NewEvents(client, "kritika", "kritika-7c9f5d6b8-xk2lp", "pod-uid", slog.New(slog.DiscardHandler))
			err := applyConfig(t.Context(), parseAccount(t, "good"), func(context.Context, *configfile.File) error { return tt.apply },
				recordApplied(applied, tt.onApplied), gauge, events, slog.New(slog.DiscardHandler))
			if (err == nil) != (tt.wantErr == "") || err != nil && err.Error() != tt.wantErr {
				t.Fatalf("applyConfig = %v, want %q", err, tt.wantErr)
			}
			if (len(applied) != 0) != tt.applied {
				t.Fatalf("onApplied called: %v, want %v", len(applied) != 0, tt.applied)
			}
			if got := applyStage(reg); got != tt.wantGauge {
				t.Fatalf("config error gauge = %v, want %v", got, tt.wantGauge)
			}
			list, err := client.EventsV1().Events("kritika").List(t.Context(), metav1.ListOptions{})
			if err != nil {
				t.Fatal(err)
			}
			reasons := make([]string, 0, len(list.Items))
			for _, e := range list.Items {
				reasons = append(reasons, e.Type+" "+e.Reason+" "+e.Regarding.Kind)
			}
			want := []string{}
			if tt.event != "" {
				kind := map[string]string{"ConfigApplied": "Normal", "ConfigRefused": "Warning"}[tt.event]
				want = []string{kind + " " + tt.event + " Pod"}
			}
			if !slices.Equal(reasons, want) {
				t.Fatalf("events = %q, want %q", reasons, want)
			}
		})
	}
}

// TestLoadConfigRefusesNoSignIn: serve refuses a configuration that leaves
// the dashboard no way to sign in.
func TestLoadConfigRefusesNoSignIn(t *testing.T) {
	for _, tt := range []struct {
		name, raw string
		wantErr   bool
	}{
		{"no sign in", "egress: {allow: [first.example]}", true},
		{"admin", "auth: {admin: {password: {env: TEST_ADMIN_PASSWORD}}}\negress: {allow: [first.example]}", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, []byte(tt.raw), 0o600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("KRITIKA_CONFIG_FILE", path)
			t.Setenv("KRITIKA_DATABASE_URL", "postgres://app@db/kritika")
			t.Setenv("TEST_ADMIN_PASSWORD", "pw")
			cfg, err := config.Load()
			if err != nil {
				t.Fatal(err)
			}
			f, err := loadConfig(cfg)
			if tt.wantErr {
				if !errors.Is(err, errNoSignIn) {
					t.Fatalf("loadConfig = %v", err)
				}
			} else if err != nil || len(f.Egress.Allow) != 1 {
				t.Fatalf("loadConfig = %+v, %v", f, err)
			}
		})
	}
}

func TestConfigurationSnapshot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	raw := []byte("auth: {admin: {password: {env: TEST_ADMIN_PASSWORD}}}\nlogLevel: debug\npollInterval: 5m\n")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KRITIKA_CONFIG_FILE", path)
	t.Setenv("KRITIKA_DATABASE_URL", "postgres://app@db/kritika")
	t.Setenv("TEST_ADMIN_PASSWORD", "pw")
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("logLevel: error\npollInterval: 10m\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	original, err := configfile.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("KRITIKA_POLL_INTERVAL", "10m")
	f, err := loadConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if f.Run.LogLevel != cfg.LogLevel || f.Run.PollInterval != cfg.PollInterval {
		t.Fatal("process and policy loader used different configuration snapshots")
	}
	if original.Hash() != f.Hash() {
		t.Fatalf("snapshot hash changed: %v", err)
	}
}

func TestCheckConfig(t *testing.T) {
	const admin = "auth: {admin: {password: {env: TEST_ADMIN_PASSWORD}}}\n"
	for _, tt := range []struct {
		name, raw string
		webURL    string
		noFile    bool
		wantErr   string
	}{
		{name: "valid", raw: admin + "webUrl: https://kritika.example.com\n"},
		{name: "web URL from environment", raw: admin, webURL: "https://kritika.example.com"},
		{name: "missing web URL", raw: admin, wantErr: "webUrl: KRITIKA_WEB_URL"},
		{name: "empty web URL", raw: admin + "webUrl: ''\n", wantErr: "webUrl: KRITIKA_WEB_URL"},
		{name: "unknown key", raw: admin + "repositories: []\n", wantErr: "field repositories not found"},
		{name: "invalid setting", raw: admin + "logLevel: loud\n", wantErr: "logLevel"},
		{name: "unset secret", raw: "auth: {admin: {password: {env: TEST_UNSET_PASSWORD}}}\n", wantErr: "TEST_UNSET_PASSWORD is not set"},
		{name: "secret serve unsets", raw: "auth: {admin: {password: {env: KRITIKA_GIT_TOKEN}}}\nwebUrl: https://kritika.example.com\n",
			wantErr: "KRITIKA_GIT_TOKEN is not set"},
		{name: "no sign in", raw: "egress: {allow: [first.example]}\n", wantErr: errNoSignIn.Error()},
		{name: "runner templates", raw: admin + "webUrl: https://kritika.example.com\n" +
			"runner: {jobTemplate: {spec: {ttlSecondsAfterFinished: 60}}, podTemplate: {spec: {containers: [{name: runner, image: runner:1}]}}}\n"},
		{name: "invalid job template", raw: admin + "webUrl: https://kritika.example.com\n" +
			"runner: {jobTemplate: {spec: {ttlSecondsAfterFinished: 0}}}\n", wantErr: "runner.jobTemplate"},
		{name: "unknown pod template field", raw: admin + "webUrl: https://kritika.example.com\n" +
			"runner: {podTemplate: {spec: {bogusField: 1}}}\n", wantErr: "runner.podTemplate"},
		{name: "no file named", noFile: true, wantErr: "usage"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			// A check needs neither the database nor KRITIKA_CONFIG_FILE.
			t.Setenv("KRITIKA_DATABASE_URL", "")
			t.Setenv("KRITIKA_CONFIG_FILE", "")
			// Set, but serve unsets it before the file's references resolve.
			t.Setenv("KRITIKA_GIT_TOKEN", "token")
			if tt.webURL != "" {
				t.Setenv("KRITIKA_WEB_URL", tt.webURL)
			}
			t.Setenv("TEST_ADMIN_PASSWORD", "pw")
			var args []string
			if !tt.noFile {
				path := filepath.Join(t.TempDir(), "config.yaml")
				if err := os.WriteFile(path, []byte(tt.raw), 0o600); err != nil {
					t.Fatal(err)
				}
				args = []string{path}
			}
			var out strings.Builder
			err := checkConfig(args, &out)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("checkConfig = %v, want an error naming %q", err, tt.wantErr)
				}
				return
			}
			if err != nil || !strings.Contains(out.String(), "the configuration is valid") {
				t.Fatalf("checkConfig = %v, output %q", err, out.String())
			}
		})
	}
}

func TestCheckConfigReadsConfigFileVariable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("auth: {admin: {password: {env: TEST_ADMIN_PASSWORD}}}\nwebUrl: https://kritika.example.com\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KRITIKA_CONFIG_FILE", path)
	t.Setenv("TEST_ADMIN_PASSWORD", "pw")
	var out strings.Builder
	if err := checkConfig(nil, &out); err != nil || !strings.HasPrefix(out.String(), path+":") {
		t.Fatalf("checkConfig = %v, output %q", err, out.String())
	}
}

func TestChatGPTLoginRequiresConfigFile(t *testing.T) {
	if err := validate(config.CommandChatGPTLogin, &config.Config{}); err == nil || !strings.Contains(err.Error(), "KRITIKA_CONFIG_FILE") {
		t.Fatalf("validate = %v", err)
	}
}

func TestStoreOptionsOwnerDSN(t *testing.T) {
	cfg := &config.Config{DatabaseURL: "postgres://app", DatabaseOwnerURL: "postgres://owner", DatabaseRunnerRole: "custom_runner"}
	opts := storeOptions(cfg, slog.New(slog.DiscardHandler))
	if opts.OwnerURL != "postgres://owner" || opts.AppURL != "postgres://app" || opts.ExpectedRole != "" {
		t.Errorf("owner = %q, app = %q, role = %q; want the owner and app DSNs, no role", opts.OwnerURL, opts.AppURL, opts.ExpectedRole)
	}
}

func TestWorkerQueuesAreMonitored(t *testing.T) {
	queues := workerQueues(&config.Config{ReviewWorkers: 2, ThreadWorkers: 1, IndexWorkers: 3})
	if len(queues) != len(jobs.Queues()) {
		t.Fatalf("worker queues = %v, monitored queues = %v", queues, jobs.Queues())
	}
	for _, name := range jobs.Queues() {
		if queue, ok := queues[name]; !ok || queue.MaxWorkers <= 0 {
			t.Errorf("queue %q has no worker capacity", name)
		}
	}
}

// TestRiverConfig: the working client keeps River's reindexer off, which
// the application role could never run, and gives a job its timeout cut
// the headroom its detached end takes before calling it stuck.
func TestRiverConfig(t *testing.T) {
	c := riverConfig(&config.Config{}, river.NewWorkers(), nil, slog.New(slog.DiscardHandler))
	if c.ReindexerIndexNames == nil || len(c.ReindexerIndexNames) != 0 {
		t.Errorf("reindexer indexes = %#v, want an empty list: nil is River's default set", c.ReindexerIndexNames)
	}
	if c.JobStuckThreshold != queueStopHeadroom {
		t.Errorf("stuck threshold = %s, want %s", c.JobStuckThreshold, queueStopHeadroom)
	}
}

func TestRunnerTemplateErrorsNameBothSources(t *testing.T) {
	for _, tt := range []struct {
		name string
		cfg  config.Config
		want []string
	}{
		{"jobTemplate", config.Config{RunnerJobTemplate: map[string]any{"spec": map[string]any{"template": map[string]any{}}}},
			[]string{"runner.jobTemplate", "KRITIKA_RUNNER_JOB_TEMPLATE"}},
		{"podTemplate", config.Config{RunnerPodTemplate: map[string]any{"spec": map[string]any{"serviceAccountName": "default"}}},
			[]string{"runner.podTemplate", "KRITIKA_RUNNER_POD_TEMPLATE"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := newExecutor(t.Context(), &tt.cfg, slog.New(slog.DiscardHandler))
			for _, want := range tt.want {
				if err == nil || !strings.Contains(err.Error(), want) {
					t.Fatalf("newExecutor = %v, want %s named", err, want)
				}
			}
		})
	}
}
