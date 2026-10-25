package egresspolicy

import (
	"net/netip"
	"testing"
)

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
