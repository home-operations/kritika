//go:build integration

package worker

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/forge"
	"github.com/home-operations/kritika/internal/jobs"
	"github.com/home-operations/kritika/internal/store"
	"github.com/home-operations/kritika/internal/store/storetest"
)

type cancelPublicationForge struct {
	*localForge
	cancel func(context.Context) error
}

func (f *cancelPublicationForge) CreateComment(ctx context.Context, _, _ string, _ int, _ string) (int64, error) {
	if err := f.cancel(ctx); err != nil {
		return 0, err
	}
	return 0, errors.New("forge: delivery failed before the cancel notification")
}

func TestPublicationCancelRacesDeliveryError(t *testing.T) {
	h := newAgenticHarness(t, 973)
	h.review.Forges = &forges{f: &cancelPublicationForge{localForge: h.lf, cancel: func(ctx context.Context) error {
		return h.st.WithAccount(ctx, h.account.ID(), func(tx pgx.Tx) error {
			var id string
			if err := tx.QueryRow(ctx, `SELECT id FROM reviews WHERE head_sha = $1 AND status = 'prepared'`, h.head).Scan(&id); err != nil {
				return err
			}
			if err := jobs.RequestCancel(ctx, tx, h.insertOnly, id); err != nil {
				return err
			}
			var canceled bool
			if err := tx.QueryRow(ctx, `SELECT j.metadata ? 'cancel_attempted_at' FROM river_job j JOIN reviews r ON r.river_job_id = j.id WHERE r.id = $1`, id).Scan(&canceled); err != nil {
				return err
			}
			if canceled {
				t.Error("prepared publication was canceled in River before its worker could end it")
			}
			return nil
		})
	}}}
	h.sm.reset(scriptSubmit)
	h.dispatch(t, h.head)
	id, status, why := h.waitReview(t, h.head)
	if status != "canceled" {
		t.Fatalf("review = %s: %s", status, why)
	}
	waitPublicationJob(t, h, id)
	h.lf.mu.Lock()
	defer h.lf.mu.Unlock()
	if h.lf.status != "error: kritika: review canceled" {
		t.Fatal(h.lf.status)
	}
}

// Recovery tests replay a completed job with a pending checkpoint, with the
// model and runner removed so any attempt to recompute the answer fails.
func publicationReplay(t *testing.T, h *agenticHarness, id string, payload json.RawMessage) (*Review, *river.Job[jobs.ReviewArgs]) {
	t.Helper()
	job := &river.Job[jobs.ReviewArgs]{JobRow: &rivertype.JobRow{Attempt: 2, MaxAttempts: 8},
		Args: jobs.ReviewArgs{AccountID: h.account.ID(), Number: h.number, HeadSHA: h.head}}
	if err := h.st.WithAccount(h.ctx, h.account.ID(), func(tx pgx.Tx) error {
		return tx.QueryRow(h.ctx, `UPDATE reviews r SET status = 'prepared', finished_at = NULL, cancel_requested_at = NULL, publication = $2
   FROM pull_requests p WHERE r.id = $1 AND p.id = r.pull_request_id RETURNING r.river_job_id, p.repository_id`, id, payload).
			Scan(&job.ID, &job.Args.RepositoryID)
	}); err != nil {
		t.Fatal(err)
	}
	worker := *h.review
	worker.Executor, worker.Steppers = nil, nil
	return &worker, job
}

func TestPublicationTerminalRecovery(t *testing.T) {
	h := newAgenticHarness(t, 974)
	h.sm.reset(scriptSubmit)
	h.dispatch(t, h.head)
	id, status, why := h.waitReview(t, h.head)
	if status != "completed" {
		t.Fatalf("review = %s: %s", status, why)
	}
	waitPublicationJob(t, h, id)
	for _, tt := range []struct {
		name, payload string
		configure     func(*configfile.File)
		forgeStatus   bool
	}{
		{name: "unsupported snapshot", payload: `{"Version":2}`, forgeStatus: true},
		{name: "invalid snapshot fields", payload: `{"Version":"broken"}`, forgeStatus: true},
		{name: "removed connection", payload: `{"Version":1}`, configure: func(f *configfile.File) { f.Connections = nil }},
		{name: "removed account", payload: `{"Version":1}`, configure: func(f *configfile.File) { f.Accounts = nil }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			worker, job := publicationReplay(t, h, id, json.RawMessage(tt.payload))
			file := *h.file
			if tt.configure != nil {
				tt.configure(&file)
			}
			worker.Current = configfile.NewCurrent(&file)
			h.lf.mu.Lock()
			h.lf.status = "pending: kritika: review running"
			h.lf.mu.Unlock()
			err := worker.Work(h.ctx, job)
			if _, ok := errors.AsType[*river.JobCancelError](err); !ok {
				t.Fatalf("Work = %v, want permanent failure", err)
			}
			if err := h.st.WithAccount(h.ctx, h.account.ID(), func(tx pgx.Tx) error {
				saved, err := store.ReadPublication(h.ctx, tx, job.ID)
				if err == nil && (saved.Status != store.ReviewFailed || string(saved.Payload) != "{}") {
					t.Fatalf("checkpoint = %+v", saved)
				}
				return err
			}); err != nil {
				t.Fatal(err)
			}
			if tt.forgeStatus {
				h.lf.mu.Lock()
				defer h.lf.mu.Unlock()
				if h.lf.status != "error: kritika: publication failed" {
					t.Fatal(h.lf.status)
				}
			}
		})
	}
}

