package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/home-operations/kritika/internal/review"
)

// AnalyticsGroup is the width of one bucket of an analytics series.
type AnalyticsGroup string

// Analytics groupings.
const (
	AnalyticsByDay   AnalyticsGroup = "day"
	AnalyticsByWeek  AnalyticsGroup = "week"
	AnalyticsByMonth AnalyticsGroup = "month"
)

// Valid reports whether g is an analytics grouping.
func (g AnalyticsGroup) Valid() bool {
	return g == AnalyticsByDay || g == AnalyticsByWeek || g == AnalyticsByMonth
}

// AnalyticsTotals is what an account's reviews came to over a window.
// Reviews and pull requests count completed reviews; findings count each
// pull request's finding once, in the window it was first reported,
// Addressed how many of those a later review no longer reported, and the
// reactions the ones posted inline drew.
type AnalyticsTotals struct {
	PullRequests int
	Reviews      int
	Failed       int
	Findings     SeverityCounts
	// Categories counts the same findings by category; a finding
	// recorded before it had one is under none of them.
	Categories    map[review.Category]int
	Addressed     int
	ReactionsUp   int
	ReactionsDown int
	CostUSD       float64
	UnpricedCalls int64
	Tokens        int64
	// MedianReviewMs is nil when no review completed, and MedianMergeMs,
	// from opened to merged, when no pull request kritika knows merged.
	MedianReviewMs *int64
	MedianMergeMs  *int64
}

// Analytics holds the current and preceding windows, a complete bucket
// series, and the most active repositories.
type Analytics struct {
	Current, Previous AnalyticsTotals
	Series            []AnalyticsPoint
	Repositories      []RepoActivity
}

// ReadAnalytics reconstructs finding state once for the whole response.
func ReadAnalytics(ctx context.Context, tx pgx.Tx, group AnalyticsGroup, from, to time.Time, limit int) (Analytics, error) {
	var out Analytics
	if !group.Valid() {
		return out, ErrFilter
	}
	previous := from.Add(-to.Sub(from))
	for _, totals := range []*AnalyticsTotals{&out.Current, &out.Previous} {
		totals.Categories = map[review.Category]int{}
		for _, c := range review.Categories() {
			totals.Categories[c] = 0
		}
	}
	points, repos := map[string]SeverityCounts{}, map[string]RepoActivity{}
	rows, err := tx.Query(ctx, `WITH `+findingIssues+`,
  metrics AS MATERIALIZED (
   SELECT p.repository_id, l.severity, l.category, l.first_at, l.addressed, l.reactions_up, l.reactions_down
   FROM latest l JOIN pull_requests p ON p.id = l.pull_request_id
   WHERE l.first_at >= $1 AND l.first_at < $3)
  SELECT k.kind, k.key, count(*), count(*) FILTER (WHERE m.severity = 'p0'),
   count(*) FILTER (WHERE m.severity = 'p1'), count(*) FILTER (WHERE m.severity = 'p2'),
   count(*) FILTER (WHERE m.addressed), coalesce(sum(m.reactions_up), 0), coalesce(sum(m.reactions_down), 0)
  FROM metrics m CROSS JOIN LATERAL (
   SELECT CASE WHEN m.first_at >= $2 THEN 'current' ELSE 'previous' END AS kind, '' AS key
   UNION ALL SELECT CASE WHEN m.first_at >= $2 THEN 'category_current' ELSE 'category_previous' END, m.category WHERE m.category <> ''
   UNION ALL SELECT 'series', to_char(date_trunc($4, m.first_at), 'YYYY-MM-DD') WHERE m.first_at >= $2
   UNION ALL SELECT 'repository', m.repository_id::text WHERE m.first_at >= $2
  ) k GROUP BY k.kind, k.key`, previous, from, to, string(group))
	if err != nil {
		return out, fmt.Errorf("store: analytics findings: %w", err)
	}
	var kind, key string
	var n, addressed, up, down int
	var counts SeverityCounts
	if _, err := pgx.ForEachRow(rows, []any{&kind, &key, &n, &counts.P0, &counts.P1, &counts.P2, &addressed, &up, &down}, func() error {
		switch kind {
		case "current", "previous":
			t := &out.Current
			if kind == "previous" {
				t = &out.Previous
			}
			t.Findings, t.Addressed, t.ReactionsUp, t.ReactionsDown = counts, addressed, up, down
		case "category_current":
			out.Current.Categories[review.Category(key)] = n
		case "category_previous":
			out.Previous.Categories[review.Category(key)] = n
		case "series":
			points[key] = counts
		case "repository":
			repos[key] = RepoActivity{Findings: counts, Addressed: addressed}
		}
		return nil
	}); err != nil {
		return out, fmt.Errorf("store: analytics findings: %w", err)
	}
	if err := readAnalyticsTotals(ctx, tx, from, to, &out.Current); err != nil {
		return out, err
	}
	if err := readAnalyticsTotals(ctx, tx, previous, from, &out.Previous); err != nil {
		return out, err
	}
	out.Series, err = readAnalyticsSeries(ctx, tx, group, from, to, points)
	if err != nil {
		return out, err
	}
	out.Repositories, err = readRepoActivity(ctx, tx, from, to, limit, repos)
	return out, err
}

