package config

import (
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/gookit/validate/v2"

	"github.com/home-operations/kritika/internal/jobtimeout"
)

// minRetention keeps transcripts and diffs available for at least a day.
const minRetention = 24 * time.Hour

// Validate checks runtime bounds shared by the process and file loaders.
func (s Settings) Validate() error {
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
	v.AddRule("DatabaseAppRole,DatabaseRunnerRole", "required")
	v.AddRule("DatabaseRunnerRole", "neField", "DatabaseAppRole")
	v.WithMessages(map[string]string{
		"gt":  "{field} must be positive",
		"gte": "{field} must not be negative",
		"min": fmt.Sprintf("{field} must be at least %s", minRetention),
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
	if err := v.ValidateErr(); err != nil {
		return fmt.Errorf("config: %w", err)
	}
	return nil
}

// ValidateServe checks what serve needs beyond the common set.
func (c *Config) ValidateServe() error {
	v := validate.Struct(c)
	v.StopOnError = false
	v.SkipOnEmpty = false
	v.AddRule("ConfigFile,WebURL,GatewayURL", "required")
	// Executor is a named string type; requiredIf compares it to a plain string.
	v.AddRule("RunnerImage", "required").SetBeforeFunc(func(*validate.Validation) bool {
		return c.Executor == ExecutorKubernetes
	})
	v.AddRule("RunnerDatabaseURL", "required").SetBeforeFunc(func(*validate.Validation) bool {
		return c.Executor == ExecutorLocal
	})
	v.AddRule("RunnerImagePullPolicy", "enum", []string{"", "Always", "IfNotPresent", "Never"})
	v.WithTranslates(map[string]string{
		"ConfigFile":            "KRITIKA_CONFIG_FILE",
		"WebURL":                "webUrl: KRITIKA_WEB_URL",
		"GatewayURL":            "gatewayUrl: KRITIKA_GATEWAY_URL",
		"RunnerImage":           "runner.image: KRITIKA_RUNNER_IMAGE",
		"RunnerDatabaseURL":     "KRITIKA_RUNNER_DATABASE_URL",
		"RunnerImagePullPolicy": "runner.imagePullPolicy: KRITIKA_RUNNER_IMAGE_PULL_POLICY",
	})
	v.WithMessages(map[string]string{
		"required":                   "{field} is required to serve",
		"RunnerImage.required":       "{field} is required with the kubernetes executor",
		"RunnerDatabaseURL.required": "{field} is required with the local executor",
		"enum":                       "{field} must be Always, IfNotPresent or Never when set",
	})
	if err := v.ValidateErr(); err != nil {
		return fmt.Errorf("config: %w", err)
	}
	return nil
}
