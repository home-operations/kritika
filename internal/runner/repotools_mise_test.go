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

	"github.com/home-operations/kritika/internal/repoconfig"
)

func TestMiseRepositoryTools(t *testing.T) {
	bin, dataDir := hostMise(t)
	t.Setenv("MISE_SYSTEM_DATA_DIR", dataDir)
	systemMiseConfig(t, testMiseConfig)
	artifact := filepath.Join(t.TempDir(), "repo-tool")
	payload := []byte("#!/bin/sh\nprintf '%s\\n' \"$REPO_ENV\"\n")
	if err := os.WriteFile(artifact, payload, 0o644); err != nil {
		t.Fatal(err)
	}
	doc := func(command string) string {
		return fmt.Sprintf(`[env]
REPO_ENV = "from repository"
NO_COLOR = "0"
[tools]
"http:repo-tool" = { version = "1.0.0", url = "file://%s", filter_bins = "%s" }
`, filepath.ToSlash(artifact), command)
	}
	lock := fmt.Sprintf(`lockfile_version = 3
[[tools."http:repo-tool"]]
version = "1.0.0"
backend = "http:repo-tool"
specifiers = ["1.0.0"]
[tools."http:repo-tool"."platforms.%s"]
url = "file://%s"
checksum = "sha256:%x"
`, misePlatform(), filepath.ToSlash(artifact), sha256.Sum256(payload))
	lockNote := "does not pin it for " + misePlatform()
	for _, tt := range []struct {
		name, lock string
		// command is what the tool is offered as, and note what the notes
		// say when it is not.
		command, note string
		offer         bool
		installErr    bool
	}{
		{"locked install", lock, "repo-tool", "", true, false},
		{"missing lock", "", "repo-tool", lockNote, false, false},
		{"missing platform", strings.ReplaceAll(lock, "platforms."+misePlatform(), "platforms.other-x64"), "repo-tool", lockNote, false, false},
		{"wrong version", strings.ReplaceAll(lock, "1.0.0", "2.0.0"), "repo-tool", lockNote, false, false},
		{"missing checksum", strings.ReplaceAll(lock, fmt.Sprintf("sha256:%x", sha256.Sum256(payload)), ""), "repo-tool", lockNote, false, false},
		{"wrong checksum", strings.ReplaceAll(lock, fmt.Sprintf("%x", sha256.Sum256(payload)), strings.Repeat("0", 64)), "repo-tool", "", true, true},
		// A repository's binary is not run with the run's GitHub token.
		{"gh refused", lock, "gh", "does not offer gh: it carries one of the run's credentials", false, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			repo, notes, err := keptRepoMiseConfig(repoconfig.Files{RepoMiseConfig: doc(tt.command), RepoMiseLock: tt.lock})
			if err != nil || len(notes) != 0 {
				t.Fatalf("parse = %v, %v", notes, err)
			}
			home, cache := t.TempDir(), t.TempDir()
			base := []string{"HOME=" + home, "PATH=/usr/bin:/bin", "XDG_CACHE_HOME=" + cache}
			m, notes, err := newMiseTools(t.Context(), bin, home, base, nil, nil, repo, []string{"gh"}, Secrets{Redacted: &Redactions{}}, slog.New(slog.DiscardHandler))
			if err != nil {
				t.Fatal(err)
			}
			if m.byCommand["yq"] == nil || (m.byCommand[tt.command] != nil) != tt.offer {
				t.Fatalf("offered = %v, tool = %+v, notes = %v", m.offered(), m.tools["http:repo-tool"], notes)
			}
			if !slices.Contains(m.commandEnv(), "NO_COLOR=0") || !slices.Contains(m.commandEnv(), "REPO_ENV=from repository") {
				t.Fatalf("repository env not merged: %v", m.commandEnv())
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
			if m.pending() {
				t.Fatal("installed tool still pending")
			}
			// mise's cache is the runner's, under the read-only HOME, not
			// in the commands' writable XDG_CACHE_HOME it would follow.
			if !slices.Contains(m.env, "MISE_CACHE_DIR="+filepath.Join(home, ".cache", "mise")) {
				t.Fatalf("mise env = %v, want its cache under HOME", m.env)
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
	systemMiseConfig(t, testMiseConfig)
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
				nil, nil, repo, []string{"gh"}, Secrets{Redacted: &Redactions{}}, slog.New(slog.DiscardHandler))
			if err != nil {
				t.Fatal(err)
			}
			if !m.tools["yq"].installed || !m.fromRepo(m.tools["yq"]) || m.byCommand["yq"] == nil {
				t.Fatalf("tool = %+v, offered = %v, notes = %v", m.tools["yq"], m.offered(), notes)
			}
		})
	}
}
