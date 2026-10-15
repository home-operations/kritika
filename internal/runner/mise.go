package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/home-operations/kritika/internal/textcut"
)

// miseBin is where the -tools image keeps mise: outside every PATH the run
// tool hands a command, so a command cannot start it by name. A runner
// image without it offers the commands it finds on its own PATH instead.
var miseBin = "/opt/mise/bin/mise"

// What bounds mise: miseTimeout one call that reads the config, since
// each runs before the agent's own timeout starts; installTimeout one
// tool's install, as fetchTimeout bounds a fetch, the reads after it
// included; miseOutputBytes what a call may print, the registry being
// under a third of it; and installOutputBytes what of an install's output
// is kept, for the log and the agent when it failed.
const (
	miseTimeout        = 30 * time.Second
	installTimeout     = 2 * time.Minute
	miseOutputBytes    = 4 << 20
	installOutputBytes = 2048
)

// miseSystemVars are the variables an image relocates mise's system config
// and installs with, the only ones of the runner's own environment that
// reach mise.
var miseSystemVars = []string{"MISE_SYSTEM_CONFIG_DIR", "MISE_SYSTEM_DATA_DIR"}

// A mise config's [env] may not set the names the runner sets for a
// command, a name that carries a credential, or one that points a tool at
// a config file or the loader at a library, which the argument checks
// never see: a .curlrc in the checkout could turn on the trace that prints
// the proxy credential. The runner's own variables win, and a dropped name
// is noted in the review's summary.
var (
	reservedEnv = []string{
		"HOME", "PATH", "HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "GITHUB_TOKEN",
		"CURL_HOME", "XDG_CONFIG_HOME", "RIPGREP_CONFIG_PATH", "DOCKER_CONFIG", "PAGER", "BROWSER", "EDITOR", "VISUAL",
	}
	reservedEnvPrefixes = []string{"GH_", "GIT_", "MISE_", "LD_"}
)

// reservedEnvName reports whether a config's [env] may not set name.
func reservedEnvName(name string) bool {
	upper := strings.ToUpper(name)
	return slices.Contains(reservedEnv, upper) ||
		slices.ContainsFunc(reservedEnvPrefixes, func(p string) bool { return strings.HasPrefix(upper, p) })
}

// miseTool is one tool the merged mise config declares.
type miseTool struct {
	// name is the tool as the config declares it: a registry short name
	// or a backend such as github:owner/repo.
	name, version string
	// installPath is where the tool is, or will be, installed, among the
	// image's system installs or in the run's own.
	installPath string
	installed   bool
	// source is the config file that declares it.
	source string
	// commands are the executables it offers.
	commands []string
}

// miseTools are the tools mise's merged config declares for a run, the
// image's system config under the instance's, and the environment mise
// gives their commands. A tool installs on the first call that needs it.
// Everything mise writes lives under the run's HOME, which the runner
// keeps read-only between its own calls to mise, so a command cannot
// replace an installed binary or the config a later install reads, and
// mise reads no config above HOME, where a command could plant one.
type miseTools struct {
	bin, home string
	// base is the commands' environment before mise: PATH, HOME and the
	// proxy variables. env is mise's own, base and the MISE_* variables.
	base, env []string
	// secrets mask mise's output; the values the config marks for
	// redaction join them.
	secrets   *Secrets
	logger    *slog.Logger
	tools     map[string]*miseTool
	byCommand map[string]*miseTool
	// path is PATH as mise computes it, its tools' bin directories before
	// base's, and extra the config's [env] less the reserved names.
	path, extra []string
	// repo is the repository's config, nil when it supplies no tools, and
	// repoSource the file it is written to, as mise names it.
	repo       *repoMiseConfig
	repoSource string
	// registry is mise's registry, read on the first tool whose commands
	// it names; nil until then.
	registry []registryEntry
	// locked says home is read-only, as it is once the run tool is built,
	// and writable that unlocked has it writable for the while; broken is
	// why it could not be made read-only again, after which no command
	// runs.
	locked, writable bool
	broken           error
}

