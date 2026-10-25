package runner

import (
	"cmp"
	"context"
	"crypto/sha256"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/home-operations/kritika/internal/runspec"
)

func TestMiseRepositoryTools(t *testing.T) {
	bin, dataDir := hostMise(t)
	t.Setenv("MISE_SYSTEM_DATA_DIR", dataDir)
	systemMiseConfig(t, testMiseConfig)
	payload := []byte("#!/bin/sh\necho installed\n")
	for _, tt := range []struct {
		// doc is the repository's config, when not github:acme/repo-tool's
		// offering command.
		name, doc string
		// command is what the tool is offered as, and note what the notes
		// say when it is not.
		command, note string
		offer         bool
		// digest is what the release publishes as its asset's, and asset
		// the URL mise downloads it from, "" for the API's server.
		digest, asset string
		// installErr is what the install's error says, "" when it succeeds.
		installErr string
		// locked is whether the run locks the tool before installing it:
		// mise lock takes the asset's checksum from its digest, and pins
		// none for a release that publishes none.
		locked bool
	}{
		{name: "declared", command: "repo-tool", offer: true},
		{name: "digest verified", command: "repo-tool", offer: true, digest: fmt.Sprintf("sha256:%x", sha256.Sum256(payload)), locked: true},
		{name: "wrong digest", command: "repo-tool", offer: true, digest: "sha256:" + strings.Repeat("0", 64), installErr: "checksum", locked: true},
		{name: "unpinnable", command: "repo-tool", offer: true, asset: "https://tools.example.invalid/repo-tool", installErr: "could not be installed"},
		// A repository's binary is not run with the run's GitHub token.
		{name: "gh refused", command: "gh", note: "does not offer gh: it carries one of the run's credentials"},
		// A registry short name may resolve to a backend that runs a
		// plugin's scripts, which .kritika.yaml cannot tell from the name.
		{name: "plugin backend", doc: "[tools]\nyarn = '1.22.22'\n", command: "yarn",
			note: "yarn not offered: its vfox:jdx/vfox-yarn backend builds or runs a plugin"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/repo-tool" {
					_, _ = w.Write(payload)
					return
				}
				repoToolRelease(cmp.Or(tt.asset, "http://"+r.Host+"/repo-tool"), tt.digest).ServeHTTP(w, r)
			}))
			defer api.Close()
			doc := cmp.Or(tt.doc, repoToolDoc(api.URL, tt.command))
			repo, err := newRepoMiseConfig(doc)
			if err != nil {
				t.Fatal(err)
			}
			home, cache := t.TempDir(), t.TempDir()
			base := []string{"HOME=" + home, "PATH=/usr/bin:/bin", "XDG_CACHE_HOME=" + cache}
			var logs strings.Builder
			m, notes, err := newMiseTools(t.Context(), bin, home, base, nil, false, repo, []string{"gh"}, runspec.Secrets{Redacted: &runspec.Redactions{}},
				slog.New(slog.NewTextHandler(&logs, nil)))
			if err != nil {
				t.Fatal(err)
			}
			if m.byCommand["yq"] == nil || (m.byCommand[tt.command] != nil) != tt.offer {
				t.Fatalf("offered = %v, tool = %+v, notes = %v", m.offered(), m.tools["github:acme/repo-tool"], notes)
			}
			if !tt.offer {
				if !strings.Contains(strings.Join(notes, "\n"), tt.note) {
					t.Fatalf("notes = %v, want one with %q", notes, tt.note)
				}
				return
			}
			if !m.pending() {
				t.Fatal("repository tool installed before first use")
			}
			if err := lockTree(t.Context(), home, true); err != nil {
				t.Fatal(err)
			}
			m.locked = true
			t.Cleanup(func() { _ = lockTree(context.Background(), home, false) })
			path, env, err := m.resolve(t.Context(), "repo-tool")
			// The install is locked when the run's own lock pins the tool;
			// one the lock does not pin installs unlocked, with a warning.
			if strings.Contains(logs.String(), "tool not locked") == tt.locked {
				t.Fatalf("log:\n%s", logs.String())
			}
			if tt.installErr != "" {
				if err == nil || !strings.Contains(strings.ToLower(err.Error()), tt.installErr) {
					t.Fatalf("install = %v, want an error with %q", err, tt.installErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(logs.String(), fmt.Sprintf("locked=%v", tt.locked)) {
				t.Fatalf("log:\n%s", logs.String())
			}
			cmd := exec.CommandContext(t.Context(), path)
			cmd.Env = env
			out, err := cmd.Output()
			if err != nil || string(out) != "installed\n" {
				t.Fatalf("run = %q, %v", out, err)
			}
			if m.pending() {
				t.Fatal("installed tool still pending")
			}
			// mise's cache is the runner's, under the read-only HOME, not
			// in the commands' writable XDG_CACHE_HOME it would follow.
			if !slices.Contains(m.env, "MISE_CACHE_DIR="+filepath.Join(home, ".cache", "mise")) {
				t.Fatalf("mise env = %v, want its cache under HOME", m.env)
			}
			if got, err := os.ReadFile(filepath.Join(home, "mise.toml")); err != nil || string(got) != doc {
				t.Fatalf("config changed: %s, %v", got, err)
			}
		})
	}
}

// TestMiseRepositoryInstalledTool: a tool the repository declares at a
// version the image has installed is the repository's, nothing being
// downloaded, however HOME is reached: mise names a config by its
// resolved path, so a symlinked HOME must match too.
func TestMiseRepositoryInstalledTool(t *testing.T) {
	bin, dataDir := hostMise(t)
	t.Setenv("MISE_SYSTEM_DATA_DIR", dataDir)
	systemMiseConfig(t, testMiseConfig)
	doc := "[tools]\nyq = '" + testYqVersion + "'\n"
	for _, tt := range []struct {
		name      string
		symlinked bool
	}{
		{"home", false},
		{"symlinked home", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			repo, err := newRepoMiseConfig(doc)
			if err != nil {
				t.Fatal(err)
			}
			home := t.TempDir()
			if tt.symlinked {
				link := filepath.Join(t.TempDir(), "link")
				if err := os.Symlink(home, link); err != nil {
					t.Fatal(err)
				}
				home = link
			}
			m, notes, err := newMiseTools(t.Context(), bin, home, []string{"HOME=" + home, "PATH=/usr/bin:/bin"},
				nil, false, repo, []string{"gh"}, runspec.Secrets{Redacted: &runspec.Redactions{}}, slog.New(slog.DiscardHandler))
			if err != nil {
				t.Fatal(err)
			}
			if !m.tools["yq"].installed || !m.fromRepo(m.tools["yq"]) || m.byCommand["yq"] == nil {
				t.Fatalf("tool = %+v, offered = %v, notes = %v", m.tools["yq"], m.offered(), notes)
			}
		})
	}
}
