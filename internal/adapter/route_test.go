package adapter

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/openai/openai-go/v3"

	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/configfile/configfiletest"
	"github.com/home-operations/kritika/internal/model"
)

func TestRoute(t *testing.T) {
	t.Setenv("TEST_PROVIDER_KEY", "sk-provider")
	f, err := configfiletest.Parse(t, `providers:
  p:
    type: openai
    apiKey: { env: TEST_PROVIDER_KEY }
  q:
    type: anthropic
    apiKey: { env: TEST_PROVIDER_KEY }
    retries: 2
apps:
  acme-bot:
    accounts: [acme]
    clientId: Iv1.test
    privateKey: { env: TEST_PROVIDER_KEY }
    webhookSecret: { env: TEST_PROVIDER_KEY }
`)
	if err != nil {
		t.Fatal(err)
	}
	built := 0
	c := &Steppers{Build: func(configfile.Provider) (model.Stepper, error) {
		built++
		return &stepperFunc{}, nil
	}}
	r, err := c.Route(f, &f.Accounts[0], "q/small", slog.New(slog.DiscardHandler))
	if err != nil || r.Ref != "q/small" || r.Stepper == nil || r.Provider.Type != configfile.ProviderAnthropic || r.Provider.Retries != 2 || built != 1 {
		t.Fatalf("Route = %+v, %v after %d builds; want q's adapter and provider", r, err, built)
	}
	if _, err := c.Route(f, &f.Accounts[0], "nowhere/small", slog.New(slog.DiscardHandler)); err == nil {
		t.Fatal("Route resolved a provider the configuration lacks")
	}
}

type pinner struct {
	stepperFunc
	asked []string
	err   error
}

func (p *pinner) Pin(_ context.Context, id string, effort model.Effort) (string, error) {
	p.asked = append(p.asked, id+" "+string(effort))
	return id + "@pinned", p.err
}

func TestStepperPin(t *testing.T) {
	t.Setenv("TEST_PROVIDER_KEY", "sk-provider")
	f, err := configfiletest.Parse(t, `providers:
  p: { type: anthropic, apiKey: { env: TEST_PROVIDER_KEY } }
  plain: { type: openrouter, apiKey: { env: TEST_PROVIDER_KEY } }
apps:
  acme-bot: { accounts: [acme], clientId: Iv1.test, privateKey: { env: TEST_PROVIDER_KEY }, webhookSecret: { env: TEST_PROVIDER_KEY } }
`)
	if err != nil {
		t.Fatal(err)
	}
	refused := errors.New("no catalog")
	for _, tt := range []struct {
		name  string
		ref   configfile.ModelRef
		err   error
		want  configfile.ModelRef
		asked []string
	}{
		{"alias", "p/~opus-latest", nil, "p/~opus-latest@pinned", []string{"~opus-latest high"}},
		{"model id", "p/claude-opus-4-6", nil, "p/claude-opus-4-6", nil},
		{"no fallback", "", nil, "", nil},
		{"unpinned", "p/~opus-latest", refused, "p/~opus-latest", []string{"~opus-latest high"}},
		{"an OpenRouter alias", "plain/~anthropic/claude-opus-latest", nil, "plain/~anthropic/claude-opus-latest", nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			pin := &pinner{err: tt.err}
			c := &Steppers{Build: func(p configfile.Provider) (model.Stepper, error) {
				if p.Type == configfile.ProviderAnthropic {
					return pin, nil
				}
				return nil, errors.New("built an adapter that resolves no aliases")
			}}
			got, err := c.Pin(t.Context(), f, &f.Accounts[0], tt.ref, model.EffortHigh)
			if got != tt.want || !errors.Is(err, tt.err) || !slices.Equal(pin.asked, tt.asked) {
				t.Fatalf("Pin = %q, %v after %v", got, err, pin.asked)
			}
		})
	}
}

