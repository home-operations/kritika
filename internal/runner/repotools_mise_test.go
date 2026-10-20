package runner

import (
	"context"
	"crypto/sha256"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestMiseRepositoryTools(t *testing.T) {
	bin, dataDir := hostMise(t)
	t.Setenv("MISE_SYSTEM_DATA_DIR", dataDir)
	systemMiseConfig(t, testMiseConfig)
	artifact := filepath.Join(t.TempDir(), "repo-tool")
	payload := []byte("#!/bin/sh\necho installed\n")
	if err := os.WriteFile(artifact, payload, 0o644); err != nil {
		t.Fatal(err)
	}
	checksum := fmt.Sprintf("sha256:%x", sha256.Sum256(payload))
	doc := func(command, checksum string) string {
		d := fmt.Sprintf("[tools.'http:repo-tool']\nversion = '1.0.0'\nurl = 'file://%s'\nfilter_bins = '%s'\n", filepath.ToSlash(artifact), command)
		if checksum != "" {
			d += "checksum = '" + checksum + "'\n"
		}
		return d
	}
	for _, tt := range []struct {
		name, doc string
		// command is what the tool is offered as, and note what the notes
		// say when it is not.
		command, note string
		offer         bool
		// installErr is what the install's error says, "" when it succeeds.
		installErr string
		// locked is whether the run locks the tool before installing it:
		// mise lock takes a checksum from the config, or hashes an asset it
		// fetches, which a file:// URL or an unreachable host is not.
		locked bool
	}{
		{"declared", doc("repo-tool", ""), "repo-tool", "", true, "", false},
		{"checksum verified", doc("repo-tool", checksum), "repo-tool", "", true, "", true},
		{"wrong checksum", doc("repo-tool", "sha256:"+strings.Repeat("0", 64)), "repo-tool", "", true, "checksum", true},
		{"unpinnable", strings.Replace(doc("repo-tool", ""), "file://"+filepath.ToSlash(artifact), "https://tools.example.invalid/repo-tool", 1),
			"repo-tool", "", true, "could not be installed", false},
		// A repository's binary is not run with the run's GitHub token.
		{"gh refused", doc("gh", ""), "gh", "does not offer gh: it carries one of the run's credentials", false, "", false},
		// A registry short name may resolve to a backend that runs a
		// plugin's scripts, which .kritika.yaml cannot tell from the name.
		{"plugin backend", "[tools]\nyarn = '1.22.22'\n", "yarn", "yarn not offered: its vfox:jdx/vfox-yarn backend builds or runs a plugin", false, "", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			repo, err := newRepoMiseConfig(tt.doc)
			if err != nil {
				t.Fatal(err)
			}
			home, cache := t.TempDir(), t.TempDir()
			base := []string{"HOME=" + home, "PATH=/usr/bin:/bin", "XDG_CACHE_HOME=" + cache}
			var logs strings.Builder
			m, notes, err := newMiseTools(t.Context(), bin, home, base, nil, false, repo, []string{"gh"}, Secrets{Redacted: &Redactions{}},
				slog.New(slog.NewTextHandler(&logs, nil)))
			if err != nil {
				t.Fatal(err)
			}
			if m.byCommand["yq"] == nil || (m.byCommand[tt.command] != nil) != tt.offer {
				t.Fatalf("offered = %v, tool = %+v, notes = %v", m.offered(), m.tools["http:repo-tool"], notes)
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
			if got, err := os.ReadFile(filepath.Join(home, "mise.toml")); err != nil || string(got) != tt.doc {
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
				nil, false, repo, []string{"gh"}, Secrets{Redacted: &Redactions{}}, slog.New(slog.DiscardHandler))
			if err != nil {
				t.Fatal(err)
			}
			if !m.tools["yq"].installed || !m.fromRepo(m.tools["yq"]) || m.byCommand["yq"] == nil {
				t.Fatalf("tool = %+v, offered = %v, notes = %v", m.tools["yq"], m.offered(), notes)
			}
		})
	}
}
