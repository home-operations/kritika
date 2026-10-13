// Package config loads process settings from the configuration file and environment.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"reflect"
	"slices"
	"strings"

	"github.com/caarlos0/env/v11"

	"github.com/home-operations/kritika/internal/egress"
)

// Command selects what a kritika process runs: the service, or one review or
// index run in a runner Job.
type Command string

// Commands a kritika process can run.
const (
	// CommandServe is the service: webhooks, the dashboard, the job
	// queues, runner Jobs, the gateway and, on the leader, the leader
	// duties.
	CommandServe Command = "serve"
	// CommandRun is one run in a runner Job, which the service creates.
	CommandRun Command = "run"
	// CommandChatGPTLogin connects a configured provider to a ChatGPT plan.
	CommandChatGPTLogin Command = "chatgpt login"
)

// ParseCommand reads the command from a process's arguments, without the
// program name: serve when there are none.
func ParseCommand(args []string) (Command, error) {
	if len(args) == 0 {
		return CommandServe, nil
	}
	if (len(args) == 3 || len(args) == 4) && args[0] == "chatgpt" && args[1] == "login" {
		return CommandChatGPTLogin, nil
	}
	if len(args) == 1 {
		switch c := Command(args[0]); c {
		case CommandServe, CommandRun:
			return c, nil
		}
	}
	return "", fmt.Errorf("config: unknown arguments %q: usage: kritika [serve | run | chatgpt login [<forge/account>] <provider>]", args)
}

// Executor selects how runners run.
type Executor string

// Executors a kritika process can run runners with.
const (
	// ExecutorKubernetes creates a Job per run in the pod's own namespace.
	ExecutorKubernetes Executor = "kubernetes"
	// ExecutorLocal runs the runner in-process and is for development and
	// tests only, because it gives the checkout the worker's credentials.
	ExecutorLocal Executor = "local"
)

// Config combines file settings with bootstrap credentials from the environment.
// Call Load to parse and validate it.
type Config struct {
	Settings

	// ConfigFile is the path of the optional application configuration file.
	// serve loads it at startup,
	// and a change takes a restart. Empty means no file, and the
	// environment alone declares them.
	ConfigFile string `env:"KRITIKA_CONFIG_FILE"`

	// DatabaseURL is the DSN every process connects with for request and
	// job work. It must be the application role: not a superuser, no BYPASSRLS,
	// owning nothing, so row-level security applies to it. Runner pods get
	// the runner role's DSN under the same variable name. Checked at
	// startup; a DSN that bypasses row-level security refuses to start.
	DatabaseURL string `env:"KRITIKA_DATABASE_URL,required,notEmpty,unset"`

	// DatabaseOwnerURL is the DSN of the role that owns the schema. It runs
	// migrations, the configuration loader and the chunks DDL, all of which
	// write across accounts. serve needs it to become leader; a runner
	// must not have it.
	DatabaseOwnerURL string `env:"KRITIKA_DATABASE_OWNER_URL,unset"`

	// RunnerDatabaseURL is the runner database role's DSN, needed only by
	// the local executor, which runs the runner inside the serve process.
	RunnerDatabaseURL string `env:"KRITIKA_RUNNER_DATABASE_URL,unset"`

	// RunSpecFile is the path of a runner's job document, a
	// versioned JSON runner spec the worker mounts into the Job from the
	// run's Secret. A file rather than a variable: a spec can outgrow the
	// kernel's 128 KiB limit on one environment string.
	RunSpecFile string `env:"KRITIKA_RUN_SPEC_FILE"`
	// GitToken and GatewayToken are the runner's credentials, read from
	// the run's own Secret. The gateway token is set only for an agentic
	// review.
	GitToken     string `env:"KRITIKA_GIT_TOKEN,unset"`
	GatewayToken string `env:"KRITIKA_GATEWAY_TOKEN,unset"`
	// DatabaseToken authenticates this run to its database rows; it has no default.
	DatabaseToken string `env:"KRITIKA_DATABASE_TOKEN,unset"`
	webURL        *url.URL
}

// ValidateServe checks what serve needs beyond the common set.
func (c *Config) ValidateServe() error {
	var errs []error
	if c.Executor == ExecutorKubernetes && c.RunnerImage == "" {
		errs = append(errs, errors.New("config: KRITIKA_RUNNER_IMAGE is required with the kubernetes executor"))
	}
	switch c.RunnerImagePullPolicy {
	case "", "Always", "IfNotPresent", "Never":
	default:
		errs = append(errs, fmt.Errorf("config: KRITIKA_RUNNER_IMAGE_PULL_POLICY %q is not Always, IfNotPresent or Never",
			c.RunnerImagePullPolicy))
	}
	if c.Executor == ExecutorLocal && c.RunnerDatabaseURL == "" {
		errs = append(errs, errors.New("config: KRITIKA_RUNNER_DATABASE_URL is required with the local executor"))
	}
	if c.WebURL == "" {
		errs = append(errs, errors.New("config: KRITIKA_WEB_URL is required to serve"))
	}
	if c.GatewayURL == "" {
		errs = append(errs, errors.New("config: KRITIKA_GATEWAY_URL is required to serve: every review reaches its model through the gateway"))
	}
	return errors.Join(errs...)
}

