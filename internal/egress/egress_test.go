package egress

import (
	"context"
	"crypto/tls"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"slices"
	"strings"
	"testing"
)

func TestRulesAllows(t *testing.T) {
	r := Rules{Allow: hosts("api.github.com", "*.githubusercontent.com")}
	for host, want := range map[string]bool{
		"api.github.com": true, "API.GITHUB.COM": true, "api.github.com.": true,
		"raw.githubusercontent.com": true, "githubusercontent.com": false, "evil-api.github.com": false, "": false,
	} {
		if got := r.Allows(host); got != want {
			t.Errorf("Allows(%q) = %v, want %v", host, got, want)
		}
	}
	any := Rules{Allow: hosts("*"), Deny: hosts("*.pastebin.com", "transfer.sh")}
	for host, want := range map[string]bool{
		"evil.example": true, "ghcr.io.": true, "": false,
		"pastebin.com": true, "Paste.Pastebin.com": false, "transfer.sh.": false, "transfer.sh.example": true,
	} {
		if got := any.Allows(host); got != want {
			t.Errorf("any.Allows(%q) = %v, want %v", host, got, want)
		}
	}
	// Deny wins over an Allow entry that names the same host.
	if (Rules{Allow: hosts("ghcr.io"), Deny: hosts("ghcr.io")}).Allows("ghcr.io") {
		t.Error("a host both allowed and denied must be refused")
	}
}

func hosts(h ...string) List { return List{Hosts: h} }

func prefixes(ps ...string) []netip.Prefix {
	out := make([]netip.Prefix, 0, len(ps))
	for _, p := range ps {
		out = append(out, netip.MustParsePrefix(p))
	}
	return out
}

// names is a Resolver over a fixed table.
type names map[string][]string

func (n names) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	addrs, ok := n[host]
	if !ok {
		return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
	}
	var out []netip.Addr
	for _, a := range addrs {
		out = append(out, netip.MustParseAddr(a))
	}
	return out, nil
}

// upstream is a TLS server the proxy reaches, whatever address the rules
// admitted; dialed records the addresses the proxy dialed.
func upstream(t *testing.T) (*httptest.Server, func(context.Context, string, string) (net.Conn, error), *[]string) {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Seen-Auth", r.Header.Get("Authorization"))
		w.Header().Set("X-Seen-Proxy-Auth", r.Header.Get("Proxy-Authorization"))
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, "/elsewhere", http.StatusFound)
			return
		}
		body, _ := io.ReadAll(r.Body)
		_, _ = io.WriteString(w, r.Method+" "+r.Host+r.URL.Path+" "+string(body))
	}))
	t.Cleanup(srv.Close)
	addr := srv.Listener.Addr().String()
	dialed := &[]string{}
	dial := func(ctx context.Context, network, a string) (net.Conn, error) {
		*dialed = append(*dialed, a)
		return (&net.Dialer{}).DialContext(ctx, network, addr)
	}
	return srv, dial, dialed
}

// publicNames resolves the hosts the tests name to public addresses, and a
// few to addresses the rules must judge.
var publicNames = names{
	"api.github.com": {"140.82.112.6"}, "raw.githubusercontent.com": {"185.199.108.133"}, "evil.example": {"203.0.113.9"},
	"intranet.example": {"10.0.0.5"}, "split.example": {"10.0.0.6", "203.0.113.6"},
	"metadata.example": {"169.254.169.254"}, "mapped.example": {"::ffff:192.168.1.1"}, "sink.example": {"203.0.113.7"},
	"nat64-sink.example": {"64:ff9b::cb00:7107"}, "nat64-private.example": {"64:ff9b::a00:5"},
}

