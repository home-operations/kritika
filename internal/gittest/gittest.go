// Package gittest prepares go-git repositories for tests.
package gittest

import (
	"testing"

	git "github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/config"
)

// Unsigned turns commit signing off in r's own configuration. go-git reads
// commit.gpgSign from the global and system configuration too, and fails a
// commit it has no signer for, so a test that commits would otherwise fail
// wherever the developer signs their commits.
func Unsigned(t testing.TB, r *git.Repository) {
	t.Helper()
	cfg, err := r.Config()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Commit.GpgSign = config.OptBoolFalse
	if err := r.SetConfig(cfg); err != nil {
		t.Fatal(err)
	}
}
