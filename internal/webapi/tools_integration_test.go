//go:build integration

package webapi

import (
	"encoding/json"
	"maps"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/home-operations/kritika/internal/store"
)

func testToolUsage(t *testing.T, e *apiEnv) {
	from := time.Date(2040, 1, 1, 0, 0, 0, 0, time.UTC)
	to := from.Add(24 * time.Hour)
	seed := func(account string, at time.Time, counts any) {
		t.Helper()
		run := e.scalar(`INSERT INTO runner_runs (account_id, kind) VALUES ($1, 'review') RETURNING id`, account)
		e.exec(`INSERT INTO agent_runs (runner_run_id, account_id, stop_reason, model, created_at, command_counts)
			VALUES ($1, $2, 'error', '', $3, $4)`, run, account, at, counts)
	}
	seed(e.a.accountID, from, map[string]int{"rg": 3, "jq": 1})
	seed(e.b.accountID, from.Add(time.Hour), map[string]int{"rg": 2, "yq": 4})
	seed(e.a.accountID, from, map[string]int{})
	seed(e.a.accountID, from.Add(-time.Second), map[string]int{"outside": 999})
	seed(e.a.accountID, to, map[string]int{"outside": 999})
	path := "/api/v1/admin/tools?from=2040-01-01&to=2040-01-02"
	status, body := e.getBody("admin", path)
	var got ToolUsage
	if status != http.StatusOK || json.Unmarshal(body, &got) != nil {
		t.Fatalf("admin: %d %s", status, body)
	}
	want := []ToolExecution{{Name: "rg", Executions: 5}, {Name: "yq", Executions: 4}, {Name: "jq", Executions: 1}}
	if !slices.Equal(got.Tools, want) || got.TotalExecutions != 10 || got.RecordedRuns != 3 ||
		!got.From.Equal(from) || !got.To.Equal(to) {
		t.Fatalf("usage = %+v", got)
	}
	for _, who := range []string{"member-a", "member-b", "nobody"} {
		status, body := e.getBody(who, path)
		want := http.StatusNotFound
		if who == "nobody" {
			want = http.StatusUnauthorized
		}
		if status != want {
			t.Fatalf("%s: %d %s", who, status, body)
		}
	}
	if err := e.st.WithAccount(t.Context(), e.b.accountID, func(tx pgx.Tx) error {
		data, err := store.ReadCommandUsage(t.Context(), tx, from, to)
		if err == nil && (!maps.Equal(data.Counts, map[string]int64{"rg": 2, "yq": 4}) || data.RecordedRuns != 1) {
			t.Errorf("account B saw other accounts: %+v", data)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	status, body = e.getBody("admin", "/api/v1/admin/tools?from=2041-01-01&to=2041-01-02")
	if status != http.StatusOK || json.Unmarshal(body, &got) != nil || got.Tools == nil || len(got.Tools) != 0 || got.TotalExecutions != 0 {
		t.Fatalf("empty: %d %s", status, body)
	}
}
