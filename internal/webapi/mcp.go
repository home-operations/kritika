package webapi

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/riverqueue/river/rivertype"

	"github.com/home-operations/kritika/internal/auth"
	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/review"
	"github.com/home-operations/kritika/internal/store"
)

// mcpPath is where the MCP endpoint is served, below the base path.
const mcpPath = "/mcp"

// mcpInstructions tell an agent how to name what it reads.
const mcpInstructions = "Reads kritika's reviews of the pull requests in the accounts your token reads. " +
	"Name a repository as owner/name: its owner is its account. " +
	"Pass forge only when your token reads accounts of that name on more than one forge."

// mcpHandler serves the read-only MCP endpoint over stateless Streamable
// HTTP, so any replica answers any request. Only a personal access token
// authenticates it. Its tools read the token's accounts as the dashboard's
// routes do, but leave out runner, usage and cost data.
func (s *Server) mcpHandler() http.Handler {
	srv := mcp.NewServer(&mcp.Implementation{Name: "kritika", Version: s.version}, &mcp.ServerOptions{Instructions: mcpInstructions})
	readOnly := &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: new(false)}
	mcp.AddTool(srv, &mcp.Tool{Name: "get_pull", Annotations: readOnly, Description: "A pull request and its reviews, newest first: " +
		"each one's status, the head commit it reviewed, and why it was skipped or failed; its review job that has not finished, " +
		"if any; and the bot's follow-ups to @-mentions. Compare its headSha with its reviews' to see whether a push was reviewed."},
		tool(s, s.getPullTool))
	mcp.AddTool(srv, &mcp.Tool{Name: "get_review", Annotations: readOnly, Description: "One review: its summary and findings, " +
		"each with where it is, why it matters, a suggested fix, a prompt for a coding agent, the rules it enforces, " +
		"and what became of it: open, addressed or dismissed."},
		tool(s, s.getReviewTool))
	mcp.AddTool(srv, &mcp.Tool{Name: "list_findings", Annotations: readOnly, Description: "The findings of an account, " +
		"or of one repository, one per pull request and fingerprint, most recently reported first, as their latest review " +
		"reported them. Pass nextCursor back as cursor for the next page."},
		tool(s, s.listFindingsTool))
	mcp.AddTool(srv, &mcp.Tool{Name: "common_findings", Annotations: readOnly, Description: "What keeps coming up among " +
		"the findings list_findings would list: how many there are, and their most common categories, rules, agents " +
		"and directories, each with how many of its findings were addressed, dismissed or drew a 👎."},
		tool(s, s.commonFindingsTool))
	mcp.AddTool(srv, &mcp.Tool{Name: "list_rules", Annotations: readOnly, Description: "The written rules and context files " +
		"an account's repositories, or one repository, are reviewed with, each with the repositories that read it. " +
		"A written rule also counts the findings that cite it, and how many of those were addressed, dismissed or drew a 👎."},
		tool(s, s.listRulesTool))
	mcp.AddTool(srv, &mcp.Tool{Name: "list_followups", Annotations: readOnly, Description: "The bot's follow-ups to " +
		"@-mentions in an account, or in one repository, newest first: who asked, where, and what came of it. " +
		"Pass nextCursor back as cursor for the next page."},
		tool(s, s.listFollowupsTool))
	sdk := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, &mcp.StreamableHTTPOptions{
		Stateless:    true,
		JSONResponse: true,
		Logger:       s.logger,
		// The protection refuses a loopback connection whose Host is not
		// loopback, which a reverse proxy on the same host makes. It guards
		// against DNS rebinding, and a rebound page cannot send the bearer
		// token every request needs.
		DisableLocalhostProtection: true,
	})
	return s.auth.RequireToken(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sdk.ServeHTTP(noStore{w}, r.WithContext(context.WithValue(r.Context(), requestKey{}, r.Context())))
	}))
}

// requestKey holds, in a tool's context, the context of the request that
// called it.
type requestKey struct{}

// noStore keeps a response's Cache-Control at no-store, which the SDK
// replaces: what a tool reports is its principal's alone.
type noStore struct{ http.ResponseWriter }

func (w noStore) WriteHeader(status int) {
	w.Header().Set("Cache-Control", "no-store")
	w.ResponseWriter.WriteHeader(status)
}

func (w noStore) Write(b []byte) (int, error) {
	w.Header().Set("Cache-Control", "no-store")
	return w.ResponseWriter.Write(b)
}

