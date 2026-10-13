package webapi

import (
	"cmp"
	"net/http"
	"slices"

	"github.com/jackc/pgx/v5"

	"github.com/home-operations/kritika/internal/auth"
	"github.com/home-operations/kritika/internal/store"
)

// The instance's analytics and usage: each readable account's series,
// read under its own row-level security, added up.

// usageByAccount is a group only the instance's series has: each account's
// usage as one row keyed by its slug.
const usageByAccount store.UsageGroup = "account"

// getInstanceAnalytics leaves the medians out: a median of per-account
// medians is not the instance's median.
func (s *Server) getInstanceAnalytics(w http.ResponseWriter, r *http.Request) error {
	group, from, to, err := s.analyticsQuery(r)
	if err != nil {
		return err
	}
	ctx := r.Context()
	out := Analytics{Group: group, From: from, To: to, Series: []AnalyticsPoint{}, Repositories: []RepoActivity{}}
	out.Current.Categories, out.Previous.Categories = categoryCounts(nil), categoryCounts(nil)
	for _, t := range readable(s.current.Get(), auth.PrincipalFrom(ctx)) {
		var data store.Analytics
		if err := s.store.WithAccount(ctx, t.ID(), func(tx pgx.Tx) error {
			var err error
			data, err = store.ReadAnalytics(ctx, tx, group, from, to, analyticsRepos)
			return err
		}); err != nil {
			return err
		}
		current, previous, series, repos := analytics(data)
		for i := range repos {
			repos[i].Account = t.Slug()
		}
		out.Current, out.Previous = addTotals(out.Current, current), addTotals(out.Previous, previous)
		out.Series = addSeries(out.Series, series)
		out.Repositories = append(out.Repositories, repos...)
	}
	slices.SortStableFunc(out.Repositories, func(a, b RepoActivity) int {
		return cmp.Or(cmp.Compare(b.Reviews, a.Reviews), cmp.Compare(a.Repository, b.Repository))
	})
	out.Repositories = out.Repositories[:min(len(out.Repositories), analyticsRepos)]
	writeJSON(w, http.StatusOK, out)
	return nil
}

func addTotals(a, b AnalyticsTotals) AnalyticsTotals {
	categories := categoryCounts(nil)
	for c := range categories {
		categories[c] = a.Categories[c] + b.Categories[c]
	}
	return AnalyticsTotals{
		PullRequests: a.PullRequests + b.PullRequests, Reviews: a.Reviews + b.Reviews, Failed: a.Failed + b.Failed,
		Findings: addSeverity(a.Findings, b.Findings), Categories: categories, Addressed: a.Addressed + b.Addressed,
		ReactionsUp: a.ReactionsUp + b.ReactionsUp, ReactionsDown: a.ReactionsDown + b.ReactionsDown,
		Tokens: a.Tokens + b.Tokens, CostUSD: a.CostUSD + b.CostUSD, UnpricedCalls: a.UnpricedCalls + b.UnpricedCalls,
	}
}

func addSeverity(a, b SeverityCounts) SeverityCounts {
	return SeverityCounts{P0: a.P0 + b.P0, P1: a.P1 + b.P1, P2: a.P2 + b.P2}
}

// addSeries adds b's buckets into a's by key, in key order.
func addSeries(a, b []AnalyticsPoint) []AnalyticsPoint {
	for _, p := range b {
		p.MedianReviewMs = nil
		i, found := slices.BinarySearchFunc(a, p, func(x, y AnalyticsPoint) int { return cmp.Compare(x.Key, y.Key) })
		if !found {
			a = slices.Insert(a, i, p)
			continue
		}
		a[i] = AnalyticsPoint{
			Key: p.Key, Reviews: a[i].Reviews + p.Reviews, Failed: a[i].Failed + p.Failed, Tokens: a[i].Tokens + p.Tokens,
			Findings: addSeverity(a[i].Findings, p.Findings), CostUSD: a[i].CostUSD + p.CostUSD,
		}
	}
	return a
}

func (s *Server) getInstanceUsage(w http.ResponseWriter, r *http.Request) error {
	chatgptOnly, err := usageBilling(r)
	if err != nil {
		return err
	}
	group := cmp.Or(store.UsageGroup(r.URL.Query().Get("group")), store.UsageByDay)
	if group != usageByAccount && !group.Valid() {
		return errBadRequest(CodeBadRequest, "group must be hour, day, week, model, repo, role, review or account")
	}
	from, to, err := s.window(r)
	if err != nil {
		return err
	}
	ctx := r.Context()
	filter := usageFilter(r)
	// By account, each account's usage is read by model, few rows, and
	// collapsed to one.
	read := group
	if group == usageByAccount {
		read = store.UsageByModel
	}
	out := UsageSeries{Group: group, From: from, To: to, Rows: []UsagePoint{}, Previous: []UsagePoint{}}
	for _, t := range readable(s.current.Get(), auth.PrincipalFrom(ctx)) {
		var rows, previous []store.UsageSeriesRow
		if err := s.store.WithAccount(ctx, t.ID(), func(tx pgx.Tx) error {
			var err error
			if rows, err = store.UsageSeries(ctx, tx, read, from, to, chatgptOnly, filter); err != nil {
				return err
			}
			previous, err = store.UsageComparison(ctx, tx, read, from.Add(-to.Sub(from)), from, chatgptOnly, filter)
			return err
		}); err != nil {
			return err
		}
		current, before := usagePoints(rows), usagePoints(previous)
		switch group {
		case usageByAccount:
			current, before = collapseUsage(current, t.Slug()), collapseUsage(before, t.Slug())
		case store.UsageByReview:
			for i := range current {
				if current[i].Key != "" && current[i].Key != "__other__" {
					current[i].Account = t.Slug()
				}
			}
		}
		out.Rows, out.Previous = addUsage(out.Rows, current), addUsage(out.Previous, before)
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// collapseUsage is rows as one, keyed and attributed to the account; none
// when there are none, so an idle account has no row.
func collapseUsage(rows []UsagePoint, slug string) []UsagePoint {
	if len(rows) == 0 {
		return nil
	}
	total := UsagePoint{Key: slug, Account: slug}
	for _, r := range rows {
		total = addUsagePoint(total, r)
	}
	return []UsagePoint{total}
}

// addUsage adds b's rows into a's by key, in key order.
func addUsage(a, b []UsagePoint) []UsagePoint {
	for _, p := range b {
		i, found := slices.BinarySearchFunc(a, p, func(x, y UsagePoint) int { return cmp.Compare(x.Key, y.Key) })
		if !found {
			a = slices.Insert(a, i, p)
			continue
		}
		a[i] = addUsagePoint(a[i], p)
	}
	return a
}

// addUsagePoint keeps a's key and account: a shared key is a time bucket
// or a remainder, which belongs to no one account.
func addUsagePoint(a, b UsagePoint) UsagePoint {
	return UsagePoint{
		Key: a.Key, Account: a.Account, InputTokens: a.InputTokens + b.InputTokens,
		CacheReadTokens: a.CacheReadTokens + b.CacheReadTokens, CacheWriteTokens: a.CacheWriteTokens + b.CacheWriteTokens,
		OutputTokens: a.OutputTokens + b.OutputTokens, CostUSD: a.CostUSD + b.CostUSD, UnpricedCalls: a.UnpricedCalls + b.UnpricedCalls,
		Calls: a.Calls + b.Calls, PlanCalls: a.PlanCalls + b.PlanCalls,
	}
}
