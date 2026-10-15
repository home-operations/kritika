package runner

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestReservedEnvName(t *testing.T) {
	for name, want := range map[string]bool{
		"HOME": true, "home": true, "PATH": true, "https_proxy": true, "NO_PROXY": true, "GH_TOKEN": true, "GITHUB_TOKEN": true,
		"GIT_EDITOR": true, "MISE_YES": true, "LD_PRELOAD": true, "CURL_HOME": true, "XDG_CONFIG_HOME": true, "RIPGREP_CONFIG_PATH": true,
		"PAGER": true, "NO_COLOR": false, "HELM_DRIVER": false, "GHX": false, "LDFLAGS": false,
	} {
		if got := reservedEnvName(name); got != want {
			t.Errorf("reservedEnvName(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestParseMiseEnv(t *testing.T) {
	out := []byte(`{"PATH":"/installs/helm/4.3.0/bin:/usr/local/bin:/usr/bin","NO_COLOR":"1","HOME":"/evil","gh_token":"x","HELM_DRIVER":"configmap"}`)
	path, extra, notes, err := parseMiseEnv(out)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(path, []string{"/installs/helm/4.3.0/bin", "/usr/local/bin", "/usr/bin"}) {
		t.Fatalf("path = %v", path)
	}
	if !slices.Equal(extra, []string{"HELM_DRIVER=configmap", "NO_COLOR=1"}) {
		t.Fatalf("extra = %v", extra)
	}
	if !slices.Equal(notes, []string{
		"mise env HOME not set for the commands: the name is reserved",
		"mise env gh_token not set for the commands: the name is reserved",
	}) {
		t.Fatalf("notes = %v", notes)
	}
	if _, _, _, err := parseMiseEnv([]byte("not json")); err == nil {
		t.Fatal("no error for a non-JSON env")
	}
	// An env without PATH gives none, not one empty entry, which would
	// name the working directory.
	if path, _, _, err := parseMiseEnv([]byte(`{"NO_COLOR":"1"}`)); err != nil || path != nil {
		t.Fatalf("path = %#v, %v", path, err)
	}
}

// TestMiseCommandEnvPath: the commands' PATH is mise's when it set one,
// else the runner's own.
func TestMiseCommandEnvPath(t *testing.T) {
	m := &miseTools{base: []string{"PATH=/usr/bin", "HOME=/h"}, extra: []string{"X=1"}}
	if got := m.commandEnv(); !slices.Equal(got, []string{"PATH=/usr/bin", "HOME=/h", "X=1"}) {
		t.Fatalf("env without a mise PATH = %v", got)
	}
	m.path = []string{"/tool/bin", "/usr/bin"}
	if got := m.commandEnv(); !slices.Equal(got, []string{"PATH=/tool/bin:/usr/bin", "HOME=/h", "X=1"}) {
		t.Fatalf("env with a mise PATH = %v", got)
	}
}

func TestRegistryBins(t *testing.T) {
	entries := []registryEntry{
		{Short: "crane", Backends: []string{"aqua:google/go-containerregistry"}, Bins: []string{"crane", "gcrane", "krane"}},
		{Short: "rg", Backends: []string{"aqua:BurntSushi/ripgrep", "cargo:ripgrep"}, Bins: []string{"rg"}},
	}
	for key, want := range map[string][]string{
		"crane": {"crane", "gcrane", "krane"}, "aqua:BurntSushi/ripgrep": {"rg"}, "cargo:ripgrep": {"rg"}, "helm": nil,
	} {
		if got := registryBins(entries, key); !slices.Equal(got, want) {
			t.Errorf("registryBins(%q) = %v, want %v", key, got, want)
		}
	}
}

func TestMiseToolInfoFilterBins(t *testing.T) {
	for raw, want := range map[string][]string{
		`{"tool_options":{"filter_bins":"flate"}}`:            {"flate"},
		`{"tool_options":{"filter_bins":["crane","gcrane"]}}`: {"crane", "gcrane"},
		`{"tool_options":{}}`:                                 nil,
		`{"tool_options":{"filter_bins":null}}`:               nil,
	} {
		var info miseToolInfo
		if err := json.Unmarshal([]byte(raw), &info); err != nil {
			t.Fatal(err)
		}
		if got := info.filterBins(); !slices.Equal(got, want) {
			t.Errorf("%s: filterBins = %v, want %v", raw, got, want)
		}
	}
}

func TestExecutablesUnder(t *testing.T) {
	install := t.TempDir()
	bin := filepath.Join(install, "bin")
	other := t.TempDir()
	for _, d := range []string{bin, other} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for path, mode := range map[string]os.FileMode{
		filepath.Join(bin, "helm"): 0o755, filepath.Join(bin, "LICENSE"): 0o644, filepath.Join(install, "tool"): 0o755,
		filepath.Join(other, "rg"): 0o755,
	} {
		if err := os.WriteFile(path, nil, mode); err != nil {
			t.Fatal(err)
		}
	}
	path := []string{other, bin, install, "/nowhere"}
	if got := executablesUnder(path, install); !slices.Equal(got, []string{"helm", "tool"}) {
		t.Fatalf("executablesUnder = %v", got)
	}
	if got := findExecutable(path, install, "helm"); got != filepath.Join(bin, "helm") {
		t.Fatalf("findExecutable(helm) = %q", got)
	}
	if got := findExecutable(path, install, "rg"); got != "" {
		t.Fatalf("findExecutable(rg) = %q, want none: rg is outside the install", got)
	}
	// A sibling directory whose name starts with the install path's is
	// not under it.
	if got := executablesUnder([]string{install + "-other"}, install); got != nil {
		t.Fatalf("executablesUnder = %v", got)
	}
}

func TestMiseToolsOffered(t *testing.T) {
	helm, rg := &miseTool{name: "helm", installed: false}, &miseTool{name: "rg", installed: true}
	m := &miseTools{byCommand: map[string]*miseTool{"helm": helm, "rg": rg}}
	for _, tt := range []struct {
		allowed []string
		want    []string
		pending bool
	}{
		{nil, []string{"helm", "rg"}, true},
		{[]string{}, nil, false},
		{[]string{"rg", "curl"}, []string{"rg"}, false},
	} {
		got := m.offered(tt.allowed)
		names := slices.Sorted(maps.Keys(got))
		if !slices.Equal(names, tt.want) || m.pending(got) != tt.pending {
			t.Errorf("offered(%v) = %v, pending %v; want %v, %v", tt.allowed, names, m.pending(got), tt.want, tt.pending)
		}
	}
}

// hostMise is a mise on this host with the tool TestMiseTools needs
// installed, yq at the version the repository's own mise config pins,
// which `mise install` leaves in the data directory. The test points
// mise's system data directory at it, so no install is needed and no
// network reached.
func hostMise(t *testing.T) (bin, dataDir string) {
	t.Helper()
	bin, err := exec.LookPath("mise")
	if err != nil {
		t.Skip(err)
	}
	dataDir = os.Getenv("MISE_DATA_DIR")
	if dataDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			t.Skip(err)
		}
		dataDir = filepath.Join(home, ".local", "share", "mise")
		if xdg := os.Getenv("XDG_DATA_HOME"); xdg != "" {
			dataDir = filepath.Join(xdg, "mise")
		}
	}
	if _, err := os.Stat(filepath.Join(dataDir, "installs", "yq", testYqVersion)); err != nil {
		t.Skip(err)
	}
	return bin, dataDir
}

// testYqVersion is the yq .mise/config.toml pins.
const testYqVersion = "4.54.1"

const testMiseConfig = "[env]\nNO_COLOR = \"1\"\nHOME = \"/evil\"\nTOKEN_X = { value = \"s3cretvalue\", redact = true }\n\n[tools]\nyq = \"" + testYqVersion + "\"\n"

func TestMiseTools(t *testing.T) {
	bin, dataDir := hostMise(t)
	t.Setenv("MISE_SYSTEM_DATA_DIR", dataDir)
	t.Setenv("MISE_SYSTEM_CONFIG_DIR", t.TempDir())
	home := t.TempDir()
	base := []string{"PATH=/usr/bin:/bin", "HOME=" + home}
	secrets := &Secrets{GitToken: "ghs_run"}
	m, notes, err := newMiseTools(t.Context(), bin, home, base, testMiseConfig, nil, secrets, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(secrets.Redacted.Values(), []string{"s3cretvalue"}) || secrets.Mask("a s3cretvalue b") != "a *** b" {
		t.Fatalf("redacted = %v", secrets.Redacted.Values())
	}
	// A value found by a later refresh, after an install, reaches every
	// mask built from the secrets before it.
	mask, copied := secrets.Mask, *secrets
	secrets.Redacted.Add("later")
	if mask("x later") != "x ***" || copied.Mask("x later") != "x ***" {
		t.Fatal("a redaction added later is not masked by an earlier mask")
	}
	if !slices.Equal(notes, []string{"mise env HOME not set for the commands: the name is reserved"}) {
		t.Fatalf("notes = %v", notes)
	}
	yq := m.tools["yq"]
	if yq == nil || !yq.installed || yq.version != testYqVersion || !slices.Equal(yq.commands, []string{"yq"}) {
		t.Fatalf("yq = %+v", yq)
	}
	if !strings.HasPrefix(yq.installPath, dataDir) {
		t.Fatalf("yq installed at %s, not from the system installs under %s", yq.installPath, dataDir)
	}
	offered := m.offered(nil)
	if _, ok := offered["yq"]; !ok || len(offered) != 1 || m.pending(offered) {
		t.Fatalf("offered = %v, pending %v", offered, m.pending(offered))
	}
	path, env, err := m.resolve(t.Context(), "yq")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(path, yq.installPath) {
		t.Fatalf("resolved %s outside %s", path, yq.installPath)
	}
	if !slices.Contains(env, "NO_COLOR=1") || !slices.Contains(env, "TOKEN_X=s3cretvalue") || !slices.Contains(env, "HOME="+home) ||
		slices.Contains(env, "HOME=/evil") {
		t.Fatalf("env = %v", env)
	}
	var pathVar string
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, "PATH="); ok {
			pathVar = v
		}
	}
	if !strings.HasPrefix(pathVar, yq.installPath) || !strings.HasSuffix(pathVar, ":/usr/bin:/bin") {
		t.Fatalf("PATH = %q: want yq's bin directory before the runner's own", pathVar)
	}
	out, err := exec.Command(path, "--version").Output()
	if err != nil || !strings.Contains(string(out), testYqVersion) {
		t.Fatalf("yq --version = %q, %v", out, err)
	}
	if _, _, err := m.resolve(t.Context(), "helm"); err == nil || !strings.Contains(err.Error(), "not among the mise tools") {
		t.Fatalf("resolve(helm) = %v", err)
	}
}

