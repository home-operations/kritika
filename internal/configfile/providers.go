package configfile

import (
	"fmt"
	"maps"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"

	"golang.org/x/net/http/httpguts"

	"github.com/home-operations/kritika/internal/model"
)

// Provider is a model endpoint. Keys are references, never values, so the
// file can live in git.
type Provider struct {
	Type ProviderType `yaml:"type"`
	// BaseURL overrides the type's default endpoint.
	BaseURL string    `yaml:"baseUrl,omitempty"`
	APIKey  SecretRef `yaml:"apiKey"`
	// Pricing, keyed by model id or floating alias, computes the cost of
	// calls the provider does not report a cost for; without a price such
	// calls are recorded as unpriced, at no cost, while their tokens still
	// count against limits.
	Pricing model.Pricing `yaml:"pricing,omitempty"`
	// Retries is how many more times a review's or a follow-up's step, the
	// scorer's call or a split review's summary call on one of the
	// provider's models is tried after it failed in a way another attempt
	// may not (a 5xx, a 429, a timeout, a cut connection), at most
	// MaxProviderRetries; zero tries once. A routing proxy that picks a
	// model per request is where it earns its keep. The embedder does not
	// use it.
	Retries int `yaml:"retries,omitempty"`
	// Headers are sent with every request to the provider, by name, and
	// replace kritika's own of the same name: a gateway's routing or
	// tagging headers, or its credential, given by reference as apiKey is.
	// model.SessionPlaceholder in a value stands for the conversation a
	// step belongs to. The embedder sends none of them.
	Headers map[string]ValueOrRef `yaml:"headers,omitempty"`

	apiKey     Secret
	headers    map[string]Secret
	sessionKey string
}

// MaxProviderRetries bounds Provider.Retries: with backoff, five more
// attempts span about a minute, which a runner's deadline absorbs.
const MaxProviderRetries = 5

// APIKeyValue returns the resolved API key.
func (p Provider) APIKeyValue() Secret { return p.apiKey }

// HeaderValues returns the resolved headers.
func (p Provider) HeaderValues() map[string]string {
	out := make(map[string]string, len(p.headers))
	for k, v := range p.headers {
		out[k] = v.Value()
	}
	return out
}

// HeaderSecrets returns the resolved values of the headers given by
// reference; an inline value is not a secret.
func (p Provider) HeaderSecrets() []string {
	var out []string
	for name, h := range p.Headers {
		if !h.Ref.empty() {
			out = append(out, p.headers[name].Value())
		}
	}
	return out
}

// ChatGPTSessionKey identifies this provider's stored sign-in, assigned
// when the file is parsed, so accounts with the same provider name stay
// separate.
func (p Provider) ChatGPTSessionKey() string { return p.sessionKey }

// Provider is the model provider name refers to for account t: the account's
// own when it declares one by that name, else the instance's. t may be nil.
// A file not built by Parse gets its session keys here.
func (f *File) Provider(t *Account, name string) (Provider, bool) {
	if t != nil {
		if p, ok := t.Providers[name]; ok {
			p.sessionKey = chatGPTSessionKey(t, name)
			return p, true
		}
	}
	p, ok := f.Providers[name]
	p.sessionKey = chatGPTSessionKey(nil, name)
	return p, ok
}

// UnpricedModel is a configured model whose calls will be unpriced.
type UnpricedModel struct {
	// Setting names where the file sets it, as its errors would.
	Setting string
	Ref     ModelRef
}

