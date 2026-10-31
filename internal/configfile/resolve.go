package configfile

import (
	"cmp"
	"maps"
	"reflect"
	"slices"

	"github.com/home-operations/kritika/internal/review"
)

// Account returns the running account name on forge.
func (f *File) Account(forge Forge, name string) (*Account, bool) {
	key := AccountKey(forge, name)
	for i := range f.Accounts {
		if f.Accounts[i].Key() == key {
			return &f.Accounts[i], true
		}
	}
	return nil, false
}

// AccountByID returns the running account whose id is id.
func (f *File) AccountByID(id string) (*Account, bool) {
	for i := range f.Accounts {
		if f.Accounts[i].ID() == id {
			return &f.Accounts[i], true
		}
	}
	return nil, false
}

// Connection returns the running connection with the given name. Names are
// unique, so this is how the webhook listener turns a hook path into a webhook
// secret.
func (f *File) Connection(name string) (*Connection, bool) {
	for i := range f.Connections {
		if f.Connections[i].Name == name {
			return &f.Connections[i], true
		}
	}
	return nil, false
}

// ConnectionFor returns the running connection serving a, nil when none
// does.
func (f *File) ConnectionFor(a *Account) *Connection {
	for i := range f.Connections {
		if in := &f.Connections[i]; in.Forge == a.Forge && in.Serves(a.Name) {
			return in
		}
	}
	return nil
}

// Settings resolves the effective configuration of account a's repositories:
// kritika's defaults under the file's root, with the account's limits over
// the root's. The caller that has the checkout lays a repository's own
// .kritika.yaml over them.
func (f *File) Settings(a *Account) Settings {
	s := Settings{
		Enabled:     true,
		Ignore:      slices.Clone(DefaultIgnore),
		Agent:       DefaultAgent,
		Incremental: IncrementalSettings{MaxDeltaFiles: DefaultMaxDeltaFiles},
		Review:      Review{InlineComments: true, Dashboard: true},
		Confidence:  Confidence{Threshold: DefaultConfidenceThreshold, Risk: review.RiskLow},
		Skills:      Skills{Paths: slices.Clone(DefaultSkillPaths)},
		RuleFiles:   RuleFiles{Paths: slices.Clone(DefaultRuleFilePaths)},
		Providers:   f.providerNames(a),
	}
	s.apply(&f.Defaults.Overrides)
	s.Limits = s.Limits.overlay(f.Defaults.Limits)
	s.Limits = s.Limits.overlay(a.Limits)
	s.Limits.Concurrency = cmp.Or(s.Limits.Concurrency, DefaultConcurrency)
	return s
}

// Runs reports whether a repository of account a, known as t, is reviewed,
// polled and indexed. An archived repository never is: it is read-only
// until unarchived. A newly discovered repository stays off until enabled
// in the dashboard, whose choice takes precedence. A fork does not
// otherwise run, since an account can reach many forks it never meant
// kritika to spend on. Any other repository runs as the configuration says.
func (f *File) Runs(a *Account, t RepoTraits) bool {
	switch {
	case t.Archived:
		return false
	case t.TurnedOn != nil:
		return *t.TurnedOn
	case t.Fork:
		return false
	default:
		return f.Settings(a).Enabled
	}
}

// Source is the layer a setting's value comes from.
type Source string

// Sources of a setting.
const (
	// SourceDefault is kritika's built-in default.
	SourceDefault Source = "default"
	SourceEnv     Source = "env"
	SourceFile    Source = "file"
	// SourceDefaults is the file's own configuration, at its root, and
	// SourceAccount an account's entry.
	SourceDefaults Source = "defaults"
	SourceAccount  Source = "account"
)

// Sources says, for each setting the policy table lets an admin write,
// where Settings(a) takes it from: a's entry for its limits when the entry
// writes some, the file's root when it writes the setting, or the built-in
// default.
func (f *File) Sources(a *Account) map[string]Source {
	out := map[string]Source{}
	for _, key := range Policies {
		out[key] = SourceDefault
		if v, ok := SpecValue(&f.Defaults, key); ok && !reflect.ValueOf(v).IsZero() {
			out[key] = SourceDefaults
		}
	}
	if a.Limits != (LimitsSpec{}) {
		out["limits"] = SourceAccount
	}
	return out
}

