package configfile

import (
	"reflect"
	"strings"
)

// The keys validation names in its errors.
const (
	keySteps   = "agent.steps"
	keyOutput  = "agent.output"
	keyParts   = "agent.parts"
	keyFetches = "agent.fetches"
)

// Instance default keys reported by FileLayer.
const (
	keyModel          = "review.model"
	keyFallback       = "review.fallback"
	keyEffort         = "review.effort"
	keyApprove        = "review.approve"
	keyFixes          = "review.fixes"
	keyIncremental    = "review.incremental"
	keyDiagram        = "review.diagram"
	keyCost           = "review.cost"
	keyReplies        = "review.replies"
	keyScorer         = "confidence.model"
	keyScorerFallback = "confidence.fallback"
	keyScorerEffort   = "confidence.effort"
	keyThreshold      = "confidence.threshold"
	keyGate           = "confidence.gate"
	keyRisk           = "confidence.risk"
	keyFloors         = "confidence.floors"
	keyConfidenceWhen = "confidence.when"
	keyLimit          = "trigger.limit"
)

// Policies are the repository settings an admin writes at the file's root,
// as the configuration spells them; a dotted key is nested. Sources
// reports where each comes from. What the repository's own .kritika.yaml
// may do with a setting is repoconfig.Merge's to say. Instance-wide
// settings are not in it.
var Policies = []string{
	"enabled",
	keyModel,
	keyFallback,
	keyEffort,
	keyFixes,
	keyApprove,
	keyIncremental,
	keyDiagram,
	keyCost,
	keyReplies,
	keyScorer,
	keyScorerFallback,
	keyScorerEffort,
	keyThreshold,
	keyGate,
	keyRisk,
	"confidence.instructions",
	keyFloors,
	keyConfidenceWhen,
	"trigger.include",
	"trigger.exclude",
	keyLimit,
	"comments",
	"comments.dashboard",
	"rules",
	"context",
	"ignore",
	"skills.paths",
	"skills.scope",
	"review.agents",
	keySteps,
	keyOutput,
	"agent.tokens",
	"agent.prompt",
	keyParts,
	"agent.timeout",
	"agent.commandTimeout",
	keyFetches,
	"limits",
}

// SpecValue is the value spec, the file's settings (a *Defaults or
// *Overrides), writes for key, found by the names the configuration
// spells, and whether the spec has the key at all. A nested key under an
// unset block is that block's zero value.
func SpecValue(spec any, key string) (any, bool) {
	v := reflect.ValueOf(spec)
	for part := range strings.SplitSeq(key, ".") {
		for v.Kind() == reflect.Pointer {
			if v.IsNil() {
				v = reflect.Zero(v.Type().Elem())
			} else {
				v = v.Elem()
			}
		}
		if v.Kind() != reflect.Struct {
			return nil, false
		}
		f, ok := fieldByKey(v, part)
		if !ok {
			return nil, false
		}
		v = f
	}
	return v.Interface(), true
}

// fieldByKey finds the field of struct v a configuration key names,
// looking into inline blocks.
func fieldByKey(v reflect.Value, key string) (reflect.Value, bool) {
	t := v.Type()
	for i := range t.NumField() {
		name, opts, _ := strings.Cut(t.Field(i).Tag.Get("yaml"), ",")
		if opts == "inline" {
			if f, ok := fieldByKey(v.Field(i), key); ok {
				return f, true
			}
			continue
		}
		if name != "" && name == key {
			return v.Field(i), true
		}
	}
	return reflect.Value{}, false
}
