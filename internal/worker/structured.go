package worker

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/home-operations/kritika/internal/adapter"
	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/model"
	"github.com/home-operations/kritika/internal/store"
)

// structuredCall is a model call publishing makes for a structured answer,
// the confidence score's or the merge's, routed to its model with the
// provider's retries and a fallback, every step recorded and charged.
type structuredCall struct {
	completer model.Structured
	// fallbacks are the fallback models on the call's own provider, which
	// the request carries; one on another provider is the route's.
	fallbacks []string
	// served is the route the last step went to, which a failed step's
	// response does not name.
	served *adapter.Route
}

// structured builds the call to ref, falling back to fallback, recorded
// as kind and charged as role. what names the call in its log lines, and
// modelName the model it is of: a fallback the configuration no longer
// has is logged and done without. halve gives a fallback on another
// provider half of what is left of the call's time.
func (p *publishPhase) structured(
	ctx context.Context, ref, fallback configfile.ModelRef, kind store.ModelCallKind, role, what, modelName string, halve bool,
) (structuredCall, error) {
	route, err := p.w.Steppers.Route(p.file, p.account, ref, p.logger)
	if err != nil {
		return structuredCall{}, err
	}
	// A failure is logged masked, as the gateway logs a step's: the
	// provider or its SDK may echo its key or the credentials in its URL.
	routed := adapter.Call{Route: route, Halve: halve, Failed: func(err error, on, next adapter.Route) {
		msg := adapter.Mask(p.file, on.Provider)(err.Error())
		if next.Ref != on.Ref {
			p.logger.Warn(what+" call failed on the "+modelName+" model; trying the fallback", "fallback", next.Ref, "error", msg)
			return
		}
		p.logger.Warn(what+" call failed; trying again", "model", on.Ref, "error", msg)
	}}
	// A fallback on the call's provider goes to the provider with the
	// call; one on another provider gets the call once the model's
	// attempts are spent.
	var fallbacks []string
	switch {
	case fallback == "":
	case fallback.Provider() == ref.Provider():
		fallbacks = []string{fallback.Model()}
	default:
		if fb, err := p.w.Steppers.Route(p.file, p.account, fallback, p.logger); err != nil {
			p.logger.Error("no adapter for the "+modelName+" fallback", "fallback", fallback, "error", err)
		} else {
			routed.Fallback = &fb
		}
	}
	served := new(adapter.Route)
	stepper := model.StepperFunc(func(ctx context.Context, req model.StepRequest) (model.StepResponse, error) {
		resp, _, on, err := routed.Do(ctx, req)
		*served = on
		return resp, err
	})
	// The call is tried again as a review's step is, with its provider's
	// retries, and recorded once, under the model that answered it or
	// failed it last; it is charged as it is made.
	completer := model.Structured{Stepper: stepper, OnStep: func(req model.StepRequest, resp model.StepResponse, err error, d time.Duration) {
		req.Model = served.Ref.Model()
		p.w.recorder().Record(ctx, p.logger, store.ModelCall{
			AccountID: p.account.ID(), ReviewID: p.reviewID, Kind: kind, Duration: d,
		}, req, resp, err, adapter.Mask(p.file, served.Provider))
		p.charge(ctx, resp, role)
	}}
	return structuredCall{completer: completer, fallbacks: fallbacks, served: served}, nil
}

// pullBody reads the pull request's description.
func (p *publishPhase) pullBody(ctx context.Context) (string, error) {
	var body string
	err := p.w.Store.WithAccount(ctx, p.account.ID(), func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT body FROM pull_requests WHERE id = $1`, p.pr.id).Scan(&body)
	})
	if err != nil {
		return "", fmt.Errorf("worker: read pull request description: %w", err)
	}
	return body, nil
}
