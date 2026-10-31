// Command kritika reviews GitHub pull requests against an index of the
// repository. "kritika serve" runs the service. Each review, follow-up, and
// index run that it makes runs in a runner Job of cmd/kritika-runner's.
// "kritika config check" makes sure that a configuration file is valid
// without serving it.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/KimMachineGun/automemlimit/memlimit"
	"github.com/go-jose/go-jose/v4"
	"github.com/jackc/pgx/v5"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivertype"
	"golang.org/x/sync/errgroup"
	"k8s.io/klog/v2"

	"github.com/home-operations/kritika/internal/adapter"
	"github.com/home-operations/kritika/internal/auth"
	"github.com/home-operations/kritika/internal/config"
	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/egress"
	"github.com/home-operations/kritika/internal/executor"
	"github.com/home-operations/kritika/internal/forge/forgejo"
	"github.com/home-operations/kritika/internal/gateway"
	"github.com/home-operations/kritika/internal/ingest"
	"github.com/home-operations/kritika/internal/jobs"
	"github.com/home-operations/kritika/internal/jobtimeout"
	"github.com/home-operations/kritika/internal/metrics"
	"github.com/home-operations/kritika/internal/model"
	"github.com/home-operations/kritika/internal/poller"
	"github.com/home-operations/kritika/internal/server"
	"github.com/home-operations/kritika/internal/store"
	"github.com/home-operations/kritika/internal/web"
	"github.com/home-operations/kritika/internal/webapi"
	"github.com/home-operations/kritika/internal/worker"
)

// Build metadata, set via -ldflags at release time (see Dockerfile / release.yaml).
var (
	version = "dev"
	commit  = "unknown"
)

// memLimitRefresh is how often GOMEMLIMIT follows the cgroup memory limit.
const memLimitRefresh = time.Minute

func main() {
	// A failure before run sets the configured logger logs as JSON on
	// stdout too, not as the log package's text on stderr.
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	if err := run(); err != nil {
		slog.Error("kritika exited", "error", err)
		os.Exit(1)
	}
}

