package configfile

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/home-operations/kritika/internal/review"
)

// The environment may set some of the file's keys: one model provider, the
// review and fallback models, feedback, the confidence model, threshold
// and risk, and settle every account and repository inherits, and the
// embedder. Each wins over the file's.

// Environment variable prefixes of the keys the environment may set.
const (
	providerEnvPrefix   = "KRITIKA_PROVIDERS_"
	reviewEnvPrefix     = "KRITIKA_REVIEW_"
	confidenceEnvPrefix = "KRITIKA_CONFIDENCE_"
	triggerEnvPrefix    = "KRITIKA_TRIGGER_"
	embeddingEnvPrefix  = "KRITIKA_EMBEDDING_"
)

// settingEnvPrefixes start the variables that set the file's own
// repository settings.
var settingEnvPrefixes = []string{reviewEnvPrefix, confidenceEnvPrefix, triggerEnvPrefix}

// reviewWorkersEnv shares reviewEnvPrefix and is no key of the file: it is
// how many review jobs a replica runs at once (internal/config).
const reviewWorkersEnv = "KRITIKA_REVIEW_WORKERS"

// DefaultEnvProvider names the environment's provider when
// KRITIKA_PROVIDERS_NAME is unset.
const DefaultEnvProvider = "openrouter"

// overlayProviderEnv declares the one provider the environment may: it
// replaces the file's provider of its name whole, or joins them. Its type
// defaults to its name when that is a provider type. It returns the
// provider's name, "" when no KRITIKA_PROVIDERS_* variable is set; a
// variable that names no key is an error.
func overlayProviderEnv(providers *map[string]Provider, environ []string) (string, error) {
	name, p := DefaultEnvProvider, Provider{}
	set := false
	for _, kv := range environ {
		env, value, _ := strings.Cut(kv, "=")
		key, ok := strings.CutPrefix(env, providerEnvPrefix)
		if !ok {
			continue
		}
		set = true
		switch key {
		case "NAME":
			name = value
		case "TYPE":
			p.Type = ProviderType(value)
		case "BASE_URL":
			p.BaseURL = value
		case "API_KEY":
			p.APIKey = SecretRef{Env: env}
		case "RETRIES":
			n, err := strconv.Atoi(value)
			if err != nil {
				return "", fmt.Errorf("configfile: environment variable %s must be a whole number, got %q", env, value)
			}
			p.Retries = n
		default:
			return "", fmt.Errorf("configfile: environment variable %s names no provider setting", env)
		}
	}
	if !set {
		return "", nil
	}
	if p.Type == "" && ProviderType(name).Valid() {
		p.Type = ProviderType(name)
	}
	if *providers == nil {
		*providers = map[string]Provider{}
	}
	(*providers)[name] = p
	return name, nil
}

// overlayDefaultsEnv sets the file's own review, confidence and trigger
// keys from KRITIKA_REVIEW_*, KRITIKA_CONFIDENCE_* and KRITIKA_TRIGGER_*,
// recording each key it sets in from.
func overlayDefaultsEnv(d *Defaults, environ []string, from map[string]bool) error {
	for _, kv := range environ {
		env, value, _ := strings.Cut(kv, "=")
		if env == reviewWorkersEnv || !slices.ContainsFunc(settingEnvPrefixes, func(p string) bool { return strings.HasPrefix(env, p) }) {
			continue
		}
		var path string
		switch env {
		case reviewEnvPrefix + "MODEL":
			ref := ModelRef(value)
			d.Review.Model, path = &ref, keyModel
		case reviewEnvPrefix + "FALLBACK":
			ref := ModelRef(value)
			d.Review.Fallback, path = &ref, keyFallback
		case reviewEnvPrefix + "FEEDBACK":
			d.Review.Feedback, path = &value, keyFeedback
		case reviewEnvPrefix + "APPROVE":
			approve, err := strconv.ParseBool(value)
			if err != nil {
				return fmt.Errorf("configfile: environment variable %s must be true or false, got %q", env, value)
			}
			d.Review.Approve, path = &approve, keyApprove
		case reviewEnvPrefix + "FIXES":
			fixes, err := strconv.ParseBool(value)
			if err != nil {
				return fmt.Errorf("configfile: environment variable %s must be true or false, got %q", env, value)
			}
			d.Review.Fixes, path = &fixes, keyFixes
		case reviewEnvPrefix + "INCREMENTAL":
			n, err := strconv.Atoi(value)
			if err != nil {
				return fmt.Errorf("configfile: environment variable %s must be a whole number, got %q", env, value)
			}
			d.Review.Incremental, path = &n, keyIncremental
		case reviewEnvPrefix + "DIAGRAM":
			diagram, err := strconv.ParseBool(value)
			if err != nil {
				return fmt.Errorf("configfile: environment variable %s must be true or false, got %q", env, value)
			}
			d.Review.Diagram, path = &diagram, keyDiagram
		case confidenceEnvPrefix + "MODEL":
			ref := ModelRef(value)
			d.Confidence.Model, path = &ref, keyScorer
		case confidenceEnvPrefix + "THRESHOLD":
			n, err := strconv.Atoi(value)
			if err != nil {
				return fmt.Errorf("configfile: environment variable %s must be a whole number, got %q", env, value)
			}
			d.Confidence.Threshold, path = &n, keyThreshold
		case confidenceEnvPrefix + "GATE":
			gate, err := strconv.ParseBool(value)
			if err != nil {
				return fmt.Errorf("configfile: environment variable %s must be true or false, got %q", env, value)
			}
			d.Confidence.Gate, path = &gate, keyGate
		case confidenceEnvPrefix + "RISK":
			risk := review.Risk(value)
			d.Confidence.Risk, path = &risk, keyRisk
		case triggerEnvPrefix + "SETTLE":
			settle, err := time.ParseDuration(value)
			if err != nil {
				return fmt.Errorf("configfile: environment variable %s: %w", env, err)
			}
			d.Trigger.Settle, path = &settle, keySettle
		case triggerEnvPrefix + "LIMIT":
			n, err := strconv.Atoi(value)
			if err != nil {
				return fmt.Errorf("configfile: environment variable %s must be a whole number, got %q", env, value)
			}
			d.Trigger.Limit, path = &n, keyLimit
		default:
			return fmt.Errorf("configfile: environment variable %s names no setting", env)
		}
		from[path] = true
	}
	return nil
}

