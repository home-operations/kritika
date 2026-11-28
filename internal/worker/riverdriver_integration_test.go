//go:build integration

package worker

import (
	"context"
	"testing"
	"time"

	"github.com/riverqueue/river/riverdriver"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"

	"github.com/home-operations/kritika/internal/store/storetest"
)

// TestRescuePilotLeavesARecordedHorizon: a stuck job's rescue, a cancel
// included, leaves an attempt whose recorded horizon is still to come and
// takes one with none recorded.
func TestRescuePilotLeavesARecordedHorizon(t *testing.T) {
	st := storetest.Open(t)
	ctx := t.Context()
	job := func(recorded bool) int64 {
		t.Helper()
		var id int64
		if err := st.App().QueryRow(ctx, `INSERT INTO river_job (kind, queue, args, state, attempt, max_attempts, attempted_at, metadata)
			VALUES ('review', 'review', '{}', 'running', 1, 8, now() - interval '5 hours', '{"cancel_attempted_at": "2026-01-01T00:00:00Z"}')
			RETURNING id`).Scan(&id); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _, _ = st.App().Exec(context.Background(), `DELETE FROM river_job WHERE id = $1`, id) })
		if recorded {
			// A replica whose horizon was 7 hours started the attempt.
			if _, err := st.App().Exec(ctx, `UPDATE river_job SET metadata = metadata || jsonb_build_object('kritika_rescue',
				jsonb_build_object('attempted_at_us', (extract(epoch FROM attempted_at) * 1e6)::bigint, 'after_seconds', 25200)) WHERE id = $1`,
				id); err != nil {
				t.Fatal(err)
			}
		}
		return id
	}
	live, unrecorded := job(true), job(false)
	now := time.Now()
	params := &riverdriver.JobRescueManyParams{
		ID: []int64{live, unrecorded}, Error: [][]byte{[]byte(`{}`), []byte(`{}`)}, FinalizedAt: []*time.Time{&now, &now},
		ScheduledAt: []time.Time{now, now}, State: []string{"cancelled", "cancelled"}, StuckHorizon: now.Add(-4 * time.Hour),
	}
	if _, err := (&rescuePilot{}).JobRescueMany(ctx, riverpgxv5.New(st.App()).GetExecutor(), params); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[int64]string{live: "running", unrecorded: "cancelled"} {
		var state string
		if err := st.App().QueryRow(ctx, `SELECT state::text FROM river_job WHERE id = $1`, id).Scan(&state); err != nil {
			t.Fatal(err)
		}
		if state != want {
			t.Errorf("job %d is %s, want %s", id, state, want)
		}
	}
}
