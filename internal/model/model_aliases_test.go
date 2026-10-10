package model

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

func TestParseAlias(t *testing.T) {
	for _, tt := range []struct {
		id              string
		want            alias
		floating, valid bool
	}{
		{"~sol-latest", alias{name: "~sol-latest", family: "sol"}, true, true},
		{"~new-family-latest", alias{name: "~new-family-latest", family: "new-family"}, true, true},
		{"~opus-latest@claude-opus-4-6", alias{name: "~opus-latest", family: "opus", pinned: "claude-opus-4-6"}, true, true},
		{"~opus-latest@claude-opus-4@20250514", alias{name: "~opus-latest", family: "opus", pinned: "claude-opus-4@20250514"}, true, true},
		{"sol-latest", alias{}, false, true},
		{"claude-3-5-sonnet-latest", alias{}, false, true},
		{"gpt-6-sol", alias{}, false, true},
		{"~", alias{}, true, false},
		{"~-latest", alias{}, true, false},
		{"~sol", alias{}, true, false},
		{"~sol@gpt-6-sol", alias{}, true, false},
	} {
		t.Run(tt.id, func(t *testing.T) {
			got, floating, err := parseAlias(tt.id)
			if got != tt.want || floating != tt.floating || (err == nil) != tt.valid {
				t.Fatalf("alias = %+v, %v, %v", got, floating, err)
			}
		})
	}
}

func TestValidAlias(t *testing.T) {
	for _, tt := range []struct {
		id    string
		valid bool
	}{
		{"~opus-latest", true},
		{"claude-opus-4-6", false},
		{"~opus", false},
		{"~opus-latest@claude-opus-4-6", false},
	} {
		t.Run(tt.id, func(t *testing.T) {
			if got := ValidAlias(tt.id); got != tt.valid {
				t.Fatalf("ValidAlias = %v", got)
			}
		})
	}
}

func TestPin(t *testing.T) {
	var asked []string
	latest := func(family string) (string, error) {
		asked = append(asked, family)
		if family == "missing" {
			return "", errors.New("no missing model")
		}
		return "gpt-6-" + family, nil
	}
	for _, tt := range []struct {
		id, want string
		asks     []string
		valid    bool
	}{
		{"~sol-latest", "~sol-latest@gpt-6-sol", []string{"sol"}, true},
		{"~sol-latest@gpt-5-sol", "~sol-latest@gpt-5-sol", nil, true},
		{"gpt-5-sol", "gpt-5-sol", nil, true},
		{"~missing-latest", "~missing-latest", []string{"missing"}, false},
		{"~sol", "~sol", nil, false},
	} {
		t.Run(tt.id, func(t *testing.T) {
			asked = nil
			got, err := pin(tt.id, latest)
			if got != tt.want || (err == nil) != tt.valid || !slices.Equal(asked, tt.asks) {
				t.Fatalf("pin = %q, %v after asking for %v", got, err, asked)
			}
		})
	}
}

func TestPricingCost(t *testing.T) {
	p := Pricing{"gpt-6-sol": {Input: 2}, "~sol-latest": {Input: 3}}
	u := Usage{Input: 1_000_000}
	for _, tt := range []struct {
		id, alias string
		want      float64
	}{
		{"gpt-6-sol", "~sol-latest", 2},
		{"gpt-7-sol", "~sol-latest", 3},
		{"gpt-7-sol", "", 0},
		{"gpt-7-astra", "~astra-latest", 0},
	} {
		if got := p.cost(tt.id, tt.alias, u); got != tt.want {
			t.Errorf("cost(%s, %s) = %v, want %v", tt.id, tt.alias, got, tt.want)
		}
	}
}

func TestFamilyVersion(t *testing.T) {
	for _, tt := range []struct {
		slug, family string
		version      [3]int
		ok           bool
	}{
		{"gpt-6-sol", "sol", [3]int{6, 0, 0}, true},
		{"gpt-6.10.1-sol", "sol", [3]int{6, 10, 1}, true},
		{"astra-6.0", "astra", [3]int{6, 0, 0}, true},
		{"gpt-6-sol", "astra", [3]int{}, false},
		{"gpt-6-sol-preview", "sol", [3]int{}, false},
		{"gpt-+6-sol", "sol", [3]int{}, false},
		{"gpt-6.1.2.3-sol", "sol", [3]int{}, false},
		{"gpt-sol", "sol", [3]int{}, false},
		{"gpt-5.2", "gpt", [3]int{5, 2, 0}, true},
		{"gpt-5.2-sol", "gpt", [3]int{}, false},
	} {
		t.Run(tt.slug+"/"+tt.family, func(t *testing.T) {
			got, ok := familyVersion(tt.slug, tt.family)
			if ok != tt.ok || (ok && got != tt.version) {
				t.Fatalf("version = %v, %v", got, ok)
			}
		})
	}
}

// settle waits for the fetch of key in flight, if any.
func settle[T any](c *modelCatalog[T], key string) {
	_, _, _ = c.fetch.Do(key, func() (any, error) { return nil, nil })
}

func TestModelCatalogRefresh(t *testing.T) {
	var c modelCatalog[string]
	now := time.Now()
	clock := func() time.Time { return now }
	var fetches atomic.Int32
	answers := make(chan func() ([]string, error), 1)
	fetch := func(context.Context) ([]string, error) {
		fetches.Add(1)
		return (<-answers)()
	}
	answer := func(models []string, err error) { answers <- func() ([]string, error) { return models, err } }
	get := func(want string) {
		t.Helper()
		if got, err := c.get(t.Context(), "", clock, fetch); err != nil || !slices.Equal(got, []string{want}) {
			t.Fatalf("catalog = %v, %v; want %s", got, err, want)
		}
	}
	fetched := func(want int32) {
		t.Helper()
		settle(&c, "")
		if fetches.Load() != want {
			t.Fatalf("catalog fetched %d times, want %d", fetches.Load(), want)
		}
	}
	answer([]string{"v1"}, nil)
	get("v1")
	// The expired catalog is served while its refresh, unanswered, runs.
	now = now.Add(modelCatalogTTL)
	get("v1")
	answer(nil, errors.New("catalog unavailable"))
	fetched(2)
	get("v1")
	fetched(2)
	now = now.Add(modelCatalogRetry)
	get("v1")
	answer([]string{"v2"}, nil)
	fetched(3)
	get("v2")
}

func TestModelCatalogSharedFetch(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var c modelCatalog[string]
		var fetches atomic.Int32
		release := make(chan struct{})
		fetch := func(context.Context) ([]string, error) {
			if fetches.Add(1) > 1 {
				return nil, errors.New("a second fetch")
			}
			<-release
			return []string{"current"}, nil
		}
		impatient, cancel := context.WithCancel(t.Context())
		first := make(chan error, 1)
		go func() {
			_, err := c.get(impatient, "", time.Now, fetch)
			first <- err
		}()
		synctest.Wait()
		cancel()
		if err := <-first; !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled waiter = %v", err)
		}
		errs := make(chan error, 8)
		for range cap(errs) {
			go func() {
				models, err := c.get(t.Context(), "", time.Now, fetch)
				if err == nil && !slices.Equal(models, []string{"current"}) {
					err = fmt.Errorf("catalog %v", models)
				}
				errs <- err
			}()
		}
		// Every waiter is blocked on the fetch the canceled one started.
		synctest.Wait()
		close(release)
		for range cap(errs) {
			if err := <-errs; err != nil {
				t.Fatal(err)
			}
		}
		if fetches.Load() != 1 {
			t.Fatalf("catalog fetched %d times, want one shared fetch", fetches.Load())
		}
	})
}
