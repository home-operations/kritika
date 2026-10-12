package worker

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/riverqueue/river/rivertype"

	"github.com/home-operations/kritika/internal/metrics"
)

func TestJobMetrics(t *testing.T) {
	for _, tt := range []struct {
		name, queue string
		scheduled   time.Duration
		wantSample  bool
	}{
		{"due", "review", -time.Minute, true},
		{"retry", "index", -time.Second, true},
		{"future or clock skew", "followup", time.Minute, true},
		{"unknown queue is not a label", "unbounded", -time.Second, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			reg := prometheus.NewRegistry()
			m := JobMetrics{Metrics: metrics.New(reg)}
			wantErr := errors.New("work failed")
			called := false
			err := m.Work(t.Context(), &rivertype.JobRow{Queue: tt.queue, ScheduledAt: time.Now().Add(tt.scheduled)}, func(ctx context.Context) error {
				called = true
				if ctx != t.Context() {
					t.Error("work context changed")
				}
				families, err := reg.Gather()
				if err != nil {
					t.Fatal(err)
				}
				found := false
				for _, f := range families {
					if f.GetName() != "kritika_queue_wait_seconds" {
						continue
					}
					found = true
					h := f.Metric[0].GetHistogram()
					want := max(0, -tt.scheduled.Seconds())
					if h.GetSampleCount() != 1 || h.GetSampleSum() < want || h.GetSampleSum() > want+1 {
						t.Errorf("wait histogram = %v, want one sample near %v", h, want)
					}
				}
				if found != tt.wantSample {
					t.Errorf("recorded = %v, want %v", found, tt.wantSample)
				}
				return wantErr
			})
			if !called || !errors.Is(err, wantErr) {
				t.Fatalf("middleware swallowed work or its error: %v", err)
			}
		})
	}
}
