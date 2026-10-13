//go:build integration

package webapi

import (
	"encoding/json"
	"testing"
)

// testInstanceSeries: the instance's analytics and usage add up the
// accounts the viewer can read, and no other.
func testInstanceSeries(t *testing.T, e *apiEnv) {
	e.signIn("member-both", "both", memberOfAccounts("github/wa", "github/wb"))
	for _, tc := range []struct {
		who     string
		reviews int
		repos   []string
	}{
		{"member-a", 1, []string{"wa/one"}},
		{"member-both", 2, []string{"wa/one", "wb/one"}},
		{"admin", 2, []string{"wa/one", "wb/one"}},
	} {
		t.Run("analytics "+tc.who, func(t *testing.T) {
			status, body := e.getBody(tc.who, "/api/v1/analytics?group=week&from=2026-01-01")
			var out Analytics
			if err := json.Unmarshal(body, &out); err != nil || status != 200 {
				t.Fatalf("analytics: %d %s (%v)", status, body, err)
			}
			if out.Current.Reviews != tc.reviews || out.Current.MedianReviewMs != nil || out.Current.Findings.P0 != tc.reviews {
				t.Fatalf("totals: %s", body)
			}
			if len(out.Repositories) != len(tc.repos) {
				t.Fatalf("repositories: %s", body)
			}
			for i, r := range out.Repositories {
				if r.Repository != tc.repos[i] || r.Account == "" {
					t.Fatalf("repository %d: %+v", i, r)
				}
			}
			var reviews int
			for _, p := range out.Series {
				reviews += p.Reviews
			}
			if reviews != tc.reviews {
				t.Fatalf("series: %s", body)
			}
		})
	}
	for _, tc := range []struct {
		who   string
		query string
		keys  []string
		cost  float64
	}{
		{"member-a", "group=account", []string{"github/wa"}, 0.5},
		{"member-both", "group=account", []string{"github/wa", "github/wb"}, 1},
		{"member-both", "group=repo", []string{"wa/one", "wb/one"}, 1},
		{"member-both", "group=model", []string{"acme/large"}, 1},
		{"member-both", "group=review", []string{e.a.reviewID, e.b.reviewID}, 1},
		{"member-both", "group=model&repo=wb/one", []string{"acme/large"}, 0.5},
	} {
		t.Run("usage "+tc.who+" "+tc.query, func(t *testing.T) {
			status, body := e.getBody(tc.who, "/api/v1/usage?from=2026-01-01&"+tc.query)
			var out UsageSeries
			if err := json.Unmarshal(body, &out); err != nil || status != 200 {
				t.Fatalf("usage: %d %s (%v)", status, body, err)
			}
			var cost float64
			keys := map[string]UsagePoint{}
			for _, r := range out.Rows {
				cost += r.CostUSD
				if r.Calls > 0 {
					keys[r.Key] = r
				}
			}
			if cost != tc.cost || len(keys) != len(tc.keys) {
				t.Fatalf("rows: %s", body)
			}
			for _, k := range tc.keys {
				r, ok := keys[k]
				if !ok {
					t.Fatalf("missing %s: %s", k, body)
				}
				if attributed := tc.query == "group=account" || tc.query == "group=review"; (r.Account != "") != attributed {
					t.Fatalf("account of %s: %+v", k, r)
				}
			}
		})
	}
	for _, path := range []string{"/api/v1/usage?group=nope", "/api/v1/analytics?group=nope", "/api/v1/usage?billing=plan"} {
		if status, _ := e.getBody("admin", path); status != 400 {
			t.Fatalf("%s: %d", path, status)
		}
	}
	if status, _ := e.getBody("nobody", "/api/v1/usage"); status != 401 {
		t.Fatalf("nobody: %d", status)
	}
}
