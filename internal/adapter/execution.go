package adapter

import (
	"context"
	"log/slog"
	"time"

	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/model"
	"github.com/home-operations/kritika/internal/store"
)

// Reservation is the gateway budget to reserve before a provider call.
type Reservation struct {
	Token  string
	Tokens int64
}

// Execution owns one call's routing, accounting and telemetry. Callers set its
// record and usage identities before invoking Step or Complete. An execution
// belongs to one invocation and must not be shared between goroutines.
type Execution struct {
	Recorder    Recorder
	Record      store.ModelCall
	Usage       store.Usage
	Reservation *Reservation
	Served      Route
	Attempts    int

	invocation string
	call       Call
	fallbacks  []string
	file       *configfile.File
	account    *configfile.Account
	logger     *slog.Logger
}

// Execution resolves both routes before the caller reserves a run's budget.
// A missing fallback is logged; the primary still gets its configured attempts.
func (c *Steppers) Execution(file *configfile.File, account *configfile.Account, ref, fallback configfile.ModelRef,
	halve bool, logger *slog.Logger,
) (*Execution, error) {
	route, err := c.Route(file, account, ref, logger)
	if err != nil {
		return nil, err
	}
	e := &Execution{file: file, account: account, logger: logger, call: Call{Route: route, Halve: halve}}
	e.call.Failed = func(err error, on, next Route) {
		msg := Mask(file, on.Provider)(err.Error())
		if next.Ref != on.Ref {
			logger.Warn("model call failed; trying the fallback", "model", on.Ref, "fallback", next.Ref, "error", msg)
		} else {
			logger.Warn("model call failed; trying again", "model", on.Ref, "error", msg)
		}
	}
	switch {
	case fallback == "":
	case fallback.Provider() == ref.Provider():
		e.fallbacks = []string{fallback.Model()}
	default:
		if fb, err := c.Route(file, account, fallback, logger); err != nil {
			logger.Error("no adapter for the fallback model", "fallback", fallback, "error", err)
		} else {
			e.call.Fallback = &fb
		}
	}
	return e, nil
}

// Step executes a runner's provider-neutral request.
func (e *Execution) Step(ctx context.Context, req model.StepRequest) (model.StepResponse, error) {
	resp, err := e.invoke(ctx, req)
	e.Recorder.Metrics.ModelCall(e.account.Key(), ServedRef(e.Served.Ref, resp.Model), e.Usage.Role, Outcome(err),
		resp.Usage.Prompt(), resp.Usage.CacheRead, resp.Usage.Output, resp.CostUSD, resp.Unpriced)
	return resp, err
}

// Complete executes and extracts a structured answer. Its metrics include
// extraction failures, while accounting retains the provider's actual usage.
func (e *Execution) Complete(ctx context.Context, req model.CompletionRequest) (model.CompletionResponse, error) {
	completer := model.Structured{Stepper: model.StepperFunc(e.invoke)}
	resp, err := completer.Complete(ctx, req)
	e.Recorder.Metrics.ModelCall(e.account.Key(), ServedRef(e.Served.Ref, resp.Model), e.Usage.Role, Outcome(err),
		resp.InputTokens, resp.CachedTokens, resp.OutputTokens, resp.CostUSD, resp.Unpriced)
	return resp, err
}

// Begin records the invocation before dispatch and atomically reserves a
// runner's budget. A gateway calls it separately to classify admission errors.
func (e *Execution) Begin(ctx context.Context) error {
	if e.invocation != "" {
		return nil
	}
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(model.GatewayStepBudget)
	}
	var token string
	var reserved int64
	if r := e.Reservation; r != nil {
		token, reserved = r.Token, r.Tokens
	}
	id, err := e.Recorder.Store.BeginModelInvocation(ctx, e.Usage, token, reserved, deadline.Add(2*time.Minute))
	if err == nil {
		e.invocation = id
	}
	return err
}

func (e *Execution) invoke(ctx context.Context, req model.StepRequest) (model.StepResponse, error) {
	e.Served = e.call.Route
	if err := e.Begin(ctx); err != nil {
		return model.StepResponse{}, err
	}
	req.Fallbacks = e.fallbacks
	started := time.Now()
	resp, attempts, served, err := e.call.Do(ctx, req)
	e.Attempts, e.Served = attempts, served
	record := e.Record
	record.Duration = time.Since(started)
	if chargeErr := e.charge(ctx, resp, err == nil); chargeErr != nil {
		// A paid answer remains useful even when accounting is unavailable.
		e.logger.Error("model call accounting pending recovery", "invocation", e.invocation, "error", chargeErr)
	}
	req.Model = served.Ref.Model()
	var token string
	if e.Reservation != nil {
		token = e.Reservation.Token
	}
	e.Recorder.Record(ctx, e.logger, record, req, resp, err, Mask(e.file, served.Provider, token))
	return resp, err
}

// charge saves the provider outcome before settlement, so a failed usage write
// can be reconciled without repeating the paid request.
func (e *Execution) charge(ctx context.Context, resp model.StepResponse, answered bool) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
	defer cancel()
	usage := e.Usage
	usage.Model, usage.Upstream = resp.Model, resp.Upstream
	usage.Input, usage.Output, usage.CostUSD = resp.Usage.Prompt(), resp.Usage.Output, resp.CostUSD
	usage.ChatGPTPlan, usage.Unpriced = resp.ChatGPTPlan, resp.Unpriced
	if err := e.Recorder.Store.CompleteModelInvocation(ctx, e.invocation, usage, answered); err != nil {
		return err
	}
	_, err := e.Recorder.Store.SettleModelInvocation(ctx, usage.AccountID, e.invocation)
	return err
}
