// Package runspec is what a worker and a runner agree on across images:
// the job document a worker hands a runner, the credentials delivered
// beside it, and the names a runner gives the gateway and its own
// variables.
package runspec

import (
	"bytes"
	"cmp"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/gitfetch"
	"github.com/home-operations/kritika/internal/repoconfig"
	"github.com/home-operations/kritika/internal/review"
	"github.com/home-operations/kritika/internal/textcut"
)

// Version is the only job document version a runner of this build
// understands. A worker and runner on different images must agree on it,
// so a runner refuses any other version instead of guessing at its
// meaning.
const Version = 40

// HeartbeatInterval is how often a runner stamps runner_runs.heartbeat_at.
// The worker's staleness threshold is several of these.
const HeartbeatInterval = 15 * time.Second

// Kind is what a run does.
type Kind string

// Kinds of run.
const (
	// KindReview fetches head and merge-base, diffs them and writes a
	// context pack.
	KindReview Kind = "review"
	// KindIndex chunks a tree into the index staging table.
	KindIndex Kind = "index"
	// KindFollowUp fetches head and merge-base, as a review does, and
	// answers a comment in a pull request thread.
	KindFollowUp Kind = "followup"
)

// Valid reports whether k is a kind of run the runner implements.
func (k Kind) Valid() bool { return k == KindReview || k == KindIndex || k == KindFollowUp }

// agentic reports whether a run of kind k runs an agent: a review's or a
// follow-up's.
func (k Kind) agentic() bool { return k == KindReview || k == KindFollowUp }

// ModelEndpoint is where an agent's model calls go: the worker's gateway,
// which holds the provider key, picks the provider model and its fallbacks,
// and counts what the run spends. The run's token for it reaches the pod as
// a job-scoped secret.
type ModelEndpoint struct {
	// GatewayURL is the gateway's address, http://host:port.
	GatewayURL string `json:"gatewayUrl"`
	// Model is the name the gateway knows the run's model by.
	Model string `json:"model"`
	// Granted is the provider/model reference the gateway answers a
	// review's run with, which its runner records with the conversation it
	// keeps; the runner calls the model only by Model.
	Granted string `json:"granted,omitempty"`
}

// AgentLimits bound a review's or a follow-up's agent. A zero limit takes the agent loop's
// default; a zero timeout leaves the tool loop to the Job deadline.
type AgentLimits struct {
	MaxSteps           int   `json:"maxSteps"`
	MaxToolOutputBytes int   `json:"maxToolOutputBytes"`
	MaxTokens          int64 `json:"maxTokens"`
	// MaxPromptTokens bounds the agent's opening prompt, the diff and
	// context it starts from; zero takes review.DefaultBudgetTokens.
	MaxPromptTokens int `json:"maxPromptTokens,omitempty"`
	// Parts is the most parts the runner may split a review into, which
	// the run's grant and deadline cover; under two it never splits.
	// Parallel is the initial capacity held by the worker. A split review
	// observes later grants through runner_runs.part_slots.
	// TimeoutSeconds bounds each part.
	Parts          int `json:"parts,omitempty"`
	Parallel       int `json:"parallel,omitempty"`
	TimeoutSeconds int `json:"timeoutSeconds,omitempty"`
	// CommandTimeoutSeconds bounds one command the run tool executes;
	// zero takes configfile.DefaultCommandTimeout.
	CommandTimeoutSeconds int `json:"commandTimeoutSeconds,omitempty"`
	// Fetches bounds fetch_repo's fetches for the run, a split review's
	// parts between them; zero takes configfile.DefaultFetches.
	Fetches int `json:"fetches,omitempty"`
	// Hosts name what the gateway lets the commands and fetch_repo reach,
	// as configfile.EgressHosts lists them, for the tools' descriptions;
	// empty leaves them unnamed.
	Hosts []string `json:"hosts,omitempty"`
	// CacheLocked says the gateway keeps downloads, as
	// configfile.GatewayCaches reports, so a repository's tool, whose
	// download the run checks against a checksum fetched apart from it, is
	// installed through the cache, naming that checksum in DigestHeader.
	CacheLocked bool `json:"cacheLocked,omitempty"`
	// Env names the variables of the runner's own environment that the
	// run tool's commands get, the admin's.
	Env []CommandEnvVar `json:"env,omitempty"`
}

