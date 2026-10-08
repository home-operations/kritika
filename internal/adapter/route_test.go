package adapter

import (
	"context"
	"errors"
	"net/http"
	"testing"

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
	r, err := c.Route(f, &f.Accounts[0], "q/small")
	if err != nil || r.Ref != "q/small" || r.Stepper == nil || r.Provider.Type != configfile.ProviderAnthropic || r.Provider.Retries != 2 || built != 1 {
		t.Fatalf("Route = %+v, %v after %d builds; want q's adapter and provider", r, err, built)
	}
	if _, err := c.Route(f, &f.Accounts[0], "nowhere/small"); err == nil {
		t.Fatal("Route resolved a provider the configuration lacks")
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
