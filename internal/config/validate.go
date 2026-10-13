package config

import (
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/gookit/validate"

	"github.com/home-operations/kritika/internal/jobtimeout"
)

// minRetention keeps transcripts and diffs available for at least a day.
const minRetention = 24 * time.Hour

// Validate checks runtime bounds shared by the process and file loaders.
func (s Settings) Validate() error {
	v := validate.Struct(s)
	v.StopOnError = false
	v.SkipOnEmpty = false
	v.AddRule("GatewayTokenTTL,LeaderRetryInterval,ReviewWorkers,IndexWorkers,ThreadWorkers,OnboardWindow,RunnerTTL,RunnerDeadline", "gt", 0)
	v.AddRule("PollInterval,PollLookback,IndexGrace", "gte", 0)
	v.AddRule("TranscriptRetention,DiffRetention", "min", minRetention)
	v.AddRule("RunnerDeadline", "max", jobtimeout.MaxRunnerDeadline)
	v.AddRule("Executor", "enum", []string{string(ExecutorKubernetes), string(ExecutorLocal)})
	v.AddRule("DatabaseAppRole,DatabaseRunnerRole", "required")
	v.AddRule("DatabaseRunnerRole", "neField", "DatabaseAppRole")
	v.WithMessages(map[string]string{
		"gt":  "{field} must be positive",
		"gte": "{field} must not be negative",
		"min": fmt.Sprintf("{field} must be at least %s", minRetention),
		"max": fmt.Sprintf("{field} must not exceed %s, or River's %s job timeout cap would cut the runner off early",
			jobtimeout.MaxRunnerDeadline, jobtimeout.MaxJobTimeout),
		"enum":     "{field} must be kubernetes or local",
		"required": "{field} must be set",
		"neField":  "{field} must be distinct from databaseAppRole",
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
