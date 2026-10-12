//go:build integration

package store

import (
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/home-operations/kritika/internal/review"
)

// TestReadAnalytics checks the window totals, a series with every bucket
// present, and the repositories' activity, over reviews and findings on
// either side of the window.
func TestReadAnalytics(t *testing.T) {
	s := openStore(t)
	ctx := t.Context()
	if err := s.ApplyConfig(ctx, parse(t, soloAccount("analytics"))); err != nil {
		t.Fatalf("ApplyConfig: %v", err)
	}
	account := accountID(t, s, "analytics")
	from := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	to := from.AddDate(0, 0, 3)
	day := func(d int, h int) time.Time { return from.AddDate(0, 0, d).Add(time.Duration(h) * time.Hour) }

	if err := s.WithAccount(ctx, account, func(tx pgx.Tx) error {
		var repoID string
		if err := tx.QueryRow(ctx, `SELECT id FROM repositories WHERE account_id = $1 AND name = 'analytics/one'`, account).Scan(&repoID); err != nil {
			return err
		}
		pull := func(number int) string {
			var id string
			if err := tx.QueryRow(ctx, `INSERT INTO pull_requests (account_id, repository_id, number, head_sha, opened_at)
				VALUES ($1, $2, $3, 'h', $4) RETURNING id`, account, repoID, number, day(-2, 0)).Scan(&id); err != nil {
				t.Fatal(err)
			}
			return id
		}
		review := func(pr, head, status string, at time.Time, took time.Duration, findings ...[2]string) {
			var id string
			if err := tx.QueryRow(ctx, `INSERT INTO reviews (account_id, pull_request_id, head_sha, status, created_at, finished_at)
				VALUES ($1, $2, $3, $4, $5, $6) RETURNING id`, account, pr, head, status, at, at.Add(took)).Scan(&id); err != nil {
				t.Fatal(err)
			}
			for _, f := range findings {
				// Each finding drew one 👍 on this review's copy of it.
				if _, err := tx.Exec(ctx, `INSERT INTO findings (account_id, review_id, path, line, severity, title, explanation, fingerprint,
					reactions_up) VALUES ($1, $2, 'a.go', 1, $3, $4, '', $4, 1)`, account, id, f[0], f[1]); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := tx.Exec(ctx, `INSERT INTO usage (account_id, repository_id, review_id, role, model, cost_usd, input_tokens, output_tokens, created_at)
				VALUES ($1, $2, $3, 'review', 'acme/large', 0.5, 100, 20, $4)`, account, repoID, id, at); err != nil {
				t.Fatal(err)
			}
		}
		a, b := pull(1), pull(2)
		// Before the window: its finding was first reported then, so it counts there.
		review(a, "h0", "completed", day(-1, 12), time.Minute, [2]string{"p0", "old"})
		review(a, "h1", "completed", day(0, 12), 2*time.Minute, [2]string{"p0", "old"}, [2]string{"p2", "naming"})
		review(a, "h2", "completed", day(2, 12), 4*time.Minute, [2]string{"p1", "race"})
		review(b, "h1", "failed", day(1, 12), time.Minute)
		review(b, "h1", "completed", day(1, 13), 3*time.Minute, [2]string{"p2", "typo"})
		// After the window.
		review(b, "h2", "completed", day(3, 1), time.Minute)
		// a merged a day and a half after it opened; b closed unmerged.
		if _, err := tx.Exec(ctx, `UPDATE pull_requests SET merged = true, closed_at = $2 WHERE id = $1`, a, day(-1, 12)); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE pull_requests SET closed_at = $2 WHERE id = $1`, b, day(1, 0)); err != nil {
			return err
		}
		return nil
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	var data Analytics
	if err := s.WithAccount(ctx, account, func(tx pgx.Tx) error {
		var err error
		data, err = ReadAnalytics(ctx, tx, AnalyticsByDay, from, to, 5)
		return err
	}); err != nil {
		t.Fatalf("read: %v", err)
	}
	totals, before, series, repos := data.Current, data.Previous, data.Series, data.Repositories

	// naming was dropped by the review at h2, and typo by the one after the
	// window; race is still open. old counts before the window.
	median := int64(3 * time.Minute / time.Millisecond)
	// The seeded findings predate categories, so every category counts zero.
	want := AnalyticsTotals{
		PullRequests: 2, Reviews: 3, Failed: 1, Findings: SeverityCounts{P1: 1, P2: 2}, Addressed: 2, ReactionsUp: 3, CostUSD: 2, Tokens: 480,
		MedianReviewMs: &median, Categories: map[review.Category]int{},
	}
	for _, c := range review.Categories() {
		want.Categories[c] = 0
	}
	if !reflect.DeepEqual(totals, want) {
		t.Errorf("totals = %+v (median %v, merge %v), want %+v (median %v)", totals, deref(totals.MedianReviewMs),
			deref(totals.MedianMergeMs), want, median)
	}
	merge := int64(36 * time.Hour / time.Millisecond)
	if before.Tokens != 120 || before.Reviews != 1 || before.Findings.P0 != 1 || before.Addressed != 1 || before.MedianMergeMs == nil || *before.MedianMergeMs != merge {
		t.Errorf("before = %+v (merge %v), want the one review and its blocking finding, addressed, and a merged in 36h",
			before, deref(before.MedianMergeMs))
	}
	wantSeries := []AnalyticsPoint{
		{Key: "2026-09-01", Reviews: 1, Tokens: 120, MedianReviewMs: new(int64(120000)), Findings: SeverityCounts{P2: 1}, CostUSD: 0.5},
		{Key: "2026-09-02", Reviews: 1, Failed: 1, Tokens: 240, MedianReviewMs: new(int64(180000)), Findings: SeverityCounts{P2: 1}, CostUSD: 1},
		{Key: "2026-09-03", Reviews: 1, Tokens: 120, MedianReviewMs: new(int64(240000)), Findings: SeverityCounts{P1: 1}, CostUSD: 0.5},
	}
	if !reflect.DeepEqual(series, wantSeries) {
		t.Errorf("series = %+v, want %+v", series, wantSeries)
	}
	wantRepos := []RepoActivity{{Repository: "analytics/one", Reviews: 3, Findings: SeverityCounts{P1: 1, P2: 2}, Addressed: 2}}
	if !reflect.DeepEqual(repos, wantRepos) {
		t.Errorf("repos = %+v, want %+v", repos, wantRepos)
	}

	checkAnalyticsGroups(t, s, account, from, to, data)
	checkAnalyticsCategories(t, s, account, from, to)
	checkAnalyticsIsolation(t, s, from, to)

}

func categoryZeros() map[review.Category]int {
	out := map[review.Category]int{}
	for _, c := range review.Categories() {
		out[c] = 0
	}
	return out
}

func deref(p *int64) any {
	if p == nil {
		return nil
	}
	return *p
}

func checkAnalyticsGroups(t *testing.T, s *Store, account string, from, to time.Time, data Analytics) {
	t.Helper()
	ctx := t.Context()
	totals, before, wantRepos := data.Current, data.Previous, data.Repositories

	for _, tc := range []struct {
		group AnalyticsGroup
		key   string
	}{
		{AnalyticsByWeek, "2026-08-31"}, {AnalyticsByMonth, "2026-09-01"},
	} {
		t.Run(string(tc.group), func(t *testing.T) {
			var got Analytics
			if err := s.WithAccount(ctx, account, func(tx pgx.Tx) error {
				var err error
				got, err = ReadAnalytics(ctx, tx, tc.group, from, to, 1)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			wantPoint := []AnalyticsPoint{{Key: tc.key, Reviews: 3, Failed: 1, Tokens: 480, MedianReviewMs: new(int64(180000)), Findings: totals.Findings, CostUSD: 2}}
			if !reflect.DeepEqual(got.Series, wantPoint) || !reflect.DeepEqual(got.Current, totals) || !reflect.DeepEqual(got.Previous, before) || !reflect.DeepEqual(got.Repositories, wantRepos) {
				t.Fatalf("grouped analytics = %+v", got)
			}
		})
	}
}
func checkAnalyticsCategories(t *testing.T, s *Store, account string, from, to time.Time) {
	t.Helper()
	ctx := t.Context()

	t.Run("latest categories and dismissals", func(t *testing.T) {
		var got Analytics
		if err := s.WithAccount(ctx, account, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, `UPDATE findings SET category = 'security', reactions_down = 2 WHERE fingerprint = 'old'`); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE findings SET category = 'correctness' WHERE fingerprint = 'naming'`); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO dismissals (account_id, pull_request_id, fingerprint, comment_id)
    SELECT $1, v.pull_request_id, f.fingerprint, 1 FROM findings f JOIN reviews v ON v.id = f.review_id WHERE f.fingerprint = 'naming'`, account); err != nil {
				return err
			}
			var err error
			got, err = ReadAnalytics(ctx, tx, AnalyticsByDay, from, to, 5)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		if got.Current.Addressed != 1 || got.Current.Categories[review.CategoryCorrectness] != 1 || got.Previous.Categories[review.CategorySecurity] != 1 || got.Previous.ReactionsDown != 2 || got.Repositories[0].Addressed != 1 {
			t.Fatalf("categorized and dismissed = %+v", got)
		}
	})
}
func checkAnalyticsIsolation(t *testing.T, s *Store, from, to time.Time) {
	t.Helper()
	ctx := t.Context()

	t.Run("empty account is isolated", func(t *testing.T) {
		if err := s.ApplyConfig(ctx, parse(t, soloAccount("analytics-empty"))); err != nil {
			t.Fatal(err)
		}
		var got Analytics
		if err := s.WithAccount(ctx, accountID(t, s, "analytics-empty"), func(tx pgx.Tx) error {
			var err error
			got, err = ReadAnalytics(ctx, tx, AnalyticsByDay, from, to, 5)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		empty := AnalyticsTotals{Categories: categoryZeros()}
		if !reflect.DeepEqual(got.Current, empty) || !reflect.DeepEqual(got.Previous, empty) || len(got.Repositories) != 0 || len(got.Series) != 3 {
			t.Fatalf("empty account = %+v", got)
		}
		for i, point := range got.Series {
			if point != (AnalyticsPoint{Key: from.AddDate(0, 0, i).Format("2006-01-02")}) {
				t.Fatalf("empty series bucket = %+v", point)
			}
		}
	})
}
