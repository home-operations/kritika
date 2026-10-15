package runner

import (
	"crypto/sha256"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/home-operations/kritika/internal/repoconfig"
)

func TestMiseRepositoryTools(t *testing.T) {
	bin, dataDir := hostMise(t)
	t.Setenv("MISE_SYSTEM_DATA_DIR", dataDir)
	t.Setenv("MISE_SYSTEM_CONFIG_DIR", t.TempDir())
	artifact := filepath.Join(t.TempDir(), "repo-tool")
	payload := []byte("#!/bin/sh\nprintf '%s\\n' \"$REPO_ENV\"\n")
	if err := os.WriteFile(artifact, payload, 0o644); err != nil {
		t.Fatal(err)
	}
	doc := fmt.Sprintf(`[env]
REPO_ENV = "from repository"
NO_COLOR = "0"
[tools]
"http:repo-tool" = { version = "1.0.0", url = "file://%s", filter_bins = "repo-tool" }
`, filepath.ToSlash(artifact))
	lock := fmt.Sprintf(`lockfile_version = 3
[[tools."http:repo-tool"]]
version = "1.0.0"
backend = "http:repo-tool"
specifiers = ["1.0.0"]
[tools."http:repo-tool"."platforms.%s"]
url = "file://%s"
checksum = "sha256:%x"
`, misePlatform(), filepath.ToSlash(artifact), sha256.Sum256(payload))
	for _, tt := range []struct {
		name, lock string
		offer      bool
		installErr bool
	}{
		{"locked install", lock, true, false},
		{"missing lock", "", false, false},
		{"missing platform", strings.ReplaceAll(lock, "platforms."+misePlatform(), "platforms.other-x64"), false, false},
		{"wrong version", strings.ReplaceAll(lock, "1.0.0", "2.0.0"), false, false},
		{"missing checksum", strings.ReplaceAll(lock, fmt.Sprintf("sha256:%x", sha256.Sum256(payload)), ""), false, false},
		{"wrong checksum", strings.ReplaceAll(lock, fmt.Sprintf("%x", sha256.Sum256(payload)), strings.Repeat("0", 64)), true, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			repo, notes, err := keptRepoMiseConfig(repoconfig.Files{RepoMiseConfig: doc, RepoMiseLock: tt.lock})
			if err != nil || len(notes) != 0 {
				t.Fatalf("parse = %v, %v", notes, err)
			}
			home := t.TempDir()
			base := []string{"HOME=" + home, "PATH=/usr/bin:/bin"}
			m, notes, err := newMiseTools(t.Context(), bin, home, base, testMiseConfig, repo, &Secrets{}, slog.New(slog.DiscardHandler))
			if err != nil {
				t.Fatal(err)
			}
			if !m.provides("yq") || m.provides("repo-tool") != tt.offer {
				t.Fatalf("offered = %v, tool = %+v, notes = %v", m.offered(nil), m.tools["http:repo-tool"], notes)
			}
			if !slices.Contains(m.commandEnv(), "NO_COLOR=0") || !slices.Contains(m.commandEnv(), "REPO_ENV=from repository") {
				t.Fatalf("repository env not merged: %v", m.commandEnv())
			}
			if !tt.offer {
				if !strings.Contains(strings.Join(notes, "\n"), "does not pin it for "+misePlatform()) {
					t.Fatalf("no lock note: %v", notes)
				}
				return
			}
			if !m.pending(m.offered(nil)) {
				t.Fatal("repository tool installed before first use")
			}
			if err := lockTree(home, true); err != nil {
				t.Fatal(err)
			}
			m.locked = true
			t.Cleanup(func() { _ = lockTree(home, false) })
			path, env, err := m.resolve(t.Context(), "repo-tool")
			if tt.installErr {
				if err == nil || !strings.Contains(strings.ToLower(err.Error()), "checksum") {
					t.Fatalf("install = %v, want checksum failure", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			cmd := exec.CommandContext(t.Context(), path)
			cmd.Env = env
			out, err := cmd.Output()
			if err != nil || string(out) != "from repository\n" {
				t.Fatalf("run = %q, %v", out, err)
			}
			if m.pending(m.offered(nil)) {
				t.Fatal("installed tool still pending")
			}
			if got, err := os.ReadFile(filepath.Join(home, "mise.lock")); err != nil || string(got) != tt.lock {
				t.Fatalf("lock changed: %s, %v", got, err)
			}
		})
	}
}

// TestMiseRepositoryInstalledTool: a tool the repository declares at a
// version the image has installed needs no lock entry, nothing being
// downloaded, and is the repository's however HOME is reached: mise names
// a config by its resolved path, so a symlinked HOME must match too.
func TestMiseRepositoryInstalledTool(t *testing.T) {
	bin, dataDir := hostMise(t)
	t.Setenv("MISE_SYSTEM_DATA_DIR", dataDir)
	t.Setenv("MISE_SYSTEM_CONFIG_DIR", t.TempDir())
	doc := "[tools]\nyq = '" + testYqVersion + "'\n"
	lock := fmt.Sprintf(`[[tools.yq]]
version = %q
backend = "aqua:mikefarah/yq"
[tools.yq."platforms.other-x64"]
url = "https://example.com/yq"
checksum = "sha256:abc"
`, testYqVersion)
	for _, tt := range []struct {
		name, lock string
		symlinked  bool
	}{
		{"no lock", "", false},
		{"another platform's lock", lock, false},
		{"symlinked home", "", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			repo, _, err := keptRepoMiseConfig(repoconfig.Files{RepoMiseConfig: doc, RepoMiseLock: tt.lock})
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
				testMiseConfig, repo, &Secrets{}, slog.New(slog.DiscardHandler))
			if err != nil {
				t.Fatal(err)
			}
			if !m.tools["yq"].installed || !m.fromRepo(m.tools["yq"]) || !m.provides("yq") {
				t.Fatalf("tool = %+v, offered = %v, notes = %v", m.tools["yq"], m.offered(nil), notes)
			}
		})
	}
}