// ValidateRunner checks what a runner pod needs. The job document itself is
// decoded and validated by the runner package.
func (c *Config) ValidateRunner() error {
	if c.RunSpecFile == "" {
		return errors.New("config: KRITIKA_RUN_SPEC_FILE is required to run")
	}
	return nil
}

// WebURLParsed returns WebURL parsed into a *url.URL, or nil when WebURL is
// unset.
func (c *Config) WebURLParsed() *url.URL { return c.webURL }

// WebBasePath is WebURL's path, "" when it has none or is unset: the
// dashboard is served under it, and the webhook listener shares it.
func (c *Config) WebBasePath() string {
	if c.webURL == nil {
		return ""
	}
	return c.webURL.Path
}

// parseWebURL trims a trailing slash from WebURL, rejects anything that
// isn't an absolute http(s) URL with a host and no query or fragment, and
// caches the result for WebURLParsed. A no-op when WebURL is unset.
func (c *Config) parseWebURL() error {
	if c.WebURL == "" {
		return nil
	}
	trimmed := strings.TrimSuffix(c.WebURL, "/")
	u, err := url.Parse(trimmed)
	if err != nil {
		return fmt.Errorf("config: KRITIKA_WEB_URL: %w", err)
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("config: KRITIKA_WEB_URL must be an absolute http(s) URL, got %q", c.WebURL)
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("config: KRITIKA_WEB_URL must not have a query or fragment, got %q", c.WebURL)
	}
	c.WebURL = trimmed
	c.webURL = u
	return nil
}

// Load reads the environment and configuration file, then validates. It fails fast
// on an invalid value so a misconfigured process never starts serving.
func Load() (*Config, error) {
	cfg, err := env.ParseAs[Config]()
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	if cfg.ConfigFile != "" {
		raw, err := os.ReadFile(cfg.ConfigFile)
		if err != nil {
			return nil, fmt.Errorf("config: %w", err)
		}
		if err := cfg.ReadFile(raw); err != nil {
			return nil, err
		}
	}
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func (c *Config) validate() error {
	if _, err := c.Level(); err != nil {
		return err
	}
	switch strings.ToLower(c.LogFormat) {
	case "json", "text":
	default:
		return fmt.Errorf("config: KRITIKA_LOG_FORMAT must be json or text, got %q", c.LogFormat)
	}
	if c.GatewayURL != "" {
		if _, err := egress.ProxyURL(c.GatewayURL); err != nil {
			return fmt.Errorf("config: KRITIKA_GATEWAY_URL: %w", err)
		}
	}
	if err := c.parseWebURL(); err != nil {
		return err
	}
	return c.Validate()
}

// Level returns the slog level named by LogLevel.
func (c *Config) Level() (slog.Level, error) {
	switch strings.ToLower(c.LogLevel) {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("config: KRITIKA_LOG_LEVEL must be debug, info, warn or error, got %q", c.LogLevel)
	}
}

// EnvVar is one environment variable Config reads, as this process has it.
// A secret, one the process unsets once read, shows only whether it is
// set. Set identifies environment values; File identifies file values.
type EnvVar struct {
	Name   string
	Value  string
	Secret bool
	Set    bool
	File   bool
}

// Env lists the variables Config reads, in the order it declares them. The
// environment has lost its secrets by the time Load returns, so a secret
// is set when its field holds a value.
func (c *Config) Env() []EnvVar {
	var out []EnvVar
	var visit func(reflect.Value)
	visit = func(v reflect.Value) {
		for i := range v.NumField() {
			if v.Type().Field(i).Anonymous {
				visit(v.Field(i))
				continue
			}
			name, opts, _ := strings.Cut(v.Type().Field(i).Tag.Get("env"), ",")
			if name == "" {
				continue
			}
			e := EnvVar{Name: name, Secret: slices.Contains(strings.Split(opts, ","), "unset")}
			e.File = c.fileEnv[name]
			field := v.Field(i)
			if e.Secret {
				e.Set, e.Value = !field.IsZero(), "not set"
				if e.Set {
					e.Value = "set"
				}
			} else {
				_, e.Set = os.LookupEnv(name)
				e.Value = fmt.Sprint(field.Interface())
			}
			out = append(out, e)
		}
	}
	visit(reflect.ValueOf(c).Elem())
	return out
}