// TestCallDo: a call is answered by its model, tried again with its
// provider's retries, and, once those are spent, by the fallback on another
// provider with its own, unless the call's ctx is done; the route that
// answered or failed last is reported with every failure the call went on
// from.
func TestCallDo(t *testing.T) {
	transient := &openai.Error{StatusCode: http.StatusBadGateway}
	final := &openai.Error{StatusCode: http.StatusBadRequest}
	type failure struct{ on, next string }
	tests := []struct {
		name         string
		errs, fbErrs []error
		retries      int
		noFallback   bool
		cancel       bool
		wantErr      error
		attempts     int
		served       string
		fbModel      string
		failed       []failure
	}{
		{name: "the model answers", attempts: 1, served: "p/big"},
		{name: "the model fails and nothing takes over", errs: []error{transient}, noFallback: true, wantErr: transient, attempts: 1, served: "p/big"},
		{
			name: "the fallback takes a transient failure", errs: []error{transient}, attempts: 2, served: "q/small", fbModel: "small",
			failed: []failure{{"p/big", "q/small"}},
		},
		{
			name: "the fallback takes a refusal too", errs: []error{final}, attempts: 2, served: "q/small", fbModel: "small",
			failed: []failure{{"p/big", "q/small"}},
		},
		{
			name: "the model's retries come first", errs: []error{transient, transient}, retries: 1, attempts: 3, served: "q/small", fbModel: "small",
			failed: []failure{{"p/big", "p/big"}, {"p/big", "q/small"}},
		},
		{
			name: "the fallback fails as well", errs: []error{transient}, fbErrs: []error{final}, wantErr: final, attempts: 2, served: "q/small",
			fbModel: "small", failed: []failure{{"p/big", "q/small"}},
		},
		{name: "no fallback once the ctx is done", errs: []error{transient}, cancel: true, wantErr: transient, attempts: 1, served: "p/big"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			primary, fallback := &stepperFunc{errs: tt.errs}, &stepperFunc{errs: tt.fbErrs}
			if tt.cancel {
				primary.then = cancel
			}
			var failed []failure
			route := Route{Ref: "p/big", Stepper: primary, Provider: configfile.Provider{Retries: tt.retries}}
			c := Call{Route: route, Failed: func(_ error, on, next Route) { failed = append(failed, failure{string(on.Ref), string(next.Ref)}) }}
			if !tt.noFallback {
				c.Fallback = &Route{Ref: "q/small", Stepper: fallback}
			}
			resp, attempts, served, err := c.Do(ctx, model.StepRequest{Model: "ignored"})
			if !errors.Is(err, tt.wantErr) || attempts != tt.attempts || string(served.Ref) != tt.served {
				t.Fatalf("Do = %+v, %d attempts, %s, %v; want %d attempts on %s, err %v", resp, attempts, served.Ref, err, tt.attempts, tt.served, tt.wantErr)
			}
			if tt.wantErr == nil && resp.Text != "ok" {
				t.Fatalf("resp = %+v", resp)
			}
			if primary.models[0] != "big" || (tt.fbModel == "" && fallback.steps != 0) || (tt.fbModel != "" && fallback.models[0] != tt.fbModel) {
				t.Fatalf("the model asked %v, the fallback %v; want big, then %q", primary.models, fallback.models, tt.fbModel)
			}
			if len(failed) != len(tt.failed) {
				t.Fatalf("failures reported %v, want %v", failed, tt.failed)
			}
			for i := range failed {
				if failed[i] != tt.failed[i] {
					t.Fatalf("failures reported %v, want %v", failed, tt.failed)
				}
			}
		})
	}
}

// TestCallHalve: a model that never answers spends half the call's time
// when a fallback on another provider waits for its turn, and all of it
// otherwise, when the fallback would fail the same way.
func TestCallHalve(t *testing.T) {
	hang := model.StepperFunc(func(ctx context.Context, _ model.StepRequest) (model.StepResponse, error) {
		<-ctx.Done()
		return model.StepResponse{}, ctx.Err()
	})
	fallback := &stepperFunc{}
	for _, tt := range []struct {
		name   string
		halve  bool
		served string
	}{
		{"halved, the fallback answers", true, "q/small"},
		{"not halved, the model takes it all", false, "p/big"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 400*time.Millisecond)
			defer cancel()
			route := Route{Ref: "p/big", Stepper: hang}
			c := Call{Route: route, Fallback: &Route{Ref: "q/small", Stepper: fallback}, Halve: tt.halve}
			start := time.Now()
			_, _, served, err := c.Do(ctx, model.StepRequest{})
			if string(served.Ref) != tt.served || (err == nil) != tt.halve {
				t.Fatalf("Do = %s, %v; want %s", served.Ref, err, tt.served)
			}
			if took := time.Since(start); tt.halve && took > 350*time.Millisecond {
				t.Fatalf("the model held the call %s of its 400ms", took)
			}
		})
	}
}
