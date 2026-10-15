//go:build integration

package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river/rivertest"

	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/forge"
	"github.com/home-operations/kritika/internal/jobs"
	"github.com/home-operations/kritika/internal/store"
)

// rival starts a review of head on the harness's pull request the way a
// second worker's job jobID would, under the publication lock.
func (h *agenticHarness) rival(t *testing.T, head string, jobID int64) (store.Started, error) {
	t.Helper()
	args := jobs.ReviewArgs{AccountID: h.account.ID(), RepositoryID: configfile.RepositoryID(h.account.ID(), "acme/widgets"),
		Number: h.number, HeadSHA: head, Trigger: "test"}
	pr, err := loadPullRequest(h.ctx, h.st, args.AccountID, args.RepositoryID, h.number)
	if err != nil {
		t.Fatal(err)
	}
	s, _, err := h.review.start(h.ctx, args, pr, h.base, "", jobID, nil)
	return s, err
}

// replacedRow is what a review a newer one replaced records.
type replacedRow struct {
	status, errText, replacedBy string
	jobID                       int64
}

func (h *agenticHarness) replacedRow(t *testing.T, id string) replacedRow {
	t.Helper()
	var r replacedRow
	if err := h.st.WithAccount(h.ctx, h.account.ID(), func(tx pgx.Tx) error {
		return tx.QueryRow(h.ctx, `SELECT status, error, coalesce(replaced_by_review_id::text, ''), coalesce(river_job_id, 0)
			FROM reviews WHERE id = $1`, id).Scan(&r.status, &r.errText, &r.replacedBy, &r.jobID)
	}); err != nil {
		t.Fatal(err)
	}
	return r
}

// waitRunning waits for a review of head to be recorded running and
// returns its id.
func (h *agenticHarness) waitRunning(t *testing.T, head string) string {
	t.Helper()
	var id string
	waitFor(t, 20*time.Second, "a running review of "+head, func() bool {
		err := h.st.WithAccount(h.ctx, h.account.ID(), func(tx pgx.Tx) error {
			return tx.QueryRow(h.ctx, `SELECT r.id FROM reviews r JOIN pull_requests p ON p.id = r.pull_request_id
				WHERE r.head_sha = $1 AND p.number = $2 AND r.status = 'running'`, head, h.number).Scan(&id)
		})
		return err == nil
	})
	return id
}

// checkReplaced asserts the review a newer one replaced ended as its
// start recorded it, that its job ran once and completed, and that the
// head's commit status is still the one the winner owns.
func (h *agenticHarness) checkReplaced(t *testing.T, id string, winner store.Started) {
	t.Helper()
	if r := h.replacedRow(t, id); r.status != "canceled" || r.errText != store.ReplacedReviewError || r.replacedBy != winner.ReviewID {
		t.Fatalf("the replaced review = %+v, want canceled, %q, replaced by %s", r, store.ReplacedReviewError, winner.ReviewID)
	}
	if err := h.waitJob(t, id); err != nil {
		t.Fatalf("the replaced review's job did not complete once: %v", err)
	}
	h.lf.mu.Lock()
	status, comments := h.lf.status, len(h.lf.comments)
	h.lf.mu.Unlock()
	if status != "pending: kritika: review running" || comments != 0 {
		t.Fatalf("forge status = %q with %d comments; want the winner's pending status and nothing posted", status, comments)
	}
}

// waitJob waits for the review's River job to complete on its first
// attempt: a replaced review is not retried.
func (h *agenticHarness) waitJob(t *testing.T, id string) error {
	t.Helper()
	var state string
	var attempt int
	waitFor(t, 10*time.Second, "the replaced review's job to end", func() bool {
		err := h.st.WithAccount(h.ctx, h.account.ID(), func(tx pgx.Tx) error {
			return tx.QueryRow(h.ctx, `SELECT j.state, j.attempt FROM river_job j JOIN reviews r ON r.river_job_id = j.id WHERE r.id = $1`, id).
				Scan(&state, &attempt)
		})
		return err == nil && state != "running"
	})
	if state != "completed" || attempt != 1 {
		return fmt.Errorf("job %s after attempt %d", state, attempt)
	}
	return nil
}