func (w noStore) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// tool adapts read, which reads the account its arguments name, to a tool
// handler: it resolves that account for the token's principal, and reports
// an error as the dashboard's routes would answer it, logging one that is
// unexpected.
func tool[In interface{ scope() scopeArgs }, Out any](
	s *Server, read func(context.Context, *accountScope, In) (Out, error),
) mcp.ToolHandlerFor[In, Out] {
	return func(ctx context.Context, req *mcp.CallToolRequest, in In) (*mcp.CallToolResult, Out, error) {
		// The SDK keeps a tool's context from ending with its request before
		// protocol version 2026-07-28; a stateless request can deliver no
		// answer once it ends, so its reads stop then, as a route's do.
		if r, ok := ctx.Value(requestKey{}).(context.Context); ok {
			var cancel context.CancelFunc
			ctx, cancel = context.WithCancel(ctx)
			defer cancel()
			defer context.AfterFunc(r, cancel)()
		}
		var out Out
		t, err := scopeAccount(s.current.Get(), auth.PrincipalFrom(ctx), in.scope())
		if err == nil {
			out, err = read(ctx, t, in)
		}
		if err == nil {
			return nil, out, nil
		}
		_, body, expected := classify(err)
		if !expected && ctx.Err() == nil {
			s.logger.ErrorContext(ctx, "webapi: tool failed", "tool", req.Params.Name, "error", err)
		}
		return nil, out, errors.New(body.Message)
	}
}

// scopeArgs name what a tool reads: one repository, or a whole account.
type scopeArgs struct {
	Repository string `json:"repository,omitempty" jsonschema:"a repository as owner/name, to read it alone; its owner is its account"`
	Account    string `json:"account,omitempty" jsonschema:"an account, to read all of its repositories"`
	Forge      string `json:"forge,omitempty" jsonschema:"github, forgejo, gitlab or gitea, when you read the name on several forges"`
}

func (a scopeArgs) scope() scopeArgs { return a }

// repoArgs name one repository.
type repoArgs struct {
	Repository string `json:"repository" jsonschema:"the repository as owner/name; its owner is its account"`
	Forge      string `json:"forge,omitempty" jsonschema:"github, forgejo, gitlab or gitea, when you read the name on several forges"`
}

func (a repoArgs) scope() scopeArgs { return scopeArgs{Repository: a.Repository, Forge: a.Forge} }

// scopeAccount resolves the account a names for p: the account, or the
// repository's owner, on a.Forge, or else on the one forge where p reads
// an account of that name. As resolveAccount does, it reports an account
// p may not read as missing.
func scopeAccount(file *configfile.File, p *auth.Principal, a scopeArgs) (*accountScope, error) {
	name := a.Account
	if a.Repository != "" {
		owner, _, _ := strings.Cut(a.Repository, "/")
		if name != "" && !strings.EqualFold(name, owner) {
			return nil, errBadRequest(CodeBadRequest, "the repository is not the account's")
		}
		name = owner
	}
	if name == "" {
		return nil, errBadRequest(CodeBadRequest, "name a repository or an account")
	}
	if a.Forge != "" {
		return resolveAccount(file, p, a.Forge, name)
	}
	var found *configfile.Account
	for i := range file.Accounts {
		acct := &file.Accounts[i]
		if !strings.EqualFold(acct.Name, name) || !p.CanRead(acct.ID()) {
			continue
		}
		if found != nil {
			return nil, errBadRequest(CodeAmbiguous, "you read accounts named "+name+" on several forges; pass forge")
		}
		found = acct
	}
	if found == nil {
		return nil, errNotFound("account")
	}
	return &accountScope{file: file, account: found, principal: p}, nil
}

// pageArgs ask for one page of a list.
type pageArgs struct {
	Limit  int    `json:"limit,omitempty" jsonschema:"how many to return: 50 unless given, 200 at most"`
	Cursor string `json:"cursor,omitempty" jsonschema:"the nextCursor of the page before"`
}

func (a pageArgs) page() (store.Page, error) {
	p := store.Page{Limit: defaultLimit}
	if a.Limit < 0 {
		return p, errBadRequest(CodeBadRequest, "limit must be a positive integer")
	}
	if a.Limit > 0 {
		p.Limit = min(a.Limit, maxLimit)
	}
	if a.Cursor != "" {
		c, err := decodeCursor(a.Cursor)
		if err != nil {
			return p, err
		}
		p.After = c
	}
	return p, nil
}

