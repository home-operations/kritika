package adapter

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/openai/openai-go/v3"

	"github.com/home-operations/kritika/internal/model"
)

// stepperFunc answers each step from the next error in errs, then the
// answer; it records how many steps it took.
type stepperFunc struct {
	errs  []error
	steps int
}

func (s *stepperFunc) Step(context.Context, model.StepRequest) (model.StepResponse, error) {
	s.steps++
	if s.steps <= len(s.errs) {
		return model.StepResponse{}, s.errs[s.steps-1]
	}
	return model.StepResponse{Model: "m", Text: "ok"}, nil
}

// TestStepStopsAtItsBudget: a step whose budget ran out while the provider
// failed is not tried again, whatever retries are left.
func TestStepStopsAtItsBudget(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	s := &stepperFunc{errs: []error{&openai.Error{StatusCode: http.StatusBadGateway}, &openai.Error{StatusCode: http.StatusBadGateway}}}
	waits := 0
	wait := func(context.Context, time.Duration) bool { waits++; cancel(); return true }
	_, attempts, err := step(ctx, s, model.StepRequest{Model: "m"}, 5, wait, func(error) {})
	if err == nil || attempts != 2 || waits != 1 {
		t.Fatalf("step = %d attempts, %d waits, %v; want the second attempt to be the last once the budget is gone", attempts, waits, err)
	}
}

func TestStepRetries(t *testing.T) {
	transient := &openai.Error{StatusCode: http.StatusBadGateway}
	final := &openai.Error{StatusCode: http.StatusBadRequest}
	tests := []struct {
		name     string
		errs     []error
		retries  int
		wantErr  bool
		attempts int
		waits    int
	}{
		{"an answer first time", nil, 3, false, 1, 0},
		{"a transient failure, then an answer", []error{transient}, 3, false, 2, 1},
		{"transient failures past the retries", []error{transient, transient, transient}, 2, true, 3, 2},
		{"no retries", []error{transient}, 0, true, 1, 0},
		{"a final failure is not tried again", []error{final}, 3, true, 1, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &stepperFunc{errs: tt.errs}
			var waits []time.Duration
			wait := func(_ context.Context, d time.Duration) bool { waits = append(waits, d); return true }
			failed := 0
			resp, attempts, err := step(t.Context(), s, model.StepRequest{Model: "m"}, tt.retries, wait, func(error) { failed++ })
			if (err != nil) != tt.wantErr || attempts != tt.attempts || s.steps != tt.attempts || len(waits) != tt.waits || failed != tt.waits {
				t.Fatalf("step = %+v, %d attempts, %v; %d waits, %d reported; want %d attempts, %d waits, err %v",
					resp, attempts, err, len(waits), failed, tt.attempts, tt.waits, tt.wantErr)
			}
			for i, d := range waits {
				if d < retryMin/2 || d > retryMax {
					t.Fatalf("wait %d = %s, outside the backoff", i, d)
				}
			}
			if !tt.wantErr && resp.Text != "ok" {
				t.Fatalf("resp = %+v", resp)
			}
		})
	}

	t.Run("a Retry-After longer than the backoff is waited out", func(t *testing.T) {
		limited := &openai.Error{StatusCode: http.StatusTooManyRequests, Response: &http.Response{Header: http.Header{"Retry-After": {"45"}}}}
		s := &stepperFunc{errs: []error{limited}}
		var waited time.Duration
		wait := func(_ context.Context, d time.Duration) bool { waited = d; return true }
		if _, attempts, err := step(t.Context(), s, model.StepRequest{Model: "m"}, 1, wait, func(error) {}); err != nil || attempts != 2 {
			t.Fatalf("step = %d attempts, %v; want an answer after two", attempts, err)
		}
		if waited != 45*time.Second {
			t.Fatalf("waited %s, want the provider's 45s", waited)
		}
	})

	t.Run("a Retry-After past the cap is cut to it", func(t *testing.T) {
		limited := &openai.Error{StatusCode: http.StatusTooManyRequests, Response: &http.Response{Header: http.Header{"Retry-After": {"3600"}}}}
		s := &stepperFunc{errs: []error{limited}}
		var waited time.Duration
		wait := func(_ context.Context, d time.Duration) bool { waited = d; return true }
		if _, attempts, err := step(t.Context(), s, model.StepRequest{Model: "m"}, 1, wait, func(error) {}); err != nil || attempts != 2 {
			t.Fatalf("step = %d attempts, %v; want an answer after two", attempts, err)
		}
		if waited != retryAfterMax {
			t.Fatalf("waited %s, want the %s cap", waited, retryAfterMax)
		}
	})

	t.Run("a wait the ctx cut ends with the failure", func(t *testing.T) {
		s := &stepperFunc{errs: []error{transient}}
		wait := func(context.Context, time.Duration) bool { return false }
		_, attempts, err := step(t.Context(), s, model.StepRequest{Model: "m"}, 3, wait, func(error) {})
		if !errors.Is(err, transient) || attempts != 1 {
			t.Fatalf("step = %d attempts, %v; want the transient failure after one", attempts, err)
		}
	})

	t.Run("sleep ends early when ctx does", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		if sleep(ctx, time.Minute) {
			t.Fatal("sleep waited a cancelled ctx out")
		}
		if !sleep(t.Context(), time.Millisecond) {
			t.Fatal("sleep did not wait a millisecond out")
		}
	})

	// Step is step with the wait between attempts, which a short backoff
	// after the first failure makes brief.
	t.Run("Step waits the backoff out", func(t *testing.T) {
		s := &stepperFunc{errs: []error{transient}}
		resp, attempts, err := Step(t.Context(), s, model.StepRequest{Model: "m"}, 1, func(error) {})
		if err != nil || attempts != 2 || resp.Text != "ok" {
			t.Fatalf("Step = %+v, %d attempts, %v; want the answer after two", resp, attempts, err)
		}
	})
}
