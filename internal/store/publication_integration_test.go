//go:build integration

package store

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/home-operations/kritika/internal/review"
)

func TestPublicationCheckpoint(t *testing.T) {
	s := openStore(t)
	ctx := t.Context()
	if err := s.ApplyConfig(ctx, parse(t, twoAccounts)); err != nil {
		t.Fatal(err)
	}
	account := accountID(t, s, "alpha")
	other := accountID(t, s, "beta")
	id := insertReview(t, ctx, s, account)
	const jobID = 987654321
	payload := json.RawMessage(`{"version":1,"model":"saved"}`)
	var pullID string
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(s.WithAccount(ctx, account, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `UPDATE reviews SET status = 'prepared', river_job_id = $2 WHERE id = $1 RETURNING pull_request_id`, id, jobID).Scan(&pullID)
	}))
	t.Run("checkpoint survives while replacement is refused", func(t *testing.T) {
		must(s.WithAccount(ctx, account, func(tx pgx.Tx) error { return SavePublication(ctx, tx, id, payload) }))
		err := s.WithAccount(ctx, account, func(tx pgx.Tx) error { return SavePublication(ctx, tx, id, json.RawMessage(`{}`)) })
		if err == nil {
			t.Fatal("replaced an existing publication")
		}
		must(s.WithAccount(ctx, account, func(tx pgx.Tx) error {
			p, err := ReadPublication(ctx, tx, jobID)
			must(err)
			var got map[string]any
			if err := json.Unmarshal(p.Payload, &got); err != nil {
				return err
			}
			if p.ReviewID != id || got["model"] != "saved" {
				t.Fatalf("checkpoint = %+v", p)
			}
			return nil
		}))
	})
	t.Run("another account cannot read the checkpoint", func(t *testing.T) {
		err := s.WithAccount(ctx, other, func(tx pgx.Tx) error { _, err := ReadPublication(ctx, tx, jobID); return err })
		if !errors.Is(err, ErrNotFound) {
			t.Fatalf("read = %v, want not found", err)
		}
	})
	t.Run("a changed head cannot publish", func(t *testing.T) {
		must(s.WithAccount(ctx, account, func(tx pgx.Tx) error {
			current, err := PublicationCurrent(ctx, tx, id)
			if err != nil {
				return err
			}
			if !current {
				t.Fatal("prepared review is not current")
			}
			if _, err := tx.Exec(ctx, `UPDATE pull_requests SET head_sha = 'new' WHERE id = $1`, pullID); err != nil {
				return err
			}
			current, err = PublicationCurrent(ctx, tx, id)
			if err != nil {
				return err
			}
			if current {
				t.Fatal("old head may publish")
			}
			_, err = tx.Exec(ctx, `UPDATE pull_requests SET head_sha = 'abc' WHERE id = $1`, pullID)
			return err
		}))
	})
	result := ReviewResult{AccountID: account, ReviewID: id, PullRequestID: pullID, Model: "saved", CommentID: 123,
		Result: review.Result{Findings: []review.Finding{{Path: "a.go", Line: 1, Severity: review.SeverityP2, Category: review.CategoryCorrectness, Title: "finding"}}},
		Inline: []InlinePosted{{Posted: true, ID: 456}}}
	t.Run("rollback leaves delivery and count pending", func(t *testing.T) {
		rollback := errors.New("lost transaction")
		err := s.WithAccount(ctx, account, func(tx pgx.Tx) error {
			if _, err := CompletePublication(ctx, tx, result, true, false); err != nil {
				return err
			}
			return rollback
		})
		if !errors.Is(err, rollback) {
			t.Fatal(err)
		}
		must(s.WithAccount(ctx, account, func(tx pgx.Tx) error {
			p, err := ReadPublication(ctx, tx, jobID)
			must(err)
			if string(p.Payload) == "{}" {
				t.Fatal("rollback lost the pending checkpoint")
			}
			var count, findings int
			var status string
			err = tx.QueryRow(ctx, `SELECT r.status, p.auto_reviews, (SELECT count(*) FROM findings WHERE review_id = r.id)
				FROM reviews r JOIN pull_requests p ON p.id = r.pull_request_id WHERE r.id = $1`, id).Scan(&status, &count, &findings)
			if err == nil && (status != "prepared" || count != 0 || findings != 0) {
				t.Fatalf("after rollback: %s, %d reviews, %d findings", status, count, findings)
			}
			return err
		}))
	})
	t.Run("completion counts and persists once", func(t *testing.T) {
		for _, want := range []bool{true, false} {
			must(s.WithAccount(ctx, account, func(tx pgx.Tx) error {
				got, err := CompletePublication(ctx, tx, result, true, false)
				if err == nil && got != want {
					t.Fatalf("completed = %v, want %v", got, want)
				}
				return err
			}))
		}
		must(s.WithAccount(ctx, account, func(tx pgx.Tx) error {
			p, err := ReadPublication(ctx, tx, jobID)
			must(err)
			if p.Status != ReviewCompleted || string(p.Payload) != "{}" {
				t.Fatalf("terminal checkpoint = %+v", p)
			}
			var count, findings int
			err = tx.QueryRow(ctx, `SELECT p.auto_reviews, (SELECT count(*) FROM findings WHERE review_id = $1)
				FROM pull_requests p WHERE p.id = $2`, id, pullID).Scan(&count, &findings)
			if err == nil && (count != 1 || findings != 1) {
				t.Fatalf("%d reviews, %d findings; want one each", count, findings)
			}
			return err
		}))
	})
}

func TestPublicationTerminalPayload(t *testing.T) {
	s := openStore(t)
	ctx := t.Context()
	if err := s.ApplyConfig(ctx, parse(t, twoAccounts)); err != nil {
		t.Fatal(err)
	}
	account := accountID(t, s, "alpha")
	id := insertReview(t, ctx, s, account)
	const jobID = 987654322
	payload := json.RawMessage(`{"version":1}`)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(s.WithAccount(ctx, account, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE reviews SET river_job_id = $2 WHERE id = $1`, id, jobID)
		return err
	}))
	for _, status := range []ReviewStatus{ReviewCanceled, ReviewFailed, ReviewSuperseded} {
		t.Run(string(status)+" releases its payload", func(t *testing.T) {
			must(s.WithAccount(ctx, account, func(tx pgx.Tx) error {
				if _, err := tx.Exec(ctx, `UPDATE reviews SET status = 'prepared', finished_at = NULL, publication = $2 WHERE id = $1`, id, payload); err != nil {
					return err
				}
				if _, err := EndReview(ctx, tx, id, ReviewEnd{Status: status, OnlyUnfinished: true}); err != nil {
					return err
				}
				p, err := ReadPublication(ctx, tx, jobID)
				if err == nil && (p.Status != status || string(p.Payload) != "{}") {
					t.Fatalf("terminal checkpoint = %+v", p)
				}
				return err
			}))
		})
	}
}
