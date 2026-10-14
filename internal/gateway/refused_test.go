package gateway

import (
	"strings"
	"testing"
)

func TestRefusedHost(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"Registry.Example.COM:443", "registry.example.com"},
		{"docs.example.com.", "docs.example.com"},
		{"[2001:db8::1]:443", "2001:db8::1"},
		{"10.0.0.5", "10.0.0.5"},
		{"bad\x00host:80", "badhost"},
		{"caf\xc3\xa9.example:80", "café.example"},
		{strings.Repeat("a", 300) + ":80", strings.Repeat("a", refusedHostBytes)},
	} {
		if got := refusedHost(tc.in); got != tc.want {
			t.Errorf("refusedHost(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	if got := refusedHost("x\xffy:80"); !strings.Contains(got, "�") || strings.Contains(got, "\xff") {
		t.Errorf("invalid UTF-8 kept: %q", got)
	}
}
