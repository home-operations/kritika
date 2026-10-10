package model

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

const (
	modelCatalogTTL     = 5 * time.Minute
	modelCatalogTimeout = 30 * time.Second
	// modelCatalogRetry is how long a catalog that failed to refresh stays
	// in use before the next refresh.
	modelCatalogRetry = time.Minute
)

// Pinner is a Stepper that resolves floating aliases, ~<family>-latest,
// ahead of a step. A run's grant pins its models once, so each of its
// steps, whichever replica serves it, goes to the same model.
type Pinner interface {
	// Pin returns id with a floating alias pinned to the model it selects
	// now for effort, as ~<family>-latest@<model>. A step on the pinned
	// alias sends that model and, when pricing has no entry for it, prices
	// it under the alias. Any other id comes back as it is.
	Pin(ctx context.Context, id string, effort Effort) (string, error)
}

// Floating reports whether id is meant as a floating alias.
func Floating(id string) bool { return strings.HasPrefix(id, "~") }

// TakesAliases reports whether providers of type p resolve floating
// aliases; the others would send one as a model ID.
func (p ProviderType) TakesAliases() bool {
	return p == ProviderAnthropic || p == ProviderOpenAI || p == ProviderChatGPT
}

// ValidAlias reports whether id is a floating alias as a configuration
// writes it, ~<family>-latest.
func ValidAlias(id string) bool {
	a, ok, err := parseAlias(id)
	return ok && err == nil && a.name == id
}

// ValidOpenRouterAlias reports whether id is one of OpenRouter's own
// floating aliases, ~<author>/<family>-latest, which OpenRouter resolves.
func ValidOpenRouterAlias(id string) bool {
	author, family, ok := strings.Cut(strings.TrimPrefix(id, "~"), "/")
	return Floating(id) && ok && author != "" && ValidAlias("~"+family)
}

type alias struct {
	// name is ~<family>-latest, and pinned the model a grant pinned it to,
	// "" for none.
	name, family, pinned string
}

// parseAlias parses id as a floating alias, ok false for a model ID.
func parseAlias(id string) (a alias, ok bool, err error) {
	rest, floating := strings.CutPrefix(id, "~")
	if !floating {
		return alias{}, false, nil
	}
	name, pinned, _ := strings.Cut(rest, "@")
	family, latest := strings.CutSuffix(name, "-latest")
	if !latest || family == "" || strings.Contains(family, "/") {
		return alias{}, true, fmt.Errorf("model: floating alias %q must be ~<family>-latest", id)
	}
	return alias{name: "~" + name, family: family, pinned: pinned}, true, nil
}

// resolve returns the model id names, and the floating alias that named
// it, "" for none: a model ID itself, a pinned alias's model, or what
// latest selects for an alias's family now.
func resolve(id string, latest func(family string) (string, error)) (model, aliasName string, err error) {
	a, ok, err := parseAlias(id)
	if !ok || err != nil {
		return id, "", err
	}
	if a.pinned != "" {
		return a.pinned, a.name, nil
	}
	model, err = latest(a.family)
	return model, a.name, err
}

// pin is Pinner.Pin over latest.
func pin(id string, latest func(family string) (string, error)) (string, error) {
	model, name, err := resolve(id, latest)
	if err != nil || name == "" {
		return id, err
	}
	return name + "@" + model, nil
}

func latestVersion(ids []string, family string) string {
	var latest string
	var version [3]int
	for _, id := range ids {
		v, ok := familyVersion(id, family)
		if ok && (latest == "" || slices.Compare(v[:], version[:]) > 0) {
			latest, version = id, v
		}
	}
	return latest
}

// familyVersion reads the version of a <family>-<version> or a
// gpt-<version>-<family> slug.
func familyVersion(slug, family string) ([3]int, bool) {
	var version [3]int
	raw, ok := strings.CutPrefix(slug, family+"-")
	if after, gpt := strings.CutPrefix(slug, "gpt-"); !ok && gpt {
		raw, ok = strings.CutSuffix(after, "-"+family)
	}
	if !ok {
		return version, false
	}
	parts := strings.Split(raw, ".")
	if len(parts) > len(version) {
		return version, false
	}
	for i, part := range parts {
		n, err := strconv.Atoi(part)
		if err != nil || n < 0 || strings.HasPrefix(part, "+") {
			return version, false
		}
		version[i] = n
	}
	return version, version[0] > 0
}

type modelCatalog[T any] struct {
	mu        sync.Mutex
	key       string
	models    []T
	fetched   bool
	expiresAt time.Time
	fetch     singleflight.Group
}

// get returns key's catalog. Only a key with none yet waits for a fetch:
// an expired catalog is returned while one fetch refreshes it, and stays
// in use while refreshing fails, so neither a slow catalog nor its outage
// holds up alias steps. Concurrent callers share one bounded fetch, which
// keeps running if a caller stops waiting.
func (c *modelCatalog[T]) get(
	ctx context.Context, key string, now func() time.Time, fetch func(context.Context) ([]T, error),
) ([]T, error) {
	models, cached, fresh := c.cached(key, now)
	if fresh {
		return models, nil
	}
	fetched := c.fetch.DoChan(key, func() (any, error) {
		if models, _, fresh := c.cached(key, now); fresh {
			return models, nil
		}
		fetchCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), modelCatalogTimeout)
		defer cancel()
		models, err := fetch(fetchCtx)
		c.mu.Lock()
		defer c.mu.Unlock()
		switch {
		case err == nil:
			c.key, c.models, c.fetched, c.expiresAt = key, models, true, now().Add(modelCatalogTTL)
		case c.fetched && c.key == key:
			c.expiresAt = now().Add(modelCatalogRetry)
		}
		return models, err
	})
	if cached {
		return models, nil
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case r := <-fetched:
		if r.Err != nil {
			return nil, r.Err
		}
		return r.Val.([]T), nil
	}
}

func (c *modelCatalog[T]) cached(key string, now func() time.Time) (models []T, cached, fresh bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	cached = c.fetched && c.key == key
	return c.models, cached, cached && now().Before(c.expiresAt)
}
