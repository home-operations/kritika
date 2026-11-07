package egress

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strings"

	"oras.land/oras-go/v2/registry"
	"oras.land/oras-go/v2/registry/remote/auth"

	"github.com/home-operations/kritika/internal/egresspolicy"
)

type accountKey struct{}

// ForAccount marks ctx's request as one that a run of the account with id
// made, so it gets the credentials given to that account.
func ForAccount(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, accountKey{}, id)
}

// accountOf is the id ctx's request was marked ForAccount with, "" for none.
func AccountOf(ctx context.Context) string {
	id, _ := ctx.Value(accountKey{}).(string)
	return id
}

// registryClient is the client that answers one registry's challenges with
// its oci credential, and keeps the tokens that it is granted.
type registryClient struct {
	username, secret string
	client           *auth.Client
}

// registryTransport sends an upgraded request to host, a registry with an
// oci credential, through a client that answers the registry's challenge
// with cred. The client keeps the tokens that it is granted for as long as
// cred stays the same. It hands a redirect back to the runner rather than
// follow it, so the only request that it makes beside the runner's is the
// one to the token endpoint that the challenge names.
func (p *Proxy) registryTransport(host string, cred egresspolicy.Credential) http.RoundTripper {
	p.registriesMu.Lock()
	defer p.registriesMu.Unlock()
	r := p.registries[host]
	if r == nil || r.username != cred.Username || r.secret != cred.Secret {
		if p.registries == nil {
			p.registries = map[string]*registryClient{}
		}
		r = &registryClient{username: cred.Username, secret: cred.Secret, client: &auth.Client{
			Client: &http.Client{
				Transport:     ownHost{host: host, next: p.httpTransport()},
				CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
			},
			Cache:      bearerOnly{auth.NewCache()},
			Credential: auth.StaticCredential(host, auth.Credential{Username: cred.Username, Password: cred.Secret}),
		}}
		p.registries[host] = r
	}
	return clientTransport{host: host, client: r.client}
}

// bearerOnly is an auth cache that answers Bearer challenges alone. The
// credential goes to the token endpoint alone, so a registry that asks for
// it on a read, with a Basic challenge, is refused. Answered, the client
// would send the credential with the runner's request, and keep the Basic
// scheme for the host for every later read.
type bearerOnly struct{ auth.Cache }

func (c bearerOnly) Set(
	ctx context.Context, host string, scheme auth.Scheme, key string, fetch func(context.Context) (string, error),
) (string, error) {
	if scheme != auth.SchemeBearer {
		return "", fmt.Errorf("egress: %s asks for its credential on a read, which the gateway sends to the token endpoint alone", host)
	}
	return c.Cache.Set(ctx, host, scheme, key, fetch)
}

// runnerRead marks the requests that a registry's client sends for the
// runner, as against the token requests that it makes on its own.
const runnerRead = "Kritika-Runner-Read"

// ownHost sends a request to host over HTTPS on 443 and refuses any other.
// A token endpoint elsewhere would be sent the credential, and a port other
// than 443 is one that no upgraded request goes to. The token endpoint must
// be no read that the gateway signs in either: a run's read of it, signed
// in, would hand the runner a token.
type ownHost struct {
	host string
	next http.RoundTripper
}

func (o ownHost) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Scheme != upgradedScheme || r.URL.Port() != "" && r.URL.Port() != "443" || !strings.EqualFold(r.URL.Hostname(), o.host) {
		return nil, fmt.Errorf("egress: %s names a token endpoint at %s://%s, but its credential goes to https://%s alone",
			o.host, r.URL.Scheme, r.URL.Host, o.host)
	}
	if _, forRunner := r.Header[runnerRead]; !forRunner && registryRead.MatchString(canonicalPath(r.URL.Path)) {
		return nil, fmt.Errorf("egress: %s names a token endpoint among the reads that the gateway signs in, so it signs none in", o.host)
	}
	r = r.Clone(r.Context())
	r.Header.Del(runnerRead)
	return o.next.RoundTrip(r)
}

// canonicalPath is a decoded path as a lenient server might route it:
// without path parameters or dot segments, in lowercase.
func canonicalPath(p string) string {
	segments := strings.Split(p, "/")
	for i, s := range segments {
		segments[i], _, _ = strings.Cut(s, ";")
	}
	return strings.ToLower(path.Clean("/" + strings.Join(segments, "/")))
}

// clientTransport sends a request to host through an auth client.
type clientTransport struct {
	host   string
	client *auth.Client
}

func (c clientTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	ctx := r.Context()
	if name := repositoryOf(r.URL); name != "" {
		// Without the scope up front, the client sends each request unsigned
		// first and finds the token that it keeps only from the challenge.
		ctx = auth.AppendRepositoryScope(ctx, registry.Reference{Registry: c.host, Repository: name}, auth.ActionPull)
	}
	// ReverseProxy keeps the server request's RequestURI, which an
	// http.Client refuses to send.
	r = r.Clone(ctx)
	r.RequestURI = ""
	r.Header.Set(runnerRead, "1")
	return c.client.Do(r)
}

// registryRead matches a read of a repository in the OCI distribution API,
// by the grammar that the distribution spec gives a name, a tag and a
// digest. Its first group is the name.
var registryRead = func() *regexp.Regexp {
	const (
		component = `[a-z0-9]+(?:(?:\.|_|__|-+)[a-z0-9]+)*`
		name      = component + `(?:/` + component + `)*`
		tag       = `[a-zA-Z0-9_][a-zA-Z0-9._-]{0,127}`
		digest    = `[a-z0-9]+(?:[.+_-][a-z0-9]+)*:[a-zA-Z0-9=_-]+`
	)
	return regexp.MustCompile(`^/v2/(` + name + `)/(?:manifests/(?:` + tag + `|` + digest + `)|blobs/` + digest +
		`|tags/list|referrers/` + digest + `)$`)
}()

// repositoryOf is the repository whose read u is, "" for a URL that is
// none. The whole path must match the grammar, unescaped: a server that
// resolves dot segments, path parameters or escapes could take another
// path for it, the token endpoint among them.
func repositoryOf(u *url.URL) string {
	m := registryRead.FindStringSubmatch(u.Path)
	if u.RawPath != "" || m == nil {
		return ""
	}
	return m[1]
}

// plainOwnURLs rewrites the URLs of host in a registry's Location and Link
// headers to plain http://. The runner asked over http://, so it follows
// one there and the gateway signs that request in too. Followed over
// HTTPS, the request would be anonymous, and the runner refuses a page
// link of another scheme than its request's.
func plainOwnURLs(h http.Header, host string) {
	if loc := h.Get("Location"); loc != "" {
		h.Set("Location", plainOwn(loc, host))
	}
	for i, link := range h["Link"] {
		var b strings.Builder
		for {
			start := strings.IndexByte(link, '<')
			if start < 0 {
				break
			}
			end := strings.IndexByte(link[start:], '>')
			if end < 0 {
				break
			}
			end += start
			b.WriteString(link[:start+1])
			b.WriteString(plainOwn(link[start+1:end], host))
			link = link[end:]
		}
		b.WriteString(link)
		h["Link"][i] = b.String()
	}
}

// plainOwn is raw with http:// for https:// when it is an absolute URL of
// host on 443, and raw as it is otherwise.
func plainOwn(raw, host string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != upgradedScheme || !strings.EqualFold(u.Hostname(), host) || u.Port() != "" && u.Port() != "443" {
		return raw
	}
	u.Scheme, u.Host = plainScheme, host
	return u.String()
}
