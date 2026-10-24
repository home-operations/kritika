package store

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/home-operations/kritika/internal/review"
)

// FindingStatus is whether a finding was addressed, a later completed
// review of its pull request, at another head, no longer reporting it, or
// dismissed by a maintainer. An incremental review re-checks each finding
// of the last one and reports it again only while it is still present.
type FindingStatus string

// Finding statuses.
const (
	FindingOpen      FindingStatus = "open"
	FindingAddressed FindingStatus = "addressed"
	FindingDismissed FindingStatus = "dismissed"
)

// findingStatus is the status of a finding that was dismissed, or dropped
// by a later review: a dismissed one is not also addressed.
func findingStatus(dismissed, addressed bool) FindingStatus {
	switch {
	case dismissed:
		return FindingDismissed
	case addressed:
		return FindingAddressed
	}
	return FindingOpen
}

// Valid reports whether s is a finding status.
func (s FindingStatus) Valid() bool {
	return s == FindingOpen || s == FindingAddressed || s == FindingDismissed
}

// AccountFinding is one finding of a pull request, however many of its
// reviews reported it, as the latest of them did.
type AccountFinding struct {
	FindingRow
	ReviewID    string
	PullRequest PullRef
	// FirstSeenAt and LastSeenAt are when the first and the latest
	// reviews that reported it ran.
	FirstSeenAt time.Time
	LastSeenAt  time.Time
}

// PullRef names a pull request.
type PullRef struct {
	Repository string
	Number     int
	Title      string
	URL        string
}

// FindingFilter narrows ListAccountFindings. Zero fields match everything;
// Category matches a finding of that kind; Rule matches a finding that
// cites that rule id; Reviewer matches a finding that reviewer raised,
// review.DefaultReviewer one that names no other; Path matches a path it
// starts; DismissedBy matches a finding that source dismissed; Downvoted
// matches a finding whose comment drew a 👎; Query matches a title,
// explanation, path or pull request title substring, or a pull request
// number.
type FindingFilter struct {
	RepositoryID string
	Severity     review.Severity
	Category     review.Category
	Status       FindingStatus
	Rule         string
	Reviewer     string
	Path         string
	DismissedBy  DismissalSource
	Downvoted    bool
	Query        string
}

// where is the predicate f narrows latest l, joined to its pull request
// p, to, with its arguments.
func (f FindingFilter) where() (string, pgx.StrictNamedArgs, error) {
	if (f.Severity != "" && !f.Severity.Valid()) || (f.Category != "" && !f.Category.Valid()) || (f.Status != "" && !f.Status.Valid()) ||
		(f.DismissedBy != "" && !f.DismissedBy.Valid()) {
		return "", nil, ErrFilter
	}
	number := -1
	if n, err := strconv.ParseInt(strings.TrimPrefix(f.Query, "#"), 10, 32); err == nil {
		number = int(n)
	}
	like := "%" + strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(f.Query) + "%"
	return `(@repository_id::uuid IS NULL OR p.repository_id = @repository_id)
			AND (@severity = '' OR l.severity = @severity)
			AND (@status = '' OR CASE @status WHEN 'dismissed' THEN l.dismissed WHEN 'addressed' THEN l.addressed
				ELSE NOT l.addressed AND NOT l.dismissed END)
			AND (@query = '' OR l.title ILIKE @like OR l.explanation ILIKE @like OR l.path ILIKE @like
				OR p.title ILIKE @like OR p.number = @number)
			AND (@rule = '' OR @rule = ANY (l.rules))
			AND (@agent = '' OR @agent = ANY (l.agents) OR (@agent = @default AND cardinality(l.agents) = 0))
			AND (@category = '' OR l.category = @category)
			AND starts_with(l.path, @path)
			AND (@dismissed_by = '' OR l.dismiss_source = @dismissed_by)
			AND (NOT @downvoted OR l.reactions_down > 0)`, pgx.StrictNamedArgs{
		"repository_id": uuidParam(f.RepositoryID), "severity": string(f.Severity), "status": string(f.Status),
		"query": f.Query, "like": like, "number": number,
		"rule": f.Rule, "category": string(f.Category), "agent": f.Reviewer, "default": review.DefaultReviewer,
		"path": f.Path, "dismissed_by": string(f.DismissedBy), "downvoted": f.Downvoted,
	}, nil
}

// findingIssues are the common table expressions over every finding of
// the account's completed reviews: seen is each report of one, and latest
// one row per pull request and fingerprint, as its latest review reported
// it, with when it was first reported, whether a maintainer dismissed it,
// and whether a later completed review at another head dropped it, which
// a dismissed finding does not count as. A finding stored without a
// fingerprint is its own.
var findingIssues = findingIssuesOf("")

