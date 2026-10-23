package repoconfig

import (
	"encoding/json"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/model"
	"github.com/home-operations/kritika/internal/review"
)

// Merged is the admin's settings with the merge-base FileName applied.
type Merged struct {
	configfile.Settings
	// InRepoFilters are the file's own conditions, passed beside the
	// admin's, which ingest has already applied.
	InRepoFilters configfile.Filters
	// Dropped says which of the file's values fell outside the admin's
	// bounds; the admin's value applies for each.
	Dropped []string
	// Tools are the file's tools as a mise config, the [tools] table the
	// runner lays over the image's; "" when the file declares none. They
	// have no admin counterpart: the image's tools are the admin's.
	Tools string
}

// Merge applies doc, the merge-base FileName or nil when the repository has
// none, over the admin's settings op. The file narrows what an admin allows
// (enabled, ignore, and include and exclude lists of its own, with no
// condition under an admin's name),
// appends its context files and rules to the
// admin's, may only turn review.fixes on and lower confidence.risk, and replaces the models,
// their effort, the confidence threshold, how the review comments,
// whether it approves and whether its summary draws a diagram, and adds
// approval and confidence conditions that must hold beside the admin's, and
// adds reviewers of its own (see reviewers). A model must be one of a provider
// op.Providers names, and an effort one of model.Efforts or empty, which
// leaves it to the provider. A value it may not take is dropped, and
// Dropped says so. A file that does not parse is ignored as a whole: op stands, and the
// error says why.
func Merge(doc []byte, op configfile.Settings) (Merged, error) {
	op.Ignore = slices.Clone(op.Ignore)
	op.Review.Context = slices.Clone(op.Review.Context)
	op.Review.Rules = slices.Clone(op.Review.Rules)
	m := Merged{Settings: op}
	if doc == nil {
		return m, nil
	}
	f, err := Parse(doc)
	if err != nil {
		return m, err
	}
	if m.Tools, err = f.Tools.TOML(); err != nil {
		return m, err
	}
	if f.Enabled != nil && !*f.Enabled {
		m.Enabled = false
	}
	m.InRepoFilters.Include = m.own("include", f.Trigger.Include, op.Filters)
	m.InRepoFilters.Exclude = m.own("exclude", f.Trigger.Exclude, op.Filters)
	for _, g := range f.Ignore {
		if !slices.Contains(m.Ignore, g) {
			m.Ignore = append(m.Ignore, g)
		}
	}
	if v := f.Review.Fixes; v != nil && *v {
		m.Review.RequireSuggestedFix = true
	} else if v != nil && op.Review.RequireSuggestedFix {
		m.drop("review.fixes", "false", "true, since an admin requires a suggested fix")
	}
	if f.Comments.Summary != nil {
		m.Review.Templates.Summary = *f.Comments.Summary
	}
	if f.Comments.Finding != nil {
		m.Review.Templates.Inline = *f.Comments.Finding
	}
	if f.Comments.Inline != nil {
		m.Review.InlineComments = *f.Comments.Inline
	}
	if a := f.Review.Approve; a != nil {
		m.Review.Approve, m.Review.ApproveWhenInRepo = a.On, a.When
	}
	if f.Review.Diagram != nil {
		m.Review.Diagram = *f.Review.Diagram
	}
	for _, c := range f.Context {
		if !slices.ContainsFunc(m.Review.Context, func(o configfile.ContextFile) bool { return o.Path == c.Path }) {
			m.Review.Context = append(m.Review.Context, c)
		} else {
			m.Dropped = append(m.Dropped, fmt.Sprintf("%s: context %s was dropped: an admin context entry has that path", FileName, c.Path))
		}
	}
	for _, r := range f.Rules {
		if slices.ContainsFunc(op.Review.Rules, func(o configfile.Rule) bool { return o.ID == r.ID }) {
			m.Dropped = append(m.Dropped, fmt.Sprintf("%s: rules %s was dropped: an admin's rule has that id", FileName, r.ID))
			continue
		}
		m.Review.Rules = append(m.Review.Rules, r)
	}
	switch th := f.Confidence.Threshold; {
	case th == nil:
	case configfile.ValidConfidence(*th):
		m.Confidence.Threshold = *th
	default:
		m.drop("confidence.threshold", strconv.Itoa(*th), "0 to "+strconv.Itoa(configfile.MaxConfidence))
	}
	if g := f.Confidence.Gate; g != nil {
		m.Confidence.Gate, m.Confidence.GateWhen = g.On, g.When
	}
	m.Confidence.WhenInRepo = f.Confidence.When
	switch risk := f.Confidence.Risk; {
	case risk == "":
	case risk.Within(op.Confidence.Risk):
		m.Confidence.Risk = risk
	default:
		m.drop("confidence.risk", strconv.Quote(string(risk)), string(op.Confidence.Risk)+" or lower")
	}
	m.Skills = configfile.WithSkills(m.Skills, f.Skills)
	m.reviewers(f.Reviewers, op)
	m.choose(&f, op.Providers)
	for _, e := range []struct {
		field string
		want  *model.Effort
		dst   *model.Effort
	}{{"review.effort", f.Review.Effort, &m.Models.Effort}, {"confidence.effort", f.Confidence.Effort, &m.Confidence.Effort}} {
		switch {
		case e.want == nil:
		case *e.want == "" || e.want.Valid():
			*e.dst = *e.want
		default:
			m.drop(e.field, strconv.Quote(string(*e.want)), model.EffortLevels)
		}
	}
	return m, nil
}

