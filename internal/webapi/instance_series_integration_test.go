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
	for _, path := range []string{"/api/v1/usage?group=nope", "/api/v1/analytics?group=nope", "/api/v1/usage?billing=plan", "/api/v1/accounts/github/wa/usage?group=account"} {
		if status, _ := e.getBody("admin", path); status != 400 {
			t.Fatalf("%s: %d", path, status)
		}
	}
	if status, _ := e.getBody("nobody", "/api/v1/usage"); status != 401 {
		t.Fatalf("nobody: %d", status)
	}
}

func testInstanceReviewUsageLimit(t *testing.T, e *apiEnv) {
	for _, account := range []seeded{e.a, e.b} {
		e.exec(`WITH seeded AS (
			INSERT INTO reviews (account_id, pull_request_id, head_sha, status)
			SELECT $1, $2, n::text, 'completed' FROM generate_series(1, 55) n RETURNING id, head_sha)
			INSERT INTO usage (account_id, repository_id, review_id, role, model, input_tokens, output_tokens, cost_usd, created_at)
			SELECT $1, $3, id, 'review', 'test', 56 - head_sha::int, 1, head_sha::int, '2052-01-01' FROM seeded`,
			account.accountID, account.prID, account.repoID)
		e.exec(`INSERT INTO usage (account_id, repository_id, role, model, input_tokens, output_tokens, cost_usd, created_at)
			VALUES ($1, $2, 'followup', 'test', 100, 10, 10000, '2052-01-01')`, account.accountID, account.repoID)
	}
	for _, metric := range []string{"cost", "tokens"} {
		t.Run(metric, func(t *testing.T) {
			status, body := e.getBody("admin", "/api/v1/usage?group=review&from=2052-01-01&to=2052-01-02&metric="+metric)
			var out UsageSeries
			if err := json.Unmarshal(body, &out); err != nil || status != 200 {
				t.Fatalf("usage: %d %s (%v)", status, body, err)
			}
			if len(out.Rows) != 52 {
				t.Fatalf("rows = %d, want 50 plus unattributed and remainder", len(out.Rows))
			}
			var cost float64
			var calls, tokens int64
			accounts := map[string]int{}
			for _, row := range out.Rows {
				cost += row.CostUSD
				calls += row.Calls
				tokens += row.InputTokens + row.OutputTokens
				switch row.Key {
				case "":
					if row.Account != "" || row.Calls != 2 || row.CostUSD != 20000 {
						t.Fatalf("unattributed: %+v", row)
					}
				case otherUsageKey:
					want := 930.0
					if metric == "tokens" {
						want = 2430
					}
					if row.Account != "" || row.Calls != 60 || row.CostUSD != want {
						t.Fatalf("remainder: %+v, want cost %v", row, want)
					}
				default:
					accounts[row.Account]++
					if (metric == "cost" && row.CostUSD < 31) || (metric == "tokens" && row.InputTokens < 31) {
						t.Fatalf("unexpected top review: %+v", row)
					}
				}
			}
			if cost != 23080 || calls != 112 || tokens != 3410 || accounts["github/wa"] != 25 || accounts["github/wb"] != 25 {
				t.Fatalf("totals: cost %v, calls %d, tokens %d, accounts %v", cost, calls, tokens, accounts)
			}
		})
	}
}