// fromRepo reports whether the repository's config declares t.
func (m *miseTools) fromRepo(t *miseTool) bool {
	return m.repo != nil && t.source == m.repoSource
}

// resolved is path with its symlinks resolved, as mise names a config
// file, or path as given when it cannot be resolved.
func resolved(path string) string {
	if r, err := filepath.EvalSymlinks(path); err == nil {
		return r
	}
	return path
}

// newMiseTools reads the merged config with mise at bin, after writing
// config, when set, as the run's global config under home: mise applies
// a global config's [settings] where it ignores the system config's, so
// the instance's mirrors and the like take effect there. repo, when set,
// is the repository's config, written as the run's local config in home,
// which mise layers over the global one, with its lock beside it; its
// tools install at what the lock pins, and one the lock does not pin for
// this platform is not offered unless the image has that version
// installed already. notes say what was not offered and why, for the
// review's summary.
func newMiseTools(
	ctx context.Context, bin, home string, base []string, config string, repo *repoMiseConfig, secrets *Secrets, logger *slog.Logger,
) (*miseTools, []string, error) {
	if config != "" {
		dir := filepath.Join(home, ".config", "mise")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, nil, fmt.Errorf("runner: mise config: %w", err)
		}
		if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(config), 0o644); err != nil {
			return nil, nil, fmt.Errorf("runner: mise config: %w", err)
		}
	}
	if repo != nil {
		for name, doc := range map[string]string{"mise.toml": repo.config, "mise.lock": repo.lock} {
			if err := os.WriteFile(filepath.Join(home, name), []byte(doc), 0o644); err != nil {
				return nil, nil, fmt.Errorf("runner: repository mise config: %w", err)
			}
		}
	}
	if secrets.Redacted == nil {
		secrets.Redacted = &Redactions{}
	}
	m := &miseTools{
		bin: bin, home: home, base: base, secrets: secrets, logger: logger, repo: repo,
		tools: map[string]*miseTool{}, byCommand: map[string]*miseTool{},
	}
	if repo != nil {
		m.repoSource = resolved(filepath.Join(home, "mise.toml"))
	}
	// Every install goes through the runner, with its timeout and notes,
	// where mise env would otherwise install what a config declares. mise
	// stages downloads under HOME too, rather than in the pod's shared
	// /tmp, so they are out of a command's reach as well. The ceiling
	// keeps mise's config search, from its working directory, HOME, up
	// to but not including the ceiling, out of the scratch directory and
	// everything above it; mise walks resolved paths, so the ceiling is
	// one too.
	m.env = append(slices.Clone(base), "MISE_YES=1", "MISE_AUTO_INSTALL=false", "MISE_TRUSTED_CONFIG_PATHS="+home,
		"MISE_CEILING_PATHS="+resolved(filepath.Dir(home)), "MISE_TMP_DIR="+filepath.Join(home, ".cache", "mise", "tmp"), "NO_COLOR=1")
	for _, name := range miseSystemVars {
		if v := os.Getenv(name); v != "" {
			m.env = append(m.env, name+"="+v)
		}
	}
	notes, err := m.refresh(ctx)
	if err != nil {
		return nil, nil, err
	}
	for _, name := range slices.Sorted(maps.Keys(m.tools)) {
		t := m.tools[name]
		if m.fromRepo(t) && !t.installed && !slices.Contains(repo.locked[name], t.version) {
			notes = append(notes, fmt.Sprintf("%s: %s@%s not offered: %s does not pin it for %s",
				RepoMiseConfig, name, t.version, RepoMiseLock, misePlatform()))
			continue
		}
		commands, err := m.commandsOf(ctx, t)
		if err != nil {
			return nil, nil, err
		}
		switch {
		case len(commands) == 0:
			notes = append(notes,
				fmt.Sprintf("mise tool %s not offered: it provides no command; set filter_bins or declare it by its registry name", name))
			continue
		case slices.Contains(commands, "mise"):
			notes = append(notes, fmt.Sprintf("mise tool %s not offered: it provides mise", name))
			continue
		}
		t.commands = commands
		for _, c := range commands {
			if other := m.byCommand[c]; other != nil {
				notes = append(notes, fmt.Sprintf("mise tool %s does not offer %s: %s offers it already", name, c, other.name))
				continue
			}
			// gh runs with the run's GitHub token, so a binary the
			// repository chose is not offered as it; one the image has
			// installed at the repository's version is the image's.
			if c == "gh" && m.fromRepo(t) && !t.installed {
				notes = append(notes, fmt.Sprintf("%s: %s does not offer gh: gh carries the run's GitHub token, "+
					"so only the image or the instance provides it", RepoMiseConfig, name))
				continue
			}
			m.byCommand[c] = t
		}
	}
	return m, notes, nil
}

