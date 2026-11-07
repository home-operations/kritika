package egresspolicy

import (
	"net/netip"
	"testing"
)

func TestCredential(t *testing.T) {
	for _, tt := range []struct {
		name         string
		cred         Credential
		header       string
		for1, forAny bool
	}{
		{"bearer for every account", Credential{Scheme: SchemeBearer, Secret: "s", Accounts: []string{AllAccounts}}, "Bearer s", true, true},
		{"basic for one account", Credential{Scheme: SchemeBasic, Username: "bot", Secret: "pw", Accounts: []string{"a1"}}, "Basic Ym90OnB3", true, false},
		{"oci for another account", Credential{Scheme: SchemeOCI, Username: "bot", Secret: "pw", Accounts: []string{"a2"}}, "", false, false},
		{"no accounts", Credential{Scheme: SchemeBearer, Secret: "s"}, "Bearer s", false, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.cred.Header(); got != tt.header {
				t.Errorf("Header() = %q, want %q", got, tt.header)
			}
			if got := tt.cred.For("a1"); got != tt.for1 {
				t.Errorf("For(a1) = %v, want %v", got, tt.for1)
			}
			if got := tt.cred.For(""); got != tt.forAny {
				t.Errorf("For(\"\") = %v, want %v", got, tt.forAny)
			}
		})
	}
	if got := (Credential{Scheme: SchemeOCI, Username: "bot", Secret: "pw"}).Basic(); got != "Ym90OnB3" {
		t.Errorf("Basic() = %q", got)
	}
	if got := (Credential{Scheme: SchemeBearer, Secret: "pw"}).Basic(); got != "" {
		t.Errorf("Basic() of a bearer = %q", got)
	}
}

func prefixes(ps ...string) []netip.Prefix {
	out := make([]netip.Prefix, 0, len(ps))
	for _, p := range ps {
		out = append(out, netip.MustParsePrefix(p))
	}
	return out
}

func TestListContainsEmbeddedIPv4(t *testing.T) {
	l := List{Prefixes: prefixes("203.0.113.0/24", "2002::/16")}
	for addr, want := range map[string]bool{
		"203.0.113.7": true, "::ffff:203.0.113.7": true, "64:ff9b::cb00:7107": true, "2002:cb00:7107::": true,
		"2002:0a00:0001::": true, "64:ff9b::a00:1": false, "198.51.100.1": false,
	} {
		if got := l.contains(netip.MustParseAddr(addr)); got != want {
			t.Errorf("contains(%s) = %v, want %v", addr, got, want)
		}
	}
}

func TestPublic(t *testing.T) {
	for addr, want := range map[string]bool{
		"140.82.112.6": true, "2606:4700::6810:84e5": true, "203.0.113.9": true,
		"10.1.2.3": false, "172.16.0.1": false, "172.32.0.1": true, "192.168.0.1": false, "100.64.0.1": false, "100.128.0.1": true,
		"127.0.0.1": false, "169.254.169.254": false, "0.0.0.0": false, "224.0.0.1": false, "255.255.255.255": false, "198.18.0.1": false,
		"168.63.129.16": false, "192.0.0.192": false,
		"::1": false, "::": false, "fd00::1": false, "fe80::1": false, "ff02::1": false,
		"::ffff:10.0.0.1": false, "::ffff:140.82.112.6": true, "64:ff9b::a00:1": false, "64:ff9b::8c52:7006": true, "2002:a00:1::": false, "2002:8c52:7006::": true,
	} {
		if got := public(netip.MustParseAddr(addr)); got != want {
			t.Errorf("public(%s) = %v, want %v", addr, got, want)
		}
	}
}
