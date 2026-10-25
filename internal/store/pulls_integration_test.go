//go:build integration

package store

import (
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/jobs"
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
	listRepos(t, s, "paused", "paused/one")
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
	listRepos(t, s, "skips", "skips/one")
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
	listRepos(t, s, "newest", "newest/one")
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
	listRepos(t, s, "counted", "counted/one")
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
		stats, err = ReadAccountStats(ctx, tx, func(configfile.RepoTraits) bool { return true })
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
// the month's total only, and a review with an unpriced call counted apart.
func TestMonthUsageCostsTheCompletedReviews(t *testing.T) {
	s := openStore(t)
	ctx := t.Context()
	if err := s.ApplyConfig(ctx, parse(t, soloAccount("costed"))); err != nil {
		t.Fatalf("ApplyConfig: %v", err)
	}
	listRepos(t, s, "costed", "costed/one")
	account := accountID(t, s, "costed")
	insert := func(u Usage) {
		t.Helper()
		if err := s.WithAccount(ctx, account, func(tx pgx.Tx) error {
			if err := tx.QueryRow(ctx, `SELECT id FROM repositories LIMIT 1`).Scan(&u.RepositoryID); err != nil {
				return err
			}
			u.AccountID, u.Model, u.Input = account, "m", 1
			return InsertUsage(ctx, tx, u)
		}); err != nil {
			t.Fatal(err)
		}
	}
	charge := func(reviewID, role string, cost float64) {
		t.Helper()
		insert(Usage{ReviewID: reviewID, Role: role, CostUSD: cost})
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
	incomplete := insertReview(t, ctx, s, account)
	charge(incomplete, RoleReview, 5)
	insert(Usage{ReviewID: incomplete, Role: RoleConfidence, Unpriced: true})
	end(incomplete, ReviewCompleted)
	var m MonthUsage
	if err := s.WithAccount(ctx, account, func(tx pgx.Tx) error {
		var err error
		m, err = ReadMonthUsage(ctx, tx)
		return err
	}); err != nil {
		t.Fatalf("ReadMonthUsage: %v", err)
	}
	if m.Reviews != 6 || m.UnpricedReviews != 1 || m.ReviewCostUSD != 17 {
		t.Errorf("Reviews, UnpricedReviews, ReviewCostUSD = %d, %d, %v, want 6 completed, the unpriced one apart and the other 5 costing 17",
			m.Reviews, m.UnpricedReviews, m.ReviewCostUSD)
	}
	if m.UnpricedCalls != 1 {
		t.Errorf("UnpricedCalls = %d, want 1", m.UnpricedCalls)
	}
	if m.MedianReviewCostUSD == nil || *m.MedianReviewCostUSD != 2 {
		t.Errorf("MedianReviewCostUSD = %v, want 2 (of 0, 1, 2, 4, 10)", m.MedianReviewCostUSD)
	}
	if m.CostUSD != 172 {
		t.Errorf("CostUSD = %v, want 172 with the failed review, the follow-up and the unpriced review's priced call", m.CostUSD)
	}
	// A pull request's cost is every review's, however it ended, and says
	// how many of its calls were unpriced.
	for reviewID, want := range map[string]struct {
		cost     float64
		unpriced int64
	}{confident: {4, 0}, failed: {100, 0}, incomplete: {5, 1}} {
		var cost float64
		var unpriced int64
		if err := s.WithAccount(ctx, account, func(tx pgx.Tx) error {
			var prID string
			if err := tx.QueryRow(ctx, `SELECT pull_request_id FROM reviews WHERE id = $1`, reviewID).Scan(&prID); err != nil {
				return err
			}
			var err error
			cost, unpriced, err = PullCost(ctx, tx, prID)
			return err
		}); err != nil {
			t.Fatalf("PullCost: %v", err)
		}
		if cost != want.cost || unpriced != want.unpriced {
			t.Errorf("PullCost = %v, %d unpriced; want %v, %d", cost, unpriced, want.cost, want.unpriced)
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
	listRepos(t, s, "lastreview", "lastreview/one")
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

// TestReviewOwed: a pull request owes a review while one asked for since
// its last completed review delivered none: its job yet to start, or its
// latest review still running or prepared, canceled by a newer review's
// start or superseded by a moved head. One that ended on its own answered
// the request, as does any review that completed after it was asked, a
// cancel asked for withdraws it, the latest request speaks for the pull
// request, and a push's review is never owed.
func TestReviewOwed(t *testing.T) {
	s := openStore(t)
	ctx := t.Context()
	if err := s.ApplyConfig(ctx, parse(t, soloAccount("owed"))); err != nil {
		t.Fatalf("ApplyConfig: %v", err)
	}
	listRepos(t, s, "owed", "owed/one")
	account := accountID(t, s, "owed")
	// A job is a review job of the pull request of trigger in state,
	// created minute minutes from now; a rev is a review of trigger,
	// status and error created then, of the job at index job in jobs, or
	// of none for -1, whose cancel was asked for when canceled.
	type job struct {
		trigger, state string
		minute         int
	}
	type rev struct {
		trigger, status string
		minute, job     int
		error           string
		canceled        bool
	}
	const manual = jobs.TriggerManual
	completed := rev{"synchronize", "completed", 1, -1, "", false}
	tests := []struct {
		name    string
		jobs    []job
		reviews []rev
		want    bool
	}{
		{"no reviews", nil, nil, false},
		{"a request yet to start", []job{{manual, "scheduled", 1}}, nil, true},
		{"a request yet to start, asked before the last completed review", []job{{manual, "scheduled", 0}}, []rev{completed}, false},
		{"a request's job that ended without a review", []job{{manual, "completed", 1}}, nil, false},
		{"a push's job yet to start", []job{{"synchronize", "scheduled", 1}}, nil, false},
		{"a request still running", nil, []rev{{manual, "running", 1, -1, "", false}}, true},
		{"a request prepared", nil, []rev{completed, {manual, "prepared", 2, -1, "", false}}, true},
		{"a request canceled by a newer review", nil, []rev{completed, {manual, "canceled", 2, -1, ReplacedReviewError, false}}, true},
		{"a request whose cancel was asked for", nil, []rev{completed, {manual, "canceled", 2, -1, ReplacedReviewError, true}}, false},
		{"a request superseded before it started", nil, []rev{completed, {manual, "superseded", 2, -1, "", false}}, true},
		{"a request superseded while the review standing in for it runs", nil,
			[]rev{{manual, "superseded", 1, -1, "", false}, {"synchronize", "running", 2, -1, "", false}}, true},
		{"a request superseded, dated by its job before the review that answered it", nil,
			[]rev{{manual, "superseded", 0, -1, "", false}, completed}, false},
		{"a stale request dated before the last completed review and a newer one running", nil,
			[]rev{{manual, "superseded", 0, -1, "", false}, completed, {manual, "running", 2, -1, "", false}}, true},
		{"a request cut by a restart whose retry runs", []job{{manual, "running", 1}},
			[]rev{completed, {manual, "superseded", 2, 0, "cut by a restart and retried", false}, {manual, "running", 3, 0, "", false}}, true},
		{"a request cut by a restart whose retry failed", []job{{manual, "completed", 1}},
			[]rev{completed, {manual, "superseded", 2, 0, "cut by a restart and retried", false}, {manual, "failed", 3, 0, "", false}}, false},
		{"a request a later review answered", nil, []rev{{manual, "canceled", 0, -1, ReplacedReviewError, false}, completed}, false},
		{"a request that completed", nil, []rev{completed, {manual, "completed", 2, -1, "", false}}, false},
		{"a request that was skipped", nil, []rev{completed, {manual, "skipped", 2, -1, "", false}}, false},
		{"a request that failed", nil, []rev{completed, {manual, "failed", 2, -1, "", false}}, false},
		{"a request that was capped", nil, []rev{completed, {manual, "capped", 2, -1, "", false}}, false},
		{"a push's review canceled", nil, []rev{completed, {"synchronize", "canceled", 2, -1, ReplacedReviewError, false}}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got bool
			err := s.WithAccount(ctx, account, func(tx pgx.Tx) error {
				var repoID, prID string
				var number int
				if err := tx.QueryRow(ctx, `SELECT id FROM repositories WHERE account_id = $1 LIMIT 1`, account).Scan(&repoID); err != nil {
					return err
				}
				if err := tx.QueryRow(ctx, `INSERT INTO pull_requests (account_id, repository_id, number, head_sha)
					VALUES ($1, $2, (random() * 1e6)::int, 'abc') RETURNING id, number`, account, repoID).Scan(&prID, &number); err != nil {
					return err
				}
				jobIDs := make([]int64, len(tt.jobs))
				for i, j := range tt.jobs {
					if err := tx.QueryRow(ctx, `INSERT INTO river_job (kind, queue, args, max_attempts, state, created_at, finalized_at)
						VALUES ('review', 'review', jsonb_build_object('account_id', $1::text, 'repository_id', $2::text, 'number', $3::int, 'trigger', $4::text),
							3, $5::text::river_job_state, now() + $6 * interval '1 minute',
							CASE WHEN $5::text IN ('completed', 'cancelled', 'discarded') THEN now() + $6 * interval '1 minute' END) RETURNING id`,
						account, repoID, number, j.trigger, j.state, j.minute).Scan(&jobIDs[i]); err != nil {
						return err
					}
				}
				for _, r := range tt.reviews {
					var jobID *int64
					if r.job >= 0 {
						jobID = &jobIDs[r.job]
					}
					if _, err := tx.Exec(ctx, `INSERT INTO reviews (account_id, pull_request_id, head_sha, status, trigger, error, created_at, river_job_id, cancel_requested_at)
						VALUES ($1, $2, 'abc', $3, $4, $5, now() + $6 * interval '1 minute', $7, CASE WHEN $8 THEN now() END)`,
						account, prID, r.status, r.trigger, r.error, r.minute, jobID, r.canceled); err != nil {
						return err
					}
				}
				var err error
				got, err = ReviewOwed(ctx, tx, prID)
				return err
			})
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Errorf("ReviewOwed = %v, want %v", got, tt.want)
			}
		})
	}
}
