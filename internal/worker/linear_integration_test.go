//go:build integration

package worker

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/home-operations/kritika/internal/configfile/configfiletest"
	"github.com/home-operations/kritika/internal/linear"
)

// TestLinkedLinearIssues: a review reads the Linear issues that the pull
// request links, of the teams that the merge-base .kritika.yaml lists, with
// its account's key, and its opening request shows them. A repository whose
// account has no key reads none, and the summary says why.
func TestLinkedLinearIssues(t *testing.T) {
	h := newAgenticHarness(t, 1524)
	var mu sync.Mutex
	var asked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Variables map[string]string }
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("Linear request: %v", err)
		}
		mu.Lock()
		asked = append(asked, r.Header.Get("Authorization")+" "+req.Variables["id"])
		mu.Unlock()
		_, _ = fmt.Fprintf(w, `{"data":{"issue":{"identifier":%q,"title":"Widgets leak","description":"Steps to reproduce.","team":{"key":"ENG"}}}}`,
			req.Variables["id"])
	}))
	t.Cleanup(srv.Close)
	h.review.Linear = linear.Client{URL: srv.URL}
	t.Setenv("TEST_LINEAR_KEY", "lin_key")
	keyed, err := configfiletest.Parse(t, h.config+"accounts:\n  acme:\n    linear: { apiKey: { env: TEST_LINEAR_KEY } }\n")
	if err != nil {
		t.Fatal(err)
	}
	h.review.Current.Set(keyed)
	t.Cleanup(func() { h.review.Current.Set(h.file) })
	h.lf.setBase(h.commit(t, ".kritika.yaml", "linear: { teams: [eng] }\n"))

	h.sm.reset(scriptSubmit)
	h.sm.mu.Lock()
	first := len(h.sm.bodies)
	h.sm.mu.Unlock()
	head := h.commit(t, "main.go", "package main\n\nfunc b() {}\n\nfunc widgets() {}\n")
	h.dispatchBody(t, head, "Fixes ENG-12 and SEC-1")
	if _, status, errText := h.waitReview(t, head); status != "completed" {
		t.Fatalf("status = %s (%s), want completed", status, errText)
	}
	mu.Lock()
	if want := []string{"lin_key ENG-12"}; !slices.Equal(asked, want) {
		t.Fatalf("Linear was asked %q, want %q", asked, want)
	}
	asked = nil
	mu.Unlock()
	h.sm.mu.Lock()
	opening := string(h.sm.bodies[first])
	h.sm.mu.Unlock()
	if !strings.Contains(opening, `id=\"ENG-12\" title=\"Widgets leak\"`) || !strings.Contains(opening, "Steps to reproduce.") {
		t.Fatalf("the opening request lacks ENG-12:\n%s", opening)
	}

	h.review.Current.Set(h.file)
	h.sm.reset(scriptSubmit)
	next := h.commit(t, "main.go", "package main\n\nfunc b() {}\n\nfunc widgets() {}\n\nfunc gadgets() {}\n")
	h.dispatchBody(t, next, "Fixes ENG-12")
	if _, status, errText := h.waitReview(t, next); status != "completed" {
		t.Fatalf("status = %s (%s), want completed", status, errText)
	}
	mu.Lock()
	if len(asked) != 0 {
		t.Fatalf("Linear was asked %q with no key", asked)
	}
	mu.Unlock()
	h.lf.mu.Lock()
	defer h.lf.mu.Unlock()
	for _, body := range h.lf.comments {
		if strings.Contains(body, ".kritika.yaml lists linear.teams, but its account has no Linear key: no Linear issue was read") {
			return
		}
	}
	t.Fatalf("no summary says why no Linear issue was read: %v", h.lf.comments)
}
