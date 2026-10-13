package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/home-operations/kritika/internal/config"
	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/configfile/configfiletest"
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

// TestApplyConfig: a refusal for the configuration's content is logged and
// raised on the gauge without ending leadership, a database error ends
// it, and the onApplied error is returned as is.
func TestApplyConfig(t *testing.T) {
	for _, tt := range []struct {
		name      string
		apply     error
		onApplied error
		wantErr   string
		wantGauge float64
		applied   bool
	}{
		{name: "applied", wantGauge: 0, applied: true},
		{name: "refused for its content", apply: fmt.Errorf("store: account refused: %w", &pgconn.PgError{Code: "23514"}), wantGauge: 1},
		{name: "database error", apply: errors.New("connection reset"), wantErr: "connection reset", wantGauge: 0},
		{name: "onApplied error", onApplied: errors.New("enqueue failed"), wantErr: "enqueue failed", wantGauge: 0, applied: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			reg := prometheus.NewRegistry()
			gauge := server.NewConfigErrorGauge(reg)
			applied := make(chan string, 1)
			err := applyConfig(t.Context(), parseAccount(t, "good"), func(context.Context, *configfile.File) error { return tt.apply },
				recordApplied(applied, tt.onApplied), gauge, slog.New(slog.DiscardHandler))
			if (err == nil) != (tt.wantErr == "") || err != nil && err.Error() != tt.wantErr {
				t.Fatalf("applyConfig = %v, want %q", err, tt.wantErr)
			}
			if (len(applied) != 0) != tt.applied {
				t.Fatalf("onApplied called: %v, want %v", len(applied) != 0, tt.applied)
			}
			if got := applyStage(reg); got != tt.wantGauge {
				t.Fatalf("config error gauge = %v, want %v", got, tt.wantGauge)
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

func TestChatGPTLoginRequiresConfigFile(t *testing.T) {
	if _, err := validate(config.CommandChatGPTLogin, &config.Config{}); err == nil || !strings.Contains(err.Error(), "KRITIKA_CONFIG_FILE") {
		t.Fatalf("validate = %v", err)
	}
}

func TestStoreOptionsOwnerDSN(t *testing.T) {
	cfg := &config.Config{DatabaseURL: "postgres://app", DatabaseOwnerURL: "postgres://owner", DatabaseRunnerRole: "custom_runner"}
	for _, tt := range []struct {
		command config.Command
		owner   string
	}{
		{config.CommandServe, "postgres://owner"},
		{config.CommandRun, ""},
	} {
		t.Run(string(tt.command), func(t *testing.T) {
			opts := storeOptions(tt.command, cfg, slog.New(slog.DiscardHandler))
			if tt.command == config.CommandRun && opts.ExpectedRole != "custom_runner" {
				t.Fatalf("runner role = %q", opts.ExpectedRole)
			}
			if opts.OwnerURL != tt.owner || opts.AppURL != "postgres://app" {
				t.Errorf("owner = %q, app = %q; want owner %q", opts.OwnerURL, opts.AppURL, tt.owner)
			}
		})
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

func TestRunnerResourceErrorNamesBothSources(t *testing.T) {
	_, err := newExecutor(t.Context(), &config.Config{}, map[string]any{"limits": map[string]any{"memory": "invalid"}}, slog.New(slog.DiscardHandler))
	if err == nil || !strings.Contains(err.Error(), "runner.resources") || !strings.Contains(err.Error(), "KRITIKA_RUNNER_RESOURCES") {
		t.Fatalf("newExecutor = %v", err)
	}
}
