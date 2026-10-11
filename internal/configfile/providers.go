package configfile

import (
	"fmt"
	"maps"
	"net/url"
	"slices"
)

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
		for _, m := range []struct {
			key string
			ref *ModelRef
		}{
			{keyModel, r.Review.Model}, {keyFallback, r.Review.Fallback},
			{keyScorer, r.Confidence.Model}, {keyScorerFallback, r.Confidence.Fallback},
		} {
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
	return nil
}

// validate checks a provider's type, endpoint, key and prices.
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
	for _, id := range slices.Sorted(maps.Keys(p.Pricing)) {
		if price := p.Pricing[id]; price.Input < 0 || price.Output < 0 || price.CacheRead < 0 || price.CacheWrite < 0 {
			return fmt.Errorf("configfile: %s.pricing.%s: prices must not be negative", where, id)
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
