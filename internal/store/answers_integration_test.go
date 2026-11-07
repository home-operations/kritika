//go:build integration

package store

import (
	"slices"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/home-operations/kritika/internal/review"
)

// TestReviewAnswers: a review keeps the answers it was shown, by finding id,
// a review that kept none reads none, and keeping them again replaces them.
func TestReviewAnswers(t *testing.T) {
	s := openStore(t)
	ctx := t.Context()
	if err := s.ApplyConfig(ctx, parse(t, soloAccount("answers"))); err != nil {
		t.Fatal(err)
	}
	account := accountID(t, s, "answers")
	var shown, unshown string
	if err := s.WithAccount(ctx, account, func(tx pgx.Tx) error {
		repo, _, err := EnsureRepository(ctx, tx, account, ReachedRepository{FullName: "answers/repo"})
		if err != nil {
			return err
		}
		var pr string
		if err := tx.QueryRow(ctx, `INSERT INTO pull_requests (account_id, repository_id, number, head_sha, head_ref, base_ref, state, url)
			VALUES ($1, $2, 1, 'head', 'f', 'main', 'open', 'https://forge.example/pull/1') RETURNING id`, account, repo).Scan(&pr); err != nil {
			return err
		}
		for _, id := range []*string{&shown, &unshown} {
			if err := tx.QueryRow(ctx, `INSERT INTO reviews (account_id, pull_request_id, head_sha, status) VALUES ($1, $2, 'head', 'running')
				RETURNING id`, account, pr).Scan(id); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	first := map[string][]review.Answer{"0a1b2c3d": {{ID: 7, Author: "maint", Text: "Known limit."}}}
	second := map[string][]review.Answer{"1a2b3c4d": {{ID: 8, Author: "owner", Text: "Fixed in abc."}}}
	for _, answers := range []map[string][]review.Answer{first, second} {
		if err := s.WithAccount(ctx, account, func(tx pgx.Tx) error {
			if err := RecordAnswers(ctx, tx, shown, answers); err != nil {
				return err
			}
			got, err := ReviewAnswers(ctx, tx, shown)
			if err != nil {
				return err
			}
			if len(got) != 1 {
				t.Fatalf("answers = %v, want %v", got, answers)
			}
			for id, want := range answers {
				if !slices.Equal(got[id], want) {
					t.Fatalf("answers = %v, want %v", got, answers)
				}
			}
			none, err := ReviewAnswers(ctx, tx, unshown)
			if err != nil || none != nil {
				t.Fatalf("a review that kept none = %v, %v", none, err)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
}