// reviewers adds the file's reviewers to the admin's: one of its own as
// written, its models among op.Providers, while there is room for it under
// configfile.MaxReviewers; and, to a reviewer of the admin's name, its
// rules, but under an id that reviewer's rules or the admin's have, and
// its skill scopes, but of a skill that reviewer scopes. Anything else it
// writes of an admin's reviewer, the default reviewer's being the
// admin's too, is dropped with a note: a pull request must not be able to
// turn off, narrow or retune a reviewer the admin set.
func (m *Merged) reviewers(file map[string]configfile.Reviewer, op configfile.Settings) {
	if len(file) == 0 {
		return
	}
	m.Reviewers = maps.Clone(m.Reviewers)
	if m.Reviewers == nil {
		m.Reviewers = map[string]configfile.Reviewer{}
	}
	for _, name := range slices.Sorted(maps.Keys(file)) {
		r := file[name]
		key := "reviewers." + name
		admin, ok := op.Reviewers[name]
		switch {
		case name == review.DefaultReviewer:
			m.Dropped = append(m.Dropped, fmt.Sprintf("%s: %s was dropped: the default reviewer is the admin's to turn off", FileName, key))
		case ok:
			m.Reviewers[name] = m.addTo(key, admin, r, op.Review.Rules)
		case others(m.Reviewers) >= configfile.MaxReviewers:
			m.Dropped = append(m.Dropped, fmt.Sprintf("%s: %s was dropped: %d reviewers besides the default one are the most a review runs",
				FileName, key, configfile.MaxReviewers))
		default:
			// A model list is applied whole or dropped whole, as
			// review.model's is.
			if r.Model != nil {
				if why := routesDropped(*r.Model, op.Providers); why != "" {
					m.Dropped = append(m.Dropped, fmt.Sprintf("%s: %s.model was dropped: %s", FileName, key, why))
					r.Model = nil
				}
			}
			if f := r.Fallback; f != "" && (f.Model() == "" || !slices.Contains(op.Providers, f.Provider())) {
				m.drop(key+".fallback", strconv.Quote(string(f)), "a model of "+list(op.Providers))
				r.Fallback = ""
			}
			m.Reviewers[name] = r
		}
	}
}