// apply lays the file's overrides over s: a field it writes replaces s's,
// and its ignore globs are added to s's.
func (s *Settings) apply(o *Overrides) {
	if o.Enabled != nil {
		s.Enabled = *o.Enabled
	}
	if o.Review.Model != nil {
		s.Models.Review = o.Review.Model.Default
	}
	if o.Review.Fallback != nil {
		s.Models.Fallback = *o.Review.Fallback
	}
	if o.Review.Effort != nil {
		s.Models.Effort = *o.Review.Effort
	}
	if o.Review.Incremental != nil {
		s.Incremental.MaxDeltaFiles = *o.Review.Incremental
	}
	if o.Confidence.Model != nil {
		s.Confidence.Model = *o.Confidence.Model
	}
	if o.Confidence.Fallback != nil {
		s.Confidence.Fallback = *o.Confidence.Fallback
	}
	if o.Confidence.Effort != nil {
		s.Confidence.Effort = *o.Confidence.Effort
	}
	if o.Confidence.Threshold != nil {
		s.Confidence.Threshold = *o.Confidence.Threshold
	}
	if g := o.Confidence.Gate; g != nil {
		s.Confidence.Gate, s.Confidence.GateWhen = g.On, g.When
	}
	if o.Confidence.Risk != nil {
		s.Confidence.Risk = *o.Confidence.Risk
	}
	if o.Confidence.Instructions != nil {
		s.Confidence.Instructions = *o.Confidence.Instructions
	}
	if o.Confidence.When != nil {
		s.Confidence.When = o.Confidence.When
	}
	if o.Confidence.Floors != nil {
		s.Confidence.Floors = o.Confidence.Floors
	}
	s.Ignore = append(s.Ignore, o.Ignore...)
	s.Skills = s.Skills.overlay(o.Skills)
	s.RuleFiles = s.RuleFiles.overlay(o.RuleFiles)
	s.Reviewers, s.Models.Routes = SplitAgents(o.Review.Agents)
	s.trigger(o)
	s.Agent = s.Agent.overlay(o.Agent)
	s.Review = s.Review.overlay(o)
}

// trigger lays the file's trigger keys over s.
func (s *Settings) trigger(o *Overrides) {
	if len(o.Trigger.Include) > 0 {
		s.Filters.Include = slices.Clone(o.Trigger.Include)
	}
	if len(o.Trigger.Exclude) > 0 {
		s.Filters.Exclude = slices.Clone(o.Trigger.Exclude)
	}
	if o.Trigger.Limit != nil {
		s.MaxAutoReviews = *o.Trigger.Limit
	}
	if o.Trigger.Settle != nil {
		s.PushSettle = *o.Trigger.Settle
	}
}

// providerNames is the names of the providers account a can use, sorted.
func (f *File) providerNames(a *Account) []string {
	names := slices.Collect(maps.Keys(f.Providers))
	names = append(names, slices.Collect(maps.Keys(a.Providers))...)
	slices.Sort(names)
	return names
}

func (l Limits) overlay(o LimitsSpec) Limits {
	if o.Concurrency != nil {
		l.Concurrency = *o.Concurrency
	}
	if o.ReviewsPerDay != nil {
		l.ReviewsPerDay = *o.ReviewsPerDay
	}
	if o.TokensPerMonth != nil {
		l.TokensPerMonth = *o.TokensPerMonth
	}
	return l
}

func (r Review) overlay(o *Overrides) Review {
	if o.Review.Fixes != nil {
		r.RequireSuggestedFix = *o.Review.Fixes
	}
	if o.Comments.Summary != nil {
		r.Templates.Summary = *o.Comments.Summary
	}
	if o.Comments.Finding != nil {
		r.Templates.Inline = *o.Comments.Finding
	}
	if o.Comments.Inline != nil {
		r.InlineComments = *o.Comments.Inline
	}
	if o.Comments.Dashboard != nil {
		r.Dashboard = *o.Comments.Dashboard
	}
	if a := o.Review.Approve; a != nil {
		r.Approve, r.ApproveWhen = a.On, a.When
	}
	if o.Review.Diagram != nil {
		r.Diagram = *o.Review.Diagram
	}
	if o.Review.Cost != nil {
		r.Cost = *o.Review.Cost
	}
	if o.Review.Replies != nil {
		r.Replies = *o.Review.Replies
	}
	if o.Context != nil {
		r.Context = o.Context
	}
	if len(o.Rules) > 0 {
		r.Rules = slices.Clone(o.Rules)
	}
	return r
}

func (a AgentSettings) overlay(o Agent) AgentSettings {
	if o.Steps != nil {
		a.MaxSteps = *o.Steps
	}
	if o.Output != nil {
		a.MaxToolOutputBytes = *o.Output
	}
	if o.Tokens != nil {
		a.MaxTokens = *o.Tokens
	}
	if o.Prompt != nil {
		a.MaxPromptTokens = *o.Prompt
	}
	if o.Parts != nil {
		a.MaxParts = *o.Parts
	}
	if o.Timeout != nil {
		a.Timeout = *o.Timeout
	}
	if o.CommandTimeout != nil {
		a.CommandTimeout = *o.CommandTimeout
	}
	if o.Fetches != nil {
		a.MaxFetches = *o.Fetches
	}
	return a
}