// UnpricedModels lists the review, fallback and confidence models of the
// file's own settings and of every account's entries whose provider's API
// reports no cost, as anthropic's and openai's do not, and whose pricing
// has no price under the model as written: its id, or the floating alias
// that prices whatever model it selects. A repository's .kritika.yaml is
// read only when it is reviewed, so its models are not listed.
func (f *File) UnpricedModels() []UnpricedModel {
	var out []UnpricedModel
	check := func(where string, t *Account, r *Overrides) {
		for _, m := range append(r.Review.Model.refs(), namedRef{keyFallback, r.Review.Fallback},
			namedRef{keyScorer, r.Confidence.Model}, namedRef{keyScorerFallback, r.Confidence.Fallback}) {
			if m.ref == nil || *m.ref == "" {
				continue
			}
			p, ok := f.Provider(t, m.ref.Provider())
			if !ok || (p.Type != ProviderAnthropic && p.Type != ProviderOpenAI) {
				continue
			}
			if _, priced := p.Pricing[m.ref.Model()]; !priced {
				out = append(out, UnpricedModel{Setting: where + m.key, Ref: *m.ref})
			}
		}
	}
	check("", nil, &f.Defaults.Overrides)
	for i := range f.Accounts {
		a := &f.Accounts[i]
		if a.pattern != "" {
			check(a.pattern+".", a, &a.Overrides)
		}
		for j := range a.Repositories {
			check(a.Repositories[j].where+".", a, &a.Repositories[j].Overrides)
		}
	}
	return out
}

// ChatGPTProviders returns the chatgpt providers account t can use, the
// instance's and its own, by name. t may be nil.
func (f *File) ChatGPTProviders(t *Account) map[string]Provider {
	out := map[string]Provider{}
	names := slices.Collect(maps.Keys(f.Providers))
	if t != nil {
		names = slices.AppendSeq(names, maps.Keys(t.Providers))
	}
	for _, name := range names {
		if p, _ := f.Provider(t, name); p.Type == ProviderChatGPT {
			out[name] = p
		}
	}
	return out
}

// chatGPTSessionKey keeps the sign-ins of accounts' providers of one name
// apart from each other and from the instance's.
func chatGPTSessionKey(t *Account, name string) string {
	if t == nil {
		return name
	}
	return t.Key() + "/" + name
}

func (p *Provider) resolve(where, sessionKey string, s *secrets) error {
	p.sessionKey = sessionKey
	if p.Type == ProviderChatGPT {
		if !p.APIKey.empty() {
			return fmt.Errorf("configfile: %s.apiKey: a chatgpt provider uses a stored sign-in", where)
		}
		return nil
	}
	v, err := s.read(p.APIKey)
	if err != nil {
		return fmt.Errorf("configfile: %s.apiKey: %w", where, err)
	}
	p.apiKey = v
	p.headers = make(map[string]Secret, len(p.Headers))
	for _, name := range slices.Sorted(maps.Keys(p.Headers)) {
		h, err := p.Headers[name].resolve(s)
		if err != nil {
			return fmt.Errorf("configfile: %s.headers.%s: %w", where, name, err)
		}
		p.headers[name] = Secret{value: h}
	}
	return nil
}

// reservedHeaders are the headers of a provider request that apiKey, the
// SDKs or Go's HTTP client set, canonicalized.
var reservedHeaders = []string{
	"Authorization", "X-Api-Key", "Host", "Content-Type", "Content-Length", "Content-Encoding",
	"Transfer-Encoding", "Connection", "Accept", "Accept-Encoding",
}

// placeholderRe matches what reads as a placeholder in a header's value,
// and not a JSON object, which has quotes or colons.
var placeholderRe = regexp.MustCompile(`\{[^{}\s"':,]+\}`)

