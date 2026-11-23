package egress

import (
	"cmp"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/home-operations/kritika/internal/egresspolicy"
)

// outbound is a forward proxy outside the cluster. It tunnels every CONNECT
// it admits to the test upstream, and records what it was asked for.
type outbound struct {
	mu sync.Mutex
	// asked are the CONNECT targets, each with the Proxy-Authorization sent.
	asked []string
	// refuse, when set, is the status every CONNECT is answered with.
	refuse int
}

func (o *outbound) serve(w http.ResponseWriter, r *http.Request, target string) {
	o.mu.Lock()
	o.asked = append(o.asked, r.Method+" "+r.Host+" "+r.Header.Get("Proxy-Authorization"))
	refuse := o.refuse
	o.mu.Unlock()
	if r.Method != http.MethodConnect || refuse != 0 {
		w.WriteHeader(cmp.Or(refuse, http.StatusMethodNotAllowed))
		return
	}
	up, err := net.Dial("tcp", target)
	if err != nil {
		w.WriteHeader(http.StatusBadGateway)
		return
	}
	client, buf, err := w.(http.Hijacker).Hijack()
	if err != nil {
		_ = up.Close()
		return
	}
	_, _ = io.WriteString(client, "HTTP/1.1 200 Connection established\r\n\r\n")
	tunnel(client, buf, up)
}

func (o *outbound) seen() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return slices.Clone(o.asked)
}

// newOutboundProxy is a proxy whose environment names an outbound forward
// proxy for every destination but those of direct. secure serves the
// outbound proxy over TLS. Only direct.example resolves, so another name
// that is reached is one the proxy did not resolve. outcomes counts the
// requests by kind and outcome so far.
func newOutboundProxy(
	t *testing.T, rules egresspolicy.Rules, secure bool, direct ...string,
) (srv *httptest.Server, out *outbound, outcomes func() map[string]int, dialed *[]string) {
	t.Helper()
	target, dial, dialed := upstream(t)
	out = &outbound{}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		out.serve(w, r, target.Listener.Addr().String())
	})
	var forward *httptest.Server
	if secure {
		forward = httptest.NewTLSServer(handler)
	} else {
		forward = httptest.NewServer(handler)
	}
	t.Cleanup(forward.Close)
	via, err := url.Parse(forward.URL)
	if err != nil {
		t.Fatal(err)
	}
	via.User = url.UserPassword("gateway", "s3cret")
	var mu sync.Mutex
	counts := map[string]int{}
	outcomes = func() map[string]int {
		mu.Lock()
		defer mu.Unlock()
		return maps.Clone(counts)
	}
	p := &Proxy{
		Rules: func() egresspolicy.Rules { return rules }, Resolver: names{"direct.example": {"203.0.113.20"}}, Dial: dial, Logger: slog.Default(),
		Observe: func(kind, outcome string) {
			mu.Lock()
			defer mu.Unlock()
			counts[kind+"/"+outcome]++
		},
		Outbound: func(addr string) (*url.URL, error) {
			host, _, _ := net.SplitHostPort(addr)
			if slices.Contains(direct, host) {
				return nil, nil
			}
			return via, nil
		},
	}
	if secure {
		p.outboundRoots = forward.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs
	}
	p.Transport = &http.Transport{DialContext: p.dialContext, TLSClientConfig: &tls.Config{InsecureSkipVerify: true}} //nolint:gosec // test upstream
	srv = httptest.NewServer(p)
	t.Cleanup(srv.Close)
	return srv, out, outcomes, dialed
}

