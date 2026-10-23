package configfile

import (
	"cmp"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"math"
	"os"
	"path"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/bmatcuk/doublestar/v4"
	"go.yaml.in/yaml/v4"

	"github.com/home-operations/kritika/internal/config"
	"github.com/home-operations/kritika/internal/jobtimeout"
	"github.com/home-operations/kritika/internal/model"
	"github.com/home-operations/kritika/internal/prfilter"
	"github.com/home-operations/kritika/internal/review"
)

// nameRe bounds connection and provider names to what is safe in a URL
// path segment, a Kubernetes label value and a log line.
var nameRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// fileDoc holds policy and sign-in configuration; config.Settings owns
// operational settings.
type fileDoc struct {
	Auth      Auth                  `yaml:"auth,omitempty"`
	Apps      map[string]Connection `yaml:"apps,omitempty"`
	Forgejo   map[string]forgejoDoc `yaml:"forgejo,omitempty"`
	GitLab    map[string]gitlabDoc  `yaml:"gitlab,omitempty"`
	Gitea     map[string]giteaDoc   `yaml:"gitea,omitempty"`
	Providers map[string]Provider   `yaml:"providers,omitempty"`
	Embedding *Embedding            `yaml:"embedding,omitempty"`
	Egress    Egress                `yaml:"egress,omitempty"`
	// Defaults are the repository settings written at the file's root.
	Defaults Defaults              `yaml:",inline"`
	Accounts map[string]accountDoc `yaml:"accounts,omitempty"`
}

// accountDoc is an entry of the file's accounts: what is an account's
// alone.
type accountDoc struct {
	Limits    LimitsSpec          `yaml:"limits,omitempty"`
	Providers map[string]Provider `yaml:"providers,omitempty"`
}

// Load reads, decodes, resolves and validates the configuration file at
// name. An empty name loads defaults and operational environment settings.
func Load(name string) (*File, error) {
	var raw []byte
	if name != "" {
		var err error
		if raw, err = os.ReadFile(name); err != nil {
			return nil, fmt.Errorf("configfile: %w", err)
		}
	}
	return Parse(raw)
}

