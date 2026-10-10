package model

import (
	"context"
	"fmt"
	"strings"

	"github.com/openai/openai-go/v3/option"
)

type chatGPTCatalogModel struct {
	Slug       string `json:"slug"`
	Visibility string `json:"visibility"`
}

// Pin implements Pinner. While the plan is paused, nothing is sent on it,
// the catalog request included.
func (c *ChatGPT) Pin(ctx context.Context, id string, _ Effort) (string, error) {
	if !Floating(id) {
		return id, nil
	}
	if err := c.pausedError(); err != nil {
		return "", err
	}
	token, err := c.tokens.Token(ctx)
	if err != nil {
		return "", fmt.Errorf("model: chatgpt: %w", err)
	}
	pinned, err := pin(id, func(family string) (string, error) { return c.resolveLatest(ctx, token, family) })
	if err != nil {
		return "", &chatGPTError{err: err, text: strings.ReplaceAll(err.Error(), token, "***")}
	}
	return pinned, nil
}

func (c *ChatGPT) resolveLatest(ctx context.Context, token, family string) (string, error) {
	models, err := c.catalog(ctx, token)
	if err != nil {
		return "", err
	}
	if latest := latestVersion(models, family); latest != "" {
		return latest, nil
	}
	return "", fmt.Errorf("model: chatgpt: no versioned %s model is available to this account", family)
}

// catalog classifies a failed fetch as a step's refusal is classified,
// once for all the callers that share it.
func (c *ChatGPT) catalog(ctx context.Context, token string) ([]string, error) {
	return c.catalogCache.get(ctx, token, c.now, func(fetchCtx context.Context) ([]string, error) {
		var catalog struct {
			Models []chatGPTCatalogModel `json:"models"`
		}
		if err := c.client.Get(fetchCtx, "models", nil, &catalog, option.WithAPIKey(token)); err != nil {
			return nil, c.refused(fetchCtx, token, fmt.Errorf("model: chatgpt: model catalog: %w", err))
		}
		var models []string
		for _, m := range catalog.Models {
			if m.Visibility == "list" {
				models = append(models, m.Slug)
			}
		}
		return models, nil
	})
}