// TestOutboundProxy: a destination that the environment names a forward
// proxy for is reached through it by name, signed in with the proxy's user,
// and neither resolved nor dialed by the gateway.
func TestOutboundProxy(t *testing.T) {
	const auth = " Basic Z2F0ZXdheTpzM2NyZXQ="
	for _, secure := range []bool{false, true} {
		name := map[bool]string{false: "http proxy", true: "https proxy"}[secure]
		t.Run(name, func(t *testing.T) {
			rules := egresspolicy.Rules{
				Allow:       egresspolicy.List{Hosts: []string{"api.github.com", "direct.example"}, Prefixes: prefixes("198.51.100.0/24")},
				Credentials: bearer("api.github.com", "secret"),
			}
			proxy, out, outcomes, dialed := newOutboundProxy(t, rules, secure, "direct.example")
			client := viaProxy(proxy)
			defer client.CloseIdleConnections()

			// A CONNECT tunnel, an upgraded request, and an address literal
			// that an allow prefix covers.
			for _, tt := range []struct{ url, want string }{
				{"https://api.github.com/tunnelled", "GET api.github.com/tunnelled "},
				{"http://api.github.com/upgraded", "GET api.github.com/upgraded "},
				{"https://198.51.100.9/literal", "GET 198.51.100.9/literal "},
			} {
				resp, err := client.Get(tt.url)
				if err != nil {
					t.Fatalf("%s: %v", tt.url, err)
				}
				body, _ := io.ReadAll(resp.Body)
				_ = resp.Body.Close()
				if resp.StatusCode != http.StatusOK || string(body) != tt.want {
					t.Fatalf("%s: status %d body %q", tt.url, resp.StatusCode, body)
				}
				// The outbound proxy's credential stays on the CONNECT.
				if got := resp.Header.Get("X-Seen-Proxy-Auth"); got != "" {
					t.Fatalf("%s: upstream saw Proxy-Authorization %q", tt.url, got)
				}
			}
			want := []string{"CONNECT api.github.com:443" + auth, "CONNECT api.github.com:443" + auth, "CONNECT 198.51.100.9:443" + auth}
			if got := out.seen(); !slices.Equal(got, want) {
				t.Fatalf("outbound proxy was asked %q, want %q", got, want)
			}
			if len(*dialed) != 0 {
				t.Fatalf("proxied destinations were dialed by the gateway: %v", *dialed)
			}

			// A destination that the environment keeps off the proxy is
			// resolved, judged and dialed as without one.
			resp, err := client.Get("https://direct.example/x")
			if err != nil || resp.StatusCode != http.StatusOK {
				t.Fatalf("direct: %v %v", resp, err)
			}
			_ = resp.Body.Close()
			if !slices.Equal(*dialed, []string{"203.0.113.20:443"}) || len(out.seen()) != len(want) {
				t.Fatalf("direct destination: dialed %v, outbound proxy asked %q", *dialed, out.seen())
			}

			// The host rules judge a destination before the outbound proxy
			// hears of it.
			for _, u := range []string{"http://evil.example/", "http://203.0.113.9/"} {
				resp, err := client.Get(u)
				if err != nil || resp.StatusCode != http.StatusForbidden {
					t.Fatalf("%s: %v %v, want 403", u, resp, err)
				}
				_ = resp.Body.Close()
				if _, err := client.Get(strings.Replace(u, "http://", "https://", 1)); err == nil || !strings.Contains(err.Error(), "Forbidden") {
					t.Fatalf("%s over CONNECT: err = %v, want a Forbidden from the proxy", u, err)
				}
			}
			if len(out.seen()) != len(want) {
				t.Fatalf("refused destinations reached the outbound proxy: %q", out.seen())
			}
			if got := outcomes(); got["connect/allowed"] != 3 || got["http/allowed"] != 1 || got["http/refused"] != 2 || got["connect/refused"] != 2 {
				t.Fatalf("outcomes = %v", got)
			}
		})
	}
}

// TestOutboundProxyRefusal: what the outbound proxy answers a CONNECT with
// reaches the runner, and the proxy's password does not.
func TestOutboundProxyRefusal(t *testing.T) {
	proxy, out, outcomes, _ := newOutboundProxy(t, egresspolicy.Rules{Allow: hosts("api.github.com")}, false)
	out.refuse = http.StatusForbidden
	client := viaProxy(proxy)
	defer client.CloseIdleConnections()
	resp, err := client.Get("http://api.github.com/x")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway || !strings.Contains(string(body), "CONNECT api.github.com:443: 403 Forbidden") {
		t.Fatalf("status %d body %q", resp.StatusCode, body)
	}
	if strings.Contains(string(body), "s3cret") || strings.Contains(string(body), "gateway@") {
		t.Fatalf("the outbound proxy's credential is in the answer: %q", body)
	}
	if _, err := client.Get("https://api.github.com/x"); err == nil || !strings.Contains(err.Error(), "Bad Gateway") {
		t.Fatalf("CONNECT: err = %v, want a Bad Gateway from the proxy", err)
	}
	if got := outcomes(); got["http/error"] != 1 || got["connect/error"] != 1 {
		t.Fatalf("outcomes = %v", got)
	}
}

