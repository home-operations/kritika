package worker

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"
	"time"

	gh "github.com/google/go-github/v93/github"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/home-operations/kritika/internal/forge/github"
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
		{"preparation", "review_prepare", -time.Second, true},
		{"publication", "review_publish", -time.Second, true},
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

// rateLimited is GitHub's primary rate limit, which resets after reset, as
// go-github reports it.
func rateLimited(reset time.Duration) error {
	req := &http.Request{Method: http.MethodGet, URL: &url.URL{Path: "/repos/o/r"}}
	return &gh.RateLimitError{
		Rate: gh.Rate{Reset: gh.Timestamp{Time: time.Now().Add(reset)}}, Response: &http.Response{Request: req}, Message: "limit",
	}
}

// TestForgeRateLimit: a job that failed on GitHub's rate limit is snoozed
// until the reset and spends no attempt. A job that cancels itself, and any
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

func TestForgeRateLimitGraphQL(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	pemKey := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	for _, tt := range []struct {
		name   string
		status int
		wait   time.Duration
	}{
		{"primary HTTP 200", http.StatusOK, 10 * time.Minute},
		{"secondary HTTP 403", http.StatusForbidden, 2 * time.Minute},
	} {
		t.Run(tt.name, func(t *testing.T) {
			mux := http.NewServeMux()
			mux.HandleFunc("POST /api/v3/app/installations/42/access_tokens", func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprintf(w, `{"token":"ghs_test","expires_at":%q}`, time.Now().Add(time.Hour).UTC().Format(time.RFC3339))
			})
			mux.HandleFunc("POST /api/graphql", func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if tt.status == http.StatusOK {
					w.Header().Set("X-RateLimit-Remaining", "0")
					w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(time.Now().Add(tt.wait).Unix(), 10))
				} else {
					w.Header().Set("Retry-After", "120")
				}
				w.WriteHeader(tt.status)
				_, _ = fmt.Fprint(w, `{"errors":[{"type":"RATE_LIMITED","message":"slow down"}]}`)
			})
			srv := httptest.NewServer(mux)
			defer srv.Close()
			app, err := github.NewApp("Iv1.test", string(pemKey), srv.URL+"/api/v3")
			if err != nil {
				t.Fatal(err)
			}
			client, err := github.NewClient(app, 42)
			if err != nil {
				t.Fatal(err)
			}
			m := &ForgeRateLimit{Logger: slog.New(slog.DiscardHandler)}
			err = m.Work(t.Context(), &rivertype.JobRow{ID: 1, Kind: "thread", Attempt: 8, MaxAttempts: 8}, func(ctx context.Context) error {
				_, _, err := client.ThreadResolved(ctx, "o", "r", 7, 11)
				return err
			})
			snooze, ok := errors.AsType[*river.JobSnoozeError](err)
			if !ok || snooze.Duration < tt.wait-time.Second || snooze.Duration > tt.wait+time.Second {
				t.Fatalf("Work = %v, want snooze for %s", err, tt.wait)
			}
		})
	}
}
