package model

import (
	"context"
	"fmt"
)

// Pin implements Pinner. OpenRouter and OpenCode select models their own
// way, so they take no aliases.
func (o *OpenAI) Pin(ctx context.Context, id string, _ Effort) (string, error) {
	if o.openRouter || o.openCode {
		return id, nil
	}
	return pin(id, func(family string) (string, error) { return o.resolveLatest(ctx, family) })
}

func (o *OpenAI) resolveLatest(ctx context.Context, family string) (string, error) {
	models, err := o.catalogCache.get(ctx, "", o.now, func(fetchCtx context.Context) ([]string, error) {
		catalog, err := o.client.Models.List(fetchCtx)
		if err != nil {
			return nil, err
		}
		models := make([]string, 0, len(catalog.Data))
		for _, m := range catalog.Data {
			models = append(models, m.ID)
		}
		return models, nil
	})
	if err != nil {
		return "", fmt.Errorf("model: openai: model catalog: %w", openAIError(err))
	}
	if latest := latestVersion(models, family); latest != "" {
		return latest, nil
	}
	return "", fmt.Errorf("model: openai: no versioned %s model is available to this API key", family)
}