func TestPublicationCancellationWithoutCheckpoint(t *testing.T) {
	h := newAgenticHarness(t, 975)
	h.sm.reset(scriptSubmit)
	h.dispatch(t, h.head)
	id, status, why := h.waitReview(t, h.head)
	if status != "completed" {
		t.Fatalf("review = %s: %s", status, why)
	}
	waitPublicationJob(t, h, id)
	worker, job := publicationReplay(t, h, id, nil)
	if err := h.st.WithAccount(h.ctx, h.account.ID(), func(tx pgx.Tx) error {
		if _, err := tx.Exec(h.ctx, `UPDATE river_job SET state = 'retryable', finalized_at = NULL, scheduled_at = now() + interval '1 hour' WHERE id = $1`, job.ID); err != nil {
			return err
		}
		return jobs.RequestCancel(h.ctx, tx, h.insertOnly, id)
	}); err != nil {
		t.Fatal(err)
	}
	_, status, why = h.waitReview(t, h.head)
	if status != "canceled" {
		t.Fatalf("review = %s: %s", status, why)
	}
	waitPublicationJob(t, h, id)
	if err := worker.Work(h.ctx, job); err != nil {
		t.Fatal(err)
	}
	h.sm.mu.Lock()
	defer h.sm.mu.Unlock()
	if h.sm.requests != 3 {
		t.Fatalf("model calls = %d, want no additional calls", h.sm.requests)
	}
}

func TestPublicationCheckpointFailureDoesNotRetryModels(t *testing.T) {
	h := newAgenticHarness(t, 976)
	owner := publicationTestOwner(t)
	if _, err := owner.Exec(h.ctx, `ALTER TABLE reviews ADD CONSTRAINT reject_publication_test
  CHECK (publication IS NULL OR NOT (publication ? 'Answer')) NOT VALID`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := owner.Exec(context.Background(), `ALTER TABLE reviews DROP CONSTRAINT reject_publication_test`); err != nil {
			t.Error(err)
		}
	})
	h.sm.reset(scriptSubmit)
	h.dispatch(t, h.head)
	id, status, why := h.waitReview(t, h.head)
	if status != "failed" || !strings.Contains(why, "reject_publication_test") {
		t.Fatalf("review = %s: %s", status, why)
	}
	waitPublicationJob(t, h, id)
	h.sm.mu.Lock()
	defer h.sm.mu.Unlock()
	if h.sm.requests != 3 {
		t.Fatalf("model calls = %d, want one model run", h.sm.requests)
	}
	var attempts int
	if err := h.st.WithAccount(h.ctx, h.account.ID(), func(tx pgx.Tx) error {
		return tx.QueryRow(h.ctx, `SELECT j.attempt FROM river_job j JOIN reviews r ON r.river_job_id = j.id WHERE r.id = $1`, id).Scan(&attempts)
	}); err != nil {
		t.Fatal(err)
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want no retry", attempts)
	}
}

