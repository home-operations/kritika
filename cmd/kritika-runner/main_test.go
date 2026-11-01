package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/home-operations/kritika/internal/runspec"
)

// serveOnly are the packages of kritika's that only serve links. One in
// the runner's binary will ship in every runner pod. A package that serve
// alone links belongs here.
var serveOnly = []string{
	"adapter", "auth", "egress", "executor", "forge", "gateway", "ingest", "metrics", "poller", "store", "web", "webapi", "webhook", "worker",
}

func TestRunnerLinksNoServePackage(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", "-f", "{{.ImportPath}}", ".").Output()
	if ee, ok := errors.AsType[*exec.ExitError](err); ok {
		t.Fatalf("go list: %v: %s", err, ee.Stderr)
	} else if err != nil {
		t.Fatalf("go list: %v", err)
	}
	for pkg := range strings.FieldsSeq(string(out)) {
		// klog comes with the certificate transparency checks of Sigstore's
		// verifier, which logs through it. client-go is what stays out.
		if strings.HasPrefix(pkg, "k8s.io/") && !strings.HasPrefix(pkg, "k8s.io/klog/") {
			t.Errorf("the runner links %s", pkg)
		}
		// A runner reports to the gateway. It holds no way to the database.
		if strings.HasPrefix(pkg, "github.com/jackc/") {
			t.Errorf("the runner links %s", pkg)
		}
		name, ok := strings.CutPrefix(pkg, "github.com/home-operations/kritika/internal/")
		if top, _, _ := strings.Cut(name, "/"); ok && slices.Contains(serveOnly, top) {
			t.Errorf("the runner links serve's %s", pkg)
		}
	}
}

// TestRunStartsFromAJobsEnvironment runs what a runner Job runs, with only what
// the executor gives its container: no database URL. The fetch fails, since
// nothing serves the repository, and the run reports that to the gateway.
func TestRunStartsFromAJobsEnvironment(t *testing.T) {
	var mu sync.Mutex
	var reports []string
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, runspec.ReportPrefix) {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer krr_report_token" {
			t.Errorf("%s has authorization %q", r.URL.Path, r.Header.Get("Authorization"))
		}
		if r.URL.Path != runspec.ReportHeartbeatPath {
			mu.Lock()
			reports = append(reports, r.URL.Path)
			mu.Unlock()
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer gateway.Close()
	spec, err := runspec.Encode(runspec.Spec{
		Version: runspec.Version, Kind: runspec.KindIndex, RunID: "run", CloneURL: gateway.URL + "/acme/widgets.git", GatewayURL: gateway.URL,
		Head: "0123456789abcdef0123456789abcdef01234567",
	})
	if err != nil {
		t.Fatal(err)
	}
	specFile := filepath.Join(t.TempDir(), "spec.json")
	if err := os.WriteFile(specFile, spec, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KRITIKA_DATABASE_URL", "")
	if err := os.Unsetenv("KRITIKA_DATABASE_URL"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KRITIKA_RUN_SPEC_FILE", specFile)
	t.Setenv("KRITIKA_REPORT_TOKEN", "krr_report_token")
	t.Setenv("KRITIKA_GIT_TOKEN", "git_token")
	t.Setenv("KRITIKA_GATEWAY_TOKEN", "krk_gateway_token")
	t.Setenv("KRITIKA_METRICS_ADDR", "127.0.0.1:0")
	args := os.Args
	t.Cleanup(func() { os.Args = args })
	os.Args = []string{"kritika-runner", "run"}

	err = run()
	if err == nil || strings.Contains(err.Error(), "KRITIKA_DATABASE_URL") {
		t.Fatalf("run = %v, want the fetch to fail", err)
	}
	mu.Lock()
	defer mu.Unlock()
	want := []string{runspec.ReportAuthorizePath, runspec.ReportPhasePath, runspec.ReportFailurePath}
	if !slices.Equal(reports, want) {
		t.Fatalf("reports = %q, want %q", reports, want)
	}
}