// addTo is the admin's reviewer with the file's rules and skill scopes
// for it added (see reviewers), key naming it in the notes.
func (m *Merged) addTo(key string, admin, r configfile.Reviewer, adminRules []configfile.Rule) configfile.Reviewer {
	rules, scopes := r.Rules, r.Skills.Scope
	r.Rules, r.Skills.Scope = nil, nil
	if !reflect.ValueOf(r).IsZero() {
		m.Dropped = append(m.Dropped, fmt.Sprintf("%s: %s was dropped but for its rules and skills: it is an admin's reviewer", FileName, key))
	}
	admin.Rules = slices.Clone(admin.Rules)
	for _, rule := range rules {
		taken := func(o configfile.Rule) bool { return o.ID == rule.ID }
		if slices.ContainsFunc(admin.Rules, taken) || slices.ContainsFunc(adminRules, taken) {
			m.Dropped = append(m.Dropped, fmt.Sprintf("%s: %s.rules %s was dropped: an admin's rule has that id", FileName, key, rule.ID))
			continue
		}
		admin.Rules = append(admin.Rules, rule)
	}
	for _, skill := range slices.Sorted(maps.Keys(scopes)) {
		if _, set := admin.Skills.Scope[skill]; set {
			m.Dropped = append(m.Dropped, fmt.Sprintf("%s: %s.skills.scope.%s was dropped: the admin scopes that skill", FileName, key, skill))
			continue
		}
		admin.Skills.Scope = maps.Clone(admin.Skills.Scope)
		if admin.Skills.Scope == nil {
			admin.Skills.Scope = map[string]configfile.SkillScope{}
		}
		admin.Skills.Scope[skill] = scopes[skill]
	}
	return admin
}

// others counts the reviewers besides the default one.
func others(reviewers map[string]configfile.Reviewer) int {
	n := len(reviewers)
	if _, ok := reviewers[review.DefaultReviewer]; ok {
		n--
	}
	return n
}

// choose applies the models f chooses: a model of one of providers. A
// review.model list is applied whole or dropped whole, since one without
// a route would pick another model than the file says.
func (m *Merged) choose(f *File, providers []string) {
	if rm := f.Review.Model; rm.Default != "" {
		switch why := routesDropped(rm, providers); {
		case why == "":
			m.Models.Review, m.Models.Routes = rm.Default, rm.Routes
		case len(rm.Routes) == 0:
			m.drop("review.model", strconv.Quote(string(rm.Default)), "a model of "+list(providers))
		default:
			m.Dropped = append(m.Dropped, fmt.Sprintf("%s: review.model was dropped: %s", FileName, why))
		}
	}
	for _, c := range []struct {
		field string
		want  configfile.ModelRef
		dst   *configfile.ModelRef
	}{
		{"review.fallback", f.Review.Fallback, &m.Models.Fallback},
		{"confidence.model", f.Confidence.Model, &m.Confidence.Model},
		{"confidence.fallback", f.Confidence.Fallback, &m.Confidence.Fallback},
	} {
		if c.want == "" {
			continue
		}
		if c.want.Model() != "" && slices.Contains(providers, c.want.Provider()) {
			*c.dst = c.want
		} else {
			m.drop(c.field, strconv.Quote(string(c.want)), "a model of "+list(providers))
		}
	}
}

// routesDropped says why the file's review.model may not apply, "" when
// it may: a model, a route's or its fallback, of a provider not among
// providers, or a route's effort that is no level.
func routesDropped(rm configfile.ReviewModel, providers []string) string {
	allowed := func(ref configfile.ModelRef) bool {
		return ref.Model() != "" && slices.Contains(providers, ref.Provider())
	}
	if !allowed(rm.Default) {
		return fmt.Sprintf("%q is not a model of %s", rm.Default, list(providers))
	}
	for i, r := range rm.Routes {
		if !allowed(r.Model) {
			return fmt.Sprintf("[%d].model %q is not a model of %s", i, r.Model, list(providers))
		}
		if r.Fallback != nil && *r.Fallback != "" && !allowed(*r.Fallback) {
			return fmt.Sprintf("[%d].fallback %q is not a model of %s", i, *r.Fallback, list(providers))
		}
		if r.Effort != nil && *r.Effort != "" && !r.Effort.Valid() {
			return fmt.Sprintf("[%d].effort %q is not one of %s", i, *r.Effort, model.EffortLevels)
		}
	}
	return ""
}

// own is the file's conditions of one list it may keep: all but those
// under a name one of the admin's conditions has, which are dropped with a
// note, so a pull request cannot pass off its own condition as the
// admin's.
func (m *Merged) own(list string, conditions []configfile.Filter, admin configfile.Filters) []configfile.Filter {
	var out []configfile.Filter
	for _, c := range conditions {
		if c.Name != "" && admin.Named(c.Name) {
			m.Dropped = append(m.Dropped, fmt.Sprintf("%s: trigger.%s %s was dropped: an admin's condition has that name", FileName, list, c.Name))
			continue
		}
		out = append(out, c)
	}
	return out
}