// cappedWriter keeps the first max bytes written to it and counts the
// rest, so a noisy process cannot grow the runner's memory.
type cappedWriter struct {
	max   int
	buf   []byte
	total int
}

func (w *cappedWriter) Write(p []byte) (int, error) {
	if room := w.max - len(w.buf); room > 0 {
		w.buf = append(w.buf, p[:min(room, len(p))]...)
	}
	w.total += len(p)
	return len(p), nil
}

// full reports whether more was written than kept.
func (w *cappedWriter) full() bool { return w.total > w.max }

// run runs mise with args in the run's environment, within miseTimeout,
// and returns its standard output.
func (m *miseTools) run(ctx context.Context, args ...string) ([]byte, error) {
	cctx, cancel := context.WithTimeout(ctx, miseTimeout)
	defer cancel()
	stdout, stderr := &cappedWriter{max: miseOutputBytes}, &cappedWriter{max: installOutputBytes}
	if err := m.unlocked(ctx, func() error { return m.exec(cctx, stdout, stderr, args...) }); err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("runner: mise %s: %w", args[0], ctx.Err())
		}
		if cctx.Err() != nil {
			return nil, fmt.Errorf("runner: mise %s: stopped after %s", args[0], miseTimeout)
		}
		detail := m.secrets.Mask(textcut.Prefix(strings.TrimSpace(string(stderr.buf)), installOutputBytes))
		return nil, fmt.Errorf("runner: mise %s: %w: %s", args[0], err, detail)
	}
	if stdout.full() {
		return nil, fmt.Errorf("runner: mise %s: printed over %d bytes", args[0], miseOutputBytes)
	}
	return stdout.buf, nil
}

// unlocked runs f, a use of mise, with home writable. Once the run tool
// is built, home is read-only between the runner's own uses of it; an
// unlocked within another leaves the locking to the outer one, so an
// install and the reads after it walk home once. A home that cannot be
// made read-only again is broken: f's error carries that too, so the
// command it was for does not run, and no command runs after.
func (m *miseTools) unlocked(ctx context.Context, f func() error) (err error) {
	if m.broken != nil {
		return m.broken
	}
	if !m.locked || m.writable {
		return f()
	}
	if err := lockTree(ctx, m.home, false); err != nil {
		m.broken = fmt.Errorf("runner: home not made writable for mise: %w", err)
		_ = lockTree(ctx, m.home, true)
		return m.broken
	}
	m.writable = true
	defer func() {
		m.writable = false
		if lerr := lockTree(ctx, m.home, true); lerr != nil {
			m.broken = fmt.Errorf("runner: home not made read-only after mise: %w", lerr)
			m.logger.Warn("commands no longer run: home not made read-only after mise", "error", lerr)
			err = errors.Join(err, m.broken)
		}
	}()
	return f()
}

// exec runs mise with args in the run's environment, within unlocked.
func (m *miseTools) exec(ctx context.Context, stdout, stderr *cappedWriter, args ...string) error {
	cmd := exec.CommandContext(ctx, m.bin, args...)
	// mise reads the configs from its working directory up to the
	// ceiling; home holds the instance's, as the run's global config.
	cmd.Dir, cmd.Env, cmd.Stdout, cmd.Stderr = m.home, m.env, stdout, stderr
	// A child mise leaves holding the output open is not waited for once
	// mise is killed.
	cmd.WaitDelay = time.Second
	return cmd.Run()
}

