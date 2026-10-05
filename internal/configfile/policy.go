package configfile

import (
	"reflect"
	"strings"
)

// Scope is where an admin writes a repository setting.
type Scope string

// Admin scopes, broadest first.
const (
	ScopeDefaults   Scope = "defaults"
	ScopeAccount    Scope = "account"
	ScopeRepository Scope = "repository"
)

// Policy names one repository setting and the scopes an admin writes it at.
// Sources reports where each setting it lists comes from. What the
// repository's own .kritika.yaml may do with a setting is repoconfig.Merge's
// to say. Instance-wide settings are not in it.
type Policy struct {
	// Key is the setting as the configuration spells it; a dotted key is
	// nested.
	Key    string
	Scopes []Scope
}

var (
	everyScope    = []Scope{ScopeDefaults, ScopeAccount, ScopeRepository}
	accountScopes = []Scope{ScopeDefaults, ScopeAccount}
)

// The keys validation names in its errors.
const (
	keySteps       = "agent.steps"
	keyOutput      = "agent.output"
	keyIncremental = "review.incremental"
)

// The keys the environment sets as well (instance.go).
const (
	keyModel     = "review.model"
	keyFallback  = "review.fallback"
	keyFeedback  = "review.feedback"
	keyScorer    = "confidence.model"
	keyThreshold = "confidence.threshold"
	keyRisk      = "confidence.risk"
	keySettle    = "trigger.settle"
)

// Policies is the table.
var Policies = []Policy{
	{Key: "enabled", Scopes: everyScope},
	{Key: keyModel, Scopes: everyScope},
	{Key: keyFallback, Scopes: everyScope},
	{Key: keyFeedback, Scopes: everyScope},
	{Key: "review.fixes", Scopes: everyScope},
	{Key: "review.approve", Scopes: everyScope},
	{Key: keyIncremental, Scopes: everyScope},
	{Key: keyScorer, Scopes: everyScope},
	{Key: keyThreshold, Scopes: everyScope},
	{Key: keyRisk, Scopes: everyScope},
	{Key: "confidence.instructions", Scopes: everyScope},
	{Key: "trigger.include", Scopes: everyScope},
	{Key: "trigger.exclude", Scopes: everyScope},
	{Key: keySettle, Scopes: everyScope},
	{Key: "trigger.limit", Scopes: everyScope},
	{Key: "trigger.lines", Scopes: everyScope},
	{Key: "comments", Scopes: everyScope},
	{Key: "rules", Scopes: everyScope},
	{Key: "context", Scopes: everyScope},
	{Key: "ignore", Scopes: everyScope},
	{Key: keySteps, Scopes: everyScope},
	{Key: keyOutput, Scopes: everyScope},
	{Key: "agent.tokens", Scopes: everyScope},
	{Key: "agent.timeout", Scopes: everyScope},
	{Key: "agent.commands", Scopes: everyScope},
	{Key: "agent.commandTimeout", Scopes: everyScope},
	{Key: "limits", Scopes: accountScopes},
}

// SpecValue is the value spec, one scope's settings (a *Defaults, *Account or
// *Repository), writes for key, found by the names the configuration
// spells, and whether the scope has the key at all. A nested key under an
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
