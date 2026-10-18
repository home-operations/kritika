package configfile

import (
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
		"--values", "../../charts/kritika/ci/lint-values.yaml", "--kube-version", "1.34.0",
		"--set", "runner.podTemplate.spec.runtimeClassName=gvisor", "--set", "runner.jobTemplate.spec.activeDeadlineSeconds=1200",
		"--set", "config.pollInterval=0s", "--set", "gatewayCache.enabled=true", "--set", "gatewayCache.storage.maxSize=512Mi",
		"--set-string", "config.webUrl=https://{{ .Release.Name }}.example.com")
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
	if f.PollInterval() != 0 || f.RunnerDeadline() != 20*time.Minute {
		t.Fatalf("rendered runtime settings = %+v", f.Run)
	}
	// The chart fills the runner container's image, which values leave empty.
	spec, _ := f.Run.RunnerPodTemplate["spec"].(map[string]any)
	containers, _ := spec["containers"].([]any)
	if len(containers) != 1 || spec["runtimeClassName"] != "gvisor" {
		t.Fatalf("rendered podTemplate = %+v", f.Run.RunnerPodTemplate)
	}
	if runner, _ := containers[0].(map[string]any); runner["name"] != "runner" || runner["image"] == "" || runner["image"] == nil {
		t.Fatalf("rendered runner container = %+v", containers[0])
	}
	if f.Run.GatewayCacheDir != "/var/cache/kritika" || f.Run.GatewayCacheMaxSize != 512<<20 {
		t.Fatalf("rendered gateway cache = %q, %d", f.Run.GatewayCacheDir, f.Run.GatewayCacheMaxSize)
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
	if c.RunnerDeadline() != 20*time.Minute || c.RunnerServiceAccount != "kritika-runner" || c.WebURL != "https://kritika.example.com" {
		t.Fatalf("rendered process settings = %+v", c.Settings)
	}
}

func TestHelmRejectsEmptyDurations(t *testing.T) {
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("helm is required to validate chart values")
	}
	for _, key := range []string{"pollInterval", "pollLookback", "leaderRetryInterval", "gatewayTokenTtl", "indexGrace", "transcriptRetention", "diffRetention"} {
		t.Run(key, func(t *testing.T) {
			cmd := exec.CommandContext(t.Context(), "helm", "template", "kritika", "../../charts/kritika",
				"--values", "../../charts/kritika/ci/lint-values.yaml", "--set-string", "config."+key+"=")
			output, err := cmd.CombinedOutput()
			if err == nil || !strings.Contains(string(output), "/config/"+key) || !strings.Contains(string(output), "minLength") {
				t.Fatalf("helm template = %v\n%s", err, output)
			}
		})
	}
}

func TestHelmS3Configuration(t *testing.T) {
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("helm is required to validate chart values")
	}
	cmd := exec.CommandContext(t.Context(), "helm", "template", "kritika", "../../charts/kritika",
		"--values", "../../charts/kritika/ci/lint-values.yaml",
		"--set", "gatewayCache.enabled=true,gatewayCache.storage.type=s3,gatewayCache.storage.bucket=cache,gatewayCache.storage.region=garage",
		"--set", "gatewayCache.storage.endpoint=https://s3.example.com,gatewayCache.storage.forcePathStyle=false")
	rendered, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("helm template: %v\n%s", err, rendered)
	}
	dec := yaml.NewDecoder(bytes.NewReader(rendered))
	for {
		var doc struct {
			Kind string            `yaml:"kind"`
			Data map[string]string `yaml:"data"`
		}
		if err := dec.Decode(&doc); err != nil {
			t.Fatalf("no valid ConfigMap found: %v", err)
		}
		if doc.Kind != "ConfigMap" || doc.Data["config.yaml"] == "" {
			continue
		}
		t.Setenv("ADMIN_PASSWORD", "test-password")
		t.Setenv("GITHUB_APP_PRIVATE_KEY", "test-key")
		t.Setenv("GITHUB_APP_WEBHOOK_SECRET", "test-webhook")
		f, err := Parse([]byte(doc.Data["config.yaml"]))
		if err != nil {
			t.Fatal(err)
		}
		s := f.Run
		if s.GatewayCacheStorage != "s3" || s.GatewayCacheS3Bucket != "cache" || s.GatewayCacheS3Region != "garage" ||
			s.GatewayCacheS3Endpoint != "https://s3.example.com" || s.GatewayCacheS3ForcePathStyle || s.GatewayCacheMaxEntrySize != 256<<20 {
			t.Fatalf("S3 configuration did not round trip: %+v", s)
		}
		break
	}
}

func TestHelmRejectsInvalidCacheStorage(t *testing.T) {
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("helm is required to validate chart values")
	}
	for _, values := range []string{
		"gatewayCache.maxSize=1Gi",
		"gatewayCache.storage.type=unknown",
		"gatewayCache.storage.type=s3",
	} {
		t.Run(values, func(t *testing.T) {
			cmd := exec.CommandContext(t.Context(), "helm", "template", "kritika", "../../charts/kritika",
				"--values", "../../charts/kritika/ci/lint-values.yaml", "--set", values)
			if output, err := cmd.CombinedOutput(); err == nil || !strings.Contains(string(output), "/gatewayCache") {
				t.Fatalf("invalid cache values accepted: %v\n%s", err, output)
			}
		})
	}
}