func run() (err error) {
	command, err := config.ParseCommand(os.Args[1:])
	if err != nil {
		return err
	}
	if command == config.CommandConfigCheck {
		return checkConfig(os.Args[3:], os.Stdout)
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if err := validate(command, cfg); err != nil {
		return err
	}

	logger := newLogger(cfg)
	slog.SetDefault(logger)
	// client-go logs through klog, its informers' list and watch errors
	// among them: through this logger too.
	klog.SetSlogLogger(logger)
	model.Version = version

	logger.Info("starting kritika",
		"version", version,
		"commit", commit,
		"command", command,
		"addr", cfg.Addr,
		"metrics_addr", cfg.MetricsAddr,
		"gateway_addr", cfg.GatewayAddr,
		"gateway_url", cfg.GatewayURL,
		"config_file", cfg.ConfigFile,
		"owner_dsn", cfg.DatabaseOwnerURL != "",
	)

	// Graceful shutdown on the usual termination signals. stop() runs as soon
	// as the first signal arrives. A second signal then restores default
	// handling and force-terminates instead of being swallowed during a slow
	// drain.
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
	if command == config.CommandChatGPTLogin {
		return loginChatGPT(ctx, cfg, os.Args[3:], logger)
	}

	// The management listener comes up first so liveness answers while the
	// database is still starting.
	mgmt := server.NewManagement(cfg.MetricsAddr, version, commit, logger)
	g, ctx := errgroup.WithContext(ctx)
	// Whatever ends run ends the group's goroutines first, and run waits for
	// them before the store closes. The close waits for every connection, and
	// the leader holds its lock connection for its tenure. Without this order,
	// an error returned while the group runs will hang the close while liveness
	// still answers. An error of the group's own that run does not return, such
	// as a listener that a runner's exit cut, is worth a line.
	ctx, cancel := context.WithCancel(ctx)
	var st *store.Store
	defer func() {
		cancel()
		if werr := g.Wait(); werr != nil && !errors.Is(werr, context.Canceled) && !errors.Is(err, werr) {
			logger.Warn("a listener or duty failed", "error", werr)
		}
		if st != nil {
			st.Close()
		}
	}()
	g.Go(func() error { return mgmt.Run(ctx) })

	// serve's public listener comes up next, before the database answers, so
	// a request that reaches the pod directly meanwhile sees that kritika is
	// starting. The replica is not ready until the database answers and the
	// webhooks and the dashboard are served. A rollout takes the previous pod
	// down only once this one can hold a delivery, because the forge does not
	// send one again. The configuration file is read before that, so a file
	// that does not load ends the process before the replica is ever ready. A
	// rollout then stops at it with the previous pods serving.
	var file *configfile.File
	var public *server.Switch
	if command == config.CommandServe {
		if file, err = loadConfig(cfg); err != nil {
			return err
		}
		public = server.NewSwitch(server.Starting())
		g.Go(func() error {
			return server.Serve(server.Lingering(ctx, linger), cfg.Addr, public, publicDrain, logger.With("listener", "public"))
		})
	}

	// serve connects with the application DSN and refuses to start if it can
	// bypass row-level security or the vector extension is missing. It also
	// opens the owner DSN, to lead.
	st, err = store.OpenWhenReady(ctx, storeOptions(cfg, logger))
	if err != nil {
		return err
	}

	if err := serve(ctx, g, st, cfg, file, public, mgmt.Registry(), logger); err != nil {
		return err
	}
	mgmt.SetReady(true)

	if err := g.Wait(); err != nil {
		return fmt.Errorf("%s: %w", command, err)
	}
	// The signal that ended serve, which the context carries as its cause.
	logger.Info("kritika stopped", "cause", context.Cause(ctx))
	return nil
}

// serve starts the service on g. The service has the configuration read at
// startup and the leader duties on the replica that holds the leader lock.
// It also has the webhooks and the dashboard on public, the gateway, and
// the job queues. When serve returns, public serves the webhooks and the
// dashboard.
func serve(
	ctx context.Context, g *errgroup.Group, st *store.Store, cfg *config.Config, file *configfile.File, public *server.Switch,
	reg *prometheus.Registry, logger *slog.Logger,
) error {
	drift := server.NewConfigDriftGauge(reg)
	configErrors := server.NewConfigErrorGauge(reg)
	m := metrics.New(reg)
	for _, account := range file.Accounts {
		m.InitVectorSearch(account.Key())
	}
	queueCollector := metrics.NewQueueCollector(ctx, func(ctx context.Context) ([]metrics.QueueStats, error) {
		stats, err := st.QueueStats(ctx)
		out := make([]metrics.QueueStats, len(stats))
		for i, s := range stats {
			out[i] = metrics.QueueStats(s)
		}
		return out, err
	}, logger)
	reg.MustRegister(metrics.NewPoolCollector(st.PoolStats()), queueCollector)
	logConfig(logger, file, "configuration loaded")
	for _, u := range file.UnpricedModels() {
		logger.Warn("model is unpriced: its provider reports no cost and pricing has no price for it; configure provider pricing",
			"setting", u.Setting, "model_ref", u.Ref)
	}
	// Once read, a secret's variable is dropped, so no later lookup or
	// child process sees it.
	for _, name := range file.SecretEnv() {
		if err := os.Unsetenv(name); err != nil {
			return fmt.Errorf("unset %s: %w", name, err)
		}
	}
	// current is the configuration read at startup. The leader applies it on
	// election, and the other replica only compares hashes.
	current := configfile.NewCurrent(file)
	g.Go(func() error {
		return reportDrift(ctx, st, current, drift, driftInterval, logger)
	})
	exec, err := newExecutor(ctx, cfg, logger)
	if err != nil {
		return err
	}
	// Insert-only client: the webhooks, the dashboard and the leader
	// enqueue jobs with it. startWorker's own client works the queues.
	inserter, err := river.NewClient(riverpgxv5.New(st.App()), &river.Config{Logger: logger})
	if err != nil {
		return fmt.Errorf("river: %w", err)
	}
	// One App or bot per connection and one forge client per connection and
	// account. The workers and the leader's poll share them, so an
	// installation's token is minted once.
	apps := &worker.Forges{Metrics: m, Issuer: forgejo.IssuerURL(cfg.WebURLParsed())}
	forges := &worker.ForgeCache{Build: apps.Build}
	svc := ingest.NewService(st, inserter)
	if st.LeaderEligible() {
		kube, _ := exec.(*executor.Kube)
		var events *executor.Events
		if kube != nil {
			events = kube.Events
		}
		g.Go(func() error {
			return st.RunAsLeader(ctx, cfg.LeaderRetryInterval, func(ctx context.Context) error {
				m.Leading(true)
				defer m.Leading(false)
				events.PodEvent(ctx, false, "LeaderElected", "Lead", "this replica took the lead")
				return lead(ctx, st, cfg, current, inserter, svc, apps, forges, kube, events, m, configErrors, logger)
			})
		})
	} else {
		logger.Warn("no owner DSN configured; this replica can never migrate or apply configuration")
	}
	if err := startPublic(ctx, g, st, cfg, current, inserter, svc, forges, m, public, logger); err != nil {
		return err
	}
	return startWorker(ctx, g, st, cfg, current, exec, forges, m, logger)
}

// errNoSignIn is a configuration that leaves the dashboard no way to sign
// in, which serve refuses to start with.
var errNoSignIn = errors.New("the dashboard has no way to sign in: configure auth in the configuration file")

// loadConfig makes sure that the policy is valid. It uses the same startup
// snapshot as the process configuration.
func loadConfig(cfg *config.Config) (*configfile.File, error) {
	f, err := configfile.FromConfig(cfg)
	if err != nil {
		return nil, err
	}
	if !f.Auth.Configured() {
		return nil, errNoSignIn
	}
	return f, nil
}

// checkConfig loads the configuration file that args name, or else the file
// that KRITIKA_CONFIG_FILE names, as serve does at startup. It says on out
// that serve will accept the file. It reads the environment as serve reads
// it. That includes the variables that the file's secret references name. It
// does not read the database, and it connects to nothing. The configuration
// that a deployment gives serve besides the file, such as the chart's
// gatewayUrl, is not required.
//
// checkConfig decodes the runner templates as the executor decodes them, but
// it leaves the API's dry run of a runner Job to startup.
func checkConfig(args []string, out io.Writer) error {
	path := os.Getenv("KRITIKA_CONFIG_FILE")
	if len(args) > 0 {
		path = args[0]
	}
	if path == "" {
		return errors.New("config: usage: kritika config check [<file>]: name a file or set KRITIKA_CONFIG_FILE")
	}
	cfg, err := config.LoadFile(path)
	if err != nil {
		return err
	}
	if _, err := loadConfig(cfg); err != nil {
		return err
	}
	if cfg.WebURL == "" {
		return errors.New("config: webUrl: KRITIKA_WEB_URL is required to serve")
	}
	if cfg.Executor == config.ExecutorKubernetes {
		if _, err := executor.DecodeJobTemplate(cfg.RunnerJobTemplate); err != nil {
			return fmt.Errorf("runner.jobTemplate: KRITIKA_RUNNER_JOB_TEMPLATE: %w", err)
		}
		// A pod template must name the runner's image, and the chart passes
		// it besides an existingConfigMap's file, which then has none.
		if len(cfg.RunnerPodTemplate) > 0 {
			if _, err := executor.DecodePodTemplate(cfg.RunnerPodTemplate); err != nil {
				return fmt.Errorf("runner.podTemplate: KRITIKA_RUNNER_POD_TEMPLATE: %w", err)
			}
		}
	}
	_, err = fmt.Fprintf(out, "%s: the configuration is valid\n", path)
	return err
}

// startPublic hands the public listener what it serves until ctx ends: the
// webhooks, and the dashboard with its sign-in and API, in place of the
// starting page.
func startPublic(
	ctx context.Context, g *errgroup.Group, st *store.Store, cfg *config.Config, current *configfile.Current,
	inserter *river.Client[pgx.Tx], svc *ingest.Service, forges *worker.ForgeCache, m *metrics.Metrics, public *server.Switch,
	logger *slog.Logger,
) error {
	hooks := ingest.NewHandler(current, svc, logger.With("listener", "hooks"))
	hooks.Metrics = m
	hooks.Deliveries = svc
	hooks.Authors = forges

	webLogger := logger.With("listener", "web")
	authHandler, err := auth.New(auth.Config{Store: st, Current: current, WebURL: cfg.WebURLParsed(), Logger: webLogger})
	if err != nil {
		return err
	}
	api := webapi.New(webapi.Config{
		Store: st, Current: current, Auth: authHandler, UI: web.FS(),
		WebURL: cfg.WebURLParsed(), Version: version, Logger: webLogger, Actions: webapi.JobActions{Queue: inserter}, Env: cfg.Env(),
	})
	g.Go(func() error { return api.Run(server.Lingering(ctx, linger)) })
	// The keys for the verification of every Forgejo connection's tokens. They
	// are read from the configuration on each request, so a rotated key is
	// served at once.
	issuerURL := forgejo.IssuerURL(cfg.WebURLParsed())
	keys := func() []jose.JSONWebKey { return forgejo.Keys(issuerURL, current.Get(), logger) }
	public.Set(server.Public(cfg.WebBasePath(), hooks, forgejo.Discovery(issuerURL, keys), api.Handler()))
	return nil
}

// startWorker serves the gateway and works the job queues until ctx ends.
func startWorker(
	ctx context.Context, g *errgroup.Group, st *store.Store, cfg *config.Config, current *configfile.Current, exec executor.Executor,
	forges *worker.ForgeCache, m *metrics.Metrics, logger *slog.Logger,
) error {
	embedders := &adapter.Embedders{Build: adapter.BuildEmbedder}
	steppers := &adapter.Steppers{Build: adapter.StepperBuilder(st)}
	workers := river.NewWorkers()
	base := worker.Base{Store: st, Current: current, Forges: forges, Logger: logger, Metrics: m}
	// The gateway is the one route out of runner pods, allowed by the hosts
	// that the current configuration names. It also has the model and
	// similar-code endpoints that a review's runner calls with its run token.
	gatewayLogger := logger.With("listener", "gateway")
	gw := &gateway.Server{Store: st, Current: current, Logger: gatewayLogger, Metrics: m, Steppers: steppers, Embedders: embedders}
	gw.AquaRegistry = &gateway.AquaRegistry{
		URL: cfg.AquaRegistryURL, Refresh: cfg.AquaRegistryRefresh, Client: http.DefaultClient, Logger: gatewayLogger,
	}
	g.Go(func() error { return gw.AquaRegistry.Run(ctx) })
	proxy := &egress.Proxy{Rules: current.Get().EgressRules, Observe: m.Egress, Refused: gw.Refused, Logger: gatewayLogger}
	if cfg.GatewayCacheDir != "" {
		cache, err := egress.OpenCache(cfg.GatewayCacheDir, int64(cfg.GatewayCacheMaxSize))
		if err != nil {
			return err
		}
		proxy.Cache = cache
		cache.MaxEntryBytes = int64(cfg.GatewayCacheMaxEntrySize)
		if cfg.GatewayCacheStorage == "s3" {
			if err := cache.UseS3(egress.S3Options{
				Endpoint: cfg.GatewayCacheS3Endpoint, Region: cfg.GatewayCacheS3Region,
				Bucket: cfg.GatewayCacheS3Bucket, Prefix: cfg.GatewayCacheS3Prefix,
				ForcePathStyle: cfg.GatewayCacheS3ForcePathStyle,
				AccessKey:      cfg.GatewayCacheS3AccessKey, SecretKey: cfg.GatewayCacheS3SecretKey,
			}); err != nil {
				return err
			}
		}
		cache.Observe(m, gatewayLogger)
		if cfg.GatewayCacheStorage == "s3" {
			g.Go(func() error { return cache.RunUploads(ctx) })
		}
	}
	gw.Proxy = proxy
	g.Go(func() error {
		return server.Serve(server.Lingering(ctx, linger), cfg.GatewayAddr, gw, gateway.Drain, gatewayLogger)
	})
	reviewer := &worker.Review{
		Base: base, Executor: exec, Steppers: steppers,
		GatewayURL: cfg.GatewayURL, GatewayTokenTTL: cfg.GatewayTokenTTL, WebURL: cfg.WebURLParsed(),
	}
	river.AddWorker(workers, reviewer)
	river.AddWorker(workers, &worker.Prepare{Base: reviewer.Base, Steppers: reviewer.Steppers, WebURL: reviewer.WebURL})
	river.AddWorker(workers, &worker.Publish{Base: reviewer.Base, WebURL: reviewer.WebURL})
	river.AddWorker(workers, &worker.FollowUp{
		Base: base, Executor: exec, Steppers: steppers, GatewayURL: cfg.GatewayURL, GatewayTokenTTL: cfg.GatewayTokenTTL,
	})
	river.AddWorker(workers, &worker.Thread{Base: base})
	river.AddWorker(workers, &worker.Index{
		Base: base, Executor: exec, Embedders: embedders, GatewayTokenTTL: cfg.GatewayTokenTTL,
	})
	// Every job this replica works carries its heartbeat. So the leader can
	// hand back the jobs of a replica that dies without a soft stop long
	// before River's rescuer can.
	middleware := []rivertype.Middleware{
		&worker.JobMetrics{Metrics: m}, &worker.JobHeartbeat{Store: st, Logger: logger}, &worker.ForgeRateLimit{Logger: logger},
	}
	queue, err := river.NewClient(riverpgxv5.New(st.App()), riverConfig(cfg, workers, middleware, logger))
	if err != nil {
		return fmt.Errorf("river: %w", err)
	}
	// A review this replica snoozed for a model slot looks once more for
	// one once its snooze is saved (see worker.SlotWake). A queue start
	// that fails partway closes every subscription, so a closed one is made
	// again for startQueue's retry.
	wake := &worker.SlotWake{Store: st, Current: current, Logger: logger}
	snoozed, unsubscribe := queue.Subscribe(river.EventKindJobSnoozed)
	g.Go(func() error {
		for {
			wake.Run(ctx, snoozed)
			unsubscribe()
			if ctx.Err() != nil {
				return nil
			}
			snoozed, unsubscribe = queue.Subscribe(river.EventKindJobSnoozed)
		}
	})
	var events *executor.Events
	stopWatch := func() {}
	if k, ok := exec.(*executor.Kube); ok {
		events, stopWatch = k.Events, k.Stop
	}
	g.Go(func() error {
		defer stopWatch()
		return workQueues(ctx, st, queue, cfg, events, logger)
	})
	return nil
}

// queueDrain is how long a stopping serve lets running jobs finish. A
// review still running then is cut and retried. queueStopHeadroom covers
// what a cut review does before it hands its job back, each step at its
// own bound: deleting the runner Job and reading its pod (20s each),
// revoking its token (10s) and waiting for its agent row (40s). A job
// still at it when the process is killed is not recorded by River and
// waits on the rescuer instead. Completed runner answers are checkpointed
// before merge and confidence calls, so rescue can resume those answers.
//
// eventsDrain then lets the Events of the runs the stop cut be written. The
// three waits fit in the chart's 200s termination grace period.
const (
	queueDrain        = 100 * time.Second
	queueStopHeadroom = 90 * time.Second
	eventsDrain       = 5 * time.Second
)

// validate makes sure that cfg has what command needs beyond the common set.
func validate(command config.Command, cfg *config.Config) error {
	if command == config.CommandChatGPTLogin {
		if cfg.ConfigFile == "" {
			return errors.New("config: KRITIKA_CONFIG_FILE is required for chatgpt login")
		}
		return nil
	}
	return cfg.ValidateServe()
}

// storeOptions is how serve connects to the database: with the owner DSN
// too, which leading needs.
func storeOptions(cfg *config.Config, logger *slog.Logger) store.Options {
	return store.Options{AppURL: cfg.DatabaseURL, OwnerURL: cfg.DatabaseOwnerURL, Logger: logger}
}

// linger is how long the public listener and the gateway keep accepting
// connections once serve is told to stop. Kubernetes and the proxies in
// front of it take a moment to stop routing to a terminating pod. A
// connection refused meanwhile is a webhook lost, because GitHub does not
// redeliver on its own, or a runner's model step retried.
const linger = 5 * time.Second

// publicDrain is how long a stopping serve lets webhook and dashboard
// requests finish. Event streams end at once, when the API's Run returns.
const publicDrain = 10 * time.Second

// riverConfig configures the working client, which runs on the
// application role's pool.
func riverConfig(cfg *config.Config, workers *river.Workers, middleware []rivertype.Middleware, logger *slog.Logger) *river.Config {
	return &river.Config{
		Logger: logger,
		// Review and index workers set their own timeouts from the
		// runner deadline. Rescue must wait out the longest of them.
		RescueStuckJobsAfter: jobtimeout.RescueStuckJobsAfter,
		// ctx ending starts a soft stop: running jobs get this long before
		// their contexts end.
		SoftStopTimeout: queueDrain,
		// A job that its timeout cut ends its run on detached contexts, as a
		// stopping job does. queueStopHeadroom covers this in the usual case.
		// Without this value, River calls every such job stuck after ten
		// seconds.
		JobStuckThreshold: queueStopHeadroom,
		// If enabled, River's reindexer runs REINDEX on this pool. The role of
		// this pool owns none of River's indexes and has no MAINTAIN on its
		// tables, so REINDEX can never succeed. An empty list keeps the
		// reindexer off, where nil is River's default set.
		ReindexerIndexNames: []string{},
		Middleware:          middleware,
		Queues:              workerQueues(cfg),
		Workers:             workers,
	}
}

func workerQueues(cfg *config.Config) map[string]river.QueueConfig {
	return map[string]river.QueueConfig{
		jobs.QueueReview:   {MaxWorkers: cfg.ReviewWorkers},
		jobs.QueuePrepare:  {MaxWorkers: cfg.ReviewWorkers},
		jobs.QueuePublish:  {MaxWorkers: cfg.ReviewWorkers},
		jobs.QueueFollowUp: {MaxWorkers: cfg.ReviewWorkers},
		jobs.QueueThread:   {MaxWorkers: cfg.ThreadWorkers},
		jobs.QueueIndex:    {MaxWorkers: cfg.IndexWorkers},
	}
}

// workQueues works the job queues until ctx ends, then waits for River's
// soft stop to let running jobs finish or cut them. The queue's tables come
// from migrations, which the leader runs. On a fresh database, the leader
// can be this very process a moment from now, or another replica. So
// workQueues waits for the schema rather than racing it.
func workQueues(
	ctx context.Context, st *store.Store, queue *river.Client[pgx.Tx], cfg *config.Config, events *executor.Events, logger *slog.Logger,
) error {
	if err := st.WaitForSchema(ctx, 2*time.Second); err != nil {
		return nil // shutdown while waiting
	}
	if err := startQueue(ctx, queue, logger); err != nil {
		return err
	}
	logger.Info("working the queues", "review_workers", cfg.ReviewWorkers, "index_workers", cfg.IndexWorkers,
		"thread_workers", cfg.ThreadWorkers, "executor", cfg.Executor)
	<-ctx.Done()
	select {
	case <-queue.Stopped():
		// Every worker returned, so no run records an Event now. The Events of
		// the runs that the stop cut can still be on their way.
		wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), eventsDrain)
		defer cancel()
		events.Wait(wctx)
	case <-time.After(queueDrain + queueStopHeadroom):
		logger.Warn("the queues did not stop in time", "drain", queueDrain)
	}
	return nil
}

