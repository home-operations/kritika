package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/home-operations/kritika/internal/config"
	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/configfile/configfiletest"
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
	path := filepath.Join(t.TempDir(), "kritika.yaml")
	if err := os.WriteFile(path, []byte("egress: { allow: [first.example] }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadConfig(path); !errors.Is(err, errNoSignIn) {
		t.Fatalf("loadConfig = %v, want errNoSignIn", err)
	}
	t.Setenv("TEST_ADMIN_PASSWORD", "pw")
	if err := os.WriteFile(path, []byte("auth: { admin: { password: { env: TEST_ADMIN_PASSWORD } } }\negress: { allow: [first.example] }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := loadConfig(path)
	if err != nil || len(f.Egress.Allow) != 1 {
		t.Fatalf("loadConfig = %+v, %v", f, err)
	}
}

func TestStoreOptionsOwnerDSN(t *testing.T) {
	cfg := &config.Config{DatabaseURL: "postgres://app", DatabaseOwnerURL: "postgres://owner"}
	for _, tt := range []struct {
		command config.Command
		owner   string
	}{
		{config.CommandServe, "postgres://owner"},
		{config.CommandRun, ""},
	} {
		t.Run(string(tt.command), func(t *testing.T) {
			opts := storeOptions(tt.command, cfg, slog.New(slog.DiscardHandler))
			if opts.OwnerURL != tt.owner || opts.AppURL != "postgres://app" {
				t.Errorf("owner = %q, app = %q; want owner %q", opts.OwnerURL, opts.AppURL, tt.owner)
			}
		})
	}
}
