package webapi

import (
	"cmp"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/home-operations/kritika/internal/review"
	"github.com/home-operations/kritika/internal/store"
)

// analyticsRepos is how many repositories the analytics list.
const analyticsRepos = 5

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