func newProxy(t *testing.T, rules Rules) (*httptest.Server, map[string]int, *[]string) {
	t.Helper()
	_, dial, dialed := upstream(t)
	outcomes := map[string]int{}
	p := &Proxy{
		Rules: func() Rules { return rules }, Resolver: publicNames, Dial: dial, Logger: slog.Default(),
		Observe: func(kind, outcome string) { outcomes[kind+"/"+outcome]++ },
	}
	// The upgrade client dials through the rules like the default one, but
	// trusts the test upstream's certificate.
	p.Client = &http.Client{
		Transport:     &http.Transport{DialContext: p.dialContext, TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}, //nolint:gosec // test upstream
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	srv := httptest.NewServer(p)
	t.Cleanup(srv.Close)
	return srv, outcomes, dialed
}

// viaProxy is a client that sends everything through the proxy, trusting
// the test upstream's certificate for CONNECT tunnels.
func viaProxy(proxy *httptest.Server) *http.Client {
	pu, _ := url.Parse(proxy.URL)
	return &http.Client{Transport: &http.Transport{
		Proxy:           http.ProxyURL(pu),
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // test upstream
	}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func TestUpgradeAddsCredentialAndStripsProxyHeaders(t *testing.T) {
	proxy, outcomes, _ := newProxy(t, Rules{
		Allow: hosts("api.github.com"), Credentials: map[string]string{"api.github.com": "Bearer secret"},
	})
	req, _ := http.NewRequest(http.MethodPost, "http://api.github.com/repos/x/y", strings.NewReader("body"))
	req.Header.Set("Authorization", "Bearer from-the-pod")
	req.Header.Set("Proxy-Authorization", "Basic abc")
	resp, err := viaProxy(proxy).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || string(body) != "POST api.github.com/repos/x/y body" {
		t.Fatalf("status %d body %q", resp.StatusCode, body)
	}
	if got := resp.Header.Get("X-Seen-Auth"); got != "Bearer secret" {
		t.Fatalf("upstream saw Authorization %q, want the configured credential", got)
	}
	if got := resp.Header.Get("X-Seen-Proxy-Auth"); got != "" {
		t.Fatalf("upstream saw Proxy-Authorization %q", got)
	}
	if outcomes["http/allowed"] != 1 {
		t.Fatalf("outcomes = %v", outcomes)
	}
}

func TestUpgradeReturnsRedirectsUnfollowed(t *testing.T) {
	proxy, _, _ := newProxy(t, Rules{Allow: hosts("api.github.com")})
	resp, err := viaProxy(proxy).Get("http://api.github.com/redirect")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != "/elsewhere" {
		t.Fatalf("status %d location %q", resp.StatusCode, resp.Header.Get("Location"))
	}
}

func TestRefusals(t *testing.T) {
	proxy, outcomes, _ := newProxy(t, Rules{Allow: hosts("api.github.com", "*.example"), Deny: hosts("evil.example")})
	for _, u := range []string{"http://evil.example/", "http://api.github.com:8443/x"} {
		resp, err := viaProxy(proxy).Get(u)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("%s: status %d, want 403", u, resp.StatusCode)
		}
	}
	// A CONNECT to a host outside the rules, or to a port other than 443.
	for _, u := range []string{"https://evil.example/", "https://api.github.com:8443/"} {
		_, err := viaProxy(proxy).Get(u)
		if err == nil || !strings.Contains(err.Error(), "Forbidden") {
			t.Fatalf("%s: err = %v, want a Forbidden from the proxy", u, err)
		}
	}
	if outcomes["http/refused"] != 2 || outcomes["connect/refused"] != 2 {
		t.Fatalf("outcomes = %v", outcomes)
	}
}

func TestConnectTunnelsAllowedHosts(t *testing.T) {
	proxy, outcomes, dialed := newProxy(t, Rules{Allow: hosts("*.github.com"), Credentials: map[string]string{"api.github.com": "Bearer x"}})
	resp, err := viaProxy(proxy).Get("https://api.github.com/tunnelled")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "GET api.github.com/tunnelled " {
		t.Fatalf("body %q", body)
	}
	// A tunnel is opaque: no credential is added inside it.
	if got := resp.Header.Get("X-Seen-Auth"); got != "" {
		t.Fatalf("tunnelled request carried Authorization %q", got)
	}
	if outcomes["connect/allowed"] != 1 {
		t.Fatalf("outcomes = %v", outcomes)
	}
	// The tunnel is dialed to the address that was checked, not the name.
	if !slices.Equal(*dialed, []string{"140.82.112.6:443"}) {
		t.Fatalf("dialed %v", *dialed)
	}
}

func TestRefusesPrivateAddresses(t *testing.T) {
	rules := Rules{Allow: List{Hosts: []string{"*"}, Prefixes: prefixes("10.0.0.7/32", "198.51.100.0/24")}, Deny: List{Prefixes: prefixes("203.0.113.7/32")}}
	proxy, outcomes, dialed := newProxy(t, rules)
	for _, tt := range []struct{ url, want string }{
		// A name resolving to a private address alone.
		{"http://intranet.example/", reasonResolvesPrivate},
		// Link-local, where cloud metadata lives, and an IPv4-mapped IPv6 form.
		{"http://metadata.example/", reasonResolvesPrivate},
		{"http://mapped.example/", reasonResolvesPrivate},
		{"http://sink.example/", reasonResolvesDenied},
		// A DNS64 resolver's NAT64 forms of a denied and a private IPv4 address.
		{"http://nat64-sink.example/", reasonResolvesDenied},
		{"http://nat64-private.example/", reasonResolvesPrivate},
		// An address literal needs an allow prefix, which "*" is not.
		{"http://10.0.0.5/", reasonAddressNotAllowed},
		{"http://203.0.113.9/", reasonAddressNotAllowed},
		{"http://203.0.113.7/", reasonAddressDenied},
		{"http://[::ffff:203.0.113.7]/", reasonAddressDenied},
	} {
		resp, err := viaProxy(proxy).Get(tt.url)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusForbidden || !strings.Contains(string(body), tt.want) {
			t.Errorf("%s: status %d body %q, want 403 with %q", tt.url, resp.StatusCode, body, tt.want)
		}
		// The same over CONNECT.
		if _, err := viaProxy(proxy).Get(strings.Replace(tt.url, "http://", "https://", 1)); err == nil || !strings.Contains(err.Error(), "Forbidden") {
			t.Errorf("%s over CONNECT: err = %v, want a Forbidden from the proxy", tt.url, err)
		}
	}
	if len(*dialed) != 0 {
		t.Fatalf("refused destinations were dialed: %v", *dialed)
	}
	if outcomes["http/refused"] != 10 || outcomes["connect/refused"] != 10 || outcomes["http/error"]+outcomes["connect/error"] != 0 {
		t.Fatalf("outcomes = %v", outcomes)
	}
	// A split-horizon name is dialed at its public address alone; an
	// address literal an allow prefix covers is dialed as given.
	for _, u := range []string{"http://split.example/", "https://split.example/", "http://198.51.100.9/", "https://198.51.100.9/", "http://10.0.0.7/"} {
		resp, err := viaProxy(proxy).Get(u)
		if err != nil || resp.StatusCode != http.StatusOK {
			t.Fatalf("%s: %v %v", u, resp, err)
		}
	}
	if want := []string{"203.0.113.6:443", "203.0.113.6:443", "198.51.100.9:443", "198.51.100.9:443", "10.0.0.7:443"}; !slices.Equal(*dialed, want) {
		t.Fatalf("dialed %v, want %v", *dialed, want)
	}
}

func TestUpgradeBracketsIPv6Literal(t *testing.T) {
	proxy, outcomes, dialed := newProxy(t, Rules{Allow: List{Prefixes: prefixes("2001:db8::1/128")}})
	resp, err := viaProxy(proxy).Get("http://[2001:db8::1]/x")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || string(body) != "GET [2001:db8::1]/x " || outcomes["http/allowed"] != 1 {
		t.Fatalf("status %d body %q outcomes %v", resp.StatusCode, body, outcomes)
	}
	if !slices.Equal(*dialed, []string{"[2001:db8::1]:443"}) {
		t.Fatalf("dialed %v", *dialed)
	}
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

func TestResolutionFailureIsAnError(t *testing.T) {
	proxy, outcomes, _ := newProxy(t, Rules{Allow: hosts("*")})
	resp, err := viaProxy(proxy).Get("http://nowhere.example/")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusBadGateway || outcomes["http/error"] != 1 {
		t.Fatalf("status %d outcomes %v", resp.StatusCode, outcomes)
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

func TestProxyURL(t *testing.T) {
	for raw, ok := range map[string]bool{
		"http://kritika-gateway:8082": true, "http://kritika-gateway:8082/": true,
		"https://kritika-gateway:8082": false, "http://": false, "http://gw/path": false, "gw:8082": false,
	} {
		if _, err := ProxyURL(raw); (err == nil) != ok {
			t.Errorf("ProxyURL(%q) err = %v, want ok=%v", raw, err, ok)
		}
	}
}
