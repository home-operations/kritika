//go:build integration

package worker

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/home-operations/kritika/internal/forge"
	"github.com/home-operations/kritika/internal/jobs"
	"github.com/home-operations/kritika/internal/store"
	"github.com/home-operations/kritika/internal/store/storetest"
)

// lostPublicationResponse stores the sticky comment, then loses the first
// response. Subsequent delivery must discover that comment by its marker.
type lostPublicationResponse struct {
	*localForge
	once     sync.Once
	snapshot func()
	payload  json.RawMessage
}

func (f *lostPublicationResponse) CreateComment(ctx context.Context, owner, repo string, number int, body string) (int64, error) {
	id, err := f.localForge.CreateComment(ctx, owner, repo, number, body)
	f.once.Do(func() {
		if f.snapshot != nil {
			f.snapshot()
		}
		err = errors.New("forge: response lost after creating comment")
	})
	return id, err
}

func (f *lostPublicationResponse) ListInline(context.Context, string, string, int) ([]forge.Comment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]forge.Comment, len(f.inline))
	for i, c := range f.inline {
		out[i] = forge.Comment{ID: int64(1001 + i), Author: "kritika[bot]", Body: c.Body}
	}
	return out, nil
}

func TestPublicationResumesWithoutModelWork(t *testing.T) {
	h := newAgenticHarness(t, 970)
	f := &lostPublicationResponse{localForge: h.lf}
	f.snapshot = func() {
		if err := h.st.WithAccount(h.ctx, h.account.ID(), func(tx pgx.Tx) error {
			return tx.QueryRow(h.ctx, `SELECT publication FROM reviews WHERE head_sha = $1 AND status = 'prepared'`, h.head).Scan(&f.payload)
		}); err != nil {
			t.Error(err)
		}
	}
	h.review.Forges = &forges{f: f}
	h.sm.reset(scriptSubmit)
	h.dispatch(t, h.head)
	id, status, why := h.waitReview(t, h.head)
	if status != "completed" {
		t.Fatalf("review = %s: %s", status, why)
	}
	waitPublicationJob(t, h, id)
	var jobID int64
	var repoID string
	var attempts, reviews, runs, count int
	if err := h.st.WithAccount(h.ctx, h.account.ID(), func(tx pgx.Tx) error {
		return tx.QueryRow(h.ctx, `SELECT r.river_job_id, p.repository_id, j.attempt, p.auto_reviews,
			(SELECT count(*) FROM reviews WHERE pull_request_id = p.id),
			(SELECT count(*) FROM runner_runs WHERE review_id = r.id)
			FROM reviews r JOIN pull_requests p ON p.id = r.pull_request_id JOIN river_job j ON j.id = r.river_job_id
			WHERE r.id = $1`, id).Scan(&jobID, &repoID, &attempts, &count, &reviews, &runs)
	}); err != nil {
		t.Fatal(err)
	}
	h.sm.mu.Lock()
	requests := h.sm.requests
	h.sm.mu.Unlock()
	if attempts != 2 || reviews != 1 || runs != 1 || count != 1 || requests != 3 {
		t.Fatalf("attempts=%d reviews=%d runs=%d automatic=%d model calls=%d; want 2,1,1,1,3", attempts, reviews, runs, count, requests)
	}
	job := &river.Job[jobs.ReviewArgs]{JobRow: &rivertype.JobRow{ID: jobID, Attempt: 3, MaxAttempts: 8},
		Args: jobs.ReviewArgs{AccountID: h.account.ID(), RepositoryID: repoID, Number: h.number, HeadSHA: h.head}}
	// The only state lost here is the database transaction after write-back.
	// The forge keeps both the sticky comment and its inline thread.
	if err := h.st.WithAccount(h.ctx, h.account.ID(), func(tx pgx.Tx) error {
		for _, query := range []string{
			`DELETE FROM findings WHERE review_id = $1`,
			`UPDATE pull_requests SET auto_reviews = 0 WHERE id = (SELECT pull_request_id FROM reviews WHERE id = $1)`,
			`UPDATE reviews SET status = 'prepared', finished_at = NULL WHERE id = $1`,
		} {
			if _, err := tx.Exec(h.ctx, query, id); err != nil {
				return err
			}
		}
		_, err := tx.Exec(h.ctx, `UPDATE reviews SET publication = $2 WHERE id = $1`, id, f.payload)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	worker := *h.review
	worker.Executor, worker.Steppers = nil, nil
	oneConnection, err := store.Open(h.ctx, store.Options{
		AppURL: storetest.Env(t, "KRITIKA_TEST_APP_URL") + "&pool_max_conns=1", Logger: h.review.Logger,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(oneConnection.Close)
	worker.Store = oneConnection
	for range 2 {
		if err := worker.Work(h.ctx, job); err != nil {
			t.Fatal(err)
		}
	}
	f.mu.Lock()
	comments, inline := len(f.comments), len(f.inline)
	f.mu.Unlock()
	if comments != 1 || inline != 1 {
		t.Fatalf("after replay: %d sticky comments, %d inline comments; want one each", comments, inline)
	}
	if err := h.st.WithAccount(h.ctx, h.account.ID(), func(tx pgx.Tx) error {
		var count, findings int
		if err := tx.QueryRow(h.ctx, `SELECT p.auto_reviews, (SELECT count(*) FROM findings WHERE review_id = r.id)
			FROM reviews r JOIN pull_requests p ON p.id = r.pull_request_id WHERE r.id = $1`, id).Scan(&count, &findings); err != nil {
			return err
		}
		if count != 1 || findings != 1 {
			t.Fatalf("after replay: %d reviews, %d findings", count, findings)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestPublicationRetryDoesNotOverwriteNewHead(t *testing.T) {
	h := newAgenticHarness(t, 971)
	h.sm.reset(scriptSubmit)
	h.dispatch(t, h.head)
	id, status, why := h.waitReview(t, h.head)
	if status != "completed" {
		t.Fatalf("review = %s: %s", status, why)
	}
	waitPublicationJob(t, h, id)
	var jobID int64
	var repoID string
	if err := h.st.WithAccount(h.ctx, h.account.ID(), func(tx pgx.Tx) error {
		if err := tx.QueryRow(h.ctx, `SELECT r.river_job_id, p.repository_id FROM reviews r
			JOIN pull_requests p ON p.id = r.pull_request_id WHERE r.id = $1`, id).Scan(&jobID, &repoID); err != nil {
			return err
		}
		if _, err := tx.Exec(h.ctx, `UPDATE reviews SET status = 'prepared', finished_at = NULL, publication = '{"Version":1}' WHERE id = $1`, id); err != nil {
			return err
		}
		_, err := tx.Exec(h.ctx, `UPDATE pull_requests SET head_sha = 'new-head' WHERE repository_id = $1 AND number = $2`, repoID, h.number)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	worker := *h.review
	worker.Executor, worker.Steppers = nil, nil
	job := &river.Job[jobs.ReviewArgs]{JobRow: &rivertype.JobRow{ID: jobID, Attempt: 2, MaxAttempts: 8},
		Args: jobs.ReviewArgs{AccountID: h.account.ID(), RepositoryID: repoID, Number: h.number, HeadSHA: h.head}}
	if err := worker.Work(h.ctx, job); err != nil {
		t.Fatal(err)
	}
	if err := h.st.WithAccount(h.ctx, h.account.ID(), func(tx pgx.Tx) error {
		p, err := store.ReadPublication(h.ctx, tx, jobID)
		if err == nil && p.Status != store.ReviewSuperseded {
			t.Fatalf("status = %s", p.Status)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

type unavailablePublication struct{ *localForge }

func (f *unavailablePublication) CreateComment(context.Context, string, string, int, string) (int64, error) {
	return 0, errors.New("forge: temporarily unavailable")
}

func TestPublicationCancellationDuringBackoff(t *testing.T) {
	h := newAgenticHarness(t, 972)
	h.review.Forges = &forges{f: &unavailablePublication{h.lf}}
	h.sm.reset(scriptSubmit)
	h.dispatch(t, h.head)
	var id string
	waitFor(t, 10*time.Second, "publication waiting for retry", func() bool {
		return h.st.WithAccount(h.ctx, h.account.ID(), func(tx pgx.Tx) error {
			return tx.QueryRow(h.ctx, `SELECT r.id FROM reviews r JOIN river_job j ON j.id = r.river_job_id
				WHERE r.head_sha = $1 AND r.publication IS NOT NULL AND r.status = 'prepared' AND j.state = 'retryable'`, h.head).Scan(&id)
		}) == nil
	})
	if err := h.st.WithAccount(h.ctx, h.account.ID(), func(tx pgx.Tx) error {
		return jobs.RequestCancel(h.ctx, tx, h.insertOnly, id)
	}); err != nil {
		t.Fatal(err)
	}
	_, status, why := h.waitReview(t, h.head)
	if status != "canceled" {
		t.Fatalf("review = %s: %s", status, why)
	}
	waitPublicationJob(t, h, id)
	h.sm.mu.Lock()
	requests := h.sm.requests
	h.sm.mu.Unlock()
	h.lf.mu.Lock()
	forgeStatus := h.lf.status
	comments := len(h.lf.comments)
	h.lf.mu.Unlock()
	if requests != 3 || comments != 0 || forgeStatus != string(forge.StatusError)+": kritika: review canceled" {
		t.Fatalf("canceled publication: %d model calls, %d comments, status %s", requests, comments, forgeStatus)
	}
}

func waitPublicationJob(t *testing.T, h *agenticHarness, id string) {
	t.Helper()
	waitFor(t, 10*time.Second, "publication job completed", func() bool {
		var completed bool
		err := h.st.WithAccount(h.ctx, h.account.ID(), func(tx pgx.Tx) error {
			return tx.QueryRow(h.ctx, `SELECT j.state = 'completed' FROM river_job j JOIN reviews r ON r.river_job_id = j.id
				WHERE r.id = $1`, id).Scan(&completed)
		})
		return err == nil && completed
	})
}