// CommandEnvVar is a variable of the runner's environment its commands
// get. Secret marks one whose value is a Secret's, which is masked as the
// run's tokens are.
type CommandEnvVar struct {
	Name   string `json:"name"`
	Secret bool   `json:"secret,omitempty"`
}

// CommandEnvPrefix prefixes, on the runner container, the names of the
// admin's variables for the commands, so they configure the commands and
// never the runner itself: a GODEBUG, TMPDIR or PGHOST of theirs would
// otherwise change how the runner fetches, logs or connects.
const CommandEnvPrefix = "KRITIKA_COMMAND_ENV_"

// Prompt is what a review run needs beyond the checkout to write its
// review prompt and to tell whether the worker will skip the review: the
// pull request, the review settings with the merge-base .kritika.yaml
// applied, and the last completed review's findings. A follow-up's prompt
// is written from the same pull request, rules, context files and
// findings.
type Prompt struct {
	Repository  string                 `json:"repository"`
	PullRequest repoconfig.PullRequest `json:"pullRequest"`
	// Issues are the issues the pull request's description says it
	// closes, as the worker read them from the forge.
	Issues []review.Issue `json:"issues,omitempty"`
	// Context names the files that explain the code, which the agent is
	// pointed at to read for itself.
	Context []configfile.ContextFile `json:"context,omitempty"`
	// Rules are the configuration's rules, each applied when the change
	// matches its paths; a file rule's file is among the spec's RepoFiles.
	Rules               []configfile.Rule `json:"rules,omitempty"`
	RequireSuggestedFix bool              `json:"requireSuggestedFix,omitempty"`
	// Diagram asks the review for a Mermaid diagram of the change's flow
	// in its summary.
	Diagram bool `json:"diagram,omitempty"`
	// MaxDeltaFiles is the incremental re-review threshold.
	MaxDeltaFiles int              `json:"maxDeltaFiles"`
	Prior         []review.Finding `json:"prior,omitempty"`
	// PriorDiagram is the last completed review's summary diagram, which a
	// re-review is asked to keep or update; "" when Diagram is off.
	PriorDiagram string `json:"priorDiagram,omitempty"`
	// PriorChecked are the last completed review's notes on what it
	// checked and found sound, which a re-review is shown with Prior.
	PriorChecked []string `json:"priorChecked,omitempty"`
	// Dismissed are the findings maintainers dismissed on the pull
	// request, which the review is told not to raise again.
	Dismissed []review.DismissedFinding `json:"dismissed,omitempty"`
	// UnchangedPatchID, when the head's patch id equals it, means the
	// worker will skip the review, so the agent is not run.
	UnchangedPatchID string `json:"unchangedPatchId,omitempty"`
	// Skills are the directories of the merge base the repository's skills
	// are looked for in, with the scope of those that have one; nil offers
	// none.
	Skills *Skills `json:"skills,omitempty"`
	// Filters are the include and exclude lists with a condition only the
	// diff can judge, each of which the pull request must pass: the
	// admin's, which the worker leaves out for a review someone asked
	// for, and the repository's own.
	Filters []configfile.Filters `json:"filters,omitempty"`
	// Continue, when set, lets an incremental review carry on the last
	// review's conversation, which the gateway serves the run, if the
	// review would send it the same system prompt and tools; nil starts
	// afresh.
	Continue *Continuation `json:"continue,omitempty"`
	// Focus is what the review someone asked for is to look hardest at,
	// in their words; "" for nothing in particular.
	Focus string `json:"focus,omitempty"`
	// Reviewers are the reviewers to run, the default one, when it is
	// among them, by its name alone, the rules, skills and the rest of the
	// prompt being its own; each other one whose when conditions the
	// worker found to hold, and which the runner runs when the change
	// touches its paths. Nil runs the default reviewer alone, as a review
	// always has. A follow-up carries the reviewer of the finding it
	// answers about, when that is not the default one.
	Reviewers []Reviewer `json:"reviewers,omitempty"`
}

// SkipUnchangedPatch is the skip reason the runner decides beyond
// repoconfig's: a bot's pull request whose patch id equals its last
// prepared review's.
const SkipUnchangedPatch = "unchanged_patch"

