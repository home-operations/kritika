// Package egress is the worker's forward proxy: the one route out of the
// cluster for runner pods, which reach it through HTTPS_PROXY and have no
// other egress. A CONNECT to an allowed host on 443 is tunnelled without
// inspection, which is how git, the model SDKs and curl reach TLS
// endpoints; a plain http:// request to an allowed host is upgraded to
// HTTPS here, with a configured credential added, so a runner can use an
// API at a connection's rate limit without holding a token. Every other
// destination is refused by hostname.
package egress

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Outcomes a request ends with, for metrics.
const (
	OutcomeAllowed = "allowed"
	OutcomeRefused = "refused"
	OutcomeError   = "error"
)

// Proxy is the forward proxy handler.
type Proxy struct {
	// Rules returns the current rules; it is called per request.
	Rules func() Rules
	// Resolver resolves a destination's name; nil means net.DefaultResolver.
	Resolver Resolver
	// Dial opens the upstream connection to an address the rules admit;
	// nil means a net.Dialer with connectTimeout.
	Dial func(ctx context.Context, network, addr string) (net.Conn, error)
	// Transport sends upgraded requests; nil dials through the rules.
	Transport http.RoundTripper
	// Observe, if set, is called once per request with its kind (connect,
	// http) and outcome.
	Observe func(kind, outcome string)
	Logger  *slog.Logger

	transportOnce sync.Once
	transport     http.RoundTripper
}

// Resolver is the part of net.Resolver the proxy uses.
type Resolver interface {
	LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
}

// refusal is a destination the rules refuse, raised from the dial so an
// upgraded request's client surfaces it through its transport.
type refusal struct {
	host, reason string
}

func (r *refusal) Error() string { return "egress: " + r.host + ": " + r.reason }

// dialContext dials addr, a host:port, as the rules admit: a name passes
// the host rules and is resolved once, the addresses that pass the
// address rules are tried in turn, and the one that connects is the one
// that was checked, so a name cannot be re-resolved to somewhere else in
// between. It is the transport dialer of upgraded requests and the dial
// of a CONNECT.
func (p *Proxy) dialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("egress: %w", err)
	}
	rules := p.Rules()
	if reason := rules.refuses(host); reason != "" {
		return nil, &refusal{host, reason}
	}
	addrs := []netip.Addr{}
	if a, err := netip.ParseAddr(host); err == nil {
		addrs = append(addrs, a.Unmap())
	} else {
		resolver := p.Resolver
		if resolver == nil {
			resolver = net.DefaultResolver
		}
		resolved, err := resolver.LookupNetIP(ctx, "ip", host)
		if err != nil {
			return nil, fmt.Errorf("egress: %w", err)
		}
		var reason string
		if addrs, reason = rules.admit(resolved); reason != "" {
			return nil, &refusal{host, reason}
		}
	}
	dial := p.Dial
	if dial == nil {
		dial = (&net.Dialer{Timeout: connectTimeout}).DialContext
	}
	var errs []error
	for _, a := range addrs {
		conn, err := dial(ctx, network, net.JoinHostPort(a.String(), port))
		if err == nil {
			return conn, nil
		}
		errs = append(errs, err)
	}
	return nil, errors.Join(errs...)
}

// refuse answers a request the rules refuse.
func (p *Proxy) refuse(w http.ResponseWriter, kind, host, reason string) {
	p.observe(kind, OutcomeRefused)
	p.Logger.Warn("egress refused", "kind", kind, "host", host, "reason", reason)
	http.Error(w, "egress: destination refused: "+reason, http.StatusForbidden)
}

const (
	connectTimeout = 15 * time.Second
	// tunnelIdle ends a CONNECT tunnel neither side has used for this long.
	tunnelIdle = 5 * time.Minute
	// upgradeTimeout bounds one upgraded request, response body included.
	upgradeTimeout = 2 * time.Minute
)

func (p *Proxy) observe(kind, outcome string) {
	if p.Observe != nil {
		p.Observe(kind, outcome)
	}
}

// ServeHTTP implements http.Handler.
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodConnect {
		p.connect(w, r)
		return
	}
	p.upgrade(w, r)
}

