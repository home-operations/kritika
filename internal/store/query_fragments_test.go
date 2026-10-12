package store

import (
	"regexp"
	"testing"
)

func TestSharedQueryFragmentsHaveNoParameters(t *testing.T) {
	placeholder := regexp.MustCompile(`\$[0-9]+|@[A-Za-z_]`)
	for _, tt := range []struct {
		name string
		sql  string
	}{
		{"pull columns", pullColumns},
		{"finding issues", findingIssues},
		{"account findings", accountFindings},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if match := placeholder.FindString(tt.sql); match != "" {
				t.Fatalf("shared SQL contains placeholder %q; named and positional bindings must not be mixed", match)
			}
		})
	}
}
