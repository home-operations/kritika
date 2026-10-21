package runner

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

// SpecVersion is the only job document version this runner understands. A
// worker and runner on different images must agree on it, so a runner
// refuses any other version instead of guessing at its meaning.
const SpecVersion = 37

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
	// configfile.GatewayCaches reports, so a repository's tool, once the
	// run has locked it, is installed through the cache.
	CacheLocked bool `json:"cacheLocked,omitempty"`
	// Env names the variables of the runner's own environment that the
	// run tool's commands get, the admin's, over a repository's [env].
	Env []CommandEnvVar `json:"env,omitempty"`
}

// CommandEnvVar is a variable of the runner's environment its commands
// get. Secret marks one whose value is a Secret's, which is masked as the
// run's tokens are.
type CommandEnvVar struct {
	Name   string `json:"name"`
	Secret bool   `json:"secret,omitempty"`
}

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
	// PriorImpact is the last completed review's merge impact, to reassess.
	PriorImpact []string `json:"priorImpact,omitempty"`
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
	// Tools is the repository's tools as a mise config, the [tools] table
	// the merge-base .kritika.yaml declares, which the run's mise reads
	// over the image's; empty offers the image's tools alone.
	Tools  string         `json:"tools,omitempty"`
	Agent  *AgentLimits   `json:"agent,omitempty"`
	Model  *ModelEndpoint `json:"model,omitempty"`
	Prompt *Prompt        `json:"prompt,omitempty"`
	// Thread is the comments a follow-up answers, oldest first, the last
	// of them the one to answer.
	Thread []review.Message `json:"thread,omitempty"`
	// Reply says the last of them replied in the thread of one of
	// kritika's findings without addressing it: the agent answers only if
	// the reviewer is needed, and may concede the finding.
	Reply bool `json:"reply,omitempty"`
}

// Validate checks a spec is one this runner can carry out.
func (s Spec) Validate() error {
	if s.Version != SpecVersion {
		return fmt.Errorf("runner: spec version %d is not supported (want %d)", s.Version, SpecVersion)
	}
	if !s.Kind.Valid() {
		return fmt.Errorf("runner: spec kind %q is not review, index or followup", s.Kind)
	}
	if s.RunID == "" || s.CloneURL == "" {
		return errors.New("runner: spec needs runId and cloneUrl")
	}
	if !gitfetch.IsSHA(s.Head) {
		return fmt.Errorf("runner: spec head %q is not a commit SHA", s.Head)
	}
	if s.Kind.agentic() && s.Base == "" {
		return fmt.Errorf("runner: a %s spec needs a base", s.Kind)
	}
	for _, c := range []struct{ name, sha string }{{"base", s.Base}, {"priorHead", s.PriorHead}, {"priorBase", s.PriorBase}} {
		if c.sha != "" && !gitfetch.IsSHA(c.sha) {
			return fmt.Errorf("runner: spec %s %q is not a commit SHA", c.name, c.sha)
		}
	}
	if s.Kind == KindFollowUp && len(s.Thread) == 0 {
		return errors.New("runner: a followup spec needs the thread it answers")
	}
	if s.Kind.agentic() {
		if s.Agent == nil {
			return fmt.Errorf("runner: a %s spec needs agent limits", s.Kind)
		}
		if s.Model == nil || s.Model.Model == "" || s.Model.GatewayURL == "" {
			return fmt.Errorf("runner: a %s spec needs a model and the gateway to reach it through", s.Kind)
		}
		if s.Prompt == nil {
			return fmt.Errorf("runner: a %s spec needs a prompt", s.Kind)
		}
		if c := s.Prompt.Continue; c != nil && (c.RunID == "" || c.Session == "") {
			return fmt.Errorf("runner: a %s spec's continuation needs a run and a session", s.Kind)
		}
	}
	return nil
}

// Spec size bounds. A spec travels as a key of the run's Secret, which
// Kubernetes caps at 1 MiB with the credentials beside it, so what grows
// with a pull request is cut before it is encoded.
const (
	// MaxSpecBytes bounds an encoded spec, leaving the Secret room for the
	// git and gateway tokens.
	MaxSpecBytes = 900 << 10
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

// maxPromptBytes is what Trim leaves an encoded prompt, under MaxSpecBytes
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

// EncodeSpec is the job document a runner reads, refused when it is too
// large to deliver.
func EncodeSpec(s Spec) ([]byte, error) {
	b, err := json.Marshal(s)
	if err != nil {
		return nil, fmt.Errorf("runner: encode spec: %w", err)
	}
	if len(b) > MaxSpecBytes {
		return nil, fmt.Errorf("runner: encoded spec is %d bytes, over the %d byte limit", len(b), MaxSpecBytes)
	}
	return b, nil
}

// ReadSpec reads and strictly decodes the job document at path.
func ReadSpec(path string) (Spec, error) {
	f, err := os.Open(path)
	if err != nil {
		return Spec{}, fmt.Errorf("runner: read spec: %w", err)
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, MaxSpecBytes+1))
	if err != nil {
		return Spec{}, fmt.Errorf("runner: read spec: %w", err)
	}
	if len(data) > MaxSpecBytes {
		return Spec{}, fmt.Errorf("runner: spec at %s is over the %d byte limit", path, MaxSpecBytes)
	}
	return DecodeSpec(data)
}

// DecodeSpec parses a job document strictly: a field this runner does not
// know is an error, not something to ignore.
func DecodeSpec(data []byte) (Spec, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var s Spec
	if err := dec.Decode(&s); err != nil {
		return Spec{}, fmt.Errorf("runner: decode spec: %w", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return Spec{}, errors.New("runner: decode spec: trailing data after the document")
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
	// Redacted are the values a mise config marks for redaction, masked
	// as the tokens are: shared by every copy of the secrets, since a
	// tool installed mid-run may add to them.
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