func startQueue(ctx context.Context, queue *river.Client[pgx.Tx], logger *slog.Logger) error {
	const retry = 5 * time.Second
	const attempts = 24
	var err error
	for i := 1; i <= attempts; i++ {
		if err = queue.Start(ctx); err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return nil
		}
		logger.Warn("queue start failed, retrying", "error", err, "attempt", i, "after", retry)
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(retry):
		}
	}
	return fmt.Errorf("river: start: %w", err)
}

// newExecutor builds the runner executor that the configuration selects.
// The templates of a Kubernetes executor decode first. Then the API makes
// sure that a runner Job built from them is valid. The Kubernetes types
// that read the templates are the executor's, not the configuration's. So
// this is the earliest that a bad value can be refused rather than failing
// every Job.
func newExecutor(ctx context.Context, cfg *config.Config, logger *slog.Logger) (executor.Executor, error) {
	if cfg.Executor == config.ExecutorLocal {
		runnerStore, err := store.Open(ctx, store.Options{AppURL: cfg.RunnerDatabaseURL, ExpectedRole: cfg.DatabaseRunnerRole, Logger: logger})
		if err != nil {
			return nil, err
		}
		return &executor.Local{Store: runnerStore}, nil
	}
	jobTemplate, err := executor.DecodeJobTemplate(cfg.RunnerJobTemplate)
	if err != nil {
		return nil, fmt.Errorf("runner.jobTemplate: KRITIKA_RUNNER_JOB_TEMPLATE: %w", err)
	}
	podTemplate, err := executor.DecodePodTemplate(cfg.RunnerPodTemplate)
	if err != nil {
		return nil, fmt.Errorf("runner.podTemplate: KRITIKA_RUNNER_POD_TEMPLATE: %w", err)
	}
	client, ns, err := executor.NewKubeInCluster("kritika/" + version)
	if err != nil {
		return nil, err
	}
	kube := &executor.Kube{
		Client: client, Namespace: ns, JobTemplate: jobTemplate, PodTemplate: podTemplate,
		DatabaseRole:   cfg.DatabaseRunnerRole,
		ServiceAccount: cfg.RunnerServiceAccount, DatabaseSecret: cfg.RunnerDatabaseSecret, DatabaseSecretKey: cfg.RunnerDatabaseSecretKey,
		GatewayURL: cfg.GatewayURL,
		Events:     executor.NewEvents(client, ns, cfg.PodName, cfg.PodUID, logger), Logger: logger,
	}
	if err := kube.CheckJob(ctx, cfg.RunnerDeadline()); err != nil {
		return nil, fmt.Errorf("runner.jobTemplate, runner.podTemplate: %w", err)
	}
	// The watch outlasts ctx, which a signal ends while runs still drain:
	// startWorker stops it once the queues stop.
	if err := kube.Start(ctx); err != nil {
		return nil, err
	}
	return kube, nil
}

