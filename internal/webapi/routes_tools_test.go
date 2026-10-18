package webapi

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/home-operations/kritika/internal/auth"
)

func TestTopTools(t *testing.T) {
	want := []ToolExecution{{Name: "rg", Executions: 20}, {Name: "jq", Executions: 3}}
	got := topTools(map[string]int64{"jq": 3, "rg": 20, "yq": 3}, 2)
	if !slices.Equal(got, want) {
		t.Fatalf("top tools = %v, want %v", got, want)
	}
	if got := topTools(nil, 10); got == nil || len(got) != 0 {
		t.Fatalf("empty = %#v", got)
	}
}

func TestToolUsageAuthorizationAndWindow(t *testing.T) {
	ts := newTestServer(t, "https://kritika.example")
	if w := ts.as(memberOf(t, ts.file, "alpha"), httptest.NewRequest("GET", "/api/v1/admin/tools", nil)); w.Code != http.StatusNotFound {
		t.Fatalf("account member: %d %s", w.Code, w.Body)
	}
	for _, query := range []string{"from=no", "to=no", "from=2026-09-02&to=2026-09-01", "from=2020-01-01&to=2026-01-01"} {
		w := ts.as(&auth.Principal{Admin: true}, httptest.NewRequest("GET", "/api/v1/admin/tools?"+query, nil))
		if w.Code != http.StatusBadRequest {
			t.Fatalf("%s: %d %s", query, w.Code, w.Body)
		}
	}
}