func TestDeclaredCommands(t *testing.T) {
	registry := []registryEntry{
		{Short: "crane", Backends: []string{"aqua:google/go-containerregistry"}, Bins: []string{"crane", "gcrane", "krane"}},
		{Short: "rg", Backends: []string{"aqua:BurntSushi/ripgrep"}, Bins: []string{"rg"}},
	}
	info := func(backend, filter string) miseToolInfo {
		var i miseToolInfo
		i.Backend = backend
		if filter != "" {
			i.Options.FilterBins = json.RawMessage(filter)
		}
		return i
	}
	for _, tt := range []struct {
		name string
		info miseToolInfo
		want []string
	}{
		{"crane", info("aqua:google/go-containerregistry", ""), []string{"crane", "gcrane", "krane"}},
		{"crane", info("aqua:google/go-containerregistry", `"crane"`), []string{"crane"}},
		{"github:google/go-containerregistry", info("github:google/go-containerregistry", ""), []string{"crane", "gcrane", "krane"}},
		{"aqua:BurntSushi/ripgrep", info("aqua:BurntSushi/ripgrep", ""), []string{"rg"}},
		{"github:home-operations/flate", info("github:home-operations/flate", ""), []string{"flate"}},
		{"github:home-operations/flate", info("github:home-operations/flate", `["flate","flatectl"]`), []string{"flate", "flatectl"}},
		{"odd", info("odd", ""), nil},
	} {
		if got := declaredCommands(tt.name, tt.info, registry); !slices.Equal(got, tt.want) {
			t.Errorf("declaredCommands(%s, %+v) = %v, want %v", tt.name, tt.info, got, tt.want)
		}
	}
}

