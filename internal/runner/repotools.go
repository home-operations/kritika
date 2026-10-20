package runner

import (
	"fmt"
	"runtime"
	"slices"

	"github.com/pelletier/go-toml/v2"
)

// repoMiseConfig is the repository's mise config: the [tools] table its
// .kritika.yaml declares, read from the merge base as the rest of the
// file is, so a pull request cannot change which tools its own review
// runs, rendered by the worker and carried by the spec.
type repoMiseConfig struct {
	config string
	tools  map[string]any
}

// newRepoMiseConfig reads doc, the spec's rendered config; nil when it
// declares nothing.
func newRepoMiseConfig(doc string) (*repoMiseConfig, error) {
	if doc == "" {
		return nil, nil
	}
	var raw struct {
		Tools map[string]any `toml:"tools"`
	}
	if err := toml.Unmarshal([]byte(doc), &raw); err != nil {
		return nil, fmt.Errorf("runner: repository tools: %w", err)
	}
	return &repoMiseConfig{config: doc, tools: raw.Tools}, nil
}

// filterBins reads the declared options because mise tool --json can
// omit them for a tool still to install.
func (r *repoMiseConfig) filterBins(name string) []string {
	options, _ := r.tools[name].(map[string]any)
	return binsOf(options["filter_bins"])
}

// binsOf is a filter_bins value as a decoded TOML or JSON document holds
// it, a name or a list of them, as a list; nil for none.
func binsOf(v any) []string {
	switch bins := v.(type) {
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
// and checksum under it, which is what a locked install needs; urls are
// those URLs, sorted. A version's platform entry is the table under the
// quoted key "platforms.<platform>".
func lockedTools(lock, platform string) (versionsByTool map[string][]string, urls []string, err error) {
	var raw struct {
		Tools map[string][]map[string]any `toml:"tools"`
	}
	if err := toml.Unmarshal([]byte(lock), &raw); err != nil {
		return nil, nil, fmt.Errorf("runner: mise lock: %w", err)
	}
	versionsByTool = map[string][]string{}
	for name, versions := range raw.Tools {
		for _, v := range versions {
			entry, _ := v["platforms."+platform].(map[string]any)
			version, _ := v["version"].(string)
			url, _ := entry["url"].(string)
			checksum, _ := entry["checksum"].(string)
			if version != "" && url != "" && checksum != "" {
				versionsByTool[name] = append(versionsByTool[name], version)
				urls = append(urls, url)
			}
		}
	}
	slices.Sort(urls)
	return versionsByTool, slices.Compact(urls), nil
}
