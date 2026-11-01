// Command kritika-runner executes a runner Job: "kritika-runner run"
// carries out the one review, follow-up or index run its job document
// names, then exits. It is built apart from kritika serve, so a runner's
// image carries none of serve's packages.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/KimMachineGun/automemlimit/memlimit"
	"golang.org/x/sync/errgroup"

	"github.com/home-operations/kritika/internal/config"
	"github.com/home-operations/kritika/internal/model"
	"github.com/home-operations/kritika/internal/runner"
	"github.com/home-operations/kritika/internal/runspec"
	"github.com/home-operations/kritika/internal/server"
)

// Build metadata, set via -ldflags at release time (see Dockerfile / release.yaml).
var (
	version = "dev"
	commit  = "unknown"
)

// memLimitRefresh is how often GOMEMLIMIT follows the cgroup memory limit.
const memLimitRefresh = time.Minute

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	if err := run(); err != nil {
		slog.Error("kritika exited", "error", err)
		os.Exit(1)
	}
}

func run() (err error) {
	if args := os.Args[1:]; len(args) != 1 || args[0] != "run" {
		return fmt.Errorf("unknown arguments %q: usage: kritika-runner run", args)
	}
	cfg, err := config.LoadRunner()
	if err != nil {
		return err
	}
	if err := cfg.ValidateRunner(); err != nil {
		return err
	}
	spec, err := runspec.Read(cfg.RunSpecFile)
	if err != nil {
		return err
	}

	logger := newLogger(cfg)
	slog.SetDefault(logger)
	model.Version = version
	logger.Info("starting kritika", "version", version, "commit", commit, "command", "run", "metrics_addr", cfg.MetricsAddr)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	context.AfterFunc(ctx, stop)
	// GOMEMLIMIT at 90% of the container's cgroup memory limit, unless
	// GOMEMLIMIT is set or AUTOMEMLIMIT=off. Otherwise the GC is unaware of the
	// limit, so a parse of a large repository can OOM-kill the pod before the
	// GC reclaims. On Linux, the limit is refreshed, for a pod resized in
	// place. Elsewhere there is no cgroup, and each refresh will log its
	// failure. Set logs its own failure, which leaves the limit as it was.
	memOpts := []memlimit.Option{memlimit.WithLogger(logger)}
	if runtime.GOOS == "linux" {
		memOpts = append(memOpts, memlimit.WithRefreshInterval(ctx, memLimitRefresh))
	}
	_, _ = memlimit.Set(memOpts...)

	// Whatever ends run ends the management listener first, and run waits for
	// it.
	mgmt := server.NewManagement(cfg.MetricsAddr, version, commit, logger)
	g, ctx := errgroup.WithContext(ctx)
	ctx, cancel := context.WithCancel(ctx)
	defer func() {
		cancel()
		if werr := g.Wait(); werr != nil && !errors.Is(werr, context.Canceled) && !errors.Is(err, werr) {
			logger.Warn("a listener failed", "error", werr)
		}
	}()
	g.Go(func() error { return mgmt.Run(ctx) })

	// A runner does one thing and exits. It never becomes ready.
	return runner.Run(ctx, spec, runspec.Secrets{
		GitToken: cfg.GitToken, GatewayToken: cfg.GatewayToken, ReportToken: cfg.ReportToken,
	}, logger)
}

func newLogger(cfg *config.Config) *slog.Logger {
	opts := &slog.HandlerOptions{Level: cfg.Level()}
	if strings.EqualFold(cfg.LogFormat, "text") {
		return slog.New(slog.NewTextHandler(os.Stdout, opts))
	}
	return slog.New(slog.NewJSONHandler(os.Stdout, opts))
}
