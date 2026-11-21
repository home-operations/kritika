//go:build integration

package webapi

import (
	"encoding/json"
	"testing"

	"github.com/home-operations/kritika/internal/configfile/configfiletest"
)

// testRepoConfigUnderDefaultOff reads a repository that the dashboard turned
// on while the admin's default is off. The default stays the admin's own
// setting, but the repository's file, which leaves enabled unset, keeps it
// on, as its reviews do. Only a file that sets enabled: false turns it off.
func testRepoConfigUnderDefaultOff(t *testing.T, e *apiEnv) {
	e.srv.current.Set(configfiletest.Load(t, integrationConfig+"\nenabled: false\n"))
	t.Cleanup(func() { e.srv.current.Set(e.file) })
	turnedOn := e.scalar(`SELECT coalesce(turned_on::text, '') FROM repositories WHERE id = $1`, e.a.repoID)
	e.exec(`UPDATE repositories SET turned_on = true WHERE id = $1`, e.a.repoID)
	t.Cleanup(func() {
		e.exec(`UPDATE repositories SET turned_on = NULLIF($2, '')::boolean WHERE id = $1`, e.a.repoID, turnedOn)
	})
	doc := e.scalar(`SELECT repo_files ->> '.kritika.yaml' FROM context_packs WHERE runner_run_id = $1`, e.a.runID)
	t.Cleanup(func() {
		e.exec(`UPDATE context_packs SET repo_files = jsonb_build_object('.kritika.yaml', $2::text) WHERE runner_run_id = $1`, e.a.runID, doc)
	})
	for _, tt := range []struct {
		name, doc string
		enabled   bool
	}{
		{name: "a file that leaves enabled unset", doc: doc, enabled: true},
		{name: "a file that turns the repository off", doc: "enabled: false\n", enabled: false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			e.exec(`UPDATE context_packs SET repo_files = jsonb_build_object('.kritika.yaml', $2::text) WHERE runner_run_id = $1`, e.a.runID, tt.doc)
			status, body := e.getBody("member-a", "/api/v1/accounts/github/wa/repos/wa/one")
			if status != 200 {
				t.Fatalf("status = %d: %s", status, body)
			}
			var d RepoDetail
			if err := json.Unmarshal(body, &d); err != nil {
				t.Fatal(err)
			}
			if !d.Enabled || d.Settings.Enabled {
				t.Fatalf("enabled = %v, the admin's = %v; want the dashboard's choice to run it under the admin's default of off", d.Enabled, d.Settings.Enabled)
			}
			if d.RepoConfig == nil {
				t.Fatal("repoConfig = null, want the file the seeded review read")
			}
			if d.RepoConfig.Settings.Enabled != tt.enabled {
				t.Fatalf("repoConfig enabled = %v, want %v", d.RepoConfig.Settings.Enabled, tt.enabled)
			}
		})
	}
}