// Reviewer is one reviewer a review runs, as its job document carries it
// (see configfile.Reviewer): its instructions, or the merge-base file
// among the spec's RepoFiles they are read from; the categories it may
// report; the changed paths it reviews, every one when none; its rules,
// whose when conditions the worker has judged; its skills, as Prompt's;
// and its agent's bounds where they are lower than the run's. Full says
// the last review did not run it, so it reviews the whole change rather
// than what changed since.
type Reviewer struct {
	Name         string            `json:"name"`
	Instructions string            `json:"instructions,omitempty"`
	File         string            `json:"file,omitempty"`
	Categories   []review.Category `json:"categories,omitempty"`
	Paths        []string          `json:"paths,omitempty"`
	Rules        []configfile.Rule `json:"rules,omitempty"`
	Skills       *Skills           `json:"skills,omitempty"`
	Limits       *ReviewerLimits   `json:"limits,omitempty"`
	Full         bool              `json:"full,omitempty"`
}

// ReviewerLimits are a reviewer's agent's bounds where they are lower than
// the run's AgentLimits; a zero bound is the run's.
type ReviewerLimits struct {
	MaxSteps       int   `json:"maxSteps,omitempty"`
	MaxTokens      int64 `json:"maxTokens,omitempty"`
	TimeoutSeconds int   `json:"timeoutSeconds,omitempty"`
}

// Continuation is the last review's conversation a review may carry on.
type Continuation struct {
	// RunID is the run that kept it, and Session the conversation its
	// steps were sent as, which the gateway sends this run's as too.
	RunID   string `json:"runId"`
	Session string `json:"session"`
}

// Skills is where a review's skills are looked for and which of them it is
// offered: Scope's paths are judged against the changed paths here, and
// Off names the skills whose conditions the worker found not to hold.
type Skills struct {
	Paths []string                         `json:"paths"`
	Scope map[string]configfile.SkillScope `json:"scope,omitempty"`
	Off   []string                         `json:"off,omitempty"`
}

// Spec is the job document a worker hands a runner: everything the run
// needs except its secrets.
type Spec struct {
	Version int    `json:"version"`
	Kind    Kind   `json:"kind"`
	RunID   string `json:"runId"`
	// CheckpointScope binds part reuse to the configuration and granted model policy.
	CheckpointScope string `json:"checkpointScope,omitempty"`
	// CloneURL is fetched with Secrets.GitToken.
	CloneURL string `json:"cloneUrl"`
	// Forge is the repository's forge, github, forgejo, gitlab or gitea; empty is github.
	// Secrets.GitToken is a GitHub token only on GitHub, where gh and the
	// repository tools' lookups in the GitHub API are given it.
	Forge string `json:"forge,omitempty"`
	// Head is the commit under review or to index. Base is the merge-base
	// for a review and the previously indexed commit for an incremental
	// index. PriorHead is the head of the last completed review,
	// PriorChanged the paths the change touched there, and PriorBase the
	// merge base it was reviewed against, so the delta since is kept to the
	// change's own paths, less what the base gained in them, under a merge
	// or a rebase.
	Head         string   `json:"head"`
	Base         string   `json:"base,omitempty"`
	PriorHead    string   `json:"priorHead,omitempty"`
	PriorChanged []string `json:"priorChanged,omitempty"`
	PriorBase    string   `json:"priorBase,omitempty"`
	// Ignore globs, the admin's and .kritika.yaml's, are skipped by the
	// context stages, and a review whose every changed path matches one is
	// skipped.
	Ignore []string `json:"ignore,omitempty"`
	// RepoFiles are repository paths read from the merge base: the files
	// the review settings name, and .kritika.yaml itself when there is one.
	RepoFiles []string `json:"repoFiles,omitempty"`
	// Tools are the repository's tools the merge-base .kritika.yaml
	// declares, each at its version; none offers the image's commands
	// alone.
	Tools  repoconfig.Tools `json:"tools,omitempty"`
	Agent  *AgentLimits     `json:"agent,omitempty"`
	Model  *ModelEndpoint   `json:"model,omitempty"`
	Prompt *Prompt          `json:"prompt,omitempty"`
	// Thread is the comments a follow-up answers, oldest first, the last
	// of them the one to answer.
	Thread []review.Message `json:"thread,omitempty"`
	// Reply says the last of them replied in the thread of one of
	// kritika's findings without addressing it: the agent answers only if
	// the reviewer is needed, and may concede the finding.
	Reply bool `json:"reply,omitempty"`
}

