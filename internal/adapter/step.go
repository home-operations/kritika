package adapter

import (
	"context"
	"time"

	"github.com/home-operations/kritika/internal/model"
	"github.com/home-operations/kritika/internal/store"
)

// A step that failed in a way that another attempt can avoid is tried again
// after retryMin, doubled each time up to retryMax. A provider's Retry-After
// is honored up to retryAfterMax: a longer one sleeps the step past its
// caller's deadline with the remaining retries and the fallback untried.
const (
	retryMin      = time.Second
	retryMax      = 30 * time.Second
	retryAfterMax = time.Minute
)

// Step runs one model step through stepper. After a transient failure
// (model.Transient), it tries again, up to retries more times, while ctx
// lives. It waits with backoff, or the wait that the provider asked for when
// that is longer, up to retryAfterMax. failed reports each failure it tries
// again after. It returns the last answer or error and how many attempts it
// made. The request is the same each time, so one reservation covers them
// all.
func Step(
	ctx context.Context, stepper model.Stepper, req model.StepRequest, retries int, failed func(error),
) (model.StepResponse, int, error) {
	return step(ctx, stepper, req, retries, sleep, failed)
}

// step is Step with the wait between attempts injected.
func step(
	ctx context.Context, stepper model.Stepper, req model.StepRequest, retries int,
	wait func(context.Context, time.Duration) bool, failed func(error),
) (model.StepResponse, int, error) {
	for attempt := 0; ; attempt++ {
		resp, err := stepper.Step(ctx, req)
		if err == nil || attempt >= retries || ctx.Err() != nil || !retryable(err) {
			return resp, attempt + 1, err
		}
		failed(err)
		if !wait(ctx, max(store.Backoff(attempt, retryMin, retryMax), min(model.RetryAfter(err), retryAfterMax))) {
			return resp, attempt + 1, err
		}
	}
}

// retryable reports whether a step that the provider failed with err is worth
// sending again. It goes by what the x-should-retry header says, when it
// says. Otherwise it goes by what Transient judges. Transient itself leaves
// the header alone, since the gateway sets it false on its refusals for the
// runner's SDK. The runner's loop waits those out by Transient.
func retryable(err error) bool {
	if retry, set := model.ShouldRetry(err); set {
		return retry
	}
	return model.Transient(err)
}

// sleep waits d, or until ctx ends, and reports whether it waited d out.
func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