// validateHeaders checks the names and resolved values of a provider's
// headers. Errors never quote a value, which may be a secret.
func (p Provider) validateHeaders(where string) error {
	if p.Type == ProviderChatGPT && len(p.Headers) > 0 {
		return fmt.Errorf("configfile: %s.headers: a chatgpt provider takes none", where)
	}
	seen := map[string]bool{}
	for _, name := range slices.Sorted(maps.Keys(p.headers)) {
		hwhere := where + ".headers." + name
		canonical := http.CanonicalHeaderKey(name)
		v := p.headers[name].Value()
		switch {
		case !httpguts.ValidHeaderFieldName(name):
			return fmt.Errorf("configfile: %s: not a valid header name", hwhere)
		case slices.Contains(reservedHeaders, canonical),
			p.Type == ProviderOpenCode && canonical == http.CanonicalHeaderKey(model.OpenCodeSessionHeader):
			return fmt.Errorf("configfile: %s: kritika sets this header itself", hwhere)
		case seen[canonical]:
			return fmt.Errorf("configfile: %s: another header differs from it only in case", hwhere)
		case v == "":
			return fmt.Errorf("configfile: %s resolved to an empty value", hwhere)
		case !httpguts.ValidHeaderFieldValue(v):
			return fmt.Errorf("configfile: %s: the value has a character a header cannot carry", hwhere)
		case placeholderRe.MatchString(strings.ReplaceAll(v, model.SessionPlaceholder, "")):
			return fmt.Errorf("configfile: %s: %s is the only placeholder a value takes", hwhere, model.SessionPlaceholder)
		case !p.Headers[name].Ref.empty() && strings.Contains(v, model.SessionPlaceholder):
			// Masking knows a secret as it is configured, not as a step
			// fills it in.
			return fmt.Errorf("configfile: %s: %s goes in an inline value, not a secret", hwhere, model.SessionPlaceholder)
		}
		seen[canonical] = true
	}
	return nil
}

// validate checks a provider's type, endpoint, key, retries, headers and
// prices.
func (p Provider) validate(where string) error {
	if !p.Type.Valid() {
		return fmt.Errorf("configfile: %s.type must be %s, %s, %s, %s or %s, got %q",
			where, ProviderOpenRouter, ProviderOpenAI, ProviderAnthropic, ProviderOpenCode, ProviderChatGPT, p.Type)
	}
	if p.BaseURL != "" {
		if u, err := url.Parse(p.BaseURL); err != nil || u.Scheme == "" || u.Host == "" {
			return fmt.Errorf("configfile: %s.baseUrl %q must be an absolute URL", where, p.BaseURL)
		}
	}
	if p.Type != ProviderChatGPT && p.apiKey.Value() == "" {
		return fmt.Errorf("configfile: %s.apiKey resolved to an empty value", where)
	}
	if p.Type == ProviderChatGPT && len(p.Pricing) > 0 {
		return fmt.Errorf("configfile: %s.pricing: a chatgpt provider is covered by a plan", where)
	}
	if p.Retries < 0 || p.Retries > MaxProviderRetries {
		return fmt.Errorf("configfile: %s.retries must be between 0 and %d, got %d", where, MaxProviderRetries, p.Retries)
	}
	if err := p.validateHeaders(where); err != nil {
		return err
	}
	for _, id := range slices.Sorted(maps.Keys(p.Pricing)) {
		price := p.Pricing[id]
		for _, rate := range []float64{price.Input, price.Output, price.CacheRead, price.CacheWrite} {
			if rate < 0 || math.IsNaN(rate) || math.IsInf(rate, 0) {
				return fmt.Errorf("configfile: %s.pricing.%s: prices must be finite and nonnegative", where, id)
			}
		}
		if err := checkAlias(where+".pricing", p.Type, id); err != nil {
			return err
		}
	}
	return nil
}

// validateAccountProviders checks an account's own providers: names a model
// reference can carry, none the instance's providers already use, each valid.
func (f *File) validateAccountProviders(where string, t *Account) error {
	for _, name := range slices.Sorted(maps.Keys(t.Providers)) {
		pwhere := where + ".providers." + name
		if !nameRe.MatchString(name) {
			return fmt.Errorf("configfile: %s: a provider name must be lowercase alphanumerics and hyphens, 1 to 63 characters", pwhere)
		}
		if _, ok := f.Providers[name]; ok {
			return fmt.Errorf("configfile: %s: the instance declares a provider by that name", pwhere)
		}
		if err := t.Providers[name].validate(pwhere); err != nil {
			return err
		}
	}
	return nil
}
