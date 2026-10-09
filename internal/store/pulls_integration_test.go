//go:build integration

package store

import (
	"slices"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/home-operations/kritika/internal/configfile"
)

// TestPausePullRequest checks that automatic reviews pause on request or
// once their count reaches the repository's maximum, that a request to
// review still finds the pull request paused until it is resumed, and
// that resuming starts the count over.
func TestPausePullRequest(t *testing.T) {
	s := openStore(t)
	ctx := t.Context()
	if err := s.ApplyConfig(ctx, parse(t, soloAccount("paused"))); err != nil {
		t.Fatalf("ApplyConfig: %v", err)
	}
	account := accountID(t, s, "paused")
	var repoID, pull string
	if err := s.WithAccount(ctx, account, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT id FROM repositories WHERE account_id = $1 AND name = 'paused/one'`, account).Scan(&repoID); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `INSERT INTO pull_requests (account_id, repository_id, number, head_sha)
			VALUES ($1, $2, 7, 'h') RETURNING id`, account, repoID).Scan(&pull)
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	paused := func() bool {
		t.Helper()
		var p bool
		if err := s.WithAccount(ctx, account, func(tx pgx.Tx) error {
			var err error
			p, err = PullRequestPaused(ctx, tx, repoID, 7)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return p
	}
	// count counts a review as the publish does: it reads whether the
	// count pauses, then counts, and the two must agree.
	count := func(maxAuto int) bool {
		t.Helper()
		var would, now bool
		if err := s.WithAccount(ctx, account, func(tx pgx.Tx) error {
			var err error
			if would, err = AutoReviewPauses(ctx, tx, pull, maxAuto); err != nil {
				return err
			}
			now, err = CountAutoReview(ctx, tx, pull, would)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		if would != now {
			t.Fatalf("AutoReviewPauses(%d) = %v, CountAutoReview = %v", maxAuto, would, now)
		}
		return now
	}
	// countTold counts a review whose read of the pause, made beside
	// another review's, was pause.
	countTold := func(pause bool) bool {
		t.Helper()
		var now bool
		if err := s.WithAccount(ctx, account, func(tx pgx.Tx) error {
			var err error
			now, err = CountAutoReview(ctx, tx, pull, pause)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return now
	}
	set := func(p bool) {
		t.Helper()
		if err := s.WithAccount(ctx, account, func(tx pgx.Tx) error { return PausePullRequest(ctx, tx, pull, p) }); err != nil {
			t.Fatal(err)
		}
	}
	if paused() {
		t.Fatal("a new pull request is not paused")
	}
	if count(0) || paused() {
		t.Fatal("without a maximum, a review never pauses")
	}
	if count(3) || paused() {
		t.Fatal("the second of three reviews does not pause")
	}
	if !count(3) || !paused() {
		t.Fatal("the third of three reviews pauses")
	}
	if count(3) || !paused() {
		t.Fatal("a review of a paused pull request reports no new pause and leaves it paused")
	}
	set(false)
	if paused() || count(3) || paused() {
		t.Fatal("resuming starts the count over")
	}
	// The second and third of three reviews, both told no pause as they
	// read the count at once, pass the limit unpaused; the next announces
	// the pause and pauses.
	for range 2 {
		if countTold(false) || paused() {
			t.Fatal("a review told no pause does not pause, even past the limit")
		}
	}
	if !count(3) || !paused() {
		t.Fatal("the review after an unpaused limit announces the pause and pauses")
	}
	set(false)
	set(true)
	if !paused() {
		t.Fatal("a request pauses")
	}
	var row PullRow
	if err := s.WithAccount(ctx, account, func(tx pgx.Tx) error {
		var err error
		row, err = FindPull(ctx, tx, repoID, 7)
		return err
	}); err != nil {
		t.Fatalf("FindPull: %v", err)
	}
	if !row.Paused {
		t.Fatal("the dashboard's read of a paused pull request says it is paused")
	}
}

// TestReviewSkipReason checks that a review ended as skipped keeps the
// reason it was given, the repository's or the runner's, and gives it back.
func TestReviewSkipReason(t *testing.T) {
	s := openStore(t)
	ctx := t.Context()
	if err := s.ApplyConfig(ctx, parse(t, soloAccount("skips"))); err != nil {
		t.Fatalf("ApplyConfig: %v", err)
	}
	account := accountID(t, s, "skips")
	for _, reason := range []string{"filtered", "only_skipped_paths", "unchanged_patch", "too_large", ""} {
		t.Run("reason "+reason, func(t *testing.T) {
			id := insertReview(t, ctx, s, account)
			var got ReviewRow
			if err := s.WithAccount(ctx, account, func(tx pgx.Tx) error {
				if _, err := EndReview(ctx, tx, id, ReviewEnd{Status: ReviewSkipped, SkipReason: reason}); err != nil {
					return err
				}
				var err error
				got, err = FindReview(ctx, tx, id)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			if got.Status != ReviewSkipped || got.SkipReason != reason {
				t.Errorf("review = %s (%q), want skipped (%q)", got.Status, got.SkipReason, reason)
			}
		})
	}
}

// TestNewestReviewID checks that a review names the pull request's newest
// review, that the newest names none, and that a skipped one is never it.
func TestNewestReviewID(t *testing.T) {
	s := openStore(t)
	ctx := t.Context()
	if err := s.ApplyConfig(ctx, parse(t, soloAccount("newest"))); err != nil {
		t.Fatalf("ApplyConfig: %v", err)
	}
	account := accountID(t, s, "newest")
	first := insertReview(t, ctx, s, account)
	var ids [2]string
	var got [3]ReviewRow
	if err := s.WithAccount(ctx, account, func(tx pgx.Tx) error {
		for i := range ids {
			if err := tx.QueryRow(ctx, `INSERT INTO reviews (account_id, pull_request_id, head_sha, status, created_at)
				SELECT account_id, pull_request_id, 'abc', 'completed', created_at + $2 * interval '1 minute' FROM reviews WHERE id = $1
				RETURNING id`, first, i+1).Scan(&ids[i]); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `INSERT INTO reviews (account_id, pull_request_id, head_sha, status, created_at)
			SELECT account_id, pull_request_id, 'abc', 'skipped', created_at + interval '3 minutes' FROM reviews WHERE id = $1`, first); err != nil {
			return err
		}
		for i, id := range []string{first, ids[0], ids[1]} {
			var err error
			if got[i], err = FindReview(ctx, tx, id); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for i, v := range got[:2] {
		if v.NewestReviewID == nil || *v.NewestReviewID != ids[1] {
			t.Errorf("review %d names %v as the newest, want %s", i, v.NewestReviewID, ids[1])
		}
	}
	if got[2].NewestReviewID != nil {
		t.Errorf("the newest review names %s as newer", *got[2].NewestReviewID)
	}
}

// TestAccountStatsCountCompletedReviews checks that the account's review
// count is of the reviews that completed, whatever else ended otherwise.
func TestAccountStatsCountCompletedReviews(t *testing.T) {
	s := openStore(t)
	ctx := t.Context()
	if err := s.ApplyConfig(ctx, parse(t, soloAccount("counted"))); err != nil {
		t.Fatalf("ApplyConfig: %v", err)
	}
	account := accountID(t, s, "counted")
	for _, status := range []ReviewStatus{ReviewCompleted, ReviewCompleted, ReviewSkipped, ReviewSuperseded, ReviewFailed} {
		id := insertReview(t, ctx, s, account)
		if err := s.WithAccount(ctx, account, func(tx pgx.Tx) error {
			_, err := EndReview(ctx, tx, id, ReviewEnd{Status: status})
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	insertReview(t, ctx, s, account)
	var stats AccountStats
	if err := s.WithAccount(ctx, account, func(tx pgx.Tx) error {
		var err error
		stats, err = ReadAccountStats(ctx, tx, func(string, configfile.RepoTraits) bool { return true })
		return err
	}); err != nil {
		t.Fatalf("ReadAccountStats: %v", err)
	}
	if stats.Reviews7d != 2 {
		t.Errorf("Reviews7d = %d, want the 2 that completed", stats.Reviews7d)
	}
}

// TestMonthUsageCostsTheCompletedReviews checks that the month's review
// costs are of the reviews that completed, each costing its own usage rows
// whatever their role, with a follow-up's and a failed review's spend in
// the month's total only.
func TestMonthUsageCostsTheCompletedReviews(t *testing.T) {
	s := openStore(t)
	ctx := t.Context()
	if err := s.ApplyConfig(ctx, parse(t, soloAccount("costed"))); err != nil {
		t.Fatalf("ApplyConfig: %v", err)
	}
	account := accountID(t, s, "costed")
	charge := func(reviewID, role string, cost float64) {
		t.Helper()
		if err := s.WithAccount(ctx, account, func(tx pgx.Tx) error {
			var repoID string
			if err := tx.QueryRow(ctx, `SELECT id FROM repositories LIMIT 1`).Scan(&repoID); err != nil {
				return err
			}
			return InsertUsage(ctx, tx, Usage{AccountID: account, RepositoryID: repoID, ReviewID: reviewID, Role: role, Model: "m", Input: 1, CostUSD: cost})
		}); err != nil {
			t.Fatal(err)
		}
	}
	end := func(reviewID string, status ReviewStatus) {
		t.Helper()
		if err := s.WithAccount(ctx, account, func(tx pgx.Tx) error {
			_, err := EndReview(ctx, tx, reviewID, ReviewEnd{Status: status})
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	for _, cost := range []float64{1, 2, 10} {
		id := insertReview(t, ctx, s, account)
		charge(id, RoleReview, cost)
		end(id, ReviewCompleted)
	}
	confident := insertReview(t, ctx, s, account)
	charge(confident, RoleReview, 3)
	charge(confident, RoleConfidence, 1)
	end(confident, ReviewCompleted)
	end(insertReview(t, ctx, s, account), ReviewCompleted)
	failed := insertReview(t, ctx, s, account)
	charge(failed, RoleReview, 100)
	end(failed, ReviewFailed)
	charge("", RoleFollowUp, 50)
	var m MonthUsage
	if err := s.WithAccount(ctx, account, func(tx pgx.Tx) error {
		var err error
		m, err = ReadMonthUsage(ctx, tx)
		return err
	}); err != nil {
		t.Fatalf("ReadMonthUsage: %v", err)
	}
	if m.Reviews != 5 || m.ReviewCostUSD != 17 {
		t.Errorf("Reviews, ReviewCostUSD = %d, %v, want the 5 completed costing 17", m.Reviews, m.ReviewCostUSD)
	}
	if m.MedianReviewCostUSD == nil || *m.MedianReviewCostUSD != 2 {
		t.Errorf("MedianReviewCostUSD = %v, want 2 (of 0, 1, 2, 4, 10)", m.MedianReviewCostUSD)
	}
	if m.CostUSD != 167 {
		t.Errorf("CostUSD = %v, want 167 with the failed review and the follow-up", m.CostUSD)
	}
	// A pull request's cost is every review's, however it ended.
	for reviewID, want := range map[string]float64{confident: 4, failed: 100} {
		var cost float64
		if err := s.WithAccount(ctx, account, func(tx pgx.Tx) error {
			var prID string
			if err := tx.QueryRow(ctx, `SELECT pull_request_id FROM reviews WHERE id = $1`, reviewID).Scan(&prID); err != nil {
				return err
			}
			var err error
			cost, err = PullCost(ctx, tx, prID)
			return err
		}); err != nil {
			t.Fatalf("PullCost: %v", err)
		}
		if cost != want {
			t.Errorf("PullCost = %v, want %v", cost, want)
		}
	}
}

// TestLastReviewIsNotASkippedOne checks that a skipped review newer than
// one with a blocking finding leaves that one the last review of its pull
// request and of its repository, and its finding in what wants attention.
func TestLastReviewIsNotASkippedOne(t *testing.T) {
	s := openStore(t)
	ctx := t.Context()
	if err := s.ApplyConfig(ctx, parse(t, soloAccount("lastreview"))); err != nil {
		t.Fatalf("ApplyConfig: %v", err)
	}
	account := accountID(t, s, "lastreview")
	completed := insertReview(t, ctx, s, account)
	var rows []PullRow
	var repo RepoRow
	var attention Attention
	if err := s.WithAccount(ctx, account, func(tx pgx.Tx) error {
		if _, err := EndReview(ctx, tx, completed, ReviewEnd{Status: ReviewCompleted}); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO findings (account_id, review_id, path, line, severity, title, explanation)
			VALUES ($1, $2, 'a.go', 1, 'p0', 't', 'b')`, account, completed); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO reviews (account_id, pull_request_id, head_sha, status, created_at)
			SELECT account_id, pull_request_id, 'def', 'skipped', created_at + interval '1 minute' FROM reviews WHERE id = $1`, completed); err != nil {
			return err
		}
		var err error
		if rows, _, err = ListPulls(ctx, tx, PullFilter{}, Page{Limit: 10}); err != nil {
			return err
		}
		if repo, err = FindRepo(ctx, tx, "lastreview/one"); err != nil {
			return err
		}
		attention, err = ReadAttention(ctx, tx)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].LastReview == nil || rows[0].LastReview.ID != completed || rows[0].LastReview.Findings.P0 != 1 {
		t.Fatalf("pull requests = %+v, want one whose last review is the completed one with its blocking finding", rows)
	}
	if repo.LastReview == nil || repo.LastReview.ID != completed {
		t.Errorf("repository's last review = %+v, want the completed one", repo.LastReview)
	}
	if attention.P0 != 1 {
		t.Errorf("attention = %+v, want the blocking finding still counted", attention)
	}
}

// TestEarlierSeverityNamesAreRenamed: a finding or a dismissal a replica
// of an earlier release records under the earlier names lands as p0 to p2.
func TestEarlierSeverityNamesAreRenamed(t *testing.T) {
	s := openStore(t)
	ctx := t.Context()
	if err := s.ApplyConfig(ctx, parse(t, soloAccount("severitynames"))); err != nil {
		t.Fatalf("ApplyConfig: %v", err)
	}
	account := accountID(t, s, "severitynames")
	reviewID := insertReview(t, ctx, s, account)
	var findings, dismissals []string
	if err := s.WithAccount(ctx, account, func(tx pgx.Tx) error {
		for i, sev := range []string{"blocking", "important", "nit", "p1"} {
			if _, err := tx.Exec(ctx, `INSERT INTO findings (account_id, review_id, path, line, severity, title, explanation)
				VALUES ($1, $2, 'a.go', $3, $4, 't', 'b')`, account, reviewID, i+1, sev); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO dismissals (account_id, pull_request_id, fingerprint, severity, comment_id)
				SELECT $1, pull_request_id, $3, $4, 1 FROM reviews WHERE id = $2`, account, reviewID, sev, sev); err != nil {
				return err
			}
		}
		rows, err := tx.Query(ctx, `SELECT severity FROM findings WHERE review_id = $1 ORDER BY line`, reviewID)
		if err != nil {
			return err
		}
		if findings, err = pgx.CollectRows(rows, pgx.RowTo[string]); err != nil {
			return err
		}
		rows, err = tx.Query(ctx, `SELECT d.severity FROM dismissals d JOIN reviews r ON r.pull_request_id = d.pull_request_id
			WHERE r.id = $1 ORDER BY array_position('{blocking,important,nit,p1}'::text[], d.fingerprint)`, reviewID)
		if err != nil {
			return err
		}
		dismissals, err = pgx.CollectRows(rows, pgx.RowTo[string])
		return err
	}); err != nil {
		t.Fatal(err)
	}
	want := []string{"p0", "p1", "p2", "p1"}
	if !slices.Equal(findings, want) || !slices.Equal(dismissals, want) {
		t.Fatalf("findings %v, dismissals %v; want %v", findings, dismissals, want)
	}
}
