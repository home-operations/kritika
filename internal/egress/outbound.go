package egress

import (
	"bufio"
	"cmp"
	"context"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"time"
)

// EnvOutbound is a Proxy's Outbound from the environment: HTTPS_PROXY,
// unless NO_PROXY covers the destination, as http.ProxyFromEnvironment reads
// them for the server's other clients. Every destination of the proxy is
// served over TLS.
func EnvOutbound(addr string) (*url.URL, error) {
	return http.ProxyFromEnvironment(&http.Request{URL: &url.URL{Scheme: upgradedScheme, Host: addr}})
}

// dialThrough opens a tunnel to addr, a host:port that the rules admit so
// far, through the forward proxy via. The proxy resolves a name, so no
// address rule judges where the name leads: the connection leaves from the
// proxy, which is the control on it.
func (p *Proxy) dialThrough(ctx context.Context, via *url.URL, addr string) (net.Conn, error) {
	// The proxy is named by its host alone: its URL can hold a password.
	fail := func(err error) (net.Conn, error) {
		return nil, fmt.Errorf("egress: outbound proxy %s: %w", via.Host, err)
	}
	port := via.Port()
	switch via.Scheme {
	case plainScheme:
		port = cmp.Or(port, "80")
	case upgradedScheme:
		port = cmp.Or(port, "443")
	default:
		return fail(fmt.Errorf("scheme %q is not supported", via.Scheme))
	}
	conn, err := (&net.Dialer{Timeout: connectTimeout}).DialContext(ctx, "tcp", net.JoinHostPort(via.Hostname(), port))
	if err != nil {
		return fail(err)
	}
	// A cancelled request must not wait out a proxy that does not answer.
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	_ = conn.SetDeadline(time.Now().Add(connectTimeout))
	tunnel, err := p.connectThrough(ctx, conn, via, addr)
	if err == nil {
		err = conn.SetDeadline(time.Time{})
	}
	if !stop() && err == nil {
		err = context.Cause(ctx)
	}
	if err != nil {
		_ = conn.Close()
		return fail(err)
	}
	return tunnel, nil
}

// connectThrough asks the proxy at the other end of conn for a tunnel to
// addr, signed in with the user of via when it names one.
func (p *Proxy) connectThrough(ctx context.Context, conn net.Conn, via *url.URL, addr string) (net.Conn, error) {
	if via.Scheme == upgradedScheme {
		secure := tls.Client(conn, &tls.Config{ServerName: via.Hostname(), MinVersion: tls.VersionTLS12, RootCAs: p.outboundRoots})
		if err := secure.HandshakeContext(ctx); err != nil {
			return nil, err
		}
		conn = secure
	}
	req := &http.Request{Method: http.MethodConnect, URL: &url.URL{Opaque: addr}, Host: addr, Header: http.Header{"User-Agent": {"kritika"}}}
	if via.User != nil {
		password, _ := via.User.Password()
		req.Header.Set("Proxy-Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(via.User.Username()+":"+password)))
	}
	if err := req.Write(conn); err != nil {
		return nil, err
	}
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, req)
	if err != nil {
		return nil, err
	}
	// Any 2xx answer to a CONNECT opens the tunnel (RFC 9110, section 9.3.6).
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("CONNECT %s: %s", addr, resp.Status)
	}
	return &tunnelConn{Conn: conn, r: br}, nil
}

// tunnelConn is a tunnel through a forward proxy. It reads through the
// reader that read the proxy's answer, which can hold the tunnel's first
// bytes.
type tunnelConn struct {
	net.Conn
	r *bufio.Reader
}

func (c *tunnelConn) Read(b []byte) (int, error) { return c.r.Read(b) }
