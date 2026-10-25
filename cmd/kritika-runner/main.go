// Command kritika-runner is the kritika of a runner Job: "kritika run"
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
	"github.com/home-operations/kritika/internal/store"
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
		return fmt.Errorf("unknown arguments %q: usage: kritika run", args)
	}
	cfg, err := config.Load()
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
	// GOMEMLIMIT is set or AUTOMEMLIMIT=off: the GC is otherwise unaware of
	// the limit, so a parse of a large repository could OOM-kill the pod
	// before it reclaims. On Linux it is refreshed, for a pod resized in
	// place; elsewhere there is no cgroup, and each refresh would log its
	// failure. Set logs its own failure, which leaves the limit as it was.
	memOpts := []memlimit.Option{memlimit.WithLogger(logger)}
	if runtime.GOOS == "linux" {
		memOpts = append(memOpts, memlimit.WithRefreshInterval(ctx, memLimitRefresh))
	}
	_, _ = memlimit.Set(memOpts...)

	// The management listener comes up first so liveness answers while the
	// database is still starting. Whatever ends run ends it first, and run
	// waits for it before the store closes.
	mgmt := server.NewManagement(cfg.MetricsAddr, version, commit, logger)
	g, ctx := errgroup.WithContext(ctx)
	ctx, cancel := context.WithCancel(ctx)
	var st *store.Store
	defer func() {
		cancel()
		if werr := g.Wait(); werr != nil && !errors.Is(werr, context.Canceled) && !errors.Is(err, werr) {
			logger.Warn("a listener failed", "error", werr)
		}
		if st != nil {
			st.Close()
		}
	}()
	g.Go(func() error { return mgmt.Run(ctx) })

	// A runner pod gets the runner role's DSN under KRITIKA_DATABASE_URL:
	// Open refuses one that could bypass row-level security or does not
	// inherit that role.
	st, err = store.OpenWhenReady(ctx, store.Options{AppURL: cfg.DatabaseURL, ExpectedRole: cfg.DatabaseRunnerRole, Logger: logger})
	if err != nil {
		return err
	}
	// A runner does one thing and exits; it never becomes ready.
	return runner.Run(ctx, st, spec, runspec.Secrets{
		GitToken: cfg.GitToken, GatewayToken: cfg.GatewayToken, DatabaseToken: cfg.DatabaseToken,
	}, logger)
}

func newLogger(cfg *config.Config) *slog.Logger {
	opts := &slog.HandlerOptions{Level: cfg.Level()}
	if strings.EqualFold(cfg.LogFormat, "text") {
		return slog.New(slog.NewTextHandler(os.Stdout, opts))
	}
	return slog.New(slog.NewJSONHandler(os.Stdout, opts))
}
