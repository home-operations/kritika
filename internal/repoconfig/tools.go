package repoconfig

import (
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"

	"github.com/pelletier/go-toml/v2"
	"go.yaml.in/yaml/v4"
)

// Tools are the tools a repository adds for its reviews, as mise's
// [tools] table: each entry a version, or a table of the version and the
// options that select, verify and extract the tool's download. The
// runner writes them as the run's mise config over the image's, and
// installs each on first use.
type Tools map[string]Tool

// Tool is one entry of Tools: its version, and for the table form its
// other options, among toolOptions.
type Tool struct {
	Version string
	Options map[string]any
}

// UnmarshalYAML decodes a version, or a table with one.
func (t *Tool) UnmarshalYAML(node *yaml.Node) error {
	switch node.Kind {
	case yaml.ScalarNode:
		if node.Tag == "!!null" {
			return errors.New("a version is required")
		}
		return node.Decode(&t.Version)
	case yaml.MappingNode:
		var options map[string]any
		if err := node.Decode(&options); err != nil {
			return err
		}
		version, ok := options[versionOption].(string)
		if _, had := options[versionOption]; !had || !ok {
			return errors.New("a version string is required")
		}
		delete(options, versionOption)
		t.Version, t.Options = version, options
		return nil
	}
	return errors.New("a version or a table with one is expected")
}

// The options a Tool is read for by name.
const (
	versionOption   = "version"
	platformsOption = "platforms"
	urlOption       = "url"
)

// optionKind is what values an option takes.
type optionKind int

const (
	stringOption optionKind = iota
	stringOrListOption
	boolOption
	intOption
)

// toolOptions are the options a Tool may take, with what each takes: the
// version, and what selects, verifies and extracts its download. The
// options that run a command (postinstall, depends) or set its
// environment (install_env), that send mise's token to another host
// (api_url, headers) and that discover versions are not among them: an
// install must only download and unpack. Each is typed as mise reads it,
// since mise refuses its whole config, the image's tools with it, over
// one option of the wrong type.
var toolOptions = map[string]optionKind{
	"os": stringOrListOption, "filter_bins": stringOrListOption,
	"asset_pattern": stringOption, "additional_asset_patterns": stringOrListOption, "matching": stringOption,
	"matching_regex": stringOption, "no_app": boolOption, "version_prefix": stringOption,
	urlOption: stringOption, "format": stringOption, "strip_components": intOption, "bin_path": stringOption,
	"bin": stringOrListOption, "rename_exe": stringOption, "exe": stringOption, "extract_all": boolOption,
	"symlink_bins": boolOption, "checksum": stringOption, "size": intOption,
}

// DownloadBackends are the mise backends whose install downloads and
// unpacks a release and runs nothing of the tool's. A backend that
// builds (cargo:, go:), runs a package manager (npm:, pipx:, gem:,
// dotnet:, spm:, conda:) or a plugin's scripts (asdf:, vfox:) is not
// offered, whether named outright or resolved from a registry short name.
var DownloadBackends = []string{"aqua", "core", "github", "gitlab", "http", "ubi"}

// DownloadBackend reports whether backend, as mise names a tool's, is one
// of DownloadBackends.
func DownloadBackend(backend string) bool {
	name, _, _ := strings.Cut(backend, ":")
	return slices.Contains(DownloadBackends, name)
}

