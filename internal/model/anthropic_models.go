package model

import (
	"context"
	"fmt"

	"github.com/anthropics/anthropic-sdk-go"
)

// Pin implements Pinner.
func (a *Anthropic) Pin(ctx context.Context, id string, effort Effort) (string, error) {
	return pin(id, func(family string) (string, error) { return a.resolveLatest(ctx, family, effort) })
}

// resolveLatest selects the first active model of family that takes
// effort: the catalog lists newer models first, and a release date may be
// unknown.
func (a *Anthropic) resolveLatest(ctx context.Context, family string, effort Effort) (string, error) {
	models, err := a.catalog(ctx)
	if err != nil {
		return "", err
	}
	for _, m := range models {
		if string(m.Line) == family && m.Lifecycle == anthropic.ModelInfoLifecycleActive && takesEffort(m, effort) {
			return m.ID, nil
		}
	}
	if effort != "" {
		return "", fmt.Errorf("model: anthropic: no active %s model that takes %s effort is available to this API key", family, effort)
	}
	return "", fmt.Errorf("model: anthropic: no active %s model is available to this API key", family)
}

// takesEffort reports whether m takes effort as the Messages API is sent
// it (anthropicEffort). Only a catalog entry that says a model lacks it
// rules the model out: a gateway's catalog may say nothing.
func takesEffort(m anthropic.ModelInfo, effort Effort) bool {
	c := m.Capabilities.Effort
	if effort == "" {
		return true
	}
	if c.JSON.Supported.Valid() && !c.Supported {
		return false
	}
	var level anthropic.CapabilitySupport
	switch anthropicEffort(effort) {
	case anthropic.OutputConfigEffortLow:
		level = c.Low
	case anthropic.OutputConfigEffortMedium:
		level = c.Medium
	case anthropic.OutputConfigEffortHigh:
		level = c.High
	case anthropic.OutputConfigEffortXhigh:
		level = c.Xhigh
	case anthropic.OutputConfigEffortMax:
		level = c.Max
	}
	return !level.JSON.Supported.Valid() || level.Supported
}

func (a *Anthropic) catalog(ctx context.Context) ([]anthropic.ModelInfo, error) {
	models, err := a.catalogCache.get(ctx, "", a.now, func(fetchCtx context.Context) ([]anthropic.ModelInfo, error) {
		pages := a.client.Models.ListAutoPaging(fetchCtx, anthropic.ModelListParams{})
		var models []anthropic.ModelInfo
		listed := map[string]bool{}
		for m := range pages.All() {
			// A gateway that ignores the page cursor serves one page
			// again and again.
			if listed[m.ID] {
				return nil, fmt.Errorf("the catalog lists %s twice, so its pages do not follow their cursor", m.ID)
			}
			listed[m.ID] = true
			models = append(models, m)
		}
		if err := pages.Err(); err != nil {
			return nil, err
		}
		return models, nil
	})
	if err != nil {
		return nil, fmt.Errorf("model: anthropic: model catalog: %w", err)
	}
	return models, nil
}
