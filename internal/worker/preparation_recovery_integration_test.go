//go:build integration

package worker

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/riverqueue/river"

	"github.com/home-operations/kritika/internal/adapter"
	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/forge"
	"github.com/home-operations/kritika/internal/jobs"
	"github.com/home-operations/kritika/internal/metrics"
	"github.com/home-operations/kritika/internal/model"
	"github.com/home-operations/kritika/internal/store"
)

func TestPreparationScoringInterruption(t *testing.T) {
	for i, tt := range []struct {
		name          string
		cause         error
		durableCancel bool
		want          store.ReviewStatus
	}{
		{"shutdown resumes saved runner", errors.New("shutdown"), false, store.ReviewPrepared},
		{"cancel after shutdown", errors.New("shutdown"), true, store.ReviewCanceled},
		{"timeout stops scoring", context.DeadlineExceeded, false, store.ReviewFailed},
	} {
		t.Run(tt.name, func(t *testing.T) {
			h := newAgenticHarness(t, 1000+i)
			job := queuedOutcomeReview(t, h)
			h.sm.reset(scriptSubmit)
			configured := *h.file
			configured.Defaults.Confidence.Model = new(configfile.ModelRef("gateway/judge"))
			h.review.Current.Set(&configured)
			ctx, cancel := context.WithCancelCause(h.ctx)
			defer cancel(nil)
			calls := 0
			fc := &fakeCompleter{}
			h.setSteppers(&adapter.Steppers{Build: func(configfile.Provider) (model.Stepper, error) {
				return model.StepperFunc(func(scoreCtx context.Context, req model.StepRequest) (model.StepResponse, error) {
					calls++
					if calls > 1 {
						return fc.Step(scoreCtx, req)
					}
					var saved store.Publication
					if err := h.st.WithAccount(h.ctx, h.account.ID(), func(tx pgx.Tx) error {
						var err error
						saved, err = store.ReadPublication(h.ctx, tx, job.ID)
						return err
					}); err != nil {
						t.Fatal(err)
					}
					var snapshot publicationSnapshot
					if err := json.Unmarshal(saved.Payload, &snapshot); err != nil || snapshot.Preparation == nil {
						t.Fatalf("runner answer missing before scoring: %s, %v", saved.Payload, err)
					}
					cancel(tt.cause)
					if tt.durableCancel {
						if err := h.st.WithAccount(h.ctx, h.account.ID(), func(tx pgx.Tx) error {
							return jobs.RequestCancel(h.ctx, tx, h.insertOnly, saved.ReviewID)
						}); err != nil {
							t.Fatal(err)
						}
					}
					<-scoreCtx.Done()
					return model.StepResponse{}, scoreCtx.Err()
				}), nil
			}})
			err := h.workStages(t, ctx, h.review, job)
			if (err != nil) != (tt.want == store.ReviewPrepared) {
				t.Fatalf("Work = %v, want %s", err, tt.want)
			}
			if calls != 1 {
				t.Fatalf("scoring calls = %d", calls)
			}
			var saved store.Publication
			if err := h.st.WithAccount(h.ctx, h.account.ID(), func(tx pgx.Tx) error {
				var err error
				saved, err = store.ReadPublication(h.ctx, tx, job.ID)
				return err
			}); err != nil || saved.Status != tt.want {
				t.Fatalf("saved status = %s, %v", saved.Status, err)
			}
			if tt.want == store.ReviewPrepared {
				h.review.Executor = nil
				if err := h.workStages(t, h.ctx, h.review, job); err != nil {
					t.Fatal(err)
				}
				if calls != 2 {
					t.Fatalf("scoring calls after retry = %d", calls)
				}
			}
			h.sm.mu.Lock()
			defer h.sm.mu.Unlock()
			if h.sm.requests != 3 {
				t.Fatalf("runner model calls = %d, want 3", h.sm.requests)
			}
		})
	}
}

func TestPreparationSnoozeDuringShutdown(t *testing.T) {
	h := newAgenticHarness(t, 1005)
	job := queuedOutcomeReview(t, h)
	job.Attempt = job.MaxAttempts
	h.sm.reset(scriptSubmit)
	ctx, cancel := context.WithCancelCause(h.ctx)
	defer cancel(nil)
	reg := prometheus.NewRegistry()
	h.setMetrics(metrics.New(reg))
	h.exec.after = func() {
		if err := h.st.WithAccount(h.ctx, h.account.ID(), func(tx pgx.Tx) error {
			if _, err := tx.Exec(h.ctx, `UPDATE context_packs SET skip_reason = 'filtered'
				WHERE runner_run_id IN (SELECT id FROM runner_runs WHERE river_job_id = $1)`, job.ID); err != nil {
				return err
			}
			_, err := tx.Exec(h.ctx, `UPDATE pull_requests SET labels = '["changed"]' WHERE repository_id = $1 AND number = $2`, job.Args.RepositoryID, h.number)
			return err
		}); err != nil {
			t.Error(err)
		}
		cancel(errors.New("shutdown"))
	}
	err := h.workStages(t, ctx, h.review, job)
	if _, ok := errors.AsType[*river.JobSnoozeError](err); !ok {
		t.Fatalf("Work = %v, want a labels-changed snooze", err)
	}
	if !strings.HasPrefix(h.lf.lastStatus(), "success: kritika: skipped") {
		t.Fatalf("snooze on the last attempt replaced the skip status: %s", h.lf.lastStatus())
	}
	want := `# HELP kritika_reviews_total Reviews finished, by terminal status.
# TYPE kritika_reviews_total counter
kritika_reviews_total{account="` + h.account.Key() + `",status="skipped"} 1
`
	if err := testutil.GatherAndCompare(reg, strings.NewReader(want), "kritika_reviews_total"); err != nil {
		t.Fatal(err)
	}
}

