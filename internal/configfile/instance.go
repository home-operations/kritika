package configfile

import (
	"strconv"
)

// FileLayer is what the configuration file sets for the instance: its
// providers, the models and the other configuration values that every
// repository inherits, and the embedder, each with where it comes from.
type FileLayer struct {
	Providers map[string]FileProvider
	Review    FileValue
	Fallback  FileValue
	// Defaults are the other configuration values that it writes: review
	// effort, approve, fixes, incremental, diagram, the confidence model,
	// fallback, effort, threshold, gate and risk, and limit, in that order, by
	// their policy keys.
	Defaults  []FileDefault
	Embedding *FileEmbedding
}

// FileDefault is one of the configuration values that the file sets
// other than the models, by its policy key.
type FileDefault struct {
	Key string
	FileValue
}

// FileProvider is one provider the file declares.
type FileProvider struct {
	Type    ProviderType
	BaseURL string
	Source  Source
}

// FileValue is a configuration value that the file sets. The zero
// FileValue is none.
type FileValue struct {
	Value  string
	Source Source
}

// FileEmbedding is the embedder the file sets.
type FileEmbedding struct {
	Model  string
	Dims   int
	Source Source
}

// FileLayer returns what f sets for the instance, each with where it comes
// from.
func (f *File) FileLayer() FileLayer {
	var out FileLayer
	for name, p := range f.Providers {
		if out.Providers == nil {
			out.Providers = map[string]FileProvider{}
		}
		out.Providers[name] = FileProvider{Type: p.Type, BaseURL: p.BaseURL, Source: SourceFile}
	}
	if r := f.Defaults.Review.Model; r != nil {
		out.Review = FileValue{Value: r.String(), Source: SourceFile}
	}
	if r := f.Defaults.Review.Fallback; r != nil {
		out.Fallback = FileValue{Value: string(*r), Source: SourceFile}
	}
	d := f.Defaults
	for _, x := range []struct {
		key, value string
		set        bool
	}{
		{keyEffort, string(deref(d.Review.Effort)), d.Review.Effort != nil},
		{keyApprove, deref(d.Review.Approve).String(), d.Review.Approve != nil},
		{keyFixes, strconv.FormatBool(deref(d.Review.Fixes)), d.Review.Fixes != nil},
		{keyIncremental, strconv.Itoa(deref(d.Review.Incremental)), d.Review.Incremental != nil},
		{keyDiagram, strconv.FormatBool(deref(d.Review.Diagram)), d.Review.Diagram != nil},
		{keyCost, strconv.FormatBool(deref(d.Review.Cost)), d.Review.Cost != nil},
		{keyReplies, strconv.FormatBool(deref(d.Review.Replies)), d.Review.Replies != nil},
		{keyScorer, string(deref(d.Confidence.Model)), d.Confidence.Model != nil},
		{keyScorerFallback, string(deref(d.Confidence.Fallback)), d.Confidence.Fallback != nil},
		{keyScorerEffort, string(deref(d.Confidence.Effort)), d.Confidence.Effort != nil},
		{keyThreshold, strconv.Itoa(deref(d.Confidence.Threshold)), d.Confidence.Threshold != nil},
		{keyGate, deref(d.Confidence.Gate).String(), d.Confidence.Gate != nil},
		{keyRisk, string(deref(d.Confidence.Risk)), d.Confidence.Risk != nil},
		{keyConfidenceWhen, whenLabel(d.Confidence.When), d.Confidence.When != nil},
		{keyLimit, strconv.Itoa(deref(d.Trigger.Limit)), d.Trigger.Limit != nil},
		{keySettle, deref(d.Trigger.Settle).String(), d.Trigger.Settle != nil},
	} {
		if x.set {
			out.Defaults = append(out.Defaults, FileDefault{Key: x.key, Value: x.value, Source: SourceFile})
		}
	}
	if e := f.Embedding; e != nil {
		out.Embedding = &FileEmbedding{Model: string(e.Ref), Dims: e.Dims, Source: SourceFile}
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
