package configfile

import (
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"go.yaml.in/yaml/v4"

	"github.com/home-operations/kritika/internal/config"
)

func TestHelmConfiguration(t *testing.T) {
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("helm is required to validate the chart's application configuration")
	}
	cmd := exec.CommandContext(t.Context(), "helm", "template", "kritika", "../../charts/kritika",
		"--values", "../../charts/kritika/ci/lint-values.yaml", "--kube-version", "1.33.0",
		"--set", "runner.runtimeClass=gvisor", "--set", "runner.deadline=20m",
		"--set", "runner.tools[0].name=helm", "--set", "runner.tools[0].image=example.com/helm:test",
		"--set", "config.pollInterval=0s")
	rendered, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("helm template: %v\n%s", err, rendered)
	}
	dec := yaml.NewDecoder(bytes.NewReader(rendered))
	var raw string
	for {
		var doc struct {
			Kind string            `yaml:"kind"`
			Data map[string]string `yaml:"data"`
		}
		if err := dec.Decode(&doc); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		if doc.Kind == "ConfigMap" && doc.Data["config.yaml"] != "" {
			raw = doc.Data["config.yaml"]
		}
	}
	if raw == "" {
		t.Fatal("chart produced no application configuration")
	}
	t.Setenv("ADMIN_PASSWORD", "test-password")
	t.Setenv("GITHUB_APP_PRIVATE_KEY", "test-key")
	t.Setenv("GITHUB_APP_WEBHOOK_SECRET", "test-webhook")
	f, err := Parse([]byte(raw))
	if err != nil {
		t.Fatalf("rendered configuration: %v", err)
	}
	if f.PollInterval() != 0 || f.Run.RunnerDeadline != 20*time.Minute || len(f.ToolsFor([]string{"helm"})) != 1 {
		t.Fatalf("rendered runtime settings = %+v", f.Run)
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KRITIKA_CONFIG_FILE", path)
	t.Setenv("KRITIKA_DATABASE_URL", "postgres://app@db/kritika")
	c, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if err := c.ValidateServe(); err != nil {
		t.Fatal(err)
	}
	if c.RunnerRuntimeClass != "gvisor" || c.RunnerServiceAccount != "kritika-runner" || c.WebURL != "https://kritika.example.com" {
		t.Fatalf("rendered process settings = %+v", c.Settings)
	}
}
