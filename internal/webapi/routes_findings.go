package webapi

import (
	"fmt"
	"mime"
	"net/http"
	"net/url"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/home-operations/kritika/internal/review"
	"github.com/home-operations/kritika/internal/store"
)

// exportLimit is how many findings an export holds at most: the newest.
const exportLimit = 1000

// listFindings serves the account's findings, one per pull request and
// fingerprint, most recently reported first.
func (s *Server) listFindings(w http.ResponseWriter, r *http.Request, t *accountScope) error {
	page, err := parsePage(r)
	if err != nil {
		return err
	}
	f, err := findingFilter(r)
	if err != nil {
		return err
	}
	ctx := r.Context()
	var rows []store.AccountFinding
	var next *store.Cursor
	if err := s.read(ctx, t, func(tx pgx.Tx) error {
		id, found, err := searchRepoFilter(ctx, tx, r)
		if err != nil || !found {
			return err
		}
		f.RepositoryID = id
		rows, next, err = store.ListAccountFindings(ctx, tx, f, page)
		return err
	}); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, newPage(accountFindings(rows), next))
	return nil
}

// summarizeFindings serves the counts of the findings the list's filters
// keep, and their most common categories, rules, agents and directories.
func (s *Server) summarizeFindings(w http.ResponseWriter, r *http.Request, t *accountScope) error {
	f, err := findingFilter(r)
	if err != nil {
		return err
	}
	ctx := r.Context()
	var sum store.FindingSummary
	if err := s.read(ctx, t, func(tx pgx.Tx) error {
		id, found, err := searchRepoFilter(ctx, tx, r)
		if err != nil || !found {
			return err
		}
		f.RepositoryID = id
		sum, err = store.SummarizeFindings(ctx, tx, f)
		return err
	}); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, findingSummary(sum))
	return nil
}

func findingSummary(sum store.FindingSummary) FindingSummary {
	return FindingSummary{
		FindingCounts: findingCounts(sum.FindingCounts), Categories: findingGroups(sum.Categories), Rules: findingGroups(sum.Rules),
		Agents: findingGroups(sum.Agents), Directories: findingGroups(sum.Directories),
	}
}

// exportFindings serves the findings the list's filters keep, newest
// first and at most exportLimit of them, as a file to download: Markdown,
// to hand to a person or a coding agent, or JSON.
func (s *Server) exportFindings(w http.ResponseWriter, r *http.Request, t *accountScope) error {
	format := r.URL.Query().Get("format")
	if format != "markdown" && format != "json" {
		return errBadRequest(CodeBadRequest, "format must be markdown or json")
	}
	f, err := findingFilter(r)
	if err != nil {
		return err
	}
	ctx := r.Context()
	var rows []store.AccountFinding
	var next *store.Cursor
	if err := s.read(ctx, t, func(tx pgx.Tx) error {
		id, found, err := searchRepoFilter(ctx, tx, r)
		if err != nil || !found {
			return err
		}
		f.RepositoryID = id
		rows, next, err = store.ListAccountFindings(ctx, tx, f, store.Page{Limit: exportLimit})
		return err
	}); err != nil {
		return err
	}
	items, truncated := accountFindings(rows), next != nil
	name := "kritika-findings-" + t.account.Name
	if format == "json" {
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name + ".json"}))
		writeJSON(w, http.StatusOK, FindingExport{Items: items, Truncated: truncated})
		return nil
	}
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name + ".md"}))
	w.WriteHeader(http.StatusOK)
	md := findingsMarkdown(t.account.Slug(), filterText(r.URL.Query()), items, truncated)
	_, _ = w.Write([]byte(md)) // a failed write has no one to report to
	return nil
}

// findingFilter reads the findings filters of r's query, all but the
// repository, which a handler looks up in its read.
func findingFilter(r *http.Request) (store.FindingFilter, error) {
	q := r.URL.Query()
	f := store.FindingFilter{
		Severity: review.Severity(q.Get("severity")), Category: review.Category(q.Get("category")),
		Status: store.FindingStatus(q.Get("status")), Rule: q.Get("rule"), Reviewer: q.Get("agent"), Path: q.Get("path"),
		DismissedBy: store.DismissalSource(q.Get("dismissed")), Query: q.Get("q"),
	}
	if err := checkFindingFilter(f); err != nil {
		return f, err
	}
	switch q.Get("is") {
	case "":
	case "downvoted":
		f.Downvoted = true
	default:
		return f, errBadRequest(CodeBadRequest, "is must be downvoted")
	}
	return f, nil
}