func publicationTestOwner(t *testing.T) *pgx.Conn {
	t.Helper()
	conn, err := pgx.Connect(t.Context(), storetest.Env(t, "KRITIKA_TEST_OWNER_URL"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := conn.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	return conn
}

type endedDuringDelivery struct {
	*localForge
	end func(context.Context) error
}

func (f *endedDuringDelivery) CreateComment(ctx context.Context, owner, repo string, number int, body string) (int64, error) {
	if err := f.end(ctx); err != nil {
		return 0, err
	}
	return f.localForge.CreateComment(ctx, owner, repo, number, body)
}

func TestPublicationDoesNotReportConcurrentTerminationAsCompleted(t *testing.T) {
	h := newAgenticHarness(t, 977)
	h.sm.reset(scriptSubmit)
	h.dispatch(t, h.head)
	id, status, why := h.waitReview(t, h.head)
	if status != "completed" {
		t.Fatalf("review = %s: %s", status, why)
	}
	waitPublicationJob(t, h, id)
	worker, job := publicationReplay(t, h, id, json.RawMessage(`{"Version":1}`))
	pr, err := loadPullRequest(h.ctx, h.st, h.account.ID(), job.Args.RepositoryID, h.number)
	if err != nil {
		t.Fatal(err)
	}
	// Force creation so the callback runs inside delivery.
	h.lf.mu.Lock()
	h.lf.comments = nil
	h.lf.mu.Unlock()
	if err := h.st.WithAccount(h.ctx, h.account.ID(), func(tx pgx.Tx) error {
		_, err := tx.Exec(h.ctx, `DELETE FROM sticky_comments WHERE pull_request_id = $1`, pr.id)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	p := &publishPhase{w: worker, account: h.account, pr: pr, reviewID: id, jobID: job.ID, logger: worker.Logger,
		trigger: jobs.TriggerManual, client: &endedDuringDelivery{localForge: h.lf, end: func(ctx context.Context) error {
			_, err := worker.endReview(ctx, h.account.ID(), id, store.ReviewEnd{Status: store.ReviewCanceled, OnlyUnfinished: true})
			return err
		}}}
	got, err := p.deliver(h.ctx, &publicationAnswer{})
	if err != nil || got != "" {
		t.Fatalf("deliver = %s, %v; want no newly recorded outcome", got, err)
	}
	got, err = p.deliver(h.ctx, &publicationAnswer{})
	if err != nil || got != "" {
		t.Fatalf("repeat deliver = %s, %v; want no outcome", got, err)
	}
}

func TestPublicationOnlyCompletedPatchesAreReusable(t *testing.T) {
	h := newAgenticHarness(t, 978)
	h.sm.reset(scriptSubmit)
	h.dispatch(t, h.head)
	id, status, why := h.waitReview(t, h.head)
	if status != "completed" {
		t.Fatalf("review = %s: %s", status, why)
	}
	waitPublicationJob(t, h, id)
	_, job := publicationReplay(t, h, id, json.RawMessage(`{"Version":1}`))
	pr, err := loadPullRequest(h.ctx, h.st, h.account.ID(), job.Args.RepositoryID, h.number)
	if err != nil {
		t.Fatal(err)
	}
	pr.authorIsBot = true
	forgeDiff := func() (string, error) {
		return h.lf.PullRequestDiff(h.ctx, "acme", "widgets", pr.number, h.base, pr.headSHA)
	}
	_, unchanged := h.review.botPatch(h.ctx, h.review.Logger, forgeDiff, h.account.ID(), pr)
	if unchanged {
		t.Fatal("unpublished bot patch was treated as already reviewed")
	}
	if err := h.st.WithAccount(h.ctx, h.account.ID(), func(tx pgx.Tx) error {
		baseline, err := store.LastReviewBaseline(h.ctx, tx, pr.id, "00000000-0000-0000-0000-000000000000")
		if baseline.PatchID != "" {
			t.Fatalf("unpublished patch reused: %s", baseline.PatchID)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

// Keep terminal failure text independent of the model path that produced it.
func TestPublicationFinalFailureStatus(t *testing.T) {
	h := newAgenticHarness(t, 979)
	h.sm.reset(scriptSubmit)
	h.dispatch(t, h.head)
	id, status, why := h.waitReview(t, h.head)
	if status != "completed" {
		t.Fatalf("review = %s: %s", status, why)
	}
	waitPublicationJob(t, h, id)
	worker, job := publicationReplay(t, h, id, json.RawMessage(`{"Version":1}`))
	job.Attempt = job.MaxAttempts
	worker.Forges = &forges{f: &unavailablePublication{h.lf}}
	h.lf.mu.Lock()
	h.lf.comments = nil
	h.lf.mu.Unlock()
	if err := h.st.WithAccount(h.ctx, h.account.ID(), func(tx pgx.Tx) error {
		_, err := tx.Exec(h.ctx, `DELETE FROM sticky_comments WHERE pull_request_id = (SELECT pull_request_id FROM reviews WHERE id = $1)`, id)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	err := worker.Work(h.ctx, job)
	if err == nil || !strings.Contains(err.Error(), "temporarily unavailable") {
		t.Fatal(err)
	}
	h.lf.mu.Lock()
	defer h.lf.mu.Unlock()
	if h.lf.status != string(forge.StatusError)+": kritika: publication failed" {
		t.Fatal(h.lf.status)
	}
}

func TestPublicationOptionalReadFailureDoesNotAbortCompletion(t *testing.T) {
	h := newAgenticHarness(t, 980)
	h.sm.reset(scriptSubmit)
	h.dispatch(t, h.head)
	id, status, why := h.waitReview(t, h.head)
	if status != "completed" {
		t.Fatalf("review = %s: %s", status, why)
	}
	waitPublicationJob(t, h, id)
	worker, job := publicationReplay(t, h, id, json.RawMessage(`{"Version":1,"Review":{"Cost":true}}`))
	owner := publicationTestOwner(t)
	if _, err := owner.Exec(h.ctx, `REVOKE SELECT ON usage FROM kritika_app`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := owner.Exec(context.Background(), `GRANT SELECT ON usage TO kritika_app`); err != nil {
			t.Error(err)
		}
	})
	if err := worker.Work(h.ctx, job); err != nil {
		t.Fatal(err)
	}
	if err := h.st.WithAccount(h.ctx, h.account.ID(), func(tx pgx.Tx) error {
		saved, err := store.ReadPublication(h.ctx, tx, job.ID)
		if err == nil && saved.Status != store.ReviewCompleted {
			t.Fatalf("review = %s", saved.Status)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}
