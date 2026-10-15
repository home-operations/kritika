package config

import (
	"errors"
	"fmt"
	"os"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/gookit/validate/v2"
	"github.com/pelletier/go-toml/v2"

	"github.com/home-operations/kritika/internal/jobtimeout"
)

// minRetention keeps transcripts and diffs available for at least a day.
const minRetention = 24 * time.Hour

// Validate checks runtime bounds shared by the process and file loaders.
func (s Settings) Validate() error {
	if _, set := os.LookupEnv("KRITIKA_RUNNER_TOOLS"); set {
		return errors.New("config: KRITIKA_RUNNER_TOOLS has been removed; declare tools in runner.miseConfig (Helm: runner.mise.config) instead")
	}
	s.LogLevel = strings.ToLower(s.LogLevel)
	s.LogFormat = strings.ToLower(s.LogFormat)
	v := validate.Struct(s)
	v.StopOnError = false
	v.SkipOnEmpty = false
	v.AddRule("GatewayTokenTTL,LeaderRetryInterval,ReviewWorkers,IndexWorkers,ThreadWorkers,OnboardWindow,RunnerTTL,RunnerDeadline", "gt", 0)
	v.AddRule("PollInterval,PollLookback,IndexGrace", "gte", 0)
	v.AddRule("TranscriptRetention,DiffRetention", "min", minRetention)
	v.AddRule("RunnerDeadline", "max", jobtimeout.MaxRunnerDeadline)
	v.AddRule("LogLevel", "enum", []string{"debug", "info", "warn", "warning", "error"})
	v.AddRule("LogFormat", "enum", []string{"json", "text"})
	v.AddRule("Executor", "enum", []string{string(ExecutorKubernetes), string(ExecutorLocal)})
	v.AddRule("Addr,MetricsAddr,GatewayAddr,DatabaseAppRole,DatabaseRunnerRole", "required")
	v.AddRule("DatabaseRunnerRole", "neField", "DatabaseAppRole")
	// The runner hands the mise config to mise as is; a document that does
	// not parse is refused here, where the message names the setting,
	// rather than in every run.
	var tomlErr error
	if s.RunnerMiseConfig != "" {
		tomlErr = toml.Unmarshal([]byte(s.RunnerMiseConfig), &map[string]any{})
	}
	v.AddValidator("toml", func(string) bool { return tomlErr == nil })
	v.AddRule("RunnerMiseConfig", "toml")
	tomlMessage := "{field} must be a TOML document"
	if tomlErr != nil {
		tomlMessage += ": " + tomlErr.Error()
	}
	v.WithMessages(map[string]string{
		"toml": tomlMessage,
		"gt":   "{field} must be positive",
		"gte":  "{field} must not be negative",
		"min":  fmt.Sprintf("{field} must be at least %s", minRetention),
		"max": fmt.Sprintf("{field} must not exceed %s, or River's %s job timeout cap would cut the runner off early",
			jobtimeout.MaxRunnerDeadline, jobtimeout.MaxJobTimeout),
		"LogLevel.enum":  "{field} must be debug, info, warn or error",
		"LogFormat.enum": "{field} must be json or text",
		"enum":           "{field} must be kubernetes or local",
		"required":       "{field} must be set",
		"neField":        "{field} must be distinct from databaseAppRole",
	})
	labels := map[string]string{}
	for _, f := range reflect.VisibleFields(reflect.TypeFor[Settings]()) {
		name := f.Tag.Get("env")
		if name == "" {
			continue
		}
		path, _, _ := strings.Cut(f.Tag.Get("yaml"), ",")
		if len(f.Index) > 1 {
			path = "runner." + path
		}
		labels[f.Name] = path + ": " + name
	}
	v.WithTranslates(labels)
	return validationError(v)
}

// ValidateServe checks what serve needs beyond the common set.
func (c *Config) ValidateServe() error {
	v := validate.Struct(c)
	v.StopOnError = false
	v.SkipOnEmpty = false
	v.AddRule("ConfigFile,WebURL,GatewayURL", "required")
	// Executor is a named string type; requiredIf compares it to a plain string.
	v.AddRule("RunnerImage,RunnerDatabaseSecret,RunnerDatabaseSecretKey", "required").SetBeforeFunc(func(*validate.Validation) bool {
		return c.Executor == ExecutorKubernetes
	})
	v.AddRule("RunnerDatabaseURL", "required").SetBeforeFunc(func(*validate.Validation) bool {
		return c.Executor == ExecutorLocal
	})
	v.AddRule("RunnerImagePullPolicy", "enum", []string{"", "Always", "IfNotPresent", "Never"})
	v.WithTranslates(map[string]string{
		"ConfigFile":              "KRITIKA_CONFIG_FILE",
		"WebURL":                  "webUrl: KRITIKA_WEB_URL",
		"GatewayURL":              "gatewayUrl: KRITIKA_GATEWAY_URL",
		"RunnerImage":             "runner.image: KRITIKA_RUNNER_IMAGE",
		"RunnerDatabaseSecret":    "runner.databaseSecret: KRITIKA_RUNNER_DATABASE_SECRET",
		"RunnerDatabaseSecretKey": "runner.databaseSecretKey: KRITIKA_RUNNER_DATABASE_SECRET_KEY",
		"RunnerDatabaseURL":       "KRITIKA_RUNNER_DATABASE_URL",
		"RunnerImagePullPolicy":   "runner.imagePullPolicy: KRITIKA_RUNNER_IMAGE_PULL_POLICY",
	})
	v.WithMessages(map[string]string{
		"required":                   "{field} is required to serve",
		"RunnerImage.required":       "{field} is required with the kubernetes executor",
		"RunnerDatabaseURL.required": "{field} is required with the local executor",
		"enum":                       "{field} must be Always, IfNotPresent or Never when set",
	})
	return validationError(v)
}

func validationError(v *validate.Validation) error {
	if v.Validate() {
		return nil
	}
	var messages []string
	for _, rules := range v.Errors {
		for _, message := range rules {
			messages = append(messages, message)
		}
	}
	slices.Sort(messages)
	return fmt.Errorf("config: %w", errors.New(strings.Join(messages, "; ")))
}