// checkFindingFilter refuses a severity, category, status or dismissal
// source that is none of them.
func checkFindingFilter(f store.FindingFilter) error {
	switch {
	case f.Severity != "" && !f.Severity.Valid():
		return errBadRequest(CodeBadRequest, "severity must be p0, p1 or p2")
	case f.Category != "" && !f.Category.Valid():
		return errBadRequest(CodeBadRequest, "category must be correctness, security, performance, reliability, maintainability or tests")
	case f.Status != "" && !f.Status.Valid():
		return errBadRequest(CodeBadRequest, "status must be open, addressed or dismissed")
	case f.DismissedBy != "" && !f.DismissedBy.Valid():
		return errBadRequest(CodeBadRequest, "dismissed must be command, resolved or conceded")
	}
	return nil
}

func accountFindings(rows []store.AccountFinding) []AccountFinding {
	items := make([]AccountFinding, len(rows))
	for i, a := range rows {
		items[i] = AccountFinding{
			Finding: finding(a.FindingRow), ReviewID: a.ReviewID,
			FirstSeenAt: a.FirstSeenAt, LastSeenAt: a.LastSeenAt, Pull: pullRef(a.PullRequest),
		}
	}
	return items
}

func findingCounts(c store.FindingCounts) FindingCounts {
	return FindingCounts{Findings: c.Findings, Addressed: c.Addressed, Dismissed: c.Dismissed, Downvoted: c.Downvoted}
}

func findingGroups(groups []store.FindingGroup) []FindingGroup {
	out := make([]FindingGroup, len(groups))
	for i, g := range groups {
		out[i] = FindingGroup{Key: g.Key, Repository: g.Repository, FindingCounts: findingCounts(g.FindingCounts)}
	}
	return out
}

// filterKeys are the findings filters in the order the dashboard's search
// box writes them.
var filterKeys = []string{"repo", "severity", "category", "status", "dismissed", "rule", "agent", "path", "is"}

// filterText is the filters of an export's query as the dashboard's search
// box shows them, its free text last.
func filterText(q url.Values) string {
	var parts []string
	for _, k := range filterKeys {
		if v := q.Get(k); v != "" {
			parts = append(parts, k+":"+v)
		}
	}
	if v := q.Get("q"); v != "" {
		parts = append(parts, v)
	}
	return strings.Join(parts, " ")
}

// findingsMarkdown is an export's findings as Markdown: a heading per
// finding, then where it is and what became of it, then its explanation
// quoted, so headings in an explanation stay inside it.
func findingsMarkdown(account, filter string, items []AccountFinding, truncated bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# kritika findings of %s\n\n", account)
	count := fmt.Sprintf("%d findings", len(items))
	if len(items) == 1 {
		count = "1 finding"
	}
	if filter != "" {
		count += " matching " + oneLine(filter)
	}
	b.WriteString(count + ", newest first.")
	if truncated {
		b.WriteString(" The filters keep more: narrow them for the rest.")
	}
	b.WriteString("\n")
	for _, f := range items {
		fmt.Fprintf(&b, "\n## %s\n\n", oneLine(f.Title))
		facts := []string{string(f.Severity)}
		if f.Category != "" {
			facts = append(facts, string(f.Category))
		}
		facts = append(facts, string(f.Status))
		fmt.Fprintf(&b, "- %s\n", strings.Join(facts, " · "))
		where := f.Path
		switch {
		case f.Line > 0 && f.EndLine > f.Line:
			where = fmt.Sprintf("%s:%d-%d", f.Path, f.Line, f.EndLine)
		case f.Line > 0:
			where = fmt.Sprintf("%s:%d", f.Path, f.Line)
		}
		fmt.Fprintf(&b, "- %s in %s#%d, %s: %s\n", where, f.Pull.Repository, f.Pull.Number, oneLine(f.Pull.Title), f.Pull.URL)
		if len(f.Rules) > 0 {
			fmt.Fprintf(&b, "- Rules: %s\n", strings.Join(f.Rules, ", "))
		}
		if len(f.Agents) > 0 {
			fmt.Fprintf(&b, "- Agents: %s\n", strings.Join(f.Agents, ", "))
		}
		if f.ReactionsUp > 0 || f.ReactionsDown > 0 {
			fmt.Fprintf(&b, "- Reactions: 👍 %d · 👎 %d\n", f.ReactionsUp, f.ReactionsDown)
		}
		if f.Status == store.FindingDismissed {
			fmt.Fprintf(&b, "- Dismissed (%s)", f.DismissSource)
			if reason := oneLine(f.DismissReason); reason != "" {
				b.WriteString(": " + reason)
			}
			b.WriteString("\n")
		}
		b.WriteString("\n")
		for line := range strings.Lines(strings.TrimSpace(f.Explanation)) {
			b.WriteString(strings.TrimRight("> "+line, " \n") + "\n")
		}
	}
	return b.String()
}

// oneLine is s with each run of whitespace, line breaks included, as one
// space.
func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }
