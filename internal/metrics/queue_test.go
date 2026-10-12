package metrics

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestQueueCollector(t *testing.T) {
	var fail bool
	var count int64 = 3
	reg := prometheus.NewPedanticRegistry()
	reg.MustRegister(NewQueueCollector(t.Context(), func(ctx context.Context) ([]QueueStats, error) {
		if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > 2*time.Second {
			t.Error("queue snapshot must have a bounded deadline")
		}
		if fail {
			return nil, errors.New("database unavailable")
		}
		return []QueueStats{{Queue: "review", Runnable: count, OldestSeconds: float64(count) * 10}}, nil
	}, slog.New(slog.DiscardHandler)))
	for _, tt := range []struct {
		name  string
		fail  bool
		count int64
	}{
		{"populated", false, 3}, {"failed scrape", true, 3}, {"empty after recovery", false, 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			fail, count = tt.fail, tt.count
			families, err := reg.Gather()
			if err != nil {
				t.Fatal(err)
			}
			if problems, err := testutil.GatherAndLint(reg); err != nil || len(problems) != 0 {
				t.Fatalf("metric lint = %v, %v", problems, err)
			}
			want := map[string]float64{"kritika_queue_stats_up": 0}
			if !fail {
				want = map[string]float64{"kritika_queue_stats_up": 1, "kritika_queue_runnable_jobs": float64(count), "kritika_queue_oldest_runnable_seconds": float64(count) * 10}
			}
			if len(families) != len(want) {
				t.Fatalf("metric families = %d, want %d", len(families), len(want))
			}
			for _, f := range families {
				value, ok := want[f.GetName()]
				if !ok || len(f.Metric) != 1 || f.Metric[0].GetGauge().GetValue() != value {
					t.Errorf("unexpected metric: %v", f)
				}
			}
		})
	}
}
