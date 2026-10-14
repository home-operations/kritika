package configfile

import (
	"fmt"
	"os"
	"slices"
	"strings"
)

func checkPolicyEnv(referenced []string) error {
	var retired []string
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if name == "KRITIKA_REVIEW_WORKERS" || slices.Contains(referenced, name) {
			continue
		}
		for _, prefix := range []string{
			"KRITIKA_AUTH_", "KRITIKA_APPS_", "KRITIKA_PROVIDERS_", "KRITIKA_REVIEW_",
			"KRITIKA_CONFIDENCE_", "KRITIKA_TRIGGER_", "KRITIKA_EMBEDDING_",
		} {
			if strings.HasPrefix(name, prefix) {
				retired = append(retired, name)
				break
			}
		}
	}
	if len(retired) == 0 {
		return nil
	}
	slices.Sort(retired)
	return fmt.Errorf("configfile: unsupported policy environment variables: %s; "+
		"move declarations and policy to the configuration file, and reference credentials explicitly with {env: NAME}",
		strings.Join(retired, ", "))
}