// drop notes a value the file may not take.
func (m *Merged) drop(field, value, allowed string) {
	m.Dropped = append(m.Dropped, fmt.Sprintf("%s: %s %s was dropped; allowed: %s", FileName, field, value, allowed))
}

// list is a bound's values for a note.
func list[T ~string](values []T) string {
	if len(values) == 0 {
		return "none"
	}
	parts := make([]string, len(values))
	for i, v := range values {
		parts[i] = string(v)
	}
	return strings.Join(parts, ", ")
}

// SkipReason says why the repository's own configuration skips a review.
// The reviews.skip_reason CHECK admits these values, and the runner's.
type SkipReason string

// Skip reasons.
const (
	SkipDisabled  SkipReason = "disabled"
	SkipFiltered  SkipReason = "filtered"
	SkipOnlyPaths SkipReason = "only_skipped_paths"
)

// Valid reports whether r is a skip reason.
func (r SkipReason) Valid() bool {
	return r == SkipDisabled || r == SkipFiltered || r == SkipOnlyPaths
}

// Description is the reason as the commit status states it.
func (r SkipReason) Description() string {
	switch r {
	case SkipDisabled:
		return "disabled in " + FileName
	case SkipFiltered:
		return "filtered"
	case SkipOnlyPaths:
		return "only ignored paths changed"
	}
	return string(r)
}

// Check returns why m skips a review of a pull request with the filter
// variables vars that changes changed, or "" when it does not, and for a
// filtered one the condition that decided: the exclusion that holds, or
// nil when no inclusion does. Its conditions that need the diff are the
// runner's to judge. A filter that fails to evaluate skips, since the file
// may only narrow; the error is returned for the log.
func (m *Merged) Check(vars map[string]any, changed []string) (reason SkipReason, by *configfile.Filter, err error) {
	if !m.Enabled {
		return SkipDisabled, nil, nil
	}
	if skip, by, err := m.InRepoFilters.Skips(vars, nil); skip {
		return SkipFiltered, by, err
	}
	if AllIgnored(m.Ignore, changed) {
		return SkipOnlyPaths, nil, nil
	}
	return "", nil, nil
}

// PullRequest is what a filter sees of a pull request, and what a review
// run's job document carries of it.
type PullRequest struct {
	Number    int       `json:"number"`
	Title     string    `json:"title"`
	Author    string    `json:"author"`
	State     string    `json:"state"`
	Merged    bool      `json:"merged,omitempty"`
	Draft     bool      `json:"draft,omitempty"`
	Fork      bool      `json:"fork,omitempty"`
	Bot       bool      `json:"bot,omitempty"`
	HeadRef   string    `json:"headRef"`
	HeadSHA   string    `json:"headSha"`
	BaseRef   string    `json:"baseRef"`
	URL       string    `json:"url,omitempty"`
	Body      string    `json:"body,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
	// Labels is the stored labels JSON array.
	Labels json.RawMessage `json:"labels,omitempty"`
	// Event is the trigger of the review the filter judges: opened,
	// reopened, ready_for_review, synchronize, poll, labeled, unlabeled or
	// manual.
	Event string `json:"event,omitempty"`
}

// Manual reports whether the review was asked for by hand: a re-run from
// the dashboard or an @<bot> review comment.
func (p PullRequest) Manual() bool { return p.Event == "manual" }

// Vars is the filter's pr variable, with the keys webhook.PullRequest's
// FilterVars gives ingest.
func (p PullRequest) Vars() (map[string]any, error) {
	labels := []any{}
	if len(p.Labels) > 0 {
		if err := json.Unmarshal(p.Labels, &labels); err != nil {
			return nil, fmt.Errorf("repoconfig: decode pull request labels: %w", err)
		}
		if labels == nil {
			labels = []any{}
		}
	}
	return map[string]any{
		"event": p.Event, "number": p.Number, "title": p.Title, "author": p.Author, "state": p.State, "open": p.State == "open",
		"merged": p.Merged, "draft": p.Draft, "fork": p.Fork, "bot": p.Bot, "headRef": p.HeadRef, "headSha": p.HeadSHA,
		"baseRef": p.BaseRef, "url": p.URL, "body": p.Body, "createdAt": p.CreatedAt, "labels": labels,
	}, nil
}
