package adapter

import (
	"context"
	"time"

	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/model"
)

// Route is a model and where it runs: its adapter and its provider, whose
// retries a step on it gets.
type Route struct {
	Ref      configfile.ModelRef
	Stepper  model.Stepper
	Provider configfile.Provider
}

// Route resolves ref, a model of a provider of account t in f, to its
// adapter and provider.
func (c *Steppers) Route(f *configfile.File, t *configfile.Account, ref configfile.ModelRef) (Route, error) {
	stepper, err := c.Stepper(f, t, ref.Provider())
	if err != nil {
		return Route{}, err
	}
	provider, _ := f.Provider(t, ref.Provider())
	return Route{Ref: ref, Stepper: stepper, Provider: provider}, nil
}

// Call is one model call as the configuration routes it: a step on Route,
// tried again with its provider's retries (Step), and, when Fallback is
// set, the same step on the fallback once those attempts are spent, with
// that provider's retries, so a review carries on there. The request is
// provider-neutral, so it goes to the fallback as it is. A fallback on the
// route's own provider is not a Call's: it goes to the provider with the
// request, as model.StepRequest.Fallbacks.
type Call struct {
	Route
	// Fallback is the model on another provider that takes the step when
	// Route's attempts are spent; nil for none.
	Fallback *Route
	// Failed reports each failure the call goes on from: err on route on,
	// which next tries again, or, for the fallback's first attempt, takes
	// over. It may be nil.
	Failed func(err error, on, next Route)
	// Halve, with a Fallback, bounds the attempts on Route to half the time
	// ctx has left, so the fallback always gets a turn; unset, they may
	// take all of it.
	Halve bool
}

// Do makes the call: it returns the answer or the last error, how many
// requests were sent in all, and the route that answered or failed last,
// which is where the call's cost, metrics and errors belong. The fallback
// gets no turn once ctx is done, since its attempt would fail the same
// way.
func (c Call) Do(ctx context.Context, req model.StepRequest) (model.StepResponse, int, Route, error) {
	req.Model = c.Ref.Model()
	own := ctx
	if deadline, ok := ctx.Deadline(); ok && c.Halve && c.Fallback != nil {
		var cancel context.CancelFunc
		own, cancel = context.WithDeadline(ctx, time.Now().Add(time.Until(deadline)/2))
		defer cancel()
	}
	resp, attempts, err := Step(own, c.Stepper, req, c.Provider.Retries, c.failedOn(c.Route))
	if err == nil || ctx.Err() != nil || c.Fallback == nil {
		return resp, attempts, c.Route, err
	}
	if c.Failed != nil {
		c.Failed(err, c.Route, *c.Fallback)
	}
	req.Model = c.Fallback.Ref.Model()
	resp, more, err := Step(ctx, c.Fallback.Stepper, req, c.Fallback.Provider.Retries, c.failedOn(*c.Fallback))
	return resp, attempts + more, *c.Fallback, err
}

// failedOn is Step's failed for the attempts on r.
func (c Call) failedOn(r Route) func(error) {
	return func(err error) {
		if c.Failed != nil {
			c.Failed(err, r, r)
		}
	}
}
