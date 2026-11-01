package webapi

import (
	"cmp"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/home-operations/kritika/internal/review"
	"github.com/home-operations/kritika/internal/store"
)

// analyticsRepos is how many repositories the analytics list, and
// analyticsRefusals how many refused hosts, each with a row per
// repository.
const (
	analyticsRepos        = 5
	analyticsRefusals     = 50
	analyticsDependencies = 50
)

// analyticsQuery reads ?group=, ?from= and ?to=: by day over the last 30
// days unless given.
func (s *Server) analyticsQuery(r *http.Request) (store.AnalyticsGroup, time.Time, time.Time, error) {
	group := store.AnalyticsGroup(cmp.Or(r.URL.Query().Get("group"), string(store.AnalyticsByDay)))
	if !group.Valid() {
		return "", time.Time{}, time.Time{}, errBadRequest(CodeBadRequest, "group must be day, week or month")
	}
	from, to, err := s.window(r)
	return group, from, to, err
}

func (s *Server) getAnalytics(w http.ResponseWriter, r *http.Request, t *accountScope) error {
	group, from, to, err := s.analyticsQuery(r)
	if err != nil {
		return err
	}
	ctx := r.Context()
	out := Analytics{Group: group, From: from, To: to}
	if err := s.read(ctx, t, func(tx pgx.Tx) error {
		data, err := store.ReadAnalytics(ctx, tx, group, from, to, analyticsRepos)
		if err != nil {
			return err
		}
		out.Current, out.Previous, out.Series, out.Repositories = analytics(data)
		refusals, err := store.ReadEgressRefusals(ctx, tx, from, to, analyticsRefusals)
		if err != nil {
			return err
		}
		out.Refusals = egressRefusals(refusals)
		return nil
	}); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

func analytics(data store.Analytics) (current, previous AnalyticsTotals, series []AnalyticsPoint, repos []RepoActivity) {
	series = make([]AnalyticsPoint, len(data.Series))
	for i, p := range data.Series {
		series[i] = AnalyticsPoint{
			Key: p.Key, Reviews: p.Reviews, Failed: p.Failed, Tokens: p.Tokens, MedianReviewMs: p.MedianReviewMs,
			Findings: severityCounts(p.Findings), CostUSD: p.CostUSD,
		}
	}
	repos = make([]RepoActivity, len(data.Repositories))
	for i, a := range data.Repositories {
		repos[i] = RepoActivity{
			Repository: a.Repository, Reviews: a.Reviews, Findings: severityCounts(a.Findings), Addressed: a.Addressed,
		}
	}
	return analyticsTotals(data.Current), analyticsTotals(data.Previous), series, repos
}

// getDependencyDrift serves how the dependencies the account's pull
// requests moved in the window were rated, the ones rated at the most
// levels first.
func (s *Server) getDependencyDrift(w http.ResponseWriter, r *http.Request, t *accountScope) error {
	from, to, err := s.window(r)
	if err != nil {
		return err
	}
	ctx := r.Context()
	out := []DependencyDrift{}
	if err := s.read(ctx, t, func(tx pgx.Tx) error {
		rows, err := store.ReadDependencyDrift(ctx, tx, from, to, analyticsDependencies)
		if err != nil {
			return err
		}
		for _, row := range rows {
			out = append(out, dependencyDrift(row))
		}
		return nil
	}); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// dependencyDrift counts row's ratings. The key is the path, a space and
// the line, as review.Bumps writes it, so the line is what follows the
// path, whatever the path holds.
func dependencyDrift(row store.DependencyDrift) DependencyDrift {
	d := DependencyDrift{
		Repository: row.Repository, Path: row.Path, Dependency: strings.TrimPrefix(row.Key, row.Path+" "),
		Bumps: len(row.Risks), LastAt: row.LastAt, Risks: map[review.Risk]int{}, Reaches: map[review.Reach]int{},
	}
	for i, risk := range row.Risks {
		d.Risks[review.Risk(risk)]++
		if i < len(row.Reaches) {
			d.Reaches[review.Reach(row.Reaches[i])]++
		}
	}
	d.Levels = len(d.Risks)
	if n := len(row.Risks); n > 0 {
		d.LastRisk = review.Risk(row.Risks[n-1])
	}
	if n := len(row.Reaches); n > 0 {
		d.LastReach = review.Reach(row.Reaches[n-1])
	}
	if n := len(row.Steps); n > 0 {
		d.LastStep = review.Step(row.Steps[n-1])
	}
	return d
}

func egressRefusals(rows []store.EgressRefusalRow) []EgressRefusal {
	out := make([]EgressRefusal, len(rows))
	for i, r := range rows {
		out[i] = EgressRefusal{Repository: r.Repository, Host: r.Host, Count: r.Count, Runs: r.Runs, Reasons: r.Reasons, LastSeenAt: r.LastSeenAt}
	}
	return out
}

func analyticsTotals(t store.AnalyticsTotals) AnalyticsTotals {
	return AnalyticsTotals{
		PullRequests: t.PullRequests, Reviews: t.Reviews, Failed: t.Failed, Findings: severityCounts(t.Findings),
		Categories: categoryCounts(t.Categories), Addressed: t.Addressed, ReactionsUp: t.ReactionsUp, ReactionsDown: t.ReactionsDown,
		CostUSD: t.CostUSD, Tokens: t.Tokens, UnpricedCalls: t.UnpricedCalls,
		MedianReviewMs: t.MedianReviewMs, MedianMergeMs: t.MedianMergeMs,
	}
}

// categoryCounts is counts with every category present, so the dashboard
// never reads a missing key.
func categoryCounts(counts map[review.Category]int) map[review.Category]int {
	out := make(map[review.Category]int, len(review.Categories()))
	for _, c := range review.Categories() {
		out[c] = counts[c]
	}
	return out
}

func severityCounts(c store.SeverityCounts) SeverityCounts {
	return SeverityCounts{P0: c.P0, P1: c.P1, P2: c.P2}
}

func pullRef(p store.PullRef) PullRef {
	return PullRef{Repository: p.Repository, Number: p.Number, Title: p.Title, URL: p.URL}
}

// window reads ?from= and ?to=: the last 30 days unless given.
func (s *Server) window(r *http.Request) (time.Time, time.Time, error) {
	q := r.URL.Query()
	to := s.now().UTC()
	if v := q.Get("to"); v != "" {
		t, ok := parseTime(v)
		if !ok {
			return time.Time{}, time.Time{}, errBadRequest(CodeBadRequest, "to must be RFC 3339 or YYYY-MM-DD")
		}
		to = t
	}
	from := to.Add(-defaultUsageWindow)
	if v := q.Get("from"); v != "" {
		t, ok := parseTime(v)
		if !ok {
			return time.Time{}, time.Time{}, errBadRequest(CodeBadRequest, "from must be RFC 3339 or YYYY-MM-DD")
		}
		from = t
	}
	if !from.Before(to) {
		return time.Time{}, time.Time{}, errBadRequest(CodeBadRequest, "from must be before to")
	}
	if to.Sub(from) > maxUsageWindow {
		return time.Time{}, time.Time{}, errBadRequest(CodeBadRequest, "from and to may span at most a year")
	}
	return from, to, nil
}
