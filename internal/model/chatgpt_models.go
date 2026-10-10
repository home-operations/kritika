package model

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/openai/openai-go/v3/option"
)

// chatGPTCatalogTTL is how long a fetched model catalog is used, and
// chatGPTCatalogTimeout bounds fetching it, a small listing that should not
// hold alias steps for a whole step's timeout.
const (
	chatGPTCatalogTTL     = 5 * time.Minute
	chatGPTCatalogTimeout = 30 * time.Second
)

type chatGPTCatalogModel struct {
	Slug       string `json:"slug"`
	Visibility string `json:"visibility"`
}

func (c *ChatGPT) resolveLatest(ctx context.Context, token, family string) (string, error) {
	models, err := c.catalog(ctx, token)
	if err != nil {
		return "", err
	}
	var latest string
	var version [3]int
	for _, m := range models {
		v, ok := familyVersion(m.Slug, family)
		if !ok || m.Visibility != "list" {
			continue
		}
		if latest == "" || slices.Compare(v[:], version[:]) > 0 {
			latest, version = m.Slug, v
		}
	}
	if latest == "" {
		return "", fmt.Errorf("model: chatgpt: no versioned %s model is available to this account", family)
	}
	return latest, nil
}

// catalog returns the models token's account can use. Concurrent steps
// share one fetch, which outlives a step that stops waiting for it, rather
// than queueing on a lock held for its whole duration.
func (c *ChatGPT) catalog(ctx context.Context, token string) ([]chatGPTCatalogModel, error) {
	c.catalogMu.Lock()
	models, fresh := c.catalogModels, c.catalogToken == token && c.now().Before(c.catalogExpiresAt)
	c.catalogMu.Unlock()
	if fresh {
		return models, nil
	}
	fetched := c.catalogFetch.DoChan(token, func() (any, error) {
		var catalog struct {
			Models []chatGPTCatalogModel `json:"models"`
		}
		if err := c.client.Get(context.WithoutCancel(ctx), "models", nil, &catalog,
			option.WithAPIKey(token), option.WithRequestTimeout(chatGPTCatalogTimeout)); err != nil {
			return nil, err
		}
		c.catalogMu.Lock()
		defer c.catalogMu.Unlock()
		c.catalogModels, c.catalogToken, c.catalogExpiresAt = catalog.Models, token, c.now().Add(chatGPTCatalogTTL)
		return catalog.Models, nil
	})
	select {
	case <-ctx.Done():
		return nil, fmt.Errorf("model: chatgpt: model catalog: %w", ctx.Err())
	case r := <-fetched:
		if r.Err != nil {
			return nil, c.refused(ctx, token, r.Err)
		}
		return r.Val.([]chatGPTCatalogModel), nil
	}
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