// TestReplacementCancelsARunningReview: a review that starts while another
// of the same head runs cancels it, as a second worker's start would. The
// older review's runner is stopped and its run recorded as such, its slot
// is let go, it sets no status and posts nothing, and its job is done: a
// retry of it starts nothing, and nor does a queued review of a head the
// pull request has moved past. A third start cancels the second the same
// way, and the first stays pointed at the second.
func TestReplacementCancelsARunningReview(t *testing.T) {
	h := newAgenticHarness(t, 1991)
	// The held runner never starts, so no agent row comes once it is stopped.
	h.review.rowWait = time.Second
	h.exec.mu.Lock()
	h.exec.hold = time.Minute
	h.exec.mu.Unlock()
	h.dispatch(t, h.head)
	loser := h.waitRunning(t, h.head)
	waitFor(t, 10*time.Second, "the review's pending status", func() bool {
		h.lf.mu.Lock()
		defer h.lf.mu.Unlock()
		return h.lf.status == "pending: kritika: review running"
	})

	winner, err := h.rival(t, h.head, 7_000_001)
	if err != nil || len(winner.Replaced) != 1 || winner.Replaced[0] != loser {
		t.Fatalf("rival start = %+v, %v; want it to replace %s", winner, err, loser)
	}
	if id, status, errText := h.waitReview(t, h.head); id != loser || status != "canceled" || errText != store.ReplacedReviewError {
		t.Fatalf("finished review = %s %s (%q), want %s canceled as replaced", id, status, errText, loser)
	}
	h.checkReplaced(t, loser, winner)
	var phase, runErr string
	var held int
	loserJob := h.replacedRow(t, loser).jobID
	if err := h.st.WithAccount(h.ctx, h.account.ID(), func(tx pgx.Tx) error {
		if err := tx.QueryRow(h.ctx, `SELECT phase, error FROM runner_runs WHERE review_id = $1`, loser).Scan(&phase, &runErr); err != nil {
			return err
		}
		return tx.QueryRow(h.ctx, `SELECT count(*) FROM model_leases WHERE job_id = $1`, loserJob).Scan(&held)
	}); err != nil {
		t.Fatal(err)
	}
	if phase != "failed" || !strings.Contains(runErr, errReplaced.Error()) || held != 0 {
		t.Fatalf("the replaced run = %s (%q) holding %d slots; want failed as replaced, holding none", phase, runErr, held)
	}
	h.lf.mu.Lock()
	eyes := h.lf.pullReactions[h.number][forge.ReactionEyes]
	h.lf.mu.Unlock()
	if !eyes {
		t.Fatal("the pull request lost its eyes while the winner runs")
	}

	if _, err := h.rival(t, h.head, loserJob); !errors.Is(err, store.ErrReviewReplaced) {
		t.Fatalf("a retry of the replaced review's job = %v, want ErrReviewReplaced", err)
	}
	if _, err := h.rival(t, strings.Repeat("e", 40), 7_000_002); !errors.Is(err, store.ErrHeadMoved) {
		t.Fatalf("a start of a head the pull request moved past = %v, want ErrHeadMoved", err)
	}
	third, err := h.rival(t, h.head, 7_000_003)
	if err != nil || len(third.Replaced) != 1 || third.Replaced[0] != winner.ReviewID {
		t.Fatalf("third start = %+v, %v; want it to replace %s", third, err, winner.ReviewID)
	}
	if r := h.replacedRow(t, winner.ReviewID); r.status != "canceled" || r.replacedBy != third.ReviewID {
		t.Fatalf("the second review = %+v, want canceled by the third", r)
	}
	if r := h.replacedRow(t, loser); r.replacedBy != winner.ReviewID {
		t.Fatalf("the first review now points at %q, want still the second", r.replacedBy)
	}
}