// Validate checks a spec is one a runner of this build can carry out.
func (s Spec) Validate() error {
	if s.Version != Version {
		return fmt.Errorf("runspec: spec version %d is not supported (want %d)", s.Version, Version)
	}
	if !s.Kind.Valid() {
		return fmt.Errorf("runspec: spec kind %q is not review, index or followup", s.Kind)
	}
	if s.RunID == "" || s.CloneURL == "" {
		return errors.New("runspec: spec needs runId and cloneUrl")
	}
	if !gitfetch.IsSHA(s.Head) {
		return fmt.Errorf("runspec: spec head %q is not a commit SHA", s.Head)
	}
	if s.Kind.agentic() && s.Base == "" {
		return fmt.Errorf("runspec: a %s spec needs a base", s.Kind)
	}
	for _, c := range []struct{ name, sha string }{{"base", s.Base}, {"priorHead", s.PriorHead}, {"priorBase", s.PriorBase}} {
		if c.sha != "" && !gitfetch.IsSHA(c.sha) {
			return fmt.Errorf("runspec: spec %s %q is not a commit SHA", c.name, c.sha)
		}
	}
	if s.Kind == KindFollowUp && len(s.Thread) == 0 {
		return errors.New("runspec: a followup spec needs the thread it answers")
	}
	if s.Prompt != nil {
		for i, r := range s.Prompt.Reviewers {
			if r.Name == "" || slices.ContainsFunc(s.Prompt.Reviewers[:i], func(o Reviewer) bool { return o.Name == r.Name }) {
				return fmt.Errorf("runspec: spec reviewer %q is unnamed or listed twice", r.Name)
			}
		}
	}
	if s.Kind.agentic() {
		if s.Agent == nil {
			return fmt.Errorf("runspec: a %s spec needs agent limits", s.Kind)
		}
		if s.Model == nil || s.Model.Model == "" || s.Model.GatewayURL == "" {
			return fmt.Errorf("runspec: a %s spec needs a model and the gateway to reach it through", s.Kind)
		}
		if s.Prompt == nil {
			return fmt.Errorf("runspec: a %s spec needs a prompt", s.Kind)
		}
		if c := s.Prompt.Continue; c != nil && (c.RunID == "" || c.Session == "") {
			return fmt.Errorf("runspec: a %s spec's continuation needs a run and a session", s.Kind)
		}
	}
	return nil
}

// Spec size bounds. A spec travels as a key of the run's Secret, which
// Kubernetes caps at 1 MiB with the credentials beside it, so what grows
// with a pull request is cut before it is encoded.
const (
	// MaxBytes bounds an encoded spec, leaving the Secret room for the
	// git and gateway tokens.
	MaxBytes = 900 << 10
	// MaxPriorFindings is how many of the last review's findings a spec
	// carries, and how many dismissed ones.
	MaxPriorFindings = 200
	// MaxBodyBytes bounds the pull request body a spec carries. Encoding
	// can grow it sixfold (each '<' becomes \u003c), which the spec bound
	// still holds.
	MaxBodyBytes = 64 << 10
	// MaxIssueBytes bounds each linked issue's body a spec carries: the
	// prompt shows a few thousand characters of one, and three worst-case
	// issues must leave the body and the findings their room.
	MaxIssueBytes = 16 << 10
)

// maxPromptBytes is what Trim leaves an encoded prompt, under MaxBytes
// with room for the rest of the spec.
const maxPromptBytes = 768 << 10

// Trim cuts what a pull request can grow without bound to what a spec
// carries: the body, at a rune boundary, and the prior findings, first to
// MaxPriorFindings and then, while the encoded prompt is still over its
// share of the spec, by half at a time, keeping the first in the order the
// worker read them.
func (p *Prompt) Trim() {
	p.PullRequest.Body = textcut.Prefix(p.PullRequest.Body, MaxBodyBytes)
	for i := range p.Issues {
		p.Issues[i].Body = textcut.Prefix(p.Issues[i].Body, MaxIssueBytes)
	}
	p.Prior = p.Prior[:min(len(p.Prior), MaxPriorFindings)]
	p.Dismissed = p.Dismissed[:min(len(p.Dismissed), MaxPriorFindings)]
	for len(p.Prior) > 0 || len(p.Dismissed) > 0 {
		b, err := json.Marshal(p)
		if err != nil || len(b) <= maxPromptBytes {
			return
		}
		p.Prior = p.Prior[:len(p.Prior)/2]
		p.Dismissed = p.Dismissed[:len(p.Dismissed)/2]
	}
}