// lead runs for as long as this replica holds the leader lock. It migrates,
// applies the configuration, and registers the repositories that its Apps
// reach. It then keeps the backstop poll, onboarding, and retention going.
// While the configuration has an embedder, lead also keeps the index schema
// to that embedder and enqueues an onboarding index job for every repository
// that has none. events, which is nil off Kubernetes, records on this
// replica's pod what became of the configuration.
func lead(
	ctx context.Context, st *store.Store, cfg *config.Config, current *configfile.Current, queue *river.Client[pgx.Tx],
	svc *ingest.Service, apps *worker.Forges, forges *worker.ForgeCache, kube *executor.Kube, events *executor.Events,
	m *metrics.Metrics, configErrors *server.ConfigErrorGauge, logger *slog.Logger,
) error {
	if err := st.Migrate(ctx, cfg.DatabaseAppRole, cfg.DatabaseRunnerRole); err != nil {
		return err
	}
	// Every leader duty ends before lead returns and the lock is released, so
	// the next leader never runs one alongside this replica's. The exception
	// is when the server dropped the lock, which goes unnoticed until the next
	// ping.
	pollCtx, stopPoll := context.WithCancel(ctx)
	var duties sync.WaitGroup
	defer duties.Wait()
	defer stopPoll()
	// The backstop poll is a leader duty: one lister per connection. It
	// starts once the configuration is applied, below: it polls at once,
	// and before then the store knows no account to poll.
	poll := &poller.Poller{
		Store: st, Current: current, Forges: forges, Reach: apps.Reach, Dispatcher: svc, Logger: logger, Metrics: m,
	}
	plans := &worker.ChatGPTRefresher{Store: st, Current: current, LeaderInterval: cfg.LeaderRetryInterval, Logger: logger}
	duties.Go(func() { plans.Run(pollCtx) })
	// So is feeding the index queue its onboarding jobs, a few at a time.
	onboarder := &worker.Onboarder{Store: st, Queue: queue, Current: current, Logger: logger}
	duties.Go(func() { onboarder.Run(pollCtx) })
	// And so is rescuing the jobs of a replica that died, and reaping the
	// runner Jobs they left, which only a Kubernetes executor has.
	rescuer := &worker.Rescuer{Store: st, Current: current, Forges: forges, Logger: logger, Metrics: m}
	if kube != nil {
		rescuer.Runs = kube
	}
	duties.Go(func() { rescuer.Run(pollCtx) })
	// And so is retention. It covers model-call transcripts past their
	// configured window and the indexes of repositories stopped past their
	// grace (owner pool, which bypasses row-level security). It also covers
	// expired dashboard sessions (app pool).
	retention := &worker.Retention{Store: st, Current: current, Logger: logger}
	duties.Go(func() { retention.Run(pollCtx) })
	if err := applyConfig(ctx, current.Get(), func(ctx context.Context, f *configfile.File) error {
		if err := st.ApplyConfig(ctx, f); err != nil {
			return err
		}
		if err := ensureIndexSchema(ctx, st, cfg.DatabaseAppRole, f.Embedding, logger); err != nil {
			return err
		}
		// Once the accounts exist: a fresh instance, or a new connection,
		// knows its repositories before any webhook names one.
		poll.SyncRepositories(ctx)
		return nil
	}, onboarder.Offer, configErrors, events, logger); err != nil {
		return err
	}
	duties.Go(func() { poll.Run(pollCtx) })
	<-ctx.Done()
	return nil
}

