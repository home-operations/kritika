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

const chatGPTCatalogTTL = 5 * time.Minute

type chatGPTCatalogModel struct {
	Slug       string `json:"slug"`
	Visibility string `json:"visibility"`
}

func (c *ChatGPT) resolveLatest(ctx context.Context, token, family string) (string, error) {
	c.catalogMu.Lock()
	defer c.catalogMu.Unlock()
	if c.catalogToken != token || !c.now().Before(c.catalogExpiresAt) {
		var catalog struct {
			Models []chatGPTCatalogModel `json:"models"`
		}
		if err := c.client.Get(ctx, "models", nil, &catalog, option.WithAPIKey(token)); err != nil {
			return "", c.refused(ctx, token, err)
		}
		c.catalogModels, c.catalogToken, c.catalogExpiresAt = catalog.Models, token, c.now().Add(chatGPTCatalogTTL)
	}
	var latest string
	var version [3]int
	for _, m := range c.catalogModels {
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

func familyVersion(slug, family string) ([3]int, bool) {
	var version [3]int
	var raw string
	if after, ok := strings.CutPrefix(slug, "gpt-"); ok {
		var found bool
		raw, found = strings.CutSuffix(after, "-"+family)
		if !found {
			return version, false
		}
	} else {
		var found bool
		raw, found = strings.CutPrefix(slug, family+"-")
		if !found {
			return version, false
		}
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
