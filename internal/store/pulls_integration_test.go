//go:build integration

package store

import (
	"context"
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
	ctx := context.Background()
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
	count := func(maxAuto int) bool {
		t.Helper()
		var now bool
		if err := s.WithAccount(ctx, account, func(tx pgx.Tx) error {
			var err error
			now, err = CountAutoReview(ctx, tx, pull, maxAuto)
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
	ctx := context.Background()
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
	ctx := context.Background()
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
	ctx := context.Background()
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

// TestLastReviewIsNotASkippedOne checks that a skipped review newer than
// one with a blocking finding leaves that one the pull request's last
// review, in the list and in what wants attention.
func TestLastReviewIsNotASkippedOne(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	if err := s.ApplyConfig(ctx, parse(t, soloAccount("lastreview"))); err != nil {
		t.Fatalf("ApplyConfig: %v", err)
	}
	account := accountID(t, s, "lastreview")
	completed := insertReview(t, ctx, s, account)
	var rows []PullRow
	var attention Attention
	if err := s.WithAccount(ctx, account, func(tx pgx.Tx) error {
		if _, err := EndReview(ctx, tx, completed, ReviewEnd{Status: ReviewCompleted}); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO findings (account_id, review_id, path, line, severity, title, explanation)
			VALUES ($1, $2, 'a.go', 1, 'blocking', 't', 'b')`, account, completed); err != nil {
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
		attention, err = ReadAttention(ctx, tx)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].LastReview == nil || rows[0].LastReview.ID != completed || rows[0].LastReview.Findings.Blocking != 1 {
		t.Fatalf("pull requests = %+v, want one whose last review is the completed one with its blocking finding", rows)
	}
	if attention.Blocking != 1 {
		t.Errorf("attention = %+v, want the blocking finding still counted", attention)
	}
}