// usable is why no command may run, nil while home is as it should be.
func (m *miseTools) usable() error {
	if m.broken != nil {
		return fmt.Errorf("commands are not run: %w", m.broken)
	}
	return nil
}

// lockTree takes the owner's write permission from everything under dir,
// dir included, or gives it back, until ctx ends. Symlinks are left
// alone: their targets are reached on their own.
func lockTree(ctx context.Context, dir string, lock bool) error {
	return filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.Type()&fs.ModeSymlink != 0 {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		mode := info.Mode().Perm()
		if lock {
			mode &^= 0o222
		} else {
			mode |= 0o200
		}
		return os.Chmod(path, mode)
	})
}

// miseLsEntry is one version of a tool in mise ls --json.
type miseLsEntry struct {
	Version     string `json:"version"`
	InstallPath string `json:"install_path"`
	Installed   bool   `json:"installed"`
	Source      struct {
		Path string `json:"path"`
	} `json:"source"`
}

// refresh reads the merged config's tools and the environment mise gives
// their commands, at the start and after each install.
func (m *miseTools) refresh(ctx context.Context) ([]string, error) {
	out, err := m.run(ctx, "ls", "--json", "--current")
	if err != nil {
		return nil, err
	}
	var ls map[string][]miseLsEntry
	if err := json.Unmarshal(out, &ls); err != nil {
		return nil, fmt.Errorf("runner: mise ls: %w", err)
	}
	for name, versions := range ls {
		if len(versions) == 0 {
			continue
		}
		v := versions[0]
		t := m.tools[name]
		if t == nil {
			t = &miseTool{name: name}
			m.tools[name] = t
		}
		t.version, t.installPath, t.installed, t.source = v.Version, v.InstallPath, v.Installed, resolved(v.Source.Path)
	}
	out, err = m.run(ctx, "env", "--json")
	if err != nil {
		return nil, err
	}
	var notes []string
	if m.path, m.extra, notes, err = parseMiseEnv(out); err != nil {
		return nil, err
	}
	// The values the config marks for redaction stay out of a command's
	// result and the review, as the run's tokens do.
	out, err = m.run(ctx, "env", "--json", "--redacted")
	if err != nil {
		return nil, err
	}
	var redacted map[string]string
	if err := json.Unmarshal(out, &redacted); err != nil {
		return nil, fmt.Errorf("runner: mise env --redacted: %w", err)
	}
	for _, name := range slices.Sorted(maps.Keys(redacted)) {
		m.secrets.Redacted.Add(redacted[name])
	}
	return notes, nil
}

// parseMiseEnv splits mise env --json into PATH's entries, nil when mise
// sets none, and the other variables it sets, less the reserved names,
// which the notes name.
func parseMiseEnv(out []byte) (path, extra, notes []string, err error) {
	var env map[string]string
	if err := json.Unmarshal(out, &env); err != nil {
		return nil, nil, nil, fmt.Errorf("runner: mise env: %w", err)
	}
	if p := env["PATH"]; p != "" {
		path = strings.Split(p, string(os.PathListSeparator))
	}
	for _, name := range slices.Sorted(maps.Keys(env)) {
		switch {
		case name == "PATH":
		case reservedEnvName(name):
			notes = append(notes, fmt.Sprintf("mise env %s not set for the commands: the name is reserved", name))
		default:
			extra = append(extra, name+"="+env[name])
		}
	}
	return path, extra, notes, nil
}

// registryEntry is one tool in mise registry --json.
type registryEntry struct {
	Short    string   `json:"short"`
	Backends []string `json:"backends"`
	Bins     []string `json:"bins"`
}

