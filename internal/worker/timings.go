package worker

import (
	"cmp"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/riverqueue/river"

	"github.com/home-operations/kritika/internal/jobs"
)

const slotWaitSinceKey = "slot_wait_since"

func (w *Review) recordSlotWait(logger *slog.Logger, job *river.Job[jobs.ReviewArgs], account string) {
	model, elapsed, ok := slotWaitElapsed(job.Metadata, time.Now())
	if !ok {
		return
	}
	w.Metrics.ReviewSlotWait(account, model, elapsed)
	logger.Info("review model slot wait ended", "job", job.ID, "attempt", job.Attempt, "model", model, "elapsed", elapsed)
}

func slotWaitElapsed(metadata []byte, now time.Time) (string, time.Duration, bool) {
	var m struct {
		Model string    `json:"slot_wait"`
		Since time.Time `json:"slot_wait_since"`
	}
	if json.Unmarshal(metadata, &m) != nil || m.Model == "" || m.Since.IsZero() {
		return "", 0, false
	}
	return m.Model, max(0, now.Sub(m.Since)), true
}

func (p *publishPhase) timeStage(stage string) func() {
	started := time.Now()
	return func() {
		p.stageElapsed(stage, time.Since(started))
	}
}

func (p *publishPhase) stageElapsed(stage string, elapsed time.Duration) {
	elapsed = max(0, elapsed)
	if p.w != nil && p.account != nil {
		p.w.Metrics.ReviewStage(p.account.Key(), stage, elapsed)
	}
	cmp.Or(p.logger, slog.Default()).Info("review stage ended", "stage", stage, "elapsed", elapsed,
		"review", p.reviewID, "run", p.runID, "job", p.jobID)
}

func (p *publishPhase) agentElapsed(stage string) {
	if p.agent != nil && !p.agent.CreatedAt.IsZero() {
		p.stageElapsed(stage, time.Since(p.agent.CreatedAt))
	}
}