// findingIssuesOf is findingIssues over the completed reviews scope keeps:
// a predicate on v, the review, led by AND, or "" for every one. It is the
// one definition of what became of a finding, whoever asks. The base fragment
// must stay parameter-free; scope must use the enclosing query's binding style.
func findingIssuesOf(scope string) string {
	return `seen AS (
		SELECT f.id, f.path, f.line, f.end_line, f.severity, f.category, f.title, f.explanation, f.suggested_fix, f.replacement,
			f.agent_prompt, f.fingerprint, f.posted_inline, f.forge_comment_id, f.created_at, f.reactions_up, f.reactions_down, f.rules,
			f.agents, v.id AS review_id, v.pull_request_id, v.head_sha, v.created_at AS seen_at,
			row_number() OVER newest AS nth, min(v.created_at) OVER issue AS first_at
		FROM findings f JOIN reviews v ON v.id = f.review_id
		WHERE v.status = 'completed'` + scope + `
		WINDOW issue AS (PARTITION BY v.pull_request_id, coalesce(nullif(f.fingerprint, ''), f.id::text)),
			newest AS (issue ORDER BY v.created_at DESC, v.id DESC)),
	latest AS (
		SELECT s.*, d.pull_request_id IS NOT NULL AS dismissed, coalesce(d.reason, '') AS dismiss_reason,
			coalesce(d.source, '') AS dismiss_source,
			d.pull_request_id IS NULL AND EXISTS (SELECT 1 FROM reviews n WHERE n.pull_request_id = s.pull_request_id
				AND n.status = 'completed' AND n.created_at > s.seen_at AND n.head_sha <> s.head_sha) AS addressed
		FROM seen s LEFT JOIN dismissals d ON d.pull_request_id = s.pull_request_id AND d.fingerprint = s.fingerprint
		WHERE s.nth = 1)`
}

var accountFindings = `WITH ` + findingIssues + `
	SELECT l.id, l.path, l.line, l.end_line, l.severity, l.category, l.title, l.explanation, l.suggested_fix, l.replacement,
		l.agent_prompt, l.fingerprint, l.posted_inline, l.forge_comment_id, l.created_at, l.reactions_up, l.reactions_down, l.rules,
		l.agents, l.review_id, r.name, p.number, p.title, p.url, l.addressed, l.dismissed, l.dismiss_reason, l.dismiss_source, l.first_at,
		l.seen_at
	FROM latest l JOIN pull_requests p ON p.id = l.pull_request_id JOIN repositories r ON r.id = p.repository_id`

// ListAccountFindings returns a page of the account's findings, most
// recently reported first.
func ListAccountFindings(ctx context.Context, tx pgx.Tx, f FindingFilter, p Page) ([]AccountFinding, *Cursor, error) {
	if err := p.check(); err != nil {
		return nil, nil, err
	}
	where, args, err := f.where()
	if err != nil {
		return nil, nil, err
	}
	args["first"], args["after_time"], args["after_id"], args["limit"] = p.After.First(), p.After.T, p.afterID(), p.Limit+1
	rows, err := tx.Query(ctx, accountFindings+`
		WHERE `+where+`
			AND (@first OR (l.seen_at, l.id) < (@after_time, @after_id::uuid))
		ORDER BY l.seen_at DESC, l.id DESC LIMIT @limit`, args)
	if err != nil {
		return nil, nil, fmt.Errorf("store: list account findings: %w", err)
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (AccountFinding, error) {
		var a AccountFinding
		var sev, cat string
		var addressed, dismissed bool
		err := row.Scan(&a.ID, &a.Path, &a.Line, &a.EndLine, &sev, &cat, &a.Title, &a.Explanation, &a.SuggestedFix, &a.Replacement,
			&a.AgentPrompt, &a.Fingerprint, &a.PostedInline, &a.ForgeCommentID, &a.CreatedAt, &a.ReactionsUp, &a.ReactionsDown, &a.Rules,
			&a.Reviewers, &a.ReviewID, &a.PullRequest.Repository, &a.PullRequest.Number, &a.PullRequest.Title, &a.PullRequest.URL,
			&addressed, &dismissed, &a.DismissReason, &a.DismissSource, &a.FirstSeenAt, &a.LastSeenAt)
		a.Severity, a.Category, a.Status = review.Severity(sev), review.Category(cat), findingStatus(dismissed, addressed)
		return a, err
	})
	if err != nil {
		return nil, nil, fmt.Errorf("store: list account findings: %w", err)
	}
	items, next := paged(out, p.Limit, func(a AccountFinding) Cursor { return Cursor{T: a.LastSeenAt, ID: a.ID} })
	return items, next, nil
}

// RuleCitation is how many of a repository's findings cite a rule id, and
// what became of those, counting a finding once per pull request as
// ListAccountFindings lists it.
type RuleCitation struct {
	Repository string
	Rule       string
	FindingCounts
}

// FindingCounts is how many findings there are, how many of those were
// addressed, how many dismissed, and how many drew a 👎 on their comment.
type FindingCounts struct {
	Findings, Addressed, Dismissed, Downvoted int
}

// findingCounts counts the findings of latest l a query groups as
// FindingCounts.
const findingCounts = `count(*), count(*) FILTER (WHERE l.addressed), count(*) FILTER (WHERE l.dismissed),
	count(*) FILTER (WHERE l.reactions_down > 0)`