// registryEntries is mise's registry, read offline once, on the first
// call: a run whose tools are all installed never reads it.
func (m *miseTools) registryEntries(ctx context.Context) ([]registryEntry, error) {
	if m.registry != nil {
		return m.registry, nil
	}
	out, err := m.run(ctx, "registry", "--json")
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(out, &m.registry); err != nil {
		return nil, fmt.Errorf("runner: mise registry: %w", err)
	}
	return m.registry, nil
}

// registryBins are the commands the registry lists for key, a short name
// or a backend; nil when it lists none.
func registryBins(entries []registryEntry, key string) []string {
	for _, e := range entries {
		if e.Short == key || slices.Contains(e.Backends, key) {
			return e.Bins
		}
	}
	return nil
}

// miseToolInfo is what mise tool --json says of a tool: its backend and
// the options the config gives it.
type miseToolInfo struct {
	Backend string `json:"backend"`
	Options struct {
		FilterBins json.RawMessage `json:"filter_bins"`
	} `json:"tool_options"`
}

// filterBins is the tool's filter_bins option, a name or a list of them.
func (i miseToolInfo) filterBins() []string {
	var one string
	if json.Unmarshal(i.Options.FilterBins, &one) == nil && one != "" {
		return []string{one}
	}
	var many []string
	if json.Unmarshal(i.Options.FilterBins, &many) == nil {
		return many
	}
	return nil
}

// commandsOf are the commands t offers: an installed tool's executables,
// and for one still to install what its config says and the registry
// lists, as declaredCommands has them.
func (m *miseTools) commandsOf(ctx context.Context, t *miseTool) ([]string, error) {
	if t.installed {
		return executablesUnder(m.path, t.installPath), nil
	}
	if m.fromRepo(t) {
		if bins := m.repo.filterBins(t.name); len(bins) > 0 {
			return bins, nil
		}
	}
	out, err := m.run(ctx, "tool", t.name, "--json")
	if err != nil {
		return nil, err
	}
	var info miseToolInfo
	if err := json.Unmarshal(out, &info); err != nil {
		return nil, fmt.Errorf("runner: mise tool %s: %w", t.name, err)
	}
	registry, err := m.registryEntries(ctx)
	if err != nil {
		return nil, err
	}
	return declaredCommands(t.name, info, registry), nil
}

// declaredCommands are the commands a tool declared as name, with info,
// offers before it installs: its filter_bins, which narrow what it
// exposes, else what the registry lists under its name or backend, a
// github:owner/repo taking the entry listed as aqua:owner/repo, else its
// repository's name.
func declaredCommands(name string, info miseToolInfo, registry []registryEntry) []string {
	if bins := info.filterBins(); len(bins) > 0 {
		return bins
	}
	for _, key := range []string{name, info.Backend} {
		if bins := registryBins(registry, key); bins != nil {
			return bins
		}
	}
	if repo, ok := strings.CutPrefix(info.Backend, "github:"); ok {
		if bins := registryBins(registry, "aqua:"+repo); bins != nil {
			return bins
		}
	}
	if i := strings.LastIndex(info.Backend, "/"); i >= 0 {
		return []string{info.Backend[i+1:]}
	}
	return nil
}

// executablesUnder names the executables in the directories of path that
// lie under installPath, sorted.
func executablesUnder(path []string, installPath string) []string {
	var names []string
	for _, dir := range path {
		if !underDir(dir, installPath) {
			continue
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if isExecutable(filepath.Join(dir, e.Name())) {
				names = append(names, e.Name())
			}
		}
	}
	slices.Sort(names)
	return slices.Compact(names)
}

// findExecutable is the path of command among the directories of path
// under installPath, "" when none has it.
func findExecutable(path []string, installPath, command string) string {
	for _, dir := range path {
		if p := filepath.Join(dir, command); underDir(dir, installPath) && isExecutable(p) {
			return p
		}
	}
	return ""
}

func underDir(dir, parent string) bool {
	return dir == parent || strings.HasPrefix(dir, parent+string(os.PathSeparator))
}

func isExecutable(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular() && info.Mode()&0o111 != 0
}

