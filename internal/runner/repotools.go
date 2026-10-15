package runner

import (
	"fmt"
	"log/slog"
	"maps"
	"runtime"
	"slices"
	"strings"

	"github.com/go-git/go-git/v6/plumbing/object"
	"github.com/pelletier/go-toml/v2"

	"github.com/home-operations/kritika/internal/repoconfig"
)

// A repository declares the tools its reviews may use in RepoMiseConfig,
// pinned by RepoMiseLock, both read from the merge base as .kritika.yaml
// is, so a pull request cannot change which tools its own review runs or
// which binaries it downloads.
const (
	RepoMiseConfig = ".kritika/mise.toml"
	RepoMiseLock   = ".kritika/mise.lock"
)

// RepositoryTools reports whether commands, an agent.commands value, lets
// a repository's own mise config supply tools: the one value "*".
func RepositoryTools(commands []string) bool { return len(commands) == 1 && commands[0] == "*" }

// repoMiseConfig is what the runner keeps of a repository's mise config:
// the config, as mise reads it, and its lock, as the repository wrote it.
type repoMiseConfig struct {
	config, lock string
	tools        map[string]any
	// locked names the tool versions the lock pins for the runner's platform.
	locked map[string][]string
}

// disabledRepoTools checks only the tree entry: without the admin's
// permission, neither the config's contents nor its lock are loaded.
func disabledRepoTools(base *object.Tree, commands []string) []string {
	if RepositoryTools(commands) {
		return nil
	}
	if entry, err := base.FindEntry(RepoMiseConfig); err == nil && entry.Mode.IsFile() {
		return []string{RepoMiseConfig + " not enabled: the admin must set agent.commands to [\"*\"]"}
	}
	return nil
}

func readRepoMise(files repoconfig.Files, logger *slog.Logger) (*repoMiseConfig, []string) {
	repo, notes, err := keptRepoMiseConfig(files)
	if err != nil {
		logger.Warn("repository mise config not read", "error", err)
		notes = append(notes, fmt.Sprintf("%s not read: %s", RepoMiseConfig, err))
	}
	return repo, notes
}

// keptRepoMiseConfig is the part of a repository's mise config the runner
// gives mise: [tools], each with the options in toolOptions, and [env] as
// literal strings under names that are not reserved. Templates, which
// read the environment and files and run commands, and the _. directives,
// which load files, are dropped, as are [settings], [tasks] and hooks, so
// only the admin sets mise's settings; the notes say what was dropped.
// nil when files has no config.
func keptRepoMiseConfig(files repoconfig.Files) (*repoMiseConfig, []string, error) {
	doc, ok := files[RepoMiseConfig]
	if !ok {
		return nil, nil, nil
	}
	var raw map[string]any
	if err := toml.Unmarshal([]byte(doc), &raw); err != nil {
		return nil, nil, fmt.Errorf("runner: %s: %w", RepoMiseConfig, err)
	}
	var notes []string
	kept := map[string]any{}
	if tools, ok := raw["tools"].(map[string]any); ok {
		keptTools := map[string]any{}
		for _, name := range slices.Sorted(maps.Keys(tools)) {
			tool, toolNotes := keptTool("tool "+name, tools[name])
			notes = append(notes, toolNotes...)
			if tool != nil {
				keptTools[name] = tool
			}
		}
		if len(keptTools) > 0 {
			kept["tools"] = keptTools
		}
	}
	if env, ok := raw["env"].(map[string]any); ok {
		literal := map[string]any{}
		for _, name := range slices.Sorted(maps.Keys(env)) {
			value, isString := env[name].(string)
			switch {
			case name == "_" || strings.HasPrefix(name, "_."):
				notes = append(notes, fmt.Sprintf("%s: env %s dropped: directives are not read", RepoMiseConfig, name))
			case reservedEnvName(name):
				notes = append(notes, fmt.Sprintf("%s: env %s dropped: the name is reserved", RepoMiseConfig, name))
			case !isString:
				notes = append(notes, fmt.Sprintf("%s: env %s dropped: only a plain string is kept", RepoMiseConfig, name))
			case templated(value):
				notes = append(notes, fmt.Sprintf("%s: env %s dropped: templates are not rendered", RepoMiseConfig, name))
			default:
				literal[name] = value
			}
		}
		if len(literal) > 0 {
			kept["env"] = literal
		}
	}
	for _, section := range []string{"settings", "tasks", "hooks"} {
		if _, ok := raw[section]; ok {
			notes = append(notes, fmt.Sprintf("%s: [%s] dropped: only the admin sets it", RepoMiseConfig, section))
		}
	}
	out, err := toml.Marshal(kept)
	if err != nil {
		return nil, nil, fmt.Errorf("runner: %s: %w", RepoMiseConfig, err)
	}
	tools, _ := kept["tools"].(map[string]any)
	r := &repoMiseConfig{config: string(out), lock: files[RepoMiseLock], tools: tools}
	if r.lock != "" {
		if r.locked, err = lockedTools(r.lock, misePlatform()); err != nil {
			return nil, nil, err
		}
	}
	return r, notes, nil
}