func TestCappedWriter(t *testing.T) {
	w := &cappedWriter{max: 5}
	for _, s := range []string{"ab", "cde", "fg"} {
		if n, err := w.Write([]byte(s)); n != len(s) || err != nil {
			t.Fatalf("Write(%q) = %d, %v", s, n, err)
		}
	}
	if string(w.buf) != "abcde" || w.total != 7 || !w.full() {
		t.Fatalf("buf = %q, total = %d, full = %v", w.buf, w.total, w.full())
	}
}

func TestLockTree(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "d")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "f")
	if err := os.WriteFile(file, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/nowhere", filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	if err := lockTree(t.Context(), root, true); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]os.FileMode{root: 0o555, dir: 0o555, file: 0o555} {
		if info, err := os.Stat(path); err != nil || info.Mode().Perm() != want {
			t.Fatalf("%s: mode %v, %v; want %v", path, info.Mode().Perm(), err, want)
		}
	}
	if os.Geteuid() != 0 {
		if err := os.WriteFile(file, []byte("y"), 0o644); err == nil {
			t.Fatal("a locked file was written")
		}
		if err := os.WriteFile(filepath.Join(dir, "new"), nil, 0o644); err == nil {
			t.Fatal("a file was created in a locked directory")
		}
	}
	if err := lockTree(t.Context(), root, false); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(file); err != nil || info.Mode().Perm() != 0o755 {
		t.Fatalf("unlocked mode = %v, %v", info.Mode().Perm(), err)
	}
	if err := os.WriteFile(file, []byte("y"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestMiseRunCanceled: a call to mise under a context that has ended
// reports the context's error rather than mise's.
func TestMiseRunCanceled(t *testing.T) {
	bin, _ := hostMise(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	m := &miseTools{bin: bin, home: t.TempDir(), secrets: &Secrets{}, logger: slog.New(slog.DiscardHandler)}
	if _, err := m.run(ctx, "ls", "--json"); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
}

// TestMiseToolsEmptyConfig: without an instance config, the chart's
// default, nothing is written to home and the image's system config alone
// supplies the tools.
func TestMiseToolsEmptyConfig(t *testing.T) {
	bin, dataDir := hostMise(t)
	t.Setenv("MISE_SYSTEM_DATA_DIR", dataDir)
	system := t.TempDir()
	if err := os.WriteFile(filepath.Join(system, "config.toml"), []byte("[tools]\nyq = \""+testYqVersion+"\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MISE_SYSTEM_CONFIG_DIR", system)
	home := t.TempDir()
	m, notes, err := newMiseTools(t.Context(), bin, home, []string{"PATH=/usr/bin", "HOME=" + home}, "", nil, &Secrets{}, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	if len(notes) != 0 {
		t.Fatalf("notes = %v", notes)
	}
	if _, err := os.Stat(filepath.Join(home, ".config")); !os.IsNotExist(err) {
		t.Fatalf("an instance config was written without one: %v", err)
	}
	if yq := m.tools["yq"]; yq == nil || !yq.installed || !strings.HasPrefix(yq.installPath, dataDir) ||
		!slices.Equal(slices.Sorted(maps.Keys(m.offered(nil))), []string{"yq"}) {
		t.Fatalf("yq = %+v, offered = %v", yq, m.offered(nil))
	}
}

// TestMiseToolsCeiling: a mise config a command could plant in the
// scratch directory, above HOME, is not read.
func TestMiseToolsCeiling(t *testing.T) {
	bin, dataDir := hostMise(t)
	t.Setenv("MISE_SYSTEM_DATA_DIR", dataDir)
	t.Setenv("MISE_SYSTEM_CONFIG_DIR", t.TempDir())
	scratch := t.TempDir()
	home := filepath.Join(scratch, "home")
	if err := os.Mkdir(home, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(scratch, "mise.toml"), []byte("[tools]\njq = \"1.8.2\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	m, _, err := newMiseTools(t.Context(), bin, home, []string{"PATH=/usr/bin", "HOME=" + home}, testMiseConfig, nil, &Secrets{}, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := m.tools["jq"]; ok || len(m.tools) != 1 {
		t.Fatalf("tools = %v: the scratch directory's config was read", slices.Sorted(maps.Keys(m.tools)))
	}
}

// TestMiseToolsBroken: once home cannot be made read-only again after a
// call to mise, no command runs.
func TestMiseToolsBroken(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads every directory")
	}
	bin, dataDir := hostMise(t)
	t.Setenv("MISE_SYSTEM_DATA_DIR", dataDir)
	t.Setenv("MISE_SYSTEM_CONFIG_DIR", t.TempDir())
	home := t.TempDir()
	m, _, err := newMiseTools(t.Context(), bin, home, []string{"PATH=/usr/bin", "HOME=" + home}, testMiseConfig, nil, &Secrets{}, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	if err := lockTree(t.Context(), home, true); err != nil {
		t.Fatal(err)
	}
	m.locked = true
	if _, _, err := m.resolve(t.Context(), "yq"); err != nil {
		t.Fatal(err)
	}
	// A directory the walk cannot read fails the relock.
	sealed := filepath.Join(home, "sealed")
	if err := lockTree(t.Context(), home, false); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(sealed, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.Chmod(sealed, 0o755)
		_ = lockTree(context.Background(), home, false)
	})
	if err := lockTree(t.Context(), home, true); err == nil {
		t.Fatal("locked a tree with an unreadable directory")
	}
	if _, err := m.run(t.Context(), "ls", "--json"); err == nil {
		t.Fatal("mise ran though home could not be made read-only after")
	}
	if _, _, err := m.resolve(t.Context(), "yq"); err == nil || !strings.Contains(err.Error(), "commands are not run") {
		t.Fatalf("resolve after a failed relock = %v", err)
	}
	if err := m.usable(); err == nil {
		t.Fatal("usable after a failed relock")
	}
}

// TestMiseExecRelock: a mise call after which home cannot be made
// read-only again fails itself, so the command it was for does not run,
// and no later one does.
func TestMiseExecRelock(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads every directory")
	}
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip(err)
	}
	home := t.TempDir()
	// A mise that leaves an unreadable directory in home.
	bin := filepath.Join(t.TempDir(), "mise")
	if err := os.WriteFile(bin, []byte("#!"+sh+"\nmkdir -m 0 \"$HOME/sealed\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.Chmod(filepath.Join(home, "sealed"), 0o755)
		_ = lockTree(context.Background(), home, false)
	})
	m := &miseTools{bin: bin, home: home, env: []string{"HOME=" + home, "PATH=/usr/bin:/bin"}, secrets: &Secrets{}, logger: slog.New(slog.DiscardHandler), locked: true}
	if err := lockTree(t.Context(), home, true); err != nil {
		t.Fatal(err)
	}
	if _, err := m.run(t.Context(), "install", "x"); err == nil || !strings.Contains(err.Error(), "not made read-only after mise") {
		t.Fatalf("run = %v", err)
	}
	if err := m.usable(); err == nil {
		t.Fatal("usable after a failed relock")
	}
}
