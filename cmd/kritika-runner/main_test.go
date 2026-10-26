package main

import (
	"errors"
	"os/exec"
	"slices"
	"strings"
	"testing"
)

// serveOnly are the packages of kritika's that only serve links: one in
// the runner's binary would ship in every runner pod. A package serve
// alone links belongs here.
var serveOnly = []string{
	"adapter", "auth", "egress", "executor", "forge", "gateway", "ingest", "metrics", "poller", "web", "webapi", "webhook", "worker",
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
		// verifier, which logs through it; client-go is what stays out.
		if strings.HasPrefix(pkg, "k8s.io/") && !strings.HasPrefix(pkg, "k8s.io/klog/") {
			t.Errorf("the runner links %s", pkg)
		}
		name, ok := strings.CutPrefix(pkg, "github.com/home-operations/kritika/internal/")
		if top, _, _ := strings.Cut(name, "/"); ok && slices.Contains(serveOnly, top) {
			t.Errorf("the runner links serve's %s", pkg)
		}
	}
}