// findingFilterArgs narrow findings; each one left out keeps them all.
type findingFilterArgs struct {
	Severity  string `json:"severity,omitempty" jsonschema:"p0, p1 or p2"`
	Category  string `json:"category,omitempty" jsonschema:"correctness, security, performance, reliability, maintainability or tests"`
	Status    string `json:"status,omitempty" jsonschema:"open, addressed or dismissed"`
	Dismissed string `json:"dismissed,omitempty" jsonschema:"what dismissed it: command, resolved or conceded"`
	Rule      string `json:"rule,omitempty" jsonschema:"the id of a rule it enforces"`
	Agent     string `json:"agent,omitempty" jsonschema:"the reviewer that raised it, default for the default reviewer"`
	Path      string `json:"path,omitempty" jsonschema:"what its path starts with: a file, or a directory with a trailing slash"`
	Downvoted bool   `json:"downvoted,omitempty" jsonschema:"only findings whose comment drew a 👎"`
	Pull      string `json:"pr,omitempty" jsonschema:"how its pull request stands: open, merged, or closed without merging"`
	Query     string `json:"query,omitempty" jsonschema:"text in its title, explanation or path, or its pull request's title or #number"`
}

func (a findingFilterArgs) filter() (store.FindingFilter, error) {
	f := store.FindingFilter{
		Severity: review.Severity(a.Severity), Category: review.Category(a.Category), Status: store.FindingStatus(a.Status),
		DismissedBy: store.DismissalSource(a.Dismissed), Rule: a.Rule, Reviewer: a.Agent, Path: a.Path, Downvoted: a.Downvoted,
		Pull: store.FindingPull(a.Pull), Query: a.Query,
	}
	return f, checkFindingFilter(f)
}

type pullArgs struct {
	repoArgs
	Number int64 `json:"number" jsonschema:"the pull request's number"`
}

// toolReview is a review as the tools report it: without the model it ran
// on or what its model calls used and cost.
type toolReview struct {
	ID         string             `json:"id"`
	Status     store.ReviewStatus `json:"status"`
	Trigger    string             `json:"trigger"`
	Scope      review.Scope       `json:"scope"`
	HeadSHA    string             `json:"headSha"`
	CreatedAt  time.Time          `json:"createdAt"`
	FinishedAt *time.Time         `json:"finishedAt"`
	SkipReason string             `json:"skipReason"`
	Error      string             `json:"error"`
	Confidence *Confidence        `json:"confidence"`
}

func toolReviewOf(v store.ReviewRow) toolReview {
	r := reviewItem(v)
	return toolReview{
		ID: r.ID, Status: r.Status, Trigger: r.Trigger, Scope: r.Scope, HeadSHA: r.HeadSHA, CreatedAt: r.CreatedAt, FinishedAt: r.FinishedAt,
		SkipReason: r.SkipReason, Error: r.Error, Confidence: r.Confidence,
	}
}

// toolJob is a pull request's review job that has not finished: waiting,
// running, or retrying after LastError.
type toolJob struct {
	State       rivertype.JobState `json:"state"`
	HeadSHA     string             `json:"headSha"`
	Attempt     int                `json:"attempt"`
	MaxAttempts int                `json:"maxAttempts"`
	ScheduledAt time.Time          `json:"scheduledAt"`
	LastError   string             `json:"lastError"`
	Cause       JobCause           `json:"cause"`
}

// toolPull is a pull request as get_pull reports it, without its cost.
type toolPull struct {
	Repository string       `json:"repository"`
	Number     int          `json:"number"`
	Title      string       `json:"title"`
	Author     string       `json:"author"`
	State      string       `json:"state"`
	Draft      bool         `json:"draft"`
	Merged     bool         `json:"merged"`
	Paused     bool         `json:"paused"`
	HeadSHA    string       `json:"headSha"`
	URL        string       `json:"url"`
	Reviews    []toolReview `json:"reviews"`
	Job        *toolJob     `json:"job"`
	Followups  []Followup   `json:"followups"`
}

func (s *Server) getPullTool(ctx context.Context, t *accountScope, in pullArgs) (toolPull, error) {
	var rec pullRecord
	if err := s.read(ctx, t, func(tx pgx.Tx) error {
		p, err := lookupPull(ctx, tx, in.Repository, in.Number)
		if err != nil {
			return err
		}
		rec, err = loadPull(ctx, tx, p)
		return err
	}); err != nil {
		return toolPull{}, err
	}
	p := rec.pull
	out := toolPull{
		Repository: p.Repository, Number: p.Number, Title: p.Title, Author: p.Author, State: p.State, Draft: p.Draft, Merged: p.Merged,
		Paused: p.Paused, HeadSHA: p.HeadSHA, URL: p.URL, Reviews: make([]toolReview, len(rec.reviews)), Followups: followupList(rec.followups),
	}
	for i, v := range rec.reviews {
		out.Reviews[i] = toolReviewOf(v)
	}
	if j := rec.job; j != nil {
		out.Job = &toolJob{
			State: j.State, HeadSHA: j.Head, Attempt: j.Attempt, MaxAttempts: j.MaxAttempts, ScheduledAt: j.ScheduledAt, LastError: j.LastError,
			Cause: jobCause(j.LastError),
		}
	}
	return out, nil
}

