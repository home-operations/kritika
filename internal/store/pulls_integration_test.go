//go:build integration

package store

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
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

// TestReviewSkipReason checks that a skipped review says why: with the
// repository's own reason, with the one its runner recorded on the context
// pack, or, with neither and no error, as a bot's unchanged patch.
func TestReviewSkipReason(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	if err := s.ApplyConfig(ctx, parse(t, soloAccount("skips"))); err != nil {
		t.Fatalf("ApplyConfig: %v", err)
	}
	account := accountID(t, s, "skips")
	tests := []struct {
		name                     string
		status, own, pack, error string
		want                     string
	}{
		{name: "the repository's own reason", status: "skipped", own: "filtered", want: "filtered"},
		{name: "the runner's too large diff", status: "skipped", pack: "too_large", want: "too_large"},
		{name: "the runner's unchanged patch", status: "skipped", pack: "unchanged_patch", want: "unchanged_patch"},
		{name: "a bot's unchanged patch, before a runner", status: "skipped", want: "unchanged_patch"},
		{name: "an admission reason is the error's", status: "skipped", error: "no review model", want: ""},
		{name: "a review that was not skipped", status: "completed", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id := insertReview(t, ctx, s, account)
			var got ReviewRow
			if err := s.WithAccount(ctx, account, func(tx pgx.Tx) error {
				if _, err := tx.Exec(ctx, `UPDATE reviews SET status = $2, skip_reason = $3, error = $4 WHERE id = $1`,
					id, tt.status, tt.own, tt.error); err != nil {
					return err
				}
				if tt.pack != "" {
					if _, err := tx.Exec(ctx, `WITH run AS (INSERT INTO runner_runs (account_id, review_id, kind)
						VALUES ($1, $2, 'review') RETURNING id)
						INSERT INTO context_packs (runner_run_id, account_id, head_sha, base_sha, patch_id, diff, skip_reason)
						SELECT id, $1, 'abc', 'base', 'patch', '', $3 FROM run`, account, id, tt.pack); err != nil {
						return err
					}
				}
				var err error
				got, err = FindReview(ctx, tx, id)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			if got.SkipReason != tt.want {
				t.Errorf("SkipReason = %q, want %q", got.SkipReason, tt.want)
			}
		})
	}
}

// TestNewestReviewID checks that a review names the pull request's newest
// review, and that the newest names none.
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