// TestDialThrough: the dial through an outbound proxy at the level of the
// connection.
func TestDialThrough(t *testing.T) {
	// serve answers the first connection with answer, "" for no answer,
	// after it read the request, and returns the proxy's URL.
	serve := func(t *testing.T, answer string) *url.URL {
		t.Helper()
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = ln.Close() })
		go func() {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			defer func() { _ = conn.Close() }()
			buf := make([]byte, 4<<10)
			for n := 0; !strings.Contains(string(buf[:n]), "\r\n\r\n"); {
				m, err := conn.Read(buf[n:])
				if err != nil {
					return
				}
				n += m
			}
			_, _ = io.WriteString(conn, answer)
			_, _ = io.Copy(io.Discard, conn)
		}()
		return &url.URL{Scheme: "http", Host: ln.Addr().String()}
	}
	p := &Proxy{}

	// Any 2xx answer opens the tunnel.
	for _, status := range []string{"200 Connection established", "204 No Content"} {
		t.Run("bytes that follow the proxy's "+status+" are the tunnel's", func(t *testing.T) {
			conn, err := p.dialThrough(t.Context(), serve(t, "HTTP/1.1 "+status+"\r\n\r\nhello"), "example.com:443")
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = conn.Close() }()
			got := make([]byte, 5)
			if _, err := io.ReadFull(conn, got); err != nil || string(got) != "hello" {
				t.Fatalf("read %q, %v", got, err)
			}
		})
	}
	t.Run("an answer that is not a 2xx fails the dial", func(t *testing.T) {
		_, err := p.dialThrough(t.Context(), serve(t, "HTTP/1.1 302 Found\r\nLocation: http://login.example/\r\n\r\n"), "example.com:443")
		if err == nil || !strings.Contains(err.Error(), "CONNECT example.com:443: 302 Found") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("a proxy that does not answer is given up at the request's end", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
		defer cancel()
		start := time.Now()
		_, err := p.dialThrough(ctx, serve(t, ""), "example.com:443")
		if err == nil || time.Since(start) > 3*time.Second {
			t.Fatalf("err = %v after %s", err, time.Since(start))
		}
	})
	t.Run("a proxy of another scheme is refused", func(t *testing.T) {
		_, err := p.dialThrough(t.Context(), &url.URL{Scheme: "socks5", Host: "127.0.0.1:1", User: url.UserPassword("u", "pw")}, "example.com:443")
		if err == nil || !strings.Contains(err.Error(), `scheme "socks5" is not supported`) || strings.Contains(err.Error(), "pw") {
			t.Fatalf("err = %v", err)
		}
	})
}

// TestEnvOutbound: the outbound proxy is the environment's HTTPS_PROXY, for
// every destination that NO_PROXY does not cover. net/http reads the
// environment once in a process, so a child process reads it here.
func TestEnvOutbound(t *testing.T) {
	addrs := []string{"gitlab.example.com:443", "direct.example:443", "10.1.2.3:443", "minio.storage.svc:443", "203.0.113.9:443", "[2001:db8::1]:443"}
	if os.Getenv("KRITIKA_TEST_ENV_OUTBOUND") != "" {
		for _, addr := range addrs {
			via, err := EnvOutbound(addr)
			if err != nil {
				fmt.Println(addr, "error", err)
				continue
			}
			if via == nil {
				fmt.Println(addr, "direct")
				continue
			}
			fmt.Println(addr, via.Host)
		}
		return
	}
	for _, tt := range []struct {
		name string
		env  []string
		want map[string]string
	}{
		{"a proxy for all but what NO_PROXY covers",
			// The upper-case names win over the lower-case ones, as for
			// the server's other clients.
			[]string{"HTTPS_PROXY=http://user:pw@proxy.example:8080", "https_proxy=http://lower.example:3128",
				"HTTP_PROXY=http://plain.example:3128", "NO_PROXY=direct.example,10.0.0.0/8,.svc", "no_proxy=gitlab.example.com"},
			map[string]string{"gitlab.example.com:443": "proxy.example:8080", "direct.example:443": "direct", "10.1.2.3:443": "direct",
				"minio.storage.svc:443": "direct", "203.0.113.9:443": "proxy.example:8080", "[2001:db8::1]:443": "proxy.example:8080"}},
		{"no proxy", nil, map[string]string{"gitlab.example.com:443": "direct", "direct.example:443": "direct", "10.1.2.3:443": "direct",
			"minio.storage.svc:443": "direct", "203.0.113.9:443": "direct", "[2001:db8::1]:443": "direct"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestEnvOutbound$")
			cmd.Env = append([]string{"KRITIKA_TEST_ENV_OUTBOUND=1"}, tt.env...)
			out, err := cmd.Output()
			if err != nil {
				t.Fatalf("child: %v\n%s", err, out)
			}
			got := map[string]string{}
			for line := range strings.Lines(string(out)) {
				if addr, via, ok := strings.Cut(strings.TrimSpace(line), " "); ok && slices.Contains(addrs, addr) {
					got[addr] = via
				}
			}
			if !maps.Equal(got, tt.want) {
				t.Fatalf("outbound = %v, want %v", got, tt.want)
			}
		})
	}
}