// offered are the commands the tools provide. The map's values are
// empty: the binaries are resolved per call.
func (m *miseTools) offered() map[string]string {
	out := make(map[string]string, len(m.byCommand))
	for c := range m.byCommand {
		out[c] = ""
	}
	return out
}

// provides reports whether a mise tool offers command.
func (m *miseTools) provides(command string) bool { return m.byCommand[command] != nil }

// pending reports whether an offered tool is still to install.
func (m *miseTools) pending(offered map[string]string) bool {
	for c := range offered {
		if t := m.byCommand[c]; t != nil && !t.installed {
			return true
		}
	}
	return false
}

// commandEnv is the commands' environment: base with mise's PATH, when
// it set one, then the config's [env].
func (m *miseTools) commandEnv() []string {
	env := make([]string, 0, len(m.base)+len(m.extra))
	for _, kv := range m.base {
		if strings.HasPrefix(kv, "PATH=") && len(m.path) > 0 {
			kv = "PATH=" + strings.Join(m.path, string(os.PathListSeparator))
		}
		env = append(env, kv)
	}
	return append(env, m.extra...)
}

// resolve is the binary command runs and its environment, installing the
// command's tool first when it is not yet. Its error is for the agent.
func (m *miseTools) resolve(ctx context.Context, command string) (string, []string, error) {
	if err := m.usable(); err != nil {
		return "", nil, err
	}
	t := m.byCommand[command]
	if t == nil {
		return "", nil, fmt.Errorf("%s is not among the mise tools", command)
	}
	if !t.installed {
		if err := m.install(ctx, t); err != nil {
			return "", nil, err
		}
	}
	bin := findExecutable(m.path, t.installPath, command)
	if bin == "" {
		have := executablesUnder(m.path, t.installPath)
		return "", nil, fmt.Errorf("%s does not provide %s; it provides %s", t.name, command, strings.Join(have, ", "))
	}
	return bin, m.commandEnv(), nil
}

// install installs t with mise, which gets the run's GitHub token for
// its own process, so its GitHub API calls count against the App
// installation's limit rather than the gateway's one address, then
// reads the tools and environment again, all within installTimeout and
// with home unlocked once.
func (m *miseTools) install(ctx context.Context, t *miseTool) error {
	started := time.Now()
	ictx, cancel := context.WithTimeout(ctx, installTimeout)
	defer cancel()
	env := m.env
	m.env = append(slices.Clone(env), "MISE_GITHUB_TOKEN="+m.secrets.GitToken)
	defer func() { m.env = env }()
	// A repository's tool installs at the URL and checksum its lock pins.
	args := []string{"install", t.name}
	if m.fromRepo(t) {
		args = []string{"install", "--locked", t.name}
	}
	out := &cappedWriter{max: installOutputBytes}
	var installed bool
	err := m.unlocked(ctx, func() error {
		if err := m.exec(ictx, out, out, args...); err != nil {
			return err
		}
		installed = true
		_, err := m.refresh(ictx)
		return err
	})
	output := m.secrets.Mask(strings.TrimSpace(string(out.buf)))
	switch {
	case ctx.Err() != nil:
		return ctx.Err()
	case err != nil && installed:
		return err
	case err != nil && ictx.Err() != nil:
		m.logger.Warn("tool not installed", "tool", t.name, "version", t.version, "source", t.source, "error", err, "output", output)
		return fmt.Errorf("%s could not be installed: stopped after %s", t.name, installTimeout)
	case err != nil:
		m.logger.Warn("tool not installed", "tool", t.name, "version", t.version, "source", t.source, "error", err, "output", output)
		return fmt.Errorf("%s could not be installed: %s", t.name, output)
	}
	if !t.installed {
		return fmt.Errorf("%s is not installed after its install: %s", t.name, output)
	}
	m.logger.Info("tool installed", "tool", t.name, "version", t.version, "source", t.source, "path", t.installPath,
		"elapsed", time.Since(started).Round(time.Millisecond), "output", output)
	return nil
}
