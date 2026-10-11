package adapter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/model"
)

func TestUnpricedWarnings(t *testing.T) {
	var logs bytes.Buffer
	// The caller's logger, scoped to its run, is the one that warns.
	logger := slog.New(slog.NewJSONHandler(&logs, nil)).With("run", "run-1")
	c := &Steppers{Build: func(configfile.Provider) (model.Stepper, error) {
		return model.StepperFunc(func(_ context.Context, req model.StepRequest) (model.StepResponse, error) {
			if req.Model == "failed" {
				return model.StepResponse{}, errors.New("unavailable")
			}
			id := req.Model
			if _, pinned, ok := strings.Cut(id, "@"); ok {
				id = pinned
			}
			return model.StepResponse{Model: id, Unpriced: id != "free" && id != "plan", ChatGPTPlan: id == "plan"}, nil
		}), nil
	}}
	f := &configfile.File{Providers: map[string]configfile.Provider{"p": {}, "q": {}}}
	alpha := &configfile.Account{Forge: configfile.ForgeGitHub, Name: "alpha"}
	beta := &configfile.Account{Forge: configfile.ForgeGitHub, Name: "beta"}
	for _, tt := range []struct {
		name         string
		account      *configfile.Account
		ref          configfile.ModelRef
		fallback     configfile.ModelRef
		wantWarnings int
	}{
		{"known zero", alpha, "p/free", "", 0},
		{"plan", alpha, "p/plan", "", 0},
		{"failed", alpha, "p/failed", "", 0},
		{"alias", alpha, "p/~family-latest@new-model", "", 1},
		{"same serving model", alpha, "p/new-model", "", 1},
		{"alias advances", alpha, "p/~family-latest@newer-model", "", 2},
		{"fallback", alpha, "p/failed", "q/new-model", 3},
		{"account scope", beta, "p/new-model", "", 4},
	} {
		t.Run(tt.name, func(t *testing.T) {
			route, err := c.Route(f, tt.account, tt.ref, logger)
			if err != nil {
				t.Fatal(err)
			}
			call := Call{Route: route}
			if tt.fallback != "" {
				fallback, err := c.Route(f, tt.account, tt.fallback, logger)
				if err != nil {
					t.Fatal(err)
				}
				call.Fallback = &fallback
			}
			var wg sync.WaitGroup
			for range 8 {
				wg.Go(func() { _, _, _, _ = call.Do(t.Context(), model.StepRequest{}) })
			}
			wg.Wait()
			if got := strings.Count(logs.String(), "\n"); got != tt.wantWarnings {
				t.Fatalf("warnings = %d, want %d: %s", got, tt.wantWarnings, &logs)
			}
		})
	}
	var first struct {
		Level    string `json:"level"`
		Run      string `json:"run"`
		Account  string `json:"account"`
		Provider string `json:"provider"`
		Ref      string `json:"model_ref"`
		Model    string `json:"model"`
	}
	if err := json.NewDecoder(&logs).Decode(&first); err != nil {
		t.Fatal(err)
	}
	if first.Level != "WARN" || first.Run != "run-1" || first.Account != alpha.Key() || first.Provider != "p" ||
		first.Ref != "p/~family-latest@new-model" || first.Model != "new-model" {
		t.Fatalf("warning = %+v", first)
	}
}