// overlayEmbeddingEnv sets the file's embedder key by key from
// KRITIKA_EMBEDDING_*, starting one when the file has none, and records in
// from that the environment set it.
func overlayEmbeddingEnv(e **Embedding, environ []string, from map[string]bool) error {
	for _, kv := range environ {
		env, value, _ := strings.Cut(kv, "=")
		key, ok := strings.CutPrefix(env, embeddingEnvPrefix)
		if !ok {
			continue
		}
		if *e == nil {
			*e = &Embedding{}
		}
		switch key {
		case "MODEL":
			(*e).Ref = ModelRef(value)
		case "DIMS":
			dims, err := strconv.Atoi(value)
			if err != nil {
				return fmt.Errorf("configfile: environment variable %s must be a whole number, got %q", env, value)
			}
			(*e).Dims = dims
		default:
			return fmt.Errorf("configfile: environment variable %s names no embedding setting", env)
		}
		from["embedding"] = true
	}
	return nil
}

// FileLayer is what the configuration file and its environment set for the
// instance: its providers, the models and the other settings every
// repository inherits, and the embedder, each with where it comes from.
type FileLayer struct {
	Providers map[string]FileProvider
	Review    FileValue
	Fallback  FileValue
	// Defaults are the other settings it writes that the environment may
	// set too: feedback, approve, fixes, incremental, diagram, the confidence
	// model, threshold, gate and risk, settle and limit, in that order,
	// by their policy keys.
	Defaults  []FileDefault
	Embedding *FileEmbedding
}

// FileDefault is one of the settings the file or the environment sets
// other than the models, by its policy key.
type FileDefault struct {
	Key string
	FileValue
}

// FileProvider is one provider the file or the environment declares.
type FileProvider struct {
	Type    ProviderType
	BaseURL string
	Source  Source
}

// FileValue is a setting the file or the environment sets; the zero
// FileValue is none.
type FileValue struct {
	Value  string
	Source Source
}

// FileEmbedding is the embedder the file or the environment sets.
type FileEmbedding struct {
	Model  string
	Dims   int
	Source Source
}

// FileLayer returns what f sets for the instance, each with where it comes
// from.
func (f *File) FileLayer() FileLayer {
	source := func(key string) Source {
		if f.envKeys[key] {
			return SourceEnv
		}
		return SourceFile
	}
	var out FileLayer
	for name, p := range f.Providers {
		if out.Providers == nil {
			out.Providers = map[string]FileProvider{}
		}
		src := SourceFile
		if name == f.envProvider {
			src = SourceEnv
		}
		out.Providers[name] = FileProvider{Type: p.Type, BaseURL: p.BaseURL, Source: src}
	}
	if r := f.Defaults.Review.Model; r != nil {
		out.Review = FileValue{Value: string(*r), Source: source(keyModel)}
	}
	if r := f.Defaults.Review.Fallback; r != nil {
		out.Fallback = FileValue{Value: string(*r), Source: source(keyFallback)}
	}
	d := f.Defaults
	for _, x := range []struct {
		key, value string
		set        bool
	}{
		{keyFeedback, deref(d.Review.Feedback), d.Review.Feedback != nil},
		{keyApprove, strconv.FormatBool(deref(d.Review.Approve)), d.Review.Approve != nil},
		{keyFixes, strconv.FormatBool(deref(d.Review.Fixes)), d.Review.Fixes != nil},
		{keyIncremental, strconv.Itoa(deref(d.Review.Incremental)), d.Review.Incremental != nil},
		{keyDiagram, strconv.FormatBool(deref(d.Review.Diagram)), d.Review.Diagram != nil},
		{keyScorer, string(deref(d.Confidence.Model)), d.Confidence.Model != nil},
		{keyThreshold, strconv.Itoa(deref(d.Confidence.Threshold)), d.Confidence.Threshold != nil},
		{keyGate, strconv.FormatBool(deref(d.Confidence.Gate)), d.Confidence.Gate != nil},
		{keyRisk, string(deref(d.Confidence.Risk)), d.Confidence.Risk != nil},
		{keySettle, durationValue(d.Trigger.Settle), d.Trigger.Settle != nil},
		{keyLimit, strconv.Itoa(deref(d.Trigger.Limit)), d.Trigger.Limit != nil},
	} {
		if x.set {
			out.Defaults = append(out.Defaults, FileDefault{Key: x.key, Value: x.value, Source: source(x.key)})
		}
	}
	if e := f.Embedding; e != nil {
		out.Embedding = &FileEmbedding{Model: string(e.Ref), Dims: e.Dims, Source: source("embedding")}
	}
	return out
}

// deref is *p, the zero value for nil.
func deref[T any](p *T) T {
	if p == nil {
		var zero T
		return zero
	}
	return *p
}

func durationValue(d *time.Duration) string {
	if d == nil {
		return ""
	}
	return d.String()
}