// TestReplacementBeforePublicationPostsNothing: a review replaced once its
// runner has answered, before it prepares its publication, ends canceled
// without posting a comment or touching the head's status.
func TestReplacementBeforePublicationPostsNothing(t *testing.T) {
	h := newAgenticHarness(t, 1992)
	h.sm.reset(scriptSubmit)
	var winner store.Started
	h.exec.after = func() {
		var err error
		if winner, err = h.rival(t, h.head, 7_000_004); err != nil {
			t.Error(err)
		}
	}
	h.dispatch(t, h.head)
	id, status, errText := h.waitReview(t, h.head)
	if status != "canceled" || errText != store.ReplacedReviewError || len(winner.Replaced) != 1 || winner.Replaced[0] != id {
		t.Fatalf("review = %s (%q), replaced %v; want canceled as replaced by the rival", status, errText, winner.Replaced)
	}
	h.checkReplaced(t, id, winner)
}

// TestReplacementWaitsForPublication: a start while the pull request's
// review is posting finds the publication lock held and comes back later
// (store.ErrPublicationBusy), so the older review either posts in full or
// never posts. Once it has, a start replaces nothing.
func TestReplacementWaitsForPublication(t *testing.T) {
	h := newAgenticHarness(t, 1993)
	h.sm.reset(scriptSubmit)
	busy := make(chan error, 1)
	h.lf.mu.Lock()
	h.lf.publishing = func() {
		_, err := h.rival(t, h.head, 7_000_005)
		busy <- err
	}
	h.lf.mu.Unlock()
	h.dispatch(t, h.head)
	id, status, errText := h.waitReview(t, h.head)
	if status != "completed" {
		t.Fatalf("review = %s: %s", status, errText)
	}
	if err := <-busy; !errors.Is(err, store.ErrPublicationBusy) {
		t.Fatalf("a start during publication = %v, want ErrPublicationBusy", err)
	}
	waitPublicationJob(t, h, id)
	later, err := h.rival(t, h.head, 7_000_006)
	if err != nil || len(later.Replaced) != 0 {
		t.Fatalf("a start after publication = %+v, %v; want it to replace nothing", later, err)
	}
	if r := h.replacedRow(t, id); r.status != "completed" || r.replacedBy != "" {
		t.Fatalf("the published review = %+v, want still completed", r)
	}
}

// TestReplacementWaitsForIncompletePublication: a review that stopped
// short posts that under the publication lock as a delivery does, so a
// start meanwhile waits for it (store.ErrPublicationBusy) rather than
// letting the older review post over the newer one's result.
func TestReplacementWaitsForIncompletePublication(t *testing.T) {
	h := newAgenticHarness(t, 1994)
	h.sm.reset(scriptProse)
	busy := make(chan error, 1)
	h.lf.mu.Lock()
	h.lf.publishing = func() {
		_, err := h.rival(t, h.head, 7_000_007)
		busy <- err
	}
	h.lf.mu.Unlock()
	h.dispatch(t, h.head)
	id, status, errText := h.waitReview(t, h.head)
	if status != "failed" || !strings.Contains(errText, "agent stopped") {
		t.Fatalf("review = %s (%q), want failed as incomplete", status, errText)
	}
	if err := <-busy; !errors.Is(err, store.ErrPublicationBusy) {
		t.Fatalf("a start during an incomplete review's publication = %v, want ErrPublicationBusy", err)
	}
	h.lf.mu.Lock()
	forgeStatus, comments := h.lf.status, len(h.lf.comments)
	h.lf.mu.Unlock()
	if !strings.HasPrefix(forgeStatus, "error: kritika: review incomplete") || comments != 1 {
		t.Fatalf("forge status = %q with %d comments; want the incomplete status and its summary", forgeStatus, comments)
	}
	if r := h.replacedRow(t, id); r.replacedBy != "" {
		t.Fatalf("the incomplete review = %+v, want it not replaced", r)
	}
}

// statusHookForge fails every comment when failComments is set, and runs
// beforeStatus once, as the first status other than pending is about to be
// set.
type statusHookForge struct {
	*localForge
	failComments bool
	beforeStatus func()
	once         sync.Once
}

func (f *statusHookForge) CreateComment(ctx context.Context, owner, repo string, number int, body string) (int64, error) {
	if f.failComments {
		return 0, errors.New("forge: comment refused")
	}
	return f.localForge.CreateComment(ctx, owner, repo, number, body)
}

func (f *statusHookForge) SetStatus(ctx context.Context, owner, repo, head string, state forge.StatusState, desc string) error {
	if state != forge.StatusPending && f.beforeStatus != nil {
		f.once.Do(f.beforeStatus)
	}
	return f.localForge.SetStatus(ctx, owner, repo, head, state, desc)
}

