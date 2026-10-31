package repoconfig

import (
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"

	"go.yaml.in/yaml/v4"
)

// Tools are the tools a repository adds for its reviews, by name, each at
// the version to run: packages of the aqua registry, named
// ToolPrefix<package>, at a release's tag. The runner installs each on
// first use. The prefix leaves room for another source of tools.
type Tools map[string]string

// ToolPrefix starts the name of a tool from the aqua registry.
const ToolPrefix = "aqua:"

// UnmarshalYAML decodes each tool's version, a string.
func (t *Tools) UnmarshalYAML(node *yaml.Node) error {
	var raw map[string]yaml.Node
	if err := node.Decode(&raw); err != nil {
		return err
	}
	*t = make(Tools, len(raw))
	for name, v := range raw {
		if v.Kind == yaml.AliasNode && v.Alias != nil {
			v = *v.Alias
		}
		if v.Kind != yaml.ScalarNode {
			return fmt.Errorf("tools: %s: the version is a release's tag, not a table or a list", name)
		}
		version := v.Value
		if v.Tag == "!!null" {
			version = ""
		}
		(*t)[name] = version
	}
	return nil
}

// toolPackage is what an aqua registry package's name can be: its
// repository's owner and name, with a path below them for one of several
// tools that a repository releases.
var toolPackage = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._~-]*(/[A-Za-z0-9~][A-Za-z0-9._~-]*)+$`)

// toolVersion is what a version must look like to name one release: a
// tag, with a digit in it. A name without one, latest or stable, is what
// a download URL that takes the version reads as whatever is newest.
var toolVersion = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+/-]*$`)

// checkTool is why version, the version of the tool declared as name, cannot
// be run, or nil when it can.
func checkTool(name, version string) error {
	pkg, ok := strings.CutPrefix(name, ToolPrefix)
	switch {
	case !ok:
		return fmt.Errorf("tools: %s: only aqua registry packages are offered, named %s<owner>/<repo>", name, ToolPrefix)
	case !toolPackage.MatchString(pkg) || strings.Contains(pkg, ".."):
		return fmt.Errorf("tools: %s: %q is not an aqua registry package's name", name, pkg)
	case version == "":
		return fmt.Errorf("tools: %s: a version is required", name)
	case !toolVersion.MatchString(version) || strings.Contains(version, "..") || strings.Contains(version, "//") ||
		!strings.ContainsAny(version, "0123456789"):
		return fmt.Errorf("tools: %s: version %q is not pinned; give the tag of the release to run", name, version)
	}
	return nil
}

// check is why one of the tools cannot be run, or nil when all can.
func (t Tools) check() error {
	for _, name := range slices.Sorted(maps.Keys(t)) {
		if err := checkTool(name, t[name]); err != nil {
			return err
		}
	}
	return nil
}