// ensureIndexSchema keeps the index table to the configuration's embedder.
// It rebuilds the table, and so every repository's index, when the model,
// dimension, or document prompt changed. The dashboard asked the admin to
// confirm that before saving it, and a configuration file edit makes the
// change without asking. Without an embedder, the table is left as it is.
func ensureIndexSchema(ctx context.Context, st *store.Store, appRole string, e *configfile.Embedding, logger *slog.Logger) error {
	if e == nil {
		return nil
	}
	rebuilt, err := st.EnsureIndexSchema(ctx, appRole, e.Model, e.Dims, e.DocumentPrompt)
	if rebuilt {
		logger.Warn("index rebuilt for a new embedder: every repository is indexed again",
			"model", e.Model, "dims", e.Dims, "document_prompt", e.DocumentPrompt)
	}
	return err
}

// applyConfig applies f to the store once, on election, and calls
// onApplied on success. A configuration that the store refuses for its
// content (store.IsConfigContentError) must not end leadership, or every
// replica will crash-loop on it in turn. Such a configuration is logged,
// raised on the gauge, and recorded as an Event on this replica's pod. The
// last applied state stays. The same content will be refused again, so it
// is not retried before a restart. Any other error is returned, which ends
// this tenure: the lock is released and competed for again.
func applyConfig(
	ctx context.Context, f *configfile.File, apply func(context.Context, *configfile.File) error,
	onApplied func(context.Context) error, gauge *server.ConfigErrorGauge, events *executor.Events, logger *slog.Logger,
) error {
	h := f.Hash()
	err := apply(ctx, f)
	switch {
	case err != nil && store.IsConfigContentError(err):
		gauge.Set(true)
		logger.Error("configuration refused by the store, keeping the last applied one", "hash", h[:12], "error", err)
		events.PodEvent(ctx, true, "ConfigRefused", "ApplyConfiguration",
			fmt.Sprintf("configuration %s refused by the store, keeping the last applied one: %v", h[:12], err))
		return nil
	case err != nil:
		return err
	}
	gauge.Set(false)
	logger.Info("configuration applied to the store", "hash", h[:12])
	events.PodEvent(ctx, false, "ConfigApplied", "ApplyConfiguration", "configuration "+h[:12]+" applied")
	return onApplied(ctx)
}