type cancelOnSkipStatus struct {
	*localForge
	cancel context.CancelCauseFunc
}

func (f cancelOnSkipStatus) SetStatus(ctx context.Context, owner, repo, head string, state forge.StatusState, desc string) error {
	f.cancel(context.DeadlineExceeded)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return f.localForge.SetStatus(ctx, owner, repo, head, state, desc)
}

func TestPreparationUnchangedPatchConfidence(t *testing.T) {
	for i, tt := range []struct {
		name, confidence string
		fails            bool
	}{
		{"timeout after commit still reports carried score", `{"score":4,"threshold":3}`, false},
		{"confidence read failure rolls back skip", `{"score":"invalid"}`, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			h, job, pr, reviewID, runID := filteredPreparation(t, 1006+i)
			if err := h.st.WithAccount(h.ctx, h.account.ID(), func(tx pgx.Tx) error {
				if _, err := tx.Exec(h.ctx, `INSERT INTO reviews (account_id, pull_request_id, head_sha, status, confidence, finished_at)
					VALUES ($1, $2, 'previous-head', 'completed', $3, now())`, h.account.ID(), pr.id, tt.confidence); err != nil {
					return err
				}
				_, err := tx.Exec(h.ctx, `UPDATE context_packs SET skip_reason = 'unchanged_patch' WHERE runner_run_id = $1`, runID)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancelCause(h.ctx)
			defer cancel(nil)
			var eff Effective
			eff.Confidence = configfile.Confidence{Model: "gateway/judge", Threshold: 5, Gate: true}
			_, _, err := h.review.afterRun(ctx, job, h.account, pr, eff, nil,
				cancelOnSkipStatus{h.lf, cancel}, reviewID, runID, priorReview{}, json.RawMessage(`[]`), h.review.Logger)
			if (err != nil) != tt.fails {
				t.Fatalf("afterRun = %v", err)
			}
			var status store.ReviewStatus
			if err := h.st.WithAccount(h.ctx, h.account.ID(), func(tx pgx.Tx) error {
				return tx.QueryRow(h.ctx, `SELECT status FROM reviews WHERE id = $1`, reviewID).Scan(&status)
			}); err != nil {
				t.Fatal(err)
			}
			if tt.fails {
				if status != store.ReviewRunning {
					t.Fatalf("failed confidence read committed %s", status)
				}
			} else {
				if status != store.ReviewSkipped || h.lf.lastStatus() != "failure: kritika: confidence 4/5, below 5, skipped (patch unchanged since the last review)" {
					t.Fatalf("review=%s commit=%s", status, h.lf.lastStatus())
				}
			}
		})
	}
}

func TestPreparationMergeRecovery(t *testing.T) {
	h := newAgenticHarness(t, 1008)
	job := queuedOutcomeReview(t, h)
	h.sm.reset(scriptSubmit)
	owner := publicationTestOwner(t)
	ctx, cancel := context.WithCancelCause(h.ctx)
	defer cancel(nil)
	h.exec.after = func() {
		if _, err := owner.Exec(h.ctx, `UPDATE agent_runs SET parts = $2 WHERE runner_run_id IN
			(SELECT id FROM runner_runs WHERE river_job_id = $1)`, job.ID,
			`[{"paths":["main.go"],"stop":"submitted","summary":{"take":"Changes main.go."}},
			{"paths":["other.go"],"stop":"submitted","summary":{"take":"Checks other.go."}}]`); err != nil {
			t.Error(err)
		}
		cancel(errors.New("shutdown"))
	}
	if err := h.workStages(t, ctx, h.review, job); !errors.Is(err, context.Cause(ctx)) {
		t.Fatalf("Work = %v, want shutdown retry", err)
	}
	h.review.Executor = nil
	calls := 0
	h.setSteppers(&adapter.Steppers{Build: func(configfile.Provider) (model.Stepper, error) {
		return model.StepperFunc(func(ctx context.Context, req model.StepRequest) (model.StepResponse, error) {
			calls++
			var slots int
			if err := h.st.WithAccount(ctx, h.account.ID(), func(tx pgx.Tx) error {
				return tx.QueryRow(ctx, `SELECT count(*) FROM model_leases WHERE job_id = $1 AND expires_at > now()`, job.ID).Scan(&slots)
			}); err != nil || slots != 1 {
				t.Fatalf("merge model slots = %d, %v", slots, err)
			}
			if req.Tools[0].Name != "summary" {
				t.Fatalf("unexpected model call: %s", req.Tools[0].Name)
			}
			return model.StepResponse{Model: req.Model, Stop: model.StopToolUse, ToolCalls: []model.ToolCall{
				{ID: "merge", Name: "summary", Input: json.RawMessage(`{"headline":"Checks widgets","take":"Changes main.go and checks other.go.","praise":[]}`)},
			}}, nil
		}), nil
	}})
	if err := h.workStages(t, h.ctx, h.review, job); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("merge calls = %d", calls)
	}
	if _, status, why := h.waitReview(t, h.head); status != "completed" {
		t.Fatalf("review = %s: %s", status, why)
	}
	h.sm.mu.Lock()
	defer h.sm.mu.Unlock()
	if h.sm.requests != 3 {
		t.Fatalf("runner model calls = %d, want 3", h.sm.requests)
	}
}