// TestFailureStatusWaitsForStarts: a review that reports its failure on the
// head does so under the publication lock, so a start meanwhile waits
// (store.ErrPublicationBusy) and the failure is not written over what the
// newer review says of the head: the fallback status of an incomplete
// review whose summary the forge refused, and the status a job's last
// attempt sets as it fails.
func TestFailureStatusWaitsForStarts(t *testing.T) {
	for i, tt := range []struct {
		name         string
		failComments bool
	}{
		{"incomplete publication fallback", true},
		{"last attempt failing", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			h := newAgenticHarness(t, 1995+i)
			job := queuedOutcomeReview(t, h)
			f := &statusHookForge{localForge: h.lf, failComments: tt.failComments}
			h.review.Forges = &forges{f: f}
			busy := make(chan error, 1)
			f.beforeStatus = func() {
				_, err := h.rival(t, h.head, 7_000_010+int64(i))
				busy <- err
			}
			if tt.failComments {
				h.sm.reset(scriptProse)
			} else {
				// A context pack the worker cannot decode fails the attempt
				// after the runner has answered; the last attempt reports that.
				h.sm.reset(scriptSubmit)
				job.Attempt = job.MaxAttempts
				h.exec.after = func() {
					if err := h.st.WithAccount(h.ctx, h.account.ID(), func(tx pgx.Tx) error {
						_, err := tx.Exec(h.ctx, `UPDATE context_packs SET repo_files = '[]' WHERE runner_run_id IN
							(SELECT rr.id FROM runner_runs rr JOIN reviews r ON r.id = rr.review_id WHERE r.river_job_id = $1)`, job.ID)
						return err
					}); err != nil {
						t.Error(err)
					}
				}
			}
			err := h.review.Work(rivertest.WorkContext(h.ctx, h.insertOnly), job)
			if (err != nil) != !tt.failComments {
				t.Fatalf("Work = %v; want an error only from the last attempt failing", err)
			}
			select {
			case err := <-busy:
				if !errors.Is(err, store.ErrPublicationBusy) {
					t.Fatalf("a start as the failure status is set = %v, want ErrPublicationBusy", err)
				}
			default:
				t.Fatal("no error status was set")
			}
			h.lf.mu.Lock()
			status := h.lf.status
			h.lf.mu.Unlock()
			if status != "error: "+reviewFailedDescription {
				t.Fatalf("forge status = %q, want the failure the review reported under the lock", status)
			}
		})
	}
}

// TestSkipStatusWaitsForStarts: a review the runner found filtered out
// completes its job as it records the skip, which frees the head for a
// review someone asks for; the skip's status and carried approval are then
// written under the publication lock, so such a start waits
// (store.ErrPublicationBusy) and the skip is not written over it.
func TestSkipStatusWaitsForStarts(t *testing.T) {
	h, job, pr, reviewID, runID := filteredPreparation(t, 1997)
	f := &statusHookForge{localForge: h.lf}
	busy := make(chan error, 1)
	f.beforeStatus = func() {
		_, err := h.rival(t, h.head, 7_000_020)
		busy <- err
	}
	ctx := rivertest.WorkContext(h.ctx, h.insertOnly)
	_, status, err := h.review.afterRun(ctx, job, h.account, pr, Effective{}, nil, f, reviewID, runID, priorReview{},
		json.RawMessage(`[]`), h.review.Logger)
	if err != nil || status != store.ReviewSkipped {
		t.Fatalf("afterRun = %s, %v; want the skip", status, err)
	}
	select {
	case err := <-busy:
		if !errors.Is(err, store.ErrPublicationBusy) {
			t.Fatalf("a start as the skip's status is set = %v, want ErrPublicationBusy", err)
		}
	default:
		t.Fatal("no skip status was set")
	}
	h.lf.mu.Lock()
	forgeStatus := h.lf.status
	h.lf.mu.Unlock()
	if !strings.HasPrefix(forgeStatus, "success: kritika: skipped") {
		t.Fatalf("forge status = %q, want the skip the review reported under the lock", forgeStatus)
	}
}