// toolOptions are the options of a [tools] entry the runner keeps: the
// version, and what selects, verifies and extracts its download. The
// options that run a command (postinstall, depends) or set its
// environment (install_env), that send mise's token to another host
// (api_url, headers) and that discover versions are dropped: the lock
// pins the version, and an install must only download and unpack.
var toolOptions = []string{
	"version", "os", "filter_bins",
	"asset_pattern", "additional_asset_patterns", "matching", "matching_regex", "no_app", "version_prefix",
	"url", "format", "strip_components", "bin_path", "bin", "rename_exe", "exe", "extract_all", "symlink_bins",
	"checksum", "size", "platforms",
}

// keptTool is tool as the runner keeps it: a version, a table of its
// options or a list of either, nil when nothing of it is kept; at names
// it in the notes.
func keptTool(at string, tool any) (any, []string) {
	switch tool := tool.(type) {
	case string:
		if templated(tool) {
			return nil, []string{fmt.Sprintf("%s: %s dropped: templates are not rendered", RepoMiseConfig, at)}
		}
		return tool, nil
	case []any:
		var kept []any
		var notes []string
		for _, v := range tool {
			k, n := keptTool(at, v)
			notes = append(notes, n...)
			if k != nil {
				kept = append(kept, k)
			}
		}
		if len(kept) == 0 {
			return nil, notes
		}
		return kept, notes
	case map[string]any:
		kept, notes := keptToolOptions(at, tool)
		// A table that lost its version would select the latest.
		if _, had := tool["version"]; had && kept["version"] == nil {
			return nil, notes
		}
		return kept, notes
	}
	return nil, []string{fmt.Sprintf("%s: %s dropped: it is not a version or a table", RepoMiseConfig, at)}
}

// keptToolOptions are the options among options, a tool's or one
// platform's under its platforms table, that toolOptions lists and that
// hold no template.
func keptToolOptions(at string, options map[string]any) (map[string]any, []string) {
	kept := map[string]any{}
	var notes []string
	for _, name := range slices.Sorted(maps.Keys(options)) {
		value := options[name]
		switch {
		case !slices.Contains(toolOptions, name):
			notes = append(notes, fmt.Sprintf("%s: %s option %s dropped: only the download's options are kept", RepoMiseConfig, at, name))
		case name == "platforms":
			platforms, ok := value.(map[string]any)
			if !ok {
				notes = append(notes, fmt.Sprintf("%s: %s option %s dropped: it is not a table", RepoMiseConfig, at, name))
				continue
			}
			keptPlatforms := map[string]any{}
			for _, platform := range slices.Sorted(maps.Keys(platforms)) {
				table, ok := platforms[platform].(map[string]any)
				if !ok {
					notes = append(notes, fmt.Sprintf("%s: %s platform %s dropped: it is not a table", RepoMiseConfig, at, platform))
					continue
				}
				k, n := keptToolOptions(at+" platform "+platform, table)
				notes = append(notes, n...)
				keptPlatforms[platform] = k
			}
			kept[name] = keptPlatforms
		case templatedValue(value):
			notes = append(notes, fmt.Sprintf("%s: %s option %s dropped: templates are not rendered", RepoMiseConfig, at, name))
		default:
			kept[name] = value
		}
	}
	return kept, notes
}

// templated reports whether mise would render s as a template.
func templated(s string) bool {
	return strings.Contains(s, "{{") || strings.Contains(s, "{%") || strings.Contains(s, "{#")
}

// templatedValue reports whether a string anywhere in v is templated.
func templatedValue(v any) bool {
	switch v := v.(type) {
	case string:
		return templated(v)
	case []any:
		return slices.ContainsFunc(v, templatedValue)
	case map[string]any:
		return slices.ContainsFunc(slices.Collect(maps.Values(v)), templatedValue)
	}
	return false
}

// filterBins reads the declared options because mise tool --json can
// omit them when a lockfile supplies the backend.
func (r *repoMiseConfig) filterBins(name string) []string {
	tool := r.tools[name]
	if versions, ok := tool.([]any); ok && len(versions) > 0 {
		tool = versions[0]
	}
	options, _ := tool.(map[string]any)
	switch bins := options["filter_bins"].(type) {
	case string:
		if bins != "" {
			return []string{bins}
		}
	case []any:
		var names []string
		for _, bin := range bins {
			if name, ok := bin.(string); ok {
				names = append(names, name)
			}
		}
		return names
	}
	return nil
}

// misePlatform is this runner's platform as a mise lock names it.
func misePlatform() string {
	arch := runtime.GOARCH
	if arch == "amd64" {
		arch = "x64"
	}
	os := runtime.GOOS
	if os == "darwin" {
		os = "macos"
	}
	return os + "-" + arch
}

// lockedTools names the tools lock pins for platform: those with a URL
// and checksum under it, which is what a locked install needs. A
// version's platform entry is the table under the quoted key
// "platforms.<platform>".
func lockedTools(lock, platform string) (map[string][]string, error) {
	var raw struct {
		Tools map[string][]map[string]any `toml:"tools"`
	}
	if err := toml.Unmarshal([]byte(lock), &raw); err != nil {
		return nil, fmt.Errorf("runner: %s: %w", RepoMiseLock, err)
	}
	versionsByTool := map[string][]string{}
	for name, versions := range raw.Tools {
		for _, v := range versions {
			entry, _ := v["platforms."+platform].(map[string]any)
			version, _ := v["version"].(string)
			url, _ := entry["url"].(string)
			checksum, _ := entry["checksum"].(string)
			if version != "" && url != "" && checksum != "" {
				versionsByTool[name] = append(versionsByTool[name], version)
			}
		}
	}
	return versionsByTool, nil
}
