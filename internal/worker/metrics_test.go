package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"testing"
	"time"

	gh "github.com/google/go-github/v92/github"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/riverqueue/river"
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
		{"thread", "thread", -time.Second, true},
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

// rateLimited is GitHub's primary rate limit, resetting after reset, as
// go-github reports it.
func rateLimited(reset time.Duration) error {
	req := &http.Request{Method: http.MethodGet, URL: &url.URL{Path: "/repos/o/r"}}
	return &gh.RateLimitError{
		Rate: gh.Rate{Reset: gh.Timestamp{Time: time.Now().Add(reset)}}, Response: &http.Response{Request: req}, Message: "limit",
	}
}

// TestForgeRateLimit: a job that failed on GitHub's rate limit is snoozed
// until the reset, spending no attempt; a job that cancels itself, and any
// other error, pass through.
func TestForgeRateLimit(t *testing.T) {
	limit := rateLimited(10 * time.Minute)
	m := &ForgeRateLimit{Logger: slog.New(slog.DiscardHandler)}
	for _, tt := range []struct {
		name   string
		err    error
		snooze bool
	}{
		{"rate limited", fmt.Errorf("worker: review: %w", limit), true},
		{"cancelled", river.JobCancel(limit), false},
		{"other", errors.New("boom"), false},
		{"done", nil, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := m.Work(t.Context(), &rivertype.JobRow{ID: 1, Kind: "review"}, func(context.Context) error { return tt.err })
			snooze, snoozed := errors.AsType[*river.JobSnoozeError](err)
			if snoozed != tt.snooze || !snoozed && err != tt.err {
				t.Fatalf("Work = %v, want snoozed %v", err, tt.snooze)
			}
			if snoozed && (snooze.Duration < 9*time.Minute || snooze.Duration > 11*time.Minute) {
				t.Fatalf("snoozed %s, want until the reset", snooze.Duration)
			}
		})
	}
}
