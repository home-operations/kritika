package adapter

import (
	"context"
	"log/slog"
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
// adapter and provider. logger, the caller's, says when a call is unpriced.
func (c *Steppers) Route(f *configfile.File, t *configfile.Account, ref configfile.ModelRef, logger *slog.Logger) (Route, error) {
	stepper, err := c.Stepper(f, t, ref.Provider())
	if err != nil {
		return Route{}, err
	}
	provider, _ := f.Provider(t, ref.Provider())
	account := ""
	if t != nil {
		account = t.Key()
	}
	observed := model.StepperFunc(func(ctx context.Context, req model.StepRequest) (model.StepResponse, error) {
		resp, err := stepper.Step(ctx, req)
		if resp.Unpriced {
			c.warnUnpriced(ctx, logger, account, ref, resp.Model)
		}
		return resp, err
	})
	return Route{Ref: ref, Stepper: observed, Provider: provider}, nil
}

// warnUnpriced reports each serving model once per account and provider
// in this process, including new models a floating alias selects.
func (c *Steppers) warnUnpriced(ctx context.Context, logger *slog.Logger, account string, ref configfile.ModelRef, served string) {
	key := [3]string{account, ref.Provider(), served}
	c.mu.Lock()
	seen := c.unpriced[key]
	if !seen {
		if c.unpriced == nil {
			c.unpriced = make(map[[3]string]bool)
		}
		c.unpriced[key] = true
	}
	c.mu.Unlock()
	if !seen {
		logger.WarnContext(ctx, "model call is unpriced: provider reported no cost and no pricing matched; configure provider pricing",
			"account", account, "provider", ref.Provider(), "model_ref", ref, "model", served)
	}
}

// Pin pins ref, when it is a floating alias kritika resolves, to the model
// it selects now for effort (model.Pinner); any other ref comes back as it
// is.
func (c *Steppers) Pin(
	ctx context.Context, f *configfile.File, t *configfile.Account, ref configfile.ModelRef, effort model.Effort,
) (configfile.ModelRef, error) {
	if p, _ := f.Provider(t, ref.Provider()); !p.Type.TakesAliases() || !model.Floating(ref.Model()) {
		return ref, nil
	}
	stepper, err := c.Stepper(f, t, ref.Provider())
	if err != nil {
		return ref, err
	}
	pinner, ok := stepper.(model.Pinner)
	if !ok {
		return ref, nil
	}
	id, err := pinner.Pin(ctx, ref.Model(), effort)
	if err != nil {
		return ref, err
	}
	return configfile.ModelRef(ref.Provider() + "/" + id), nil
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
