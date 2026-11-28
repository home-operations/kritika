package main

import (
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/home-operations/kritika/internal/config"
	"github.com/home-operations/kritika/internal/executor"
	"github.com/home-operations/kritika/internal/tuning"
)

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
	tuning.Set(cfg.Tuning)
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