// driftInterval is how often a replica compares its configuration with
// the one the leader applied, which differ while a rollout is part done.
const driftInterval = 10 * time.Second

// reportDrift compares this replica's file with what the leader applied and
// exposes a mismatch as a gauge. It is deliberately not on /readyz: a stale
// ConfigMap on one node must not take an ingest replica out of the Service.
func reportDrift(
	ctx context.Context, st *store.Store, current *configfile.Current, gauge *server.ConfigDriftGauge, every time.Duration,
	logger *slog.Logger,
) error {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			applied, err := st.AppliedConfigHash(ctx)
			if err != nil {
				logger.Warn("could not read applied configuration hash", "error", err)
				continue
			}
			gauge.Set(applied != "" && applied != current.Get().Hash())
		}
	}
}

func logConfig(logger *slog.Logger, f *configfile.File, msg string) {
	logger.Info(msg, "providers", len(f.Providers), "accounts", len(f.Accounts), "connections", len(f.Connections))
}

func newLogger(cfg *config.Config) *slog.Logger {
	opts := &slog.HandlerOptions{Level: cfg.Level()}
	if strings.EqualFold(cfg.LogFormat, "text") {
		return slog.New(slog.NewTextHandler(os.Stdout, opts))
	}
	return slog.New(slog.NewJSONHandler(os.Stdout, opts))
}