// connect tunnels a CONNECT to host:443 when the rules admit the host.
func (p *Proxy) connect(w http.ResponseWriter, r *http.Request) {
	host, port, err := net.SplitHostPort(r.Host)
	if err != nil || port != "443" {
		p.refuse(w, "connect", r.Host, "only port 443 is served")
		return
	}
	if reason := p.Rules().refuses(host); reason != "" {
		p.refuse(w, "connect", r.Host, reason)
		return
	}
	upstream, err := p.dialContext(r.Context(), "tcp", net.JoinHostPort(host, port))
	if refused, ok := errors.AsType[*refusal](err); ok {
		p.refuse(w, "connect", r.Host, refused.reason)
		return
	}
	if err != nil {
		p.observe("connect", OutcomeError)
		p.Logger.Warn("egress dial failed", "host", r.Host, "error", err)
		http.Error(w, "egress: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer func() { _ = upstream.Close() }()
	hj, ok := w.(http.Hijacker)
	if !ok {
		p.observe("connect", OutcomeError)
		http.Error(w, "egress: connection cannot be hijacked", http.StatusInternalServerError)
		return
	}
	client, buf, err := hj.Hijack()
	if err != nil {
		p.observe("connect", OutcomeError)
		p.Logger.Warn("egress hijack failed", "host", r.Host, "error", err)
		return
	}
	// Written to the connection itself: a 200 through the ResponseWriter
	// would carry a Transfer-Encoding, which a CONNECT response must not.
	if _, err := io.WriteString(client, "HTTP/1.1 200 Connection established\r\n\r\n"); err != nil {
		_ = client.Close()
		p.observe("connect", OutcomeError)
		return
	}
	defer func() { _ = client.Close() }()
	p.observe("connect", OutcomeAllowed)
	p.Logger.Debug("egress tunnel", "host", r.Host)
	tunnel(client, buf, upstream)
}

// tunnel copies both ways until either side closes or the tunnel idles
// out, then closes both.
func tunnel(client net.Conn, buffered io.Reader, upstream net.Conn) {
	var once sync.Once
	stop := func() {
		once.Do(func() {
			_ = client.Close()
			_ = upstream.Close()
		})
	}
	touch := func() {
		deadline := time.Now().Add(tunnelIdle)
		_ = client.SetDeadline(deadline)
		_ = upstream.SetDeadline(deadline)
	}
	touch()
	var wg sync.WaitGroup
	pipe := func(dst io.Writer, src io.Reader) {
		defer stop()
		buf := make([]byte, 32<<10)
		for {
			n, err := src.Read(buf)
			if n > 0 {
				touch()
				if _, werr := dst.Write(buf[:n]); werr != nil {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}
	wg.Go(func() { pipe(upstream, buffered) })
	wg.Go(func() { pipe(client, upstream) })
	wg.Wait()
}

// upgrade forwards an absolute-URI http:// request to the same host over
// HTTPS, adding the host's credential.
func (p *Proxy) upgrade(w http.ResponseWriter, r *http.Request) {
	if !r.URL.IsAbs() || r.URL.Scheme != "http" {
		p.observe("http", OutcomeRefused)
		http.Error(w, "egress: only absolute http:// requests and CONNECT are served", http.StatusBadRequest)
		return
	}
	host := r.URL.Hostname()
	rules := p.Rules()
	if r.URL.Port() != "" && r.URL.Port() != "80" {
		p.refuse(w, "http", r.URL.Host, "only port 80 is served")
		return
	}
	if reason := rules.refuses(host); reason != "" {
		p.refuse(w, "http", r.URL.Host, reason)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), upgradeTimeout)
	defer cancel()
	proxy := &httputil.ReverseProxy{
		Transport: p.httpTransport(),
		ErrorLog:  slog.NewLogLogger(p.Logger.Handler(), slog.LevelWarn),
		Rewrite: func(req *httputil.ProxyRequest) {
			req.Out.URL.Scheme, req.Out.URL.Host = "https", host
			if strings.Contains(host, ":") {
				req.Out.URL.Host = "[" + host + "]"
			}
			req.Out.URL.User = nil
			req.Out.Host = req.Out.URL.Host
			req.Out.Header.Del("Authorization")
			if cred, ok := rules.Credentials[strings.ToLower(host)]; ok {
				req.Out.Header.Set("Authorization", cred)
			}
		},
		ModifyResponse: func(resp *http.Response) error {
			p.observe("http", OutcomeAllowed)
			p.Logger.Debug("egress request", "host", host, "method", r.Method, "path", r.URL.Path, "status", resp.StatusCode)
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			if refused, ok := errors.AsType[*refusal](err); ok {
				p.refuse(w, "http", r.URL.Host, refused.reason)
				return
			}
			p.observe("http", OutcomeError)
			p.Logger.Warn("egress request failed", "host", host, "error", err)
			http.Error(w, "egress: "+err.Error(), http.StatusBadGateway)
		},
	}
	proxy.ServeHTTP(w, r.WithContext(ctx))
}

// httpTransport reuses connections while checking new destinations against the rules.
func (p *Proxy) httpTransport() http.RoundTripper {
	p.transportOnce.Do(func() {
		if p.transport = p.Transport; p.transport != nil {
			return
		}
		transport := http.DefaultTransport.(*http.Transport).Clone()
		transport.Proxy = nil
		transport.DialContext = p.dialContext
		p.transport = transport
	})
	return p.transport
}

// ProxyURL checks a gateway URL a runner will be handed: http, a host, no
// path.
func ProxyURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("egress: gateway url: %w", err)
	}
	if u.Scheme != "http" || u.Host == "" || (u.Path != "" && u.Path != "/") {
		return nil, errors.New("egress: gateway url must be http://host:port")
	}
	return u, nil
}
