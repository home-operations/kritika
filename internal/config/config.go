// Package config loads process configuration from the configuration file and
// the environment.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"reflect"
	"slices"
	"strings"

	"github.com/caarlos0/env/v11"
)

// Command selects what a kritika process runs: the service, the
// connection of a provider to a ChatGPT plan, or a check of a
// configuration file. A runner Job runs cmd/kritika-runner instead.
type Command string

// Commands a kritika process can run.
const (
	// CommandServe is the service: webhooks, the dashboard, the job
	// queues, runner Jobs, the gateway and, on the leader, the leader
	// duties.
	CommandServe Command = "serve"
	// CommandChatGPTLogin connects a configured provider to a ChatGPT plan.
	CommandChatGPTLogin Command = "chatgpt login"
	// CommandConfigCheck loads a configuration file as serve does at
	// startup, without the database, and reports whether serve will
	// accept it.
	CommandConfigCheck Command = "config check"
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
	if (len(args) == 2 || len(args) == 3) && args[0] == "config" && args[1] == "check" {
		return CommandConfigCheck, nil
	}
	if len(args) == 1 && Command(args[0]) == CommandServe {
		return CommandServe, nil
	}
	return "", fmt.Errorf("config: unknown arguments %q: usage: "+
		"kritika [serve | chatgpt login [<forge/account>] <provider> | config check [<file>]]", args)
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

// Config combines file configuration with bootstrap credentials from the
// environment. Call Load to parse it and make sure that it is valid.
type Config struct {
	Settings

	// ConfigFile is the application configuration path, required by serve and chatgpt login.
	// Changes take effect after a restart.
	ConfigFile string `env:"KRITIKA_CONFIG_FILE"`

	// DatabaseURL is the DSN that every process connects with for request and
	// job work. It must be the application role: not a superuser, no BYPASSRLS,
	// owning nothing, so row-level security applies to it. Runner pods get
	// the runner role's DSN under the same variable name. The process makes
	// sure that this is true at startup. A DSN that bypasses row-level security
	// refuses to start.
	DatabaseURL string `env:"KRITIKA_DATABASE_URL,required,notEmpty,unset"`

	// DatabaseOwnerURL is the DSN of the role that owns the schema. It runs
	// migrations, the configuration loader and the chunks DDL, all of which
	// write across accounts. serve needs it to become leader. A runner
	// must not have it.
	DatabaseOwnerURL string `env:"KRITIKA_DATABASE_OWNER_URL,unset"`

	// RunnerDatabaseURL is the runner database role's DSN, needed only by
	// the local executor, which runs the runner inside the serve process.
	RunnerDatabaseURL string `env:"KRITIKA_RUNNER_DATABASE_URL,unset"`

	// GatewayCacheS3AccessKey and GatewayCacheS3SecretKey authenticate only
	// serve to the cache bucket. Empty selects the SDK's IAM credentials.
	GatewayCacheS3AccessKey string `env:"KRITIKA_GATEWAY_CACHE_S3_ACCESS_KEY,unset"`
	GatewayCacheS3SecretKey string `env:"KRITIKA_GATEWAY_CACHE_S3_SECRET_KEY,unset"`

	// PodName and PodUID are serve's own pod's, from the downward API: its
	// Kubernetes Events name the pod by them, and kubectl describe matches a
	// pod's Events by its UID. A pod's hostname is not always its name.
	PodName string `env:"KRITIKA_POD_NAME"`
	PodUID  string `env:"KRITIKA_POD_UID"`

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
	// DatabaseToken authenticates this run to its database rows. It has no
	// default.
	DatabaseToken string `env:"KRITIKA_DATABASE_TOKEN,unset"`
	webURL        *url.URL
	logLevel      slog.Level
	// document is the startup snapshot shared with the policy loader.
	document Document
}

// FileDocument returns the document read by Load. Treat the snapshot as read-only.
func (c *Config) FileDocument() Document { return c.document }

// ValidateRunner makes sure that a runner pod has what it needs. Package
// runspec decodes the job document itself and makes sure that it is valid.
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

// parseWebURL trims a trailing slash from WebURL. It rejects anything that
// is not an absolute http(s) URL with a host and no query or fragment. It
// caches the result for WebURLParsed. It is a no-op when WebURL is unset.
func (c *Config) parseWebURL() error {
	if c.WebURL == "" {
		return nil
	}
	trimmed := strings.TrimSuffix(c.WebURL, "/")
	u, err := url.Parse(trimmed)
	if err != nil {
		return fmt.Errorf("config: webUrl: KRITIKA_WEB_URL: %w", err)
	}
	if (u.Scheme != schemeHTTP && u.Scheme != schemeHTTPS) || u.Hostname() == "" {
		return fmt.Errorf("config: webUrl: KRITIKA_WEB_URL must be an absolute http(s) URL, got %q", c.WebURL)
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("config: webUrl: KRITIKA_WEB_URL must not have a query or fragment, got %q", c.WebURL)
	}
	c.WebURL = trimmed
	c.webURL = u
	return nil
}

// Load reads the environment and configuration file, then makes sure that
// the values are valid. It fails fast on an invalid value so a misconfigured
// process never starts serving.
func Load() (*Config, error) {
	cfg, err := env.ParseAs[Config]()
	if err != nil {
		return nil, envError[Config](err)
	}
	if err := cfg.load(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// LoadFile reads the Settings and the configuration file at path as Load
// does, for kritika config check. The variables that only a running process
// reads are neither read nor required. The database DSNs are among them.
func LoadFile(path string) (*Config, error) {
	s, err := env.ParseAs[Settings]()
	if err != nil {
		return nil, envError[Settings](err)
	}
	// Load's parse unsets these before the file's secret references
	// resolve, so a reference to one fails there and must fail here too.
	params, err := env.GetFieldParams(&Config{})
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	for _, p := range params {
		if p.Unset {
			if err := os.Unsetenv(p.Key); err != nil {
				return nil, fmt.Errorf("config: %w", err)
			}
		}
	}
	cfg := Config{Settings: s, ConfigFile: path}
	if err := cfg.load(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// load reads the configuration file, when there is one, and makes sure that
// the configuration is valid.
func (c *Config) load() error {
	if c.ConfigFile != "" {
		raw, err := os.ReadFile(c.ConfigFile)
		if err != nil {
			return fmt.Errorf("config: %w", err)
		}
		if c.document, err = c.readFile(raw); err != nil {
			return err
		}
	}
	return c.validate()
}

func (c *Config) validate() error {
	if err := c.Validate(); err != nil {
		return err
	}
	if c.GatewayURL != "" {
		if err := checkGatewayURL(c.GatewayURL); err != nil {
			return fmt.Errorf("config: gatewayUrl: KRITIKA_GATEWAY_URL: %w", err)
		}
	}
	if err := c.parseWebURL(); err != nil {
		return err
	}
	c.logLevel = logLevels[strings.ToLower(c.LogLevel)]
	return nil
}

// The schemes of the URLs that the configuration names.
const (
	schemeHTTP  = "http"
	schemeHTTPS = "https"
)

// checkGatewayURL makes sure that a gateway URL that a runner will be handed
// is http, with a host, no path, and no credentials. It has no credentials
// because the run token becomes its password.
func checkGatewayURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return err
	}
	if u.Scheme != schemeHTTP || u.Host == "" || u.User != nil || (u.Path != "" && u.Path != "/") {
		return errors.New("must be http://host:port")
	}
	return nil
}

// logLevels are the slog levels LogLevel can name.
var logLevels = map[string]slog.Level{
	"debug": slog.LevelDebug, "info": slog.LevelInfo, "warn": slog.LevelWarn, "warning": slog.LevelWarn, "error": slog.LevelError,
}

// Level is the slog level LogLevel names.
func (c *Config) Level() slog.Level { return c.logLevel }

// EnvVar is one environment variable that Config reads, as this process has
// it. A secret, which the process unsets once read, shows only whether it is
// set. Set identifies environment values. File identifies file values, and
// Path names their YAML key.
type EnvVar struct {
	Name   string
	Path   string
	Value  string
	Secret bool
	Set    bool
	File   bool
}

// Env lists the variables Config reads, in the order it declares them. When
// Load returns, the secrets are gone from the environment, so a secret is
// set when its field holds a value.
func (c *Config) Env() []EnvVar {
	var out []EnvVar
	var visit func(reflect.Value, string)
	visit = func(v reflect.Value, prefix string) {
		for i := range v.NumField() {
			key, _, _ := strings.Cut(v.Type().Field(i).Tag.Get("yaml"), ",")
			if v.Type().Field(i).Anonymous {
				nested := prefix
				if key != "" {
					nested += key + "."
				}
				visit(v.Field(i), nested)
				continue
			}
			name, opts, _ := strings.Cut(v.Type().Field(i).Tag.Get("env"), ",")
			if name == "" {
				continue
			}
			e := EnvVar{Name: name, Secret: slices.Contains(strings.Split(opts, ","), "unset")}
			e.File = c.fileEnv[name]
			if e.File {
				e.Path = prefix + key
			}
			field := v.Field(i)
			if e.Secret {
				e.Set, e.Value = !field.IsZero(), "not set"
				if e.Set {
					e.Value = "set"
				}
			} else {
				_, e.Set = os.LookupEnv(name)
				switch value := field.Interface().(type) {
				case jsonObject:
					raw, err := json.Marshal(value)
					if err != nil {
						e.Value = "invalid JSON: " + err.Error()
					} else {
						e.Value = string(raw)
					}
				default:
					e.Value = fmt.Sprint(value)
				}
			}
			out = append(out, e)
		}
	}
	visit(reflect.ValueOf(c).Elem(), "")
	return out
}
