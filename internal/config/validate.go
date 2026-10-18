package config

import (
	"errors"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/gookit/validate/v2"
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
	v.AddRule("GatewayTokenTTL,GatewayCacheMaxAge,LeaderRetryInterval,ReviewWorkers,IndexWorkers,ThreadWorkers,OnboardWindow", "gt", 0)
	// The numeric rules do not read a named integer type.
	v.AddValidator("size", func(b ByteSize) bool { return b > 0 })
	v.AddRule("GatewayCacheMaxSize", "size")
	v.AddRule("PollInterval,PollLookback,IndexGrace", "gte", 0)
	v.AddRule("TranscriptRetention,DiffRetention", "min", minRetention)
	v.AddRule("LogLevel", "enum", slices.Sorted(maps.Keys(logLevels)))
	v.AddRule("LogFormat", "enum", []string{"json", "text"})
	v.AddRule("Executor", "enum", []string{string(ExecutorKubernetes), string(ExecutorLocal)})
	v.AddRule("Addr,MetricsAddr,GatewayAddr,DatabaseAppRole,DatabaseRunnerRole", "required")
	v.AddRule("DatabaseRunnerRole", "neField", "DatabaseAppRole")
	v.WithMessages(map[string]string{
		"gt":             "{field} must be positive",
		"size":           "{field} must be positive",
		"gte":            "{field} must not be negative",
		"min":            fmt.Sprintf("{field} must be at least %s", minRetention),
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
	var extra []string
	if _, err := s.deadline(); err != nil {
		extra = append(extra, err.Error())
	}
	return validationError(v, extra...)
}

// ValidateServe checks what serve needs beyond the common set.
func (c *Config) ValidateServe() error {
	v := validate.Struct(c)
	v.StopOnError = false
	v.SkipOnEmpty = false
	v.AddRule("ConfigFile,WebURL,GatewayURL", "required")
	// Executor is a named string type; requiredIf compares it to a plain string.
	v.AddRule("RunnerDatabaseSecret,RunnerDatabaseSecretKey", "required").SetBeforeFunc(func(*validate.Validation) bool {
		return c.Executor == ExecutorKubernetes
	})
	v.AddRule("RunnerDatabaseURL", "required").SetBeforeFunc(func(*validate.Validation) bool {
		return c.Executor == ExecutorLocal
	})
	v.WithTranslates(map[string]string{
		"ConfigFile":              "KRITIKA_CONFIG_FILE",
		"WebURL":                  "webUrl: KRITIKA_WEB_URL",
		"GatewayURL":              "gatewayUrl: KRITIKA_GATEWAY_URL",
		"RunnerDatabaseSecret":    "runner.databaseSecret: KRITIKA_RUNNER_DATABASE_SECRET",
		"RunnerDatabaseSecretKey": "runner.databaseSecretKey: KRITIKA_RUNNER_DATABASE_SECRET_KEY",
		"RunnerDatabaseURL":       "KRITIKA_RUNNER_DATABASE_URL",
	})
	v.WithMessages(map[string]string{
		"required":                   "{field} is required to serve",
		"RunnerDatabaseURL.required": "{field} is required with the local executor",
	})
	return validationError(v)
}

func validationError(v *validate.Validation, extra ...string) error {
	if v.Validate() && len(extra) == 0 {
		return nil
	}
	messages := slices.Clone(extra)
	for _, rules := range v.Errors {
		for _, message := range rules {
			messages = append(messages, message)
		}
	}
	slices.Sort(messages)
	return fmt.Errorf("config: %w", errors.New(strings.Join(messages, "; ")))
}
