package egress

import (
	"net/netip"
	"strings"
)

// Rules is what the proxy allows, refuses and adds.
type Rules struct {
	// Allow are the destinations a runner may reach and Deny those it may
	// not, even when Allow matches them. The port is not part of a rule:
	// CONNECT is allowed to 443 only, and an upgraded request always goes
	// to 443.
	Allow, Deny List
	// Credentials map a host to the Authorization header value an
	// upgraded request to it carries.
	Credentials map[string]string
}

// List is one side of the rules.
type List struct {
	// Hosts are lowercase: an exact host, a suffix with a leading "*.", or
	// "*" alone for every host. "*" says nothing about addresses.
	Hosts []string
	// Prefixes are addresses, one address alone as a /32 or /128. A
	// destination given as an address is reached only when an Allow prefix
	// covers it, and a name's resolved address that is not public must be
	// covered too.
	Prefixes []netip.Prefix
}

func (l List) matchesHost(host string) bool {
	for _, rule := range l.Hosts {
		switch {
		case rule == "*":
			return true
		case strings.HasPrefix(rule, "*."):
			if strings.HasSuffix(host, rule[1:]) && len(host) > len(rule)-1 {
				return true
			}
		case host == rule:
			return true
		}
	}
	return false
}

// contains reports whether a prefix covers addr, or the IPv4 address it
// embeds, so a rule written for an IPv4 destination holds when a DNS64
// resolver or 6to4 hands the destination over as IPv6.
func (l List) contains(addr netip.Addr) bool {
	v4, embeds := embedded(addr)
	for _, p := range l.Prefixes {
		if p.Contains(addr) || embeds && p.Contains(v4) {
			return true
		}
	}
	return false
}

// Allows reports whether host, a name without a port, matches an Allow
// entry and no Deny entry.
func (r Rules) Allows(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	return host != "" && !r.Deny.matchesHost(host) && r.Allow.matchesHost(host)
}

// Reasons a destination is refused for, as the runner sees them.
const (
	reasonHostNotAllowed    = "host not allowed"
	reasonHostDenied        = "host denied"
	reasonAddressNotAllowed = "address not allowed"
	reasonAddressDenied     = "address denied"
	reasonResolvesPrivate   = "resolves to a private address no allow entry covers"
	reasonResolvesDenied    = "resolves to a denied address"
)

// refuses returns why the rules refuse a destination before it is
// resolved, or "" when they admit it so far: a name must pass the host
// rules, and an address must be covered by an Allow prefix.
func (r Rules) refuses(host string) string {
	if addr, err := netip.ParseAddr(host); err == nil {
		switch addr = addr.Unmap(); {
		case r.Deny.contains(addr):
			return reasonAddressDenied
		case !r.Allow.contains(addr):
			return reasonAddressNotAllowed
		}
		return ""
	}
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	switch {
	case r.Deny.matchesHost(host):
		return reasonHostDenied
	case host == "" || !r.Allow.matchesHost(host):
		return reasonHostNotAllowed
	}
	return ""
}

// admit returns the resolved addresses of a name the rules let the proxy
// dial, or why none may be: a denied address never, a private one only
// when an Allow prefix covers it.
func (r Rules) admit(addrs []netip.Addr) ([]netip.Addr, string) {
	var ok []netip.Addr
	denied := false
	for _, a := range addrs {
		a = a.Unmap()
		switch {
		case r.Deny.contains(a):
			denied = true
		case !public(a) && !r.Allow.contains(a):
		default:
			ok = append(ok, a)
		}
	}
	switch {
	case len(ok) > 0:
		return ok, ""
	case denied:
		return nil, reasonResolvesDenied
	}
	return nil, reasonResolvesPrivate
}

// notPublic are the address ranges that are not routed on the internet,
// so a runner may reach them only through an explicit Allow prefix:
// private and shared address space, loopback, link-local (where the cloud
// metadata endpoints live), multicast, reserved, benchmarking and the
// unspecified address, plus the metadata endpoints some clouds put
// elsewhere.
var notPublic = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("10.0.0.0/8"), netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("127.0.0.0/8"), netip.MustParsePrefix("169.254.0.0/16"), netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.0.0.0/24"), netip.MustParsePrefix("192.168.0.0/16"), netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("224.0.0.0/4"), netip.MustParsePrefix("240.0.0.0/4"), netip.MustParsePrefix("168.63.129.16/32"),
	netip.MustParsePrefix("::/128"), netip.MustParsePrefix("::1/128"), netip.MustParsePrefix("fc00::/7"),
	netip.MustParsePrefix("fe80::/10"), netip.MustParsePrefix("ff00::/8"),
}

// IPv6 prefixes that carry an IPv4 address: the NAT64 well-known prefix
// and 6to4.
var (
	nat64  = netip.MustParsePrefix("64:ff9b::/96")
	sixTo4 = netip.MustParsePrefix("2002::/16")
)

// embedded returns the IPv4 address an IPv6 address carries, when it does.
func embedded(a netip.Addr) (netip.Addr, bool) {
	if !a.Is6() {
		return netip.Addr{}, false
	}
	b := a.As16()
	switch {
	case a.Is4In6():
		return a.Unmap(), true
	case nat64.Contains(a):
		return netip.AddrFrom4([4]byte(b[12:16])), true
	case sixTo4.Contains(a):
		return netip.AddrFrom4([4]byte(b[2:6])), true
	}
	return netip.Addr{}, false
}

// public reports whether an address is routed on the internet, judging one
// that carries an IPv4 address by that address.
func public(a netip.Addr) bool {
	if v4, ok := embedded(a); ok {
		a = v4
	}
	for _, p := range notPublic {
		if p.Contains(a) {
			return false
		}
	}
	return true
}