// configHash identifies the file and the names of referenced secrets.
// Secret values stay out of the stored hash to prevent offline guessing.
func configHash(raw []byte, secretEnv []string) string {
	h := sha256.New()
	h.Write(raw)
	for _, name := range secretEnv {
		h.Write([]byte("\x00" + name))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Parse is Load for bytes already in hand; nil or blank bytes are no file.
func Parse(raw []byte) (*File, error) {
	settings, doc, err := config.LoadSettings(raw)
	if err != nil {
		return nil, err
	}
	return parse(doc, settings)
}

// FromConfig uses the file snapshot and runtime settings already read by config.Load.
func FromConfig(cfg *config.Config) (*File, error) {
	return parse(cfg.FileDocument(), cfg.Settings)
}

func parse(input config.Document, settings config.Settings) (*File, error) {
	var doc fileDoc
	if err := input.Policy.Load(&doc, yaml.WithKnownFields()); err != nil {
		return nil, fmt.Errorf("configfile: parse: %w", err)
	}
	conns, err := connectionsOf(doc.Apps, doc.Forgejo, doc.GitLab, doc.Gitea)
	if err != nil {
		return nil, err
	}
	accounts, err := accountsOf(doc.Accounts, forgesServing(conns))
	if err != nil {
		return nil, err
	}
	f := &File{
		Auth: doc.Auth, Connections: conns, Providers: doc.Providers, Defaults: doc.Defaults, Egress: doc.Egress,
		Embedding: doc.Embedding, Run: settings,
	}
	s := &secrets{}
	if err := f.Auth.resolve(s); err != nil {
		return nil, err
	}
	if err := f.resolve(accounts, s); err != nil {
		return nil, err
	}
	slices.Sort(s.env)
	f.secretEnv = s.env
	if err := f.Auth.validate(); err != nil {
		return nil, err
	}
	if err := f.validate(accounts); err != nil {
		return nil, err
	}
	f.Accounts, f.unserved = servedAccounts(f.Connections, accounts)
	f.hash = configHash(input.Source, s.env)
	return f, nil
}

// resolve reads f's secrets and those of the account entries, and compiles
// the root's filters.
func (f *File) resolve(accounts []Account, s *secrets) error {
	for _, name := range slices.Sorted(maps.Keys(f.Providers)) {
		p := f.Providers[name]
		if err := p.resolve("providers."+name, chatGPTSessionKey(nil, name), s); err != nil {
			return err
		}
		f.Providers[name] = p
	}
	f.Egress.credentials = make(map[string]Secret, len(f.Egress.Credentials))
	for _, host := range slices.Sorted(maps.Keys(f.Egress.Credentials)) {
		v, err := s.read(f.Egress.Credentials[host])
		if err != nil {
			return fmt.Errorf("configfile: egress.credentials.%s: %w", host, err)
		}
		f.Egress.credentials[strings.ToLower(host)] = v
	}
	if err := f.resolveEmbedding(); err != nil {
		return err
	}
	if err := f.Defaults.Trigger.Compile(); err != nil {
		return fmt.Errorf("configfile: trigger.%w", err)
	}
	for i := range f.Connections {
		if err := f.Connections[i].resolve(f.Connections[i].where(), s); err != nil {
			return err
		}
	}
	for i := range accounts {
		if err := accounts[i].resolve(s); err != nil {
			return err
		}
	}
	return nil
}

// resolveEmbedding gives the embedder its provider's endpoint and key: an
// openrouter or openai provider of the instance, since an account's own
// keys pay for its reviews, not the instance's index.
func (f *File) resolveEmbedding() error {
	e := f.Embedding
	if e == nil {
		return nil
	}
	name := e.Ref.Provider()
	if name == "" || e.Ref.Model() == "" {
		return fmt.Errorf("configfile: embedding.model must be \"<provider>/<model>\", got %q", e.Ref)
	}
	p, ok := f.Providers[name]
	if !ok {
		return fmt.Errorf("configfile: embedding.model references provider %q, which is not declared under providers", name)
	}
	switch p.Type {
	case ProviderOpenRouter:
		e.BaseURL = cmp.Or(p.BaseURL, model.OpenRouterBaseURL)
	case ProviderOpenAI:
		e.BaseURL = cmp.Or(p.BaseURL, model.OpenAIBaseURL)
	default:
		return fmt.Errorf("configfile: embedding.model names a %s provider; embeddings need an %s or %s one",
			p.Type, ProviderOpenRouter, ProviderOpenAI)
	}
	if model.Floating(e.Ref.Model()) {
		return fmt.Errorf("configfile: embedding.model takes no floating alias, got %q", e.Ref)
	}
	e.Model, e.apiKey = e.Ref.Model(), p.apiKey
	return nil
}

// accountsOf gathers the accounts entries, in the order of their names. An
// entry is keyed by the account's name alone, so it is the account's on
// the forge serving that name, forges; one no connection serves is
// GitHub's, and one served on two forges names neither.
func accountsOf(entries map[string]accountDoc, forges map[string][]Forge) ([]Account, error) {
	byKey := map[string]*Account{}
	for _, name := range slices.Sorted(maps.Keys(entries)) {
		where := "accounts." + name
		if err := checkAccountName(where, name); err != nil {
			return nil, err
		}
		forge := ForgeGitHub
		switch served := forges[strings.ToLower(name)]; len(served) {
		case 0:
		case 1:
			forge = served[0]
		default:
			return nil, fmt.Errorf("configfile: %s: %s is served on %s and %s; an entry names an account on one forge",
				where, name, served[0], served[1])
		}
		key := AccountKey(forge, name)
		if a, ok := byKey[key]; ok {
			return nil, fmt.Errorf("configfile: %s duplicates %s", where, a.entry)
		}
		byKey[key] = &Account{Forge: forge, Name: name, entry: where, Limits: entries[name].Limits, Providers: entries[name].Providers}
	}
	out := make([]Account, 0, len(byKey))
	for _, key := range slices.Sorted(maps.Keys(byKey)) {
		out = append(out, *byKey[key])
	}
	return out, nil
}

// resolve reads the account's secret references.
func (a *Account) resolve(s *secrets) error {
	for _, name := range slices.Sorted(maps.Keys(a.Providers)) {
		p := a.Providers[name]
		if err := p.resolve(a.entry+".providers."+name, chatGPTSessionKey(a, name), s); err != nil {
			return err
		}
		a.Providers[name] = p
	}
	return nil
}

func (i *Connection) resolve(where string, s *secrets) error {
	var err error
	if i.Forge == ForgeGitLab {
		b := &i.GitLab
		b.token, b.runnerToken, b.webhookSecret, err = readTokens(where, s, b.Token, b.RunnerToken, b.WebhookSecret)
		return err
	}
	if i.Forge == ForgeGitea {
		b := &i.Gitea
		b.token, b.runnerToken, b.webhookSecret, err = readTokens(where, s, b.Token, b.RunnerToken, b.WebhookSecret)
		return err
	}
	if i.Forge == ForgeForgejo {
		b := &i.Forgejo
		if b.audience, err = b.Audience.resolve(s); err != nil {
			return fmt.Errorf("configfile: %s.audience: %w", where, err)
		}
		if b.runnerAudience, err = b.RunnerAudience.resolve(s); err != nil {
			return fmt.Errorf("configfile: %s.runnerAudience: %w", where, err)
		}
		if b.privateKey, err = s.read(b.PrivateKey); err != nil {
			return fmt.Errorf("configfile: %s.privateKey: %w", where, err)
		}
		if !b.PreviousKey.empty() {
			if b.previousKey, err = s.read(b.PreviousKey); err != nil {
				return fmt.Errorf("configfile: %s.previousKey: %w", where, err)
			}
		}
		if b.webhookSecret, err = s.read(b.WebhookSecret); err != nil {
			return fmt.Errorf("configfile: %s.webhookSecret: %w", where, err)
		}
		return nil
	}
	if i.App.clientID, err = i.App.ClientID.resolve(s); err != nil {
		return fmt.Errorf("configfile: %s.clientId: %w", where, err)
	}
	if i.App.privateKey, err = s.read(i.App.PrivateKey); err != nil {
		return fmt.Errorf("configfile: %s.privateKey: %w", where, err)
	}
	if i.App.webhookSecret, err = s.read(i.App.WebhookSecret); err != nil {
		return fmt.Errorf("configfile: %s.webhookSecret: %w", where, err)
	}
	return nil
}

// validate checks every invariant the rest of kritika relies on, over f and
// the account entries.
func (f *File) validate(accounts []Account) error {
	if err := f.validateProviders(); err != nil {
		return err
	}
	// Connections first: egress checks their hosts.
	if err := validateConnections(f.Connections); err != nil {
		return err
	}
	if err := f.validateIssuer(); err != nil {
		return err
	}
	if err := f.validateEgress(); err != nil {
		return err
	}
	if err := f.validateEmbedding(); err != nil {
		return err
	}
	return f.validateAccounts(accounts)
}

// validateEmbedding checks the embedder's dimension fits the index column,
// its bounds are not negative and prompts come with a floor of their own;
// its provider is checked with the rest.
func (f *File) validateEmbedding() error {
	e := f.Embedding
	if e == nil {
		return nil
	}
	if e.Dims <= 0 || e.Dims > MaxEmbedDims {
		return fmt.Errorf("configfile: embedding.dims must be between 1 and %d (the index's halfvec limit), got %d", MaxEmbedDims, e.Dims)
	}
	if e.MaxBatch < 0 || e.MaxBatchChars < 0 || e.MaxItemChars < 0 {
		return errors.New("configfile: embedding.maxBatch, maxBatchChars and maxItemChars must not be negative")
	}
	// NaN passes any comparison's negation, and would then keep no chunk.
	if math.IsNaN(e.SimilarFloor) || e.SimilarFloor < 0 || e.SimilarFloor > 1 {
		return fmt.Errorf("configfile: embedding.similarFloor must be between 0 and 1 (a cosine similarity), got %g", e.SimilarFloor)
	}
	if m := e.SimilarMargin; m != nil && (math.IsNaN(*m) || *m < 0 || *m > 1) {
		return fmt.Errorf("configfile: embedding.similarMargin must be between 0 and 1 (a difference of cosine similarities), got %g", *m)
	}
	// Prompts move every score, up or down depending on the model, so the
	// default floor could keep everything or nothing without a sign of it.
	if e.SimilarFloor == 0 && (e.QueryPrompt != "" || e.DocumentPrompt != "") {
		return errors.New("configfile: embedding.queryPrompt and documentPrompt need embedding.similarFloor set, " +
			"since prompts move the scores the floor compares")
	}
	return nil
}

func (f *File) validateProviders() error {
	for _, name := range slices.Sorted(maps.Keys(f.Providers)) {
		if !nameRe.MatchString(name) {
			return fmt.Errorf("configfile: providers.%s: a provider name must be lowercase alphanumerics and hyphens, 1 to 63 characters", name)
		}
		if err := f.Providers[name].validate("providers." + name); err != nil {
			return err
		}
	}
	return nil
}

// validateAccounts checks the file's own settings and every account's entries,
// served or not, so an entry is judged when it is written rather than when
// a connection first serves it.
func (f *File) validateAccounts(accounts []Account) error {
	if err := checkLimits("limits", f.Defaults.Limits); err != nil {
		return err
	}
	if err := f.validateOverrides(&f.Defaults.Overrides); err != nil {
		return err
	}
	for i := range accounts {
		if err := f.validateAccount(&accounts[i]); err != nil {
			return err
		}
	}
	return nil
}

// validateAccount checks an account's accounts entry.
func (f *File) validateAccount(a *Account) error {
	if err := checkLimits(a.entry+".limits", a.Limits); err != nil {
		return err
	}
	return f.validateAccountProviders(a.entry, a)
}

// validateOverrides checks the settings the file's root writes: its models
// name providers the instance declares, and its trigger, agent, review and
// comments keys are in range.
func (f *File) validateOverrides(r *Overrides) error {
	for _, m := range append(r.Review.Model.refs(), namedRef{keyFallback, r.Review.Fallback},
		namedRef{keyScorer, r.Confidence.Model}, namedRef{keyScorerFallback, r.Confidence.Fallback}) {
		if m.ref == nil || *m.ref == "" {
			continue
		}
		if err := f.checkModelRef(m.key, *m.ref); err != nil {
			return err
		}
	}
	if err := r.Review.Model.check(); err != nil {
		return err
	}
	for _, e := range []struct {
		key    string
		effort *model.Effort
	}{{keyEffort, r.Review.Effort}, {keyScorerEffort, r.Confidence.Effort}} {
		if e.effort != nil && *e.effort != "" && !e.effort.Valid() {
			return fmt.Errorf("configfile: %s must be %s, got %q", e.key, model.EffortLevels, *e.effort)
		}
	}
	if th := r.Confidence.Threshold; th != nil && !ValidConfidence(*th) {
		return fmt.Errorf("configfile: %s must be between 0 and %d, got %d", keyThreshold, MaxConfidence, *th)
	}
	if risk := r.Confidence.Risk; risk != nil && !risk.Valid() {
		return fmt.Errorf("configfile: %s must be %s, got %q", keyRisk, review.RiskLevels, *risk)
	}
	for _, level := range slices.Sorted(maps.Keys(r.Confidence.Floors)) {
		if !level.Valid() {
			return fmt.Errorf("configfile: %s keys must be %s, got %q", keyFloors, review.RiskLevels, level)
		}
		for gi, g := range r.Confidence.Floors[level] {
			if !ValidGlob(g) {
				return fmt.Errorf("configfile: %s.%s[%d] %q is not a valid glob", keyFloors, level, gi, g)
			}
		}
	}
	if r.Trigger.Limit != nil && *r.Trigger.Limit < 0 {
		return fmt.Errorf("configfile: trigger.limit must not be negative")
	}
	for gi, g := range r.Ignore {
		if !ValidGlob(g) {
			return fmt.Errorf("configfile: ignore[%d] %q is not a valid glob", gi, g)
		}
	}
	for _, c := range []struct {
		name string
		v    *int
	}{
		{keySteps, r.Agent.Steps},
		{keyOutput, r.Agent.Output},
		{keyParts, r.Agent.Parts},
		{keyFetches, r.Agent.Fetches},
		{keyIncremental, r.Review.Incremental},
	} {
		if c.v != nil && *c.v <= 0 {
			return fmt.Errorf("configfile: %s must be positive", c.name)
		}
	}
	if err := validateAgent(r.Agent); err != nil {
		return err
	}
	return cmp.Or(f.validateReviewers(r.Review.Reviewers), validateReview(r))
}

// validateAgent checks the agent bounds the file writes beyond its steps
// and output.
func validateAgent(a Agent) error {
	if a.Tokens != nil && *a.Tokens <= 0 {
		return fmt.Errorf("configfile: agent.tokens must be positive")
	}
	if a.Prompt != nil && *a.Prompt < MinPromptTokens {
		return fmt.Errorf("configfile: agent.prompt must be at least %d", MinPromptTokens)
	}
	if a.Timeout != nil && *a.Timeout <= 0 {
		return fmt.Errorf("configfile: agent.timeout must be positive")
	}
	if a.Timeout != nil && *a.Timeout > jobtimeout.MaxAgentTimeout {
		return fmt.Errorf("configfile: agent.timeout must not exceed %s, or River's %s job timeout cap would cut the review short",
			jobtimeout.MaxAgentTimeout, jobtimeout.MaxJobTimeout)
	}
	// The job document carries whole seconds.
	if a.CommandTimeout != nil && *a.CommandTimeout < time.Second {
		return fmt.Errorf("configfile: agent.commandTimeout must be at least 1s")
	}
	return nil
}

// validateReview checks what the file writes of a review's context, rules
// and comment templates: the paths stay inside the repository.
func validateReview(r *Overrides) error {
	for i, c := range r.Context {
		if err := c.Check(); err != nil {
			return fmt.Errorf("configfile: context[%d]: %w", i, err)
		}
	}
	if err := CheckRules(r.Rules); err != nil {
		return fmt.Errorf("configfile: %w", err)
	}
	if err := checkConditions(r); err != nil {
		return err
	}
	if err := CheckSkills(deref(r.Skills.Paths), r.Skills.Scope); err != nil {
		return fmt.Errorf("configfile: %w", err)
	}
	for _, t := range []struct {
		name string
		path *string
	}{{"summary", r.Comments.Summary}, {"finding", r.Comments.Finding}} {
		if t.path == nil || *t.path == "" {
			continue
		}
		if err := CheckRepoPath(*t.path); err != nil {
			return fmt.Errorf("configfile: comments.%s: %w", t.name, err)
		}
	}
	return nil
}

// Check rejects a context file with no description, a path outside the
// repository, or a glob that is not valid.
func (c ContextFile) Check() error {
	if err := CheckRepoPath(c.Path); err != nil {
		return err
	}
	if strings.TrimSpace(c.Description) == "" {
		return errors.New("description is required")
	}
	for i, g := range c.Paths {
		if !ValidGlob(g) {
			return fmt.Errorf("paths[%d] %q is not a valid glob", i, g)
		}
	}
	return nil
}

// ValidGlob reports whether g is a doublestar glob a setting may take.
func ValidGlob(g string) bool { return strings.TrimSpace(g) != "" && doublestar.ValidatePattern(g) }

// ValidConfidence reports whether n is a confidence score.
func ValidConfidence(n int) bool { return n >= 0 && n <= MaxConfidence }

// CheckRepoPath rejects a repository path that is empty, absolute or
// escapes the repository root: every path a configuration names is read
// in a checkout, so an unbounded one would read outside the repository.
func CheckRepoPath(p string) error {
	if strings.TrimSpace(p) == "" {
		return errors.New("path must not be empty")
	}
	if path.IsAbs(p) {
		return fmt.Errorf("path %q must be relative", p)
	}
	if c := path.Clean(p); c == ".." || strings.HasPrefix(c, "../") {
		return fmt.Errorf("path %q escapes the repository", p)
	}
	return nil
}

func (i *Connection) validate(where string) error {
	switch i.Forge {
	case ForgeGitHub:
		if i.App.clientID == "" {
			return fmt.Errorf("configfile: %s.clientId is required", where)
		}
		if i.App.privateKey.Value() == "" {
			return fmt.Errorf("configfile: %s.privateKey is required", where)
		}
		if i.App.webhookSecret.Value() == "" {
			return fmt.Errorf("configfile: %s.webhookSecret is required", where)
		}
	case ForgeForgejo:
		b := &i.Forgejo
		if b.Host == "" {
			return fmt.Errorf("configfile: %s.host is required", where)
		}
		// The host is matched by the egress gateway and joined into URLs,
		// so it is a bare lowercase hostname, as an egress entry is.
		if err := checkHost(b.Host); err != nil || strings.HasPrefix(b.Host, "*") {
			return fmt.Errorf("configfile: %s.host: %q must be a lowercase hostname, without a scheme, port or path", where, b.Host)
		}
		if b.privateKey.Value() == "" {
			return fmt.Errorf("configfile: %s.privateKey is required", where)
		}
		if b.runnerAudience != "" && b.audience == "" {
			return fmt.Errorf("configfile: %s.runnerAudience needs audience set", where)
		}
		if b.webhookSecret.Value() == "" {
			return fmt.Errorf("configfile: %s.webhookSecret is required", where)
		}
	case ForgeGitLab:
		b := &i.GitLab
		if b.Host == "" {
			return fmt.Errorf("configfile: %s.host is required", where)
		}
		if err := checkHost(b.Host); err != nil || strings.HasPrefix(b.Host, "*") {
			return fmt.Errorf("configfile: %s.host: %q must be a lowercase hostname, without a scheme, port or path", where, b.Host)
		}
		if b.token.Value() == "" {
			return fmt.Errorf("configfile: %s.token is required", where)
		}
		if b.webhookSecret.Value() == "" {
			return fmt.Errorf("configfile: %s.webhookSecret is required", where)
		}
		encoded, ok := strings.CutPrefix(b.webhookSecret.Value(), "whsec_")
		if key, err := base64.StdEncoding.DecodeString(encoded); !ok || err != nil || len(key) == 0 {
			return fmt.Errorf("configfile: %s.webhookSecret must be the webhook's signing token, \"whsec_\" followed by base64", where)
		}
	case ForgeGitea:
		return i.Gitea.validate(where)
	default:
		return fmt.Errorf("configfile: %s: forge must be %s, %s, %s or %s, got %q",
			where, ForgeGitHub, ForgeForgejo, ForgeGitLab, ForgeGitea, i.Forge)
	}
	return nil
}

// checkModelRef rejects a model reference that is not
// "<provider>/<model>" of a provider the instance declares, or a floating
// alias that provider cannot resolve.
func (f *File) checkModelRef(where string, ref ModelRef) error {
	name := ref.Provider()
	if name == "" || ref.Model() == "" {
		return fmt.Errorf("configfile: %s must be \"<provider>/<model>\", got %q", where, ref)
	}
	p, ok := f.Provider(nil, name)
	if !ok {
		return fmt.Errorf("configfile: %s references provider %q, which is not declared under providers", where, name)
	}
	return checkAlias(where, p.Type, ref.Model())
}

// checkAlias rejects a floating alias, a model ID starting with ~, that a
// provider of type t cannot resolve: OpenRouter resolves its own
// ~<author>/<family>-latest, kritika ~<family>-latest for the types that
// take aliases, and the others would send one as it is.
func checkAlias(where string, t ProviderType, id string) error {
	switch {
	case !model.Floating(id):
		return nil
	case t == ProviderOpenRouter:
		if !model.ValidOpenRouterAlias(id) {
			return fmt.Errorf("configfile: %s: an openrouter alias must be ~<author>/<family>-latest, got %q", where, id)
		}
	case !t.TakesAliases():
		return fmt.Errorf("configfile: %s: a provider of type %s takes no floating alias, got %q", where, t, id)
	case !model.ValidAlias(id):
		return fmt.Errorf("configfile: %s: a floating alias must be ~<family>-latest, got %q", where, id)
	}
	return nil
}

func checkLimits(where string, l LimitsSpec) error {
	if (l.ReviewsPerDay != nil && *l.ReviewsPerDay < 0) || (l.TokensPerMonth != nil && *l.TokensPerMonth < 0) {
		return fmt.Errorf("configfile: %s: limits must not be negative", where)
	}
	if l.Concurrency != nil && *l.Concurrency <= 0 {
		return fmt.Errorf("configfile: %s.concurrency must be positive", where)
	}
	return nil
}

// compileFilter compiles a CEL filter and smoke-tests it against a sample PR,
// so a filter that type-checks but fails at runtime (a field of the wrong
// type, a non-boolean result) is caught at load rather than on the first
// webhook. An empty filter compiles to nil, meaning "no restriction".
func compileFilter(expr string) (*prfilter.Program, error) {
	if strings.TrimSpace(expr) == "" {
		return nil, nil
	}
	prg, err := prfilter.Compile(expr)
	if err != nil {
		return nil, err
	}
	if _, err := prg.Eval(SamplePR()); err != nil {
		return nil, fmt.Errorf("smoke test against a sample pull request: %w", err)
	}
	return prg, nil
}

// SamplePR is the pull request every filter is evaluated against at load. It
// is also the documented shape of the `pr` variable: every key here is
// present at runtime, lines for a trigger condition alone, which is then
// judged once the diff is fetched.
func SamplePR() map[string]any {
	return map[string]any{
		"event":     "opened",
		"number":    1,
		"title":     "feat: sample",
		"author":    "octocat",
		"state":     "open",
		"open":      true,
		"merged":    false,
		"draft":     false,
		"fork":      false,
		"bot":       false,
		"headRef":   "feature",
		"headSha":   "0000000",
		"baseRef":   "main",
		"url":       "https://example.invalid/pull/1",
		"createdAt": time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		"labels":    []any{map[string]any{"name": "sample", "color": "ffffff"}},
		"body":      "sample body",
		linesVar:    10,
	}
}

func (r SecretRef) empty() bool { return r.Env == "" }

// secrets reads the secrets one Parse resolves, and records the variables
// they came from in env.
type secrets struct{ env []string }

// read resolves r. An unset variable is an error, never an empty value, so
// a typo cannot silently disable authentication.
func (s *secrets) read(r SecretRef) (Secret, error) {
	if r.Env == "" {
		return Secret{}, errors.New("reference must set env")
	}
	v, ok := os.LookupEnv(r.Env)
	if !ok {
		return Secret{}, fmt.Errorf("environment variable %s is not set", r.Env)
	}
	if !slices.Contains(s.env, r.Env) {
		s.env = append(s.env, r.Env)
	}
	return Secret{value: strings.TrimRight(v, "\r\n")}, nil
}
