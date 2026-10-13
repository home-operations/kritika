package configfile

import (
	"fmt"
	"os"
	"strings"

	"go.yaml.in/yaml/v4"
)

func checkOverlaySources(raw []byte, environ []string) error {
	var fields map[string]any
	if err := yaml.Unmarshal(raw, &fields); err != nil {
		return fmt.Errorf("configfile: %w", err)
	}
	for _, kv := range environ {
		name, _, _ := strings.Cut(kv, "=")
		for _, prefix := range overlayPrefixes {
			if name == reviewWorkersEnv || !strings.HasPrefix(name, prefix) {
				continue
			}
			var path string
			switch prefix {
			case connectionEnvPrefix, providerEnvPrefix:
				section, fallback := "apps", DefaultEnvConnection
				if prefix == providerEnvPrefix {
					section, fallback = "providers", DefaultEnvProvider
				}
				key, ok := os.LookupEnv(prefix + "NAME")
				if !ok {
					key = fallback
				}
				entries, _ := fields[section].(map[string]any)
				if _, exists := entries[key]; exists {
					path = section + "." + key
				}
			default:
				target := strings.ToLower(strings.ReplaceAll(strings.TrimPrefix(name, "KRITIKA_"), "_", ""))
				path = matchingPath(fields, "", target)
			}
			if path != "" {
				return fmt.Errorf("configfile: %s conflicts with %s: remove the environment variable and keep the file setting", path, name)
			}
		}
	}
	return nil
}

func matchingPath(fields map[string]any, prefix, target string) string {
	for key, value := range fields {
		path := prefix + key
		if strings.ToLower(strings.ReplaceAll(path, ".", "")) == target {
			return path
		}
		if nested, ok := value.(map[string]any); ok {
			if found := matchingPath(nested, path+".", target); found != "" {
				return found
			}
		}
	}
	return ""
}