// RuleCitations counts the account's findings by repository and the rule
// ids they cite.
func RuleCitations(ctx context.Context, tx pgx.Tx) ([]RuleCitation, error) {
	rows, err := tx.Query(ctx, `WITH `+findingIssues+`
		SELECT r.name, c.rule, `+findingCounts+`
		FROM latest l JOIN pull_requests p ON p.id = l.pull_request_id JOIN repositories r ON r.id = p.repository_id,
			unnest(l.rules) AS c(rule)
		GROUP BY r.name, c.rule ORDER BY r.name, c.rule`)
	if err != nil {
		return nil, fmt.Errorf("store: count rule citations: %w", err)
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (RuleCitation, error) {
		var c RuleCitation
		err := row.Scan(&c.Repository, &c.Rule, &c.Findings, &c.Addressed, &c.Dismissed, &c.Downvoted)
		return c, err
	})
	if err != nil {
		return nil, fmt.Errorf("store: count rule citations: %w", err)
	}
	return out, nil
}

// FindingGroup is the findings a filter keeps that share a Key, counted: a
// category, a rule id, an agent's name, review.DefaultReviewer for the
// default pass, or a directory of Repository one below the filter's Path:
// the Path and the next directory of a file's path after it, with a
// trailing slash, so that a Path filter of it keeps the group's findings
// and no other, or the Path itself for a file in no directory below it.
type FindingGroup struct {
	Repository string
	Key        string
	FindingCounts
}

// FindingSummary is the findings a filter keeps, counted once per pull
// request as ListAccountFindings lists them, and the most common
// categories, rules, agents and directories among them.
type FindingSummary struct {
	FindingCounts
	Categories, Rules, Agents, Directories []FindingGroup
}

// summaryTop is how many groups of each kind a FindingSummary keeps.
const summaryTop = 6

// SummarizeFindings counts the account's findings f keeps, and the
// summaryTop most common groups of each kind among them, the most common
// first. A finding that cites several rules, or that several agents
// raised, counts in each of their groups.
func SummarizeFindings(ctx context.Context, tx pgx.Tx, f FindingFilter) (FindingSummary, error) {
	var out FindingSummary
	where, args, err := f.where()
	if err != nil {
		return out, err
	}
	args["top"] = summaryTop
	rows, err := tx.Query(ctx, `WITH `+findingIssues+`,
		kept AS (
			SELECT l.category, l.rules, l.agents, l.path, l.addressed, l.dismissed, l.reactions_down, r.name AS repository
			FROM latest l JOIN pull_requests p ON p.id = l.pull_request_id JOIN repositories r ON r.id = p.repository_id
			WHERE `+where+`),
		grouped (kind, repository, key, findings, addressed, dismissed, downvoted) AS (
			SELECT 'total', '', '', `+findingCounts+` FROM kept l
			UNION ALL SELECT 'category', '', l.category, `+findingCounts+` FROM kept l WHERE l.category <> '' GROUP BY l.category
			UNION ALL SELECT 'rule', '', c.rule, `+findingCounts+` FROM kept l, unnest(l.rules) AS c(rule) GROUP BY c.rule
			UNION ALL SELECT 'agent', '', a.agent, `+findingCounts+` FROM kept l,
				unnest(CASE cardinality(l.agents) WHEN 0 THEN ARRAY[@default::text] ELSE l.agents END) AS a(agent) GROUP BY a.agent
			UNION ALL SELECT 'directory', l.repository, d.dir, `+findingCounts+` FROM kept l,
				LATERAL (SELECT substr(l.path, char_length(@path::text) + 1) AS rest) r,
				LATERAL (SELECT CASE WHEN strpos(r.rest, '/') > 0 THEN @path::text || split_part(r.rest, '/', 1) || '/' ELSE @path::text END AS dir) d
				GROUP BY l.repository, d.dir)
		SELECT kind, repository, key, findings, addressed, dismissed, downvoted FROM (
			SELECT g.*, row_number() OVER (PARTITION BY kind ORDER BY findings DESC, repository, key) AS nth FROM grouped g) ranked
		WHERE nth <= @top ORDER BY kind, nth`, args)
	if err != nil {
		return out, fmt.Errorf("store: summarize findings: %w", err)
	}
	var kind string
	var g FindingGroup
	_, err = pgx.ForEachRow(rows, []any{&kind, &g.Repository, &g.Key, &g.Findings, &g.Addressed, &g.Dismissed, &g.Downvoted}, func() error {
		switch kind {
		case "total":
			out.FindingCounts = g.FindingCounts
		case "category":
			out.Categories = append(out.Categories, g)
		case "rule":
			out.Rules = append(out.Rules, g)
		case "agent":
			out.Agents = append(out.Agents, g)
		case "directory":
			out.Directories = append(out.Directories, g)
		}
		return nil
	})
	if err != nil {
		return out, fmt.Errorf("store: summarize findings: %w", err)
	}
	return out, nil
}
