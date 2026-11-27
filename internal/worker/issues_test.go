package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/home-operations/kritika/internal/forge"
	"github.com/home-operations/kritika/internal/linear"
	"github.com/home-operations/kritika/internal/repoconfig"
	"github.com/home-operations/kritika/internal/review"
	"github.com/home-operations/kritika/internal/tuning"
)

// issueForge answers Issue from a fixed set and fails the numbers it is
// told to.
type issueForge struct {
	forge.Client
	issues  map[int]forge.Issue
	failing int
	asked   []int
}

func (f *issueForge) Issue(_ context.Context, _, _ string, number int) (forge.Issue, error) {
	f.asked = append(f.asked, number)
	if number == f.failing {
		return forge.Issue{}, errors.New("github: 403 Resource not accessible by integration")
	}
	is, ok := f.issues[number]
	if !ok {
		return forge.Issue{}, fmt.Errorf("github: issue %d: %w", number, forge.ErrNotFound)
	}
	return is, nil
}

func TestLinkedIssues(t *testing.T) {
	client := &issueForge{issues: map[int]forge.Issue{
		12: {Number: 12, Title: "Widgets leak", Body: "Steps.", URL: "https://github.com/o/r/issues/12"},
		13: {Number: 13, Title: "A pull request", PullRequest: true},
	}, failing: 15}
	issues, notes := linkedIssues(t.Context(), client, "o", "r", "Closes #12, closes #13, fixes #14, resolves #15, and #16 later", slog.New(slog.DiscardHandler))
	want := []review.Issue{{ID: "#12", Title: "Widgets leak", Body: "Steps."}}
	if !slices.Equal(issues, want) {
		t.Fatalf("issues = %+v, want %+v", issues, want)
	}
	if !slices.Equal(client.asked, []int{12, 13, 14}) {
		t.Fatalf("asked the forge for %v, want the first %d linked issues", client.asked, tuning.Get().Review.MaxLinkedIssues)
	}
	if len(notes) != 0 {
		t.Fatalf("notes = %q, want none for issues within the first three", notes)
	}
	_, notes = linkedIssues(t.Context(), client, "o", "r", "Fixes #15", slog.New(slog.DiscardHandler))
	if len(notes) != 1 || notes[0] != "issue #15, which the description says this closes, could not be read" {
		t.Fatalf("notes = %q, want the unreadable issue noted", notes)
	}
}

// TestLinearIssues: the linked issues of the repository's teams are read
// with the account's key. One that Linear does not give, that moved to a
// team the repository does not read, or that a link names another workspace
// for, is left out without a note. One that Linear fails to give is left out
// with one.
func TestLinearIssues(t *testing.T) {
	var mu sync.Mutex
	var asked []string
	takeAsked := func() []string {
		mu.Lock()
		defer mu.Unlock()
		out := asked
		asked = nil
		return out
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Variables map[string]string }
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || r.Header.Get("Authorization") != "lin_key" {
			t.Errorf("request = %+v, %v with Authorization %q", req, err, r.Header.Get("Authorization"))
		}
		id := req.Variables["id"]
		mu.Lock()
		asked = append(asked, id)
		mu.Unlock()
		issue := func(identifier, team string) string {
			return fmt.Sprintf(`{"data":{"issue":{"identifier":%q,"title":"Title of %s","description":"Body of %s",`+
				`"team":{"key":%q,"organization":{"urlKey":"acme"}}}}}`, identifier, id, id, team)
		}
		switch id {
		case "ENG-1":
			_, _ = w.Write([]byte(issue("ENG-1", "ENG")))
		case "ENG-2":
			_, _ = w.Write([]byte(`{"errors":[{"message":"Entity not found: Issue","extensions":{"type":"invalid input"}}]}`))
		case "ENG-3":
			_, _ = w.Write([]byte(issue("SEC-9", "SEC")))
		case "OPS-4":
			w.WriteHeader(http.StatusBadGateway)
		case "OPS-5":
			_, _ = w.Write([]byte(issue("ENG-1", "ENG")))
		default:
			_, _ = w.Write([]byte(issue(id, strings.Split(id, "-")[0])))
		}
	}))
	defer srv.Close()
	client := &linear.Client{URL: srv.URL}
	pull := repoconfig.PullRequest{Title: "ENG-1: add widgets", HeadRef: "eng-2-widgets", Body: "Fixes ENG-3, OPS-4, OPS-5, " +
		"https://linear.app/other/issue/OPS-7/x, https://linear.app/Acme/issue/OPS-8/y and ENG-6"}
	teams := []string{"ENG", "OPS"}
	issues, notes := linearIssues(t.Context(), client, "lin_key", teams, pull, 8, slog.New(slog.DiscardHandler))
	want := []review.Issue{
		{ID: "ENG-1", Title: "Title of ENG-1", Body: "Body of ENG-1"},
		{ID: "OPS-8", Title: "Title of OPS-8", Body: "Body of OPS-8"},
		{ID: "ENG-6", Title: "Title of ENG-6", Body: "Body of ENG-6"},
	}
	if !slices.Equal(issues, want) {
		t.Fatalf("issues = %+v, want %+v", issues, want)
	}
	if want := []string{"Linear issue OPS-4, which the pull request links, could not be read"}; !slices.Equal(notes, want) {
		t.Fatalf("notes = %q, want %q", notes, want)
	}
	if got, want := takeAsked(), []string{"ENG-1", "ENG-2", "ENG-3", "OPS-4", "OPS-5", "OPS-7", "OPS-8", "ENG-6"}; !slices.Equal(got, want) {
		t.Fatalf("asked Linear for %v, want %v", got, want)
	}
	issues, _ = linearIssues(t.Context(), client, "lin_key", teams, pull, 1, slog.New(slog.DiscardHandler))
	if got := takeAsked(); len(issues) != 1 || !slices.Equal(got, []string{"ENG-1"}) {
		t.Fatalf("with room for one, issues = %+v and asked %v; want ENG-1 alone", issues, got)
	}
}