// Encode is the job document a runner reads, refused when it is too
// large to deliver.
func Encode(s Spec) ([]byte, error) {
	b, err := json.Marshal(s)
	if err != nil {
		return nil, fmt.Errorf("runspec: encode spec: %w", err)
	}
	if len(b) > MaxBytes {
		return nil, fmt.Errorf("runspec: encoded spec is %d bytes, over the %d byte limit", len(b), MaxBytes)
	}
	return b, nil
}

// Read reads and strictly decodes the job document at path.
func Read(path string) (Spec, error) {
	f, err := os.Open(path)
	if err != nil {
		return Spec{}, fmt.Errorf("runspec: read spec: %w", err)
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, MaxBytes+1))
	if err != nil {
		return Spec{}, fmt.Errorf("runspec: read spec: %w", err)
	}
	if len(data) > MaxBytes {
		return Spec{}, fmt.Errorf("runspec: spec at %s is over the %d byte limit", path, MaxBytes)
	}
	return Decode(data)
}

// Decode parses a job document strictly: a field a runner of this build
// does not know is an error, not something to ignore.
func Decode(data []byte) (Spec, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var s Spec
	if err := dec.Decode(&s); err != nil {
		return Spec{}, fmt.Errorf("runspec: decode spec: %w", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return Spec{}, errors.New("runspec: decode spec: trailing data after the document")
	}
	if err := s.Validate(); err != nil {
		return Spec{}, err
	}
	return s, nil
}

// Secrets are a run's credentials, delivered apart from the spec: its
// git token, database capability and gateway token, which admits it to
// the egress proxy and, for a review or follow-up, to its model.
type Secrets struct {
	GitToken      string
	GatewayToken  string
	DatabaseToken string
	// Redacted are further values masked as the tokens are, the admin's
	// secret variables for the commands among them: shared by every copy
	// of the secrets, since the run tool adds them as it is built.
	Redacted *Redactions
}

// Redactions are values to mask, added to as a run goes on and read by
// every mask that holds them.
type Redactions struct {
	mu     sync.Mutex
	values []string
}

// Add adds v, once; an empty value is nothing to mask.
func (r *Redactions) Add(v string) {
	if v == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !slices.Contains(r.values, v) {
		r.values = append(r.values, v)
	}
}

// Values are the values added so far.
func (r *Redactions) Values() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.values)
}

// Mask replaces every occurrence of each non-empty secret in text with
// "***", the gateway token also as the Basic credential curl prints for
// its proxy. Longer secrets go first so one containing another is masked
// whole.
func (s Secrets) Mask(text string) string {
	values := []string{s.GitToken, s.GatewayToken, s.DatabaseToken}
	if s.Redacted != nil {
		values = append(values, s.Redacted.Values()...)
	}
	if s.GatewayToken != "" {
		values = append(values, base64.StdEncoding.EncodeToString([]byte(":"+s.GatewayToken)))
	}
	slices.SortFunc(values, func(a, b string) int { return cmp.Compare(len(b), len(a)) })
	for _, v := range values {
		if v != "" {
			text = strings.ReplaceAll(text, v, "***")
		}
	}
	return text
}

// DigestHeader names, on a plain HTTP request a runner makes of the
// gateway, the digest its body must have, as "sha256:<hex>" or
// "sha512:<hex>": the checksum the runner fetched apart from a download
// and checks it against. The gateway's cache keeps such a download by that
// digest, from any host the rules allow, and only a body that has it; the
// gateway marks the request egress.ForDigest and sends the header no
// further. A command can name a digest too, and gets a cached body only if
// it has it; a request the cache passes over, a body without the digest
// among them, gets the origin's answer as is, which the caller still
// checks, as the runner does.
const DigestHeader = "Kritika-Digest"

// AquaRegistryPath is where a runner reads, with its run token, the aqua
// registry the gateway keeps.
const AquaRegistryPath = "/v1/aqua/registry.yaml"