func readAnalyticsTotals(ctx context.Context, tx pgx.Tx, from, to time.Time, t *AnalyticsTotals) error {
	var median *float64
	err := tx.QueryRow(ctx, `SELECT count(DISTINCT pull_request_id) FILTER (WHERE status = 'completed'),
			count(*) FILTER (WHERE status = 'completed'), count(*) FILTER (WHERE status = 'failed'),
			percentile_cont(0.5) WITHIN GROUP (ORDER BY extract(epoch FROM finished_at - created_at) * 1000)
				FILTER (WHERE status = 'completed' AND finished_at IS NOT NULL)
		FROM reviews WHERE created_at >= $1 AND created_at < $2`, from, to).
		Scan(&t.PullRequests, &t.Reviews, &t.Failed, &median)
	if err != nil {
		return fmt.Errorf("store: analytics totals: %w", err)
	}
	if median != nil {
		ms := int64(*median)
		t.MedianReviewMs = &ms
	}
	err = tx.QueryRow(ctx, `SELECT coalesce(sum(cost_usd), 0)::float8, count(*) FILTER (WHERE unpriced),
			coalesce(sum(input_tokens + output_tokens), 0)
		FROM usage WHERE created_at >= $1 AND created_at < $2`, from, to).Scan(&t.CostUSD, &t.UnpricedCalls, &t.Tokens)
	if err != nil {
		return fmt.Errorf("store: analytics spend: %w", err)
	}
	var merge *float64
	err = tx.QueryRow(ctx, `SELECT percentile_cont(0.5) WITHIN GROUP (ORDER BY extract(epoch FROM closed_at - opened_at) * 1000)
		FROM pull_requests WHERE merged AND opened_at IS NOT NULL AND closed_at >= $1 AND closed_at < $2`, from, to).Scan(&merge)
	if err != nil {
		return fmt.Errorf("store: analytics merge time: %w", err)
	}
	if merge != nil {
		ms := int64(*merge)
		t.MedianMergeMs = &ms
	}
	return nil
}

// AnalyticsPoint is one bucket of an analytics series, keyed by the date
// it starts on.
type AnalyticsPoint struct {
	Key            string
	Reviews        int
	Failed         int
	Tokens         int64
	MedianReviewMs *int64
	Findings       SeverityCounts
	CostUSD        float64
}

func readAnalyticsSeries(
	ctx context.Context, tx pgx.Tx, group AnalyticsGroup, from, to time.Time, findings map[string]SeverityCounts,
) ([]AnalyticsPoint, error) {
	rows, err := tx.Query(ctx, `WITH
		buckets AS (
			SELECT b FROM generate_series(date_trunc($3, $1::timestamptz), $2::timestamptz - interval '1 microsecond',
				('1 ' || $3)::interval) AS b),
		rev AS (
			SELECT date_trunc($3, created_at) AS b, count(*) FILTER (WHERE status = 'completed') AS n,
				count(*) FILTER (WHERE status = 'failed') AS failed,
				(percentile_cont(0.5) WITHIN GROUP (ORDER BY extract(epoch FROM finished_at - created_at) * 1000)
					FILTER (WHERE status = 'completed' AND finished_at IS NOT NULL))::bigint AS duration FROM reviews
			WHERE created_at >= $1 AND created_at < $2 GROUP BY 1),
		spend AS (
			SELECT date_trunc($3, created_at) AS b, sum(cost_usd)::float8 AS cost, sum(input_tokens + output_tokens) AS tokens FROM usage
			WHERE created_at >= $1 AND created_at < $2 GROUP BY 1)
		SELECT to_char(k.b, 'YYYY-MM-DD'), coalesce(rev.n, 0), coalesce(spend.cost, 0), coalesce(rev.failed, 0),
			coalesce(spend.tokens, 0), rev.duration
		FROM buckets k LEFT JOIN rev ON rev.b = k.b LEFT JOIN spend ON spend.b = k.b
		ORDER BY k.b`, from, to, string(group))
	if err != nil {
		return nil, fmt.Errorf("store: analytics series: %w", err)
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (AnalyticsPoint, error) {
		var p AnalyticsPoint
		err := row.Scan(&p.Key, &p.Reviews, &p.CostUSD, &p.Failed, &p.Tokens, &p.MedianReviewMs)
		p.Findings = findings[p.Key]
		return p, err
	})
	if err != nil {
		return nil, fmt.Errorf("store: analytics series: %w", err)
	}
	return out, nil
}

// RepoActivity is what one repository's reviews came to over a window.
type RepoActivity struct {
	Repository string
	Reviews    int
	Findings   SeverityCounts
	Addressed  int
}

func readRepoActivity(
	ctx context.Context, tx pgx.Tx, from, to time.Time, limit int, findings map[string]RepoActivity,
) ([]RepoActivity, error) {
	ids := make([]string, 0, len(findings))
	for id := range findings {
		ids = append(ids, id)
	}
	rows, err := tx.Query(ctx, `WITH
		rev AS (
			SELECT p.repository_id AS id, count(*) AS n FROM reviews v JOIN pull_requests p ON p.id = v.pull_request_id
			WHERE v.status = 'completed' AND v.created_at >= $1 AND v.created_at < $2 GROUP BY 1)
		SELECT r.id::text, r.name, coalesce(rev.n, 0)
		FROM repositories r LEFT JOIN rev ON rev.id = r.id
		WHERE rev.id IS NOT NULL OR r.id = ANY($3::uuid[])
		ORDER BY coalesce(rev.n, 0) DESC, r.name LIMIT $4`, from, to, ids, limit)
	if err != nil {
		return nil, fmt.Errorf("store: repository activity: %w", err)
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (RepoActivity, error) {
		var id, name string
		var n int
		err := row.Scan(&id, &name, &n)
		a := findings[id]
		a.Repository, a.Reviews = name, n
		return a, err
	})
	if err != nil {
		return nil, fmt.Errorf("store: repository activity: %w", err)
	}
	return out, nil
}