type reviewArgs struct {
	repoArgs
	ReviewID string `json:"reviewId" jsonschema:"the review's id, as get_pull or list_findings report it"`
}

// toolReviewDetail is a review as get_review reports it, with its pull
// request, summary and findings. NewestReviewID is the pull request's
// newest review that was not skipped, null when none is newer than this.
type toolReviewDetail struct {
	toolReview
	Pull           PullRef   `json:"pull"`
	NewestReviewID *string   `json:"newestReviewId"`
	Summary        *Summary  `json:"summary"`
	Findings       []Finding `json:"findings"`
}

func (s *Server) getReviewTool(ctx context.Context, t *accountScope, in reviewArgs) (toolReviewDetail, error) {
	var v store.ReviewRow
	var findings []store.FindingRow
	if err := s.read(ctx, t, func(tx pgx.Tx) error {
		var err error
		v, err = store.FindReview(ctx, tx, in.ReviewID)
		if errors.Is(err, store.ErrNotFound) || (err == nil && !strings.EqualFold(v.Repository, in.Repository)) {
			return errNotFound("review")
		}
		if err != nil {
			return err
		}
		findings, err = store.ListFindings(ctx, tx, v.ID)
		return err
	}); err != nil {
		return toolReviewDetail{}, err
	}
	out := toolReviewDetail{
		toolReview: toolReviewOf(v), Pull: PullRef{Repository: v.Repository, Number: v.Number, Title: v.Title, URL: v.URL},
		NewestReviewID: v.NewestReviewID, Findings: make([]Finding, len(findings)),
	}
	if v.Summary != nil {
		out.Summary = &Summary{Headline: v.Summary.Headline, Take: v.Summary.Take, Praise: v.Summary.Praise, Diagram: v.Summary.Diagram}
	}
	for i, f := range findings {
		out.Findings[i] = finding(f)
	}
	return out, nil
}

type findingsArgs struct {
	scopeArgs
	findingFilterArgs
	pageArgs
}

func (s *Server) listFindingsTool(ctx context.Context, t *accountScope, in findingsArgs) (Page[AccountFinding], error) {
	page, err := in.page()
	if err != nil {
		return Page[AccountFinding]{}, err
	}
	f, err := in.filter()
	if err != nil {
		return Page[AccountFinding]{}, err
	}
	var rows []store.AccountFinding
	var next *store.Cursor
	if err := s.read(ctx, t, func(tx pgx.Tx) error {
		var err error
		if f.RepositoryID, err = repoID(ctx, tx, in.Repository); err != nil {
			return err
		}
		rows, next, err = store.ListAccountFindings(ctx, tx, f, page)
		return err
	}); err != nil {
		return Page[AccountFinding]{}, err
	}
	return newPage(accountFindings(rows), next), nil
}

type commonFindingsArgs struct {
	scopeArgs
	findingFilterArgs
}

func (s *Server) commonFindingsTool(ctx context.Context, t *accountScope, in commonFindingsArgs) (FindingSummary, error) {
	f, err := in.filter()
	if err != nil {
		return FindingSummary{}, err
	}
	var sum store.FindingSummary
	if err := s.read(ctx, t, func(tx pgx.Tx) error {
		var err error
		if f.RepositoryID, err = repoID(ctx, tx, in.Repository); err != nil {
			return err
		}
		sum, err = store.SummarizeFindings(ctx, tx, f)
		return err
	}); err != nil {
		return FindingSummary{}, err
	}
	return findingSummary(sum), nil
}

// toolRules is what list_rules reports.
type toolRules struct {
	Rules []Rule `json:"rules"`
}

func (s *Server) listRulesTool(ctx context.Context, t *accountScope, in scopeArgs) (toolRules, error) {
	rules, err := s.accountRules(ctx, t, in.Repository)
	return toolRules{Rules: rules}, err
}

type followupsArgs struct {
	scopeArgs
	pageArgs
}

func (s *Server) listFollowupsTool(ctx context.Context, t *accountScope, in followupsArgs) (Page[Followup], error) {
	page, err := in.page()
	if err != nil {
		return Page[Followup]{}, err
	}
	var rows []store.FollowupRow
	var next *store.Cursor
	if err := s.read(ctx, t, func(tx pgx.Tx) error {
		var f store.FollowupFilter
		var err error
		if f.RepositoryID, err = repoID(ctx, tx, in.Repository); err != nil {
			return err
		}
		rows, next, err = store.ListFollowups(ctx, tx, f, page)
		return err
	}); err != nil {
		return Page[Followup]{}, err
	}
	return newPage(followupList(rows), next), nil
}