// toolName is what a tool's name may be: a registry short name, or a
// backend and its package. mise reads options from a name's [...] suffix,
// which the allowlist of options must not be bypassed by, and a request
// from its @ suffix.
var toolName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]*(:[A-Za-z0-9][A-Za-z0-9._/+-]*)?$`)

// pinnedVersion is what a version must look like to name one release: a
// number, after an optional v. Every request mise resolves anew is
// something else: latest, system, lts, lts/iron and lts-iron, prefix:4,
// sub-1:latest, path:/x, and ref:, tag:, branch: and rev: with their
// hyphenated spellings, which mise reads the same way.
var pinnedVersion = regexp.MustCompile(`^v?[0-9][A-Za-z0-9._+-]*$`)

// check is why t, declared as name, may not be run, nil when it may.
func (t Tool) check(name string) error {
	if !toolName.MatchString(name) {
		return fmt.Errorf("tools: %q is not a tool name: a registry short name, or a backend and its package, without options", name)
	}
	if backend, _, ok := strings.Cut(name, ":"); ok {
		switch {
		// filter_bins is how a tool declared by its backend names its
		// commands, and mise refuses it on the http backend. A short name
		// mise resolves to http is offered: the registry names its commands.
		case backend == "http":
			return fmt.Errorf("tools: %s: the http backend is not offered by name: it cannot name the tool's commands; "+
				"declare the tool by its registry short name or as github:owner/repo", name)
		case !slices.Contains(DownloadBackends, backend):
			return fmt.Errorf("tools: %s: the %s backend is not offered: an install may only download and unpack", name, backend)
		}
	}
	switch v := t.Version; {
	case templated(v):
		return fmt.Errorf("tools: %s: the version holds a template, which is not rendered", name)
	case v == "":
		return fmt.Errorf("tools: %s: a version is required", name)
	case !pinnedVersion.MatchString(v):
		return fmt.Errorf("tools: %s: version %q is not pinned; give the number of the version to run", name, v)
	}
	for _, option := range slices.Sorted(maps.Keys(t.Options)) {
		value := t.Options[option]
		if option == platformsOption {
			platforms, ok := value.(map[string]any)
			if !ok {
				return fmt.Errorf("tools: %s: platforms is not a table", name)
			}
			for _, platform := range slices.Sorted(maps.Keys(platforms)) {
				options, ok := platforms[platform].(map[string]any)
				if !ok {
					return fmt.Errorf("tools: %s: platform %s is not a table", name, platform)
				}
				for _, o := range slices.Sorted(maps.Keys(options)) {
					if err := checkOption(o, options[o]); err != nil {
						return fmt.Errorf("tools: %s: platform %s: %w", name, platform, err)
					}
				}
			}
			continue
		}
		if err := checkOption(option, value); err != nil {
			return fmt.Errorf("tools: %s: %w", name, err)
		}
	}
	return nil
}

// checkOption is why v is no value option takes: one not among
// toolOptions, a value of another type than the option's, one holding a
// template, or a URL that is not https://, since a path or a plain HTTP
// URL would have the install take what a command could write or an
// upstream could not have served.
func checkOption(option string, v any) error {
	kind, ok := toolOptions[option]
	if !ok {
		return fmt.Errorf("option %s is not one of a download's", option)
	}
	switch kind {
	case boolOption:
		if _, ok := v.(bool); !ok {
			return fmt.Errorf("option %s is not a boolean", option)
		}
	case intOption:
		n, ok := v.(int)
		if !ok || n < 0 {
			return fmt.Errorf("option %s is not a whole number", option)
		}
	case stringOption, stringOrListOption:
		var values []string
		switch v := v.(type) {
		case string:
			values = []string{v}
		case []any:
			if kind != stringOrListOption {
				return fmt.Errorf("option %s is not a string", option)
			}
			for _, item := range v {
				s, ok := item.(string)
				if !ok {
					return fmt.Errorf("option %s is not a list of strings", option)
				}
				values = append(values, s)
			}
		default:
			if kind == stringOrListOption {
				return fmt.Errorf("option %s is not a string or a list of strings", option)
			}
			return fmt.Errorf("option %s is not a string", option)
		}
		if slices.ContainsFunc(values, templated) {
			return fmt.Errorf("option %s holds a template, which is not rendered", option)
		}
		if option == urlOption && !strings.HasPrefix(v.(string), "https://") {
			return fmt.Errorf("option %s is not an https:// URL", option)
		}
	}
	return nil
}

// templated reports whether mise would render s as a template.
func templated(s string) bool {
	return strings.Contains(s, "{{") || strings.Contains(s, "{%") || strings.Contains(s, "{#")
}

// check is why one of the tools may not be run, nil when all may.
func (t Tools) check() error {
	for _, name := range slices.Sorted(maps.Keys(t)) {
		if err := t[name].check(name); err != nil {
			return err
		}
	}
	return nil
}

// TOML is the tools as a mise config: its [tools] table, "" when there
// are none.
func (t Tools) TOML() (string, error) {
	if len(t) == 0 {
		return "", nil
	}
	tools := make(map[string]any, len(t))
	for name, tool := range t {
		if len(tool.Options) == 0 {
			tools[name] = tool.Version
			continue
		}
		table := maps.Clone(tool.Options)
		table[versionOption] = tool.Version
		tools[name] = table
	}
	out, err := toml.Marshal(map[string]any{"tools": tools})
	if err != nil {
		return "", fmt.Errorf("repoconfig: tools: %w", err)
	}
	return string(out), nil
}
