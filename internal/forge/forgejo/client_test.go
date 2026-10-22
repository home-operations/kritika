package forgejo

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/home-operations/kritika/internal/forge"
)

// fakeForge is enough of a Forgejo or Gitea instance for the client: a
// mux of the routes a test registers, and a log of what was called.
type fakeForge struct {
	t      *testing.T
	mux    *http.ServeMux
	srv    *httptest.Server
	issuer *Issuer
	// token is the Gitea access token every call must carry; empty for
	// Forgejo, whose calls carry a token the issuer signed.
	token string

	mu    sync.Mutex
	calls []string
	// bodies keeps each request's JSON body by method and path.
	bodies map[string][]string
}

func newFake(t *testing.T) *fakeForge {
	t.Helper()
	return startFake(t, "")
}

// newGiteaFake is a Gitea instance, which takes the bot's access token.
func newGiteaFake(t *testing.T) *fakeForge {
	t.Helper()
	return startFake(t, "gitea-api")
}

func startFake(t *testing.T, apiToken string) *fakeForge {
	t.Helper()
	f := &fakeForge{t: t, mux: http.NewServeMux(), bodies: map[string][]string{}, token: apiToken}
	f.issuer = testIssuer(t)
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Every call carries a token of the integration, signed by the
		// issuer, as Forgejo checks it, or Gitea's access token.
		token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		switch {
		case !ok:
			t.Errorf("%s %s: Authorization = %q", r.Method, r.URL.Path, r.Header.Get("Authorization"))
		case f.token != "":
			if token != f.token {
				t.Errorf("%s %s: token = %q, want Gitea's %q", r.Method, r.URL.Path, token, f.token)
			}
		default:
			if claims, _ := verify(t, f.issuer, token); len(claims.Audience) != 1 || claims.Audience[0] != "aud-api" || claims.Issuer != f.issuer.URL() {
				t.Errorf("%s %s: token claims = %+v", r.Method, r.URL.Path, claims)
			}
		}
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		key := r.Method + " " + r.URL.Path
		f.calls = append(f.calls, key)
		f.bodies[key] = append(f.bodies[key], string(body))
		f.mu.Unlock()
		r.Body = io.NopCloser(strings.NewReader(string(body)))
		f.mux.ServeHTTP(w, r)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeForge) client(t *testing.T) *Client {
	t.Helper()
	c, err := NewClient(f.srv.URL, f.issuer, "aud-api", "aud-runner", nil)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func (f *fakeForge) giteaClient(t *testing.T) *Client {
	t.Helper()
	c, err := NewGiteaClient(f.srv.URL, f.token, "gitea-read", nil)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// json registers a GET of path answering v, or a status alone.
func (f *fakeForge) json(method, path string, status int, v any) {
	f.mux.HandleFunc(method+" "+path, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		if v != nil {
			_ = json.NewEncoder(w).Encode(v)
		}
	})
}

func (f *fakeForge) called(key string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if c == key {
			n++
		}
	}
	return n
}

// body is the first request body sent to key.
func (f *fakeForge) body(key string) map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out map[string]any
	if err := json.Unmarshal([]byte(f.bodies[key][0]), &out); err != nil {
		f.t.Fatalf("body of %s: %v", key, err)
	}
	return out
}

const repoAPI = "/api/v1/repos/acme/widgets"

func TestNewClient(t *testing.T) {
	issuer := testIssuer(t)
	for _, tt := range []struct {
		host, api, web string
	}{
		{"forge.example.com", "https://forge.example.com/api/v1", "https://forge.example.com"},
		{"http://127.0.0.1:8080/", "http://127.0.0.1:8080/api/v1", "http://127.0.0.1:8080"},
	} {
		c, err := NewClient(tt.host, issuer, "aud", "", nil)
		if err != nil || c.api != tt.api || c.web != tt.web {
			t.Fatalf("NewClient(%q) = %+v, %v", tt.host, c, err)
		}
	}
	for _, host := range []string{"", "://", "https://"} {
		if _, err := NewClient(host, issuer, "aud", "", nil); err == nil {
			t.Fatalf("NewClient(%q) must fail", host)
		}
	}
	if _, err := NewClient("forge.example.com", issuer, "", "", nil); err == nil || !strings.Contains(err.Error(), "audience") {
		t.Fatalf("without an audience = %v; the integration must exist first", err)
	}
	// A runner's token is the read-only integration's when there is one,
	// else the API's, and lasts the run's hour.
	c, _ := NewClient("forge.example.com", issuer, "aud-api", "aud-read", nil)
	tok, err := c.GitToken(t.Context(), "widgets")
	if err != nil {
		t.Fatal(err)
	}
	if claims, _ := verify(t, issuer, tok); claims.Audience[0] != "aud-read" || claims.Expiry.Time().Sub(claims.IssuedAt.Time()) != runnerTokenTTL+skew {
		t.Fatalf("runner token claims = %+v", claims)
	}
	c, _ = NewClient("forge.example.com", issuer, "aud-api", "", nil)
	tok, _ = c.GitToken(t.Context(), "widgets")
	if claims, _ := verify(t, issuer, tok); claims.Audience[0] != "aud-api" {
		t.Fatalf("runner token without a read-only integration = %+v", claims)
	}
	if c.CloneURL("acme", "widgets") != "https://forge.example.com/acme/widgets.git" {
		t.Fatalf("CloneURL = %s", c.CloneURL("acme", "widgets"))
	}
	if got := c.FileURL("acme", "widgets", "abc", "dir/a b.go", 3, 5); got != "https://forge.example.com/acme/widgets/src/commit/abc/dir/a%20b.go#L3-L5" {
		t.Fatalf("FileURL = %s", got)
	}
	if got := c.FileURL("acme", "widgets", "abc", "a.go", 3, 0); got != "https://forge.example.com/acme/widgets/src/commit/abc/a.go#L3" {
		t.Fatalf("FileURL = %s", got)
	}
	if got := c.CommitURL("acme", "widgets", "abc"); got != "https://forge.example.com/acme/widgets/commit/abc" {
		t.Fatalf("CommitURL = %s", got)
	}
	if got := c.ThreadURL("acme", "widgets", 7, 42); got != "https://forge.example.com/acme/widgets/pulls/7#issuecomment-42" {
		t.Fatalf("ThreadURL = %s", got)
	}
}

func TestPullRequestReads(t *testing.T) {
	f := newFake(t)
	f.json("GET", repoAPI+"/pulls/7", 200, map[string]any{
		"number": 7, "title": "feat", "body": "b", "state": "open", "merge_base": "base1", "html_url": "https://x/acme/widgets/pulls/7",
		"user": map[string]any{"login": "renovate[bot]"}, "updated_at": "2026-10-20T10:00:00Z", "created_at": "2026-10-19T10:00:00Z",
		"head":   map[string]any{"ref": "f", "sha": "head1", "repo": map[string]any{"full_name": "someone/widgets"}},
		"base":   map[string]any{"ref": "main", "sha": "b", "repo": map[string]any{"full_name": "acme/widgets", "default_branch": "main"}},
		"labels": []map[string]any{{"name": "deps", "color": "0e8a16"}}, "additions": 3, "deletions": 1, "changed_files": 2,
	})
	f.json("GET", repoAPI+"/pulls/8", 404, map[string]any{"message": "not found"})
	f.json("GET", repoAPI+"/pulls/9", 200, map[string]any{"number": 9, "merge_base": "b", "head": map[string]any{"sha": "h"}, "user": map[string]any{"login": "x"}})
	f.mux.HandleFunc("GET "+repoAPI+"/pulls/7.diff", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "diff --git a/x b/x\n")
	})
	f.mux.HandleFunc("GET "+repoAPI+"/pulls/9.diff", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(make([]byte, maxDiffBytes+1))
	})
	c := f.client(t)
	base, err := c.MergeBase(t.Context(), "acme", "widgets", 7, "main", "head1")
	if err != nil || base != "base1" {
		t.Fatalf("MergeBase = %q, %v", base, err)
	}
	if _, err := c.MergeBase(t.Context(), "acme", "widgets", 8, "main", "x"); !errors.Is(err, forge.ErrNotFound) {
		t.Fatalf("a missing pull request = %v, want ErrNotFound", err)
	}
	// The pull request moved on since the head under review: its merge
	// base and diff are the new head's, which the review must not take.
	if _, err := c.MergeBase(t.Context(), "acme", "widgets", 7, "main", "older"); err == nil || !strings.Contains(err.Error(), "moved") {
		t.Fatalf("MergeBase of an earlier head = %v, want a refusal", err)
	}
	if _, err := c.PullRequestDiff(t.Context(), "acme", "widgets", 7, "base1", "older"); err == nil || !strings.Contains(err.Error(), "moved") {
		t.Fatalf("PullRequestDiff of an earlier head = %v, want a refusal", err)
	}
	diff, err := c.PullRequestDiff(t.Context(), "acme", "widgets", 7, "base1", "head1")
	if err != nil || !strings.HasPrefix(diff, "diff --git") {
		t.Fatalf("PullRequestDiff = %q, %v", diff, err)
	}
	if _, err := c.PullRequestDiff(t.Context(), "acme", "widgets", 9, "b", "h"); err == nil {
		t.Fatal("an oversized diff must be an error, never a cut one")
	}
	pr, err := c.PullRequest(t.Context(), "acme", "widgets", 7)
	if err != nil || pr.Number != 7 || !pr.Fork || pr.HeadSHA != "head1" || pr.DefaultBranch != "main" || !pr.AuthorIsBot ||
		pr.Additions != 3 || pr.ChangedFiles != 2 || len(pr.Labels) != 1 || pr.Labels[0].Color != "0e8a16" {
		t.Fatalf("PullRequest = %+v, %v", pr, err)
	}
	if _, err := c.PullRequest(t.Context(), "acme", "widgets", 8); !errors.Is(err, forge.ErrNotFound) {
		t.Fatalf("PullRequest(8) = %v, want ErrNotFound", err)
	}
}

func TestRepositoryReads(t *testing.T) {
	f := newFake(t)
	f.json("GET", repoAPI, 200, map[string]any{"default_branch": "trunk"})
	f.json("GET", repoAPI+"/branches/trunk", 200, map[string]any{"commit": map[string]any{"id": "tip1"}})
	f.json("GET", repoAPI+"/branches/feature", 200, map[string]any{"commit": map[string]any{"id": "tip2"}})
	f.json("GET", repoAPI+"/git/commits/tip1", 200, map[string]any{"commit": map[string]any{"message": "  fix: a thing \n\nmore"}})
	f.mux.HandleFunc("GET "+repoAPI+"/raw/dir/a.yaml", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("ref") != "tip1" {
			t.Errorf("ref = %q", r.URL.Query().Get("ref"))
		}
		_, _ = io.WriteString(w, "a: 1\n")
	})
	f.mux.HandleFunc("GET "+repoAPI+"/raw/big", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(make([]byte, forge.MaxFileBytes+1))
	})
	f.json("GET", repoAPI+"/issues/3", 200, map[string]any{"number": 3, "title": "bug", "body": "it breaks", "html_url": "https://x/i/3"})
	f.json("GET", repoAPI+"/issues/7", 200, map[string]any{"number": 7, "title": "pr", "pull_request": map[string]any{"merged": false}})
	f.json("GET", repoAPI+"/issues/9", 404, nil)
	c := f.client(t)
	sha, branch, err := c.BranchTip(t.Context(), "acme", "widgets", "")
	if err != nil || sha != "tip1" || branch != "trunk" {
		t.Fatalf("BranchTip = %q %q %v", sha, branch, err)
	}
	if sha, _, _ = c.BranchTip(t.Context(), "acme", "widgets", "feature"); sha != "tip2" {
		t.Fatalf("BranchTip(feature) = %q", sha)
	}
	if subject, err := c.CommitSubject(t.Context(), "acme", "widgets", "tip1"); err != nil || subject != "fix: a thing" {
		t.Fatalf("CommitSubject = %q, %v", subject, err)
	}
	if content, err := c.FileAt(t.Context(), "acme", "widgets", "tip1", "dir/a.yaml"); err != nil || string(content) != "a: 1\n" {
		t.Fatalf("FileAt = %q, %v", content, err)
	}
	if _, err := c.FileAt(t.Context(), "acme", "widgets", "tip1", "missing"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("a missing file = %v, want fs.ErrNotExist", err)
	}
	if _, err := c.FileAt(t.Context(), "acme", "widgets", "tip1", "big"); !errors.Is(err, forge.ErrFileTooLarge) {
		t.Fatalf("a large file = %v, want ErrFileTooLarge", err)
	}
	is, err := c.Issue(t.Context(), "acme", "widgets", 3)
	if err != nil || is.Title != "bug" || is.PullRequest || is.URL != "https://x/i/3" {
		t.Fatalf("Issue = %+v, %v", is, err)
	}
	if is, _ = c.Issue(t.Context(), "acme", "widgets", 7); !is.PullRequest {
		t.Fatal("a pull request among the issues must say so")
	}
	if _, err := c.Issue(t.Context(), "acme", "widgets", 9); !errors.Is(err, forge.ErrNotFound) {
		t.Fatalf("a missing issue = %v, want ErrNotFound", err)
	}
}

func TestConversationComments(t *testing.T) {
	f := newFake(t)
	f.json("GET", "/api/v1/user", 200, map[string]any{"login": "kritika-bot"})
	f.json("GET", repoAPI+"/issues/7/comments", 200, []map[string]any{
		{"id": 2, "body": "<!-- kritika -->\nsecond", "user": map[string]any{"login": "kritika-bot"}, "created_at": "2026-10-20T10:00:02Z"},
		{"id": 1, "body": "<!-- kritika -->\nfirst", "user": map[string]any{"login": "kritika-bot"}, "created_at": "2026-10-20T10:00:01Z"},
		{"id": 3, "body": "hi", "user": map[string]any{"login": "dev"}, "created_at": "2026-10-20T10:00:03Z"},
	})
	f.json("POST", repoAPI+"/issues/7/comments", 201, map[string]any{"id": 9})
	f.json("PATCH", repoAPI+"/issues/comments/9", 200, map[string]any{"id": 9})
	f.json("PATCH", repoAPI+"/issues/comments/10", 404, nil)
	f.json("GET", repoAPI+"/issues/comments/3", 200, map[string]any{"id": 3, "body": "hi", "user": map[string]any{"login": "dev"}})
	c := f.client(t)
	login, err := c.BotLogin(t.Context())
	if err != nil || login != "kritika-bot" {
		t.Fatalf("BotLogin = %q, %v", login, err)
	}
	if _, _ = c.BotLogin(t.Context()); f.called("GET /api/v1/user") != 1 {
		t.Fatal("BotLogin must be cached")
	}
	comments, err := c.ListConversation(t.Context(), "acme", "widgets", 7)
	if err != nil || len(comments) != 3 || comments[0].ID != 1 || comments[2].Author != "dev" {
		t.Fatalf("ListConversation = %+v, %v; want oldest first", comments, err)
	}
	if id, err := c.FindComment(t.Context(), "acme", "widgets", 7, "kritika-bot", "<!-- kritika -->"); err != nil || id != 1 {
		t.Fatalf("FindComment = %d, %v; want the oldest match", id, err)
	}
	if id, _ := c.FindComment(t.Context(), "acme", "widgets", 7, "dev", "<!-- kritika -->"); id != 0 {
		t.Fatalf("FindComment by another author = %d", id)
	}
	if id, err := c.CreateComment(t.Context(), "acme", "widgets", 7, "hello"); err != nil || id != 9 {
		t.Fatalf("CreateComment = %d, %v", id, err)
	}
	if got := f.body("POST " + repoAPI + "/issues/7/comments")["body"]; got != "hello" {
		t.Fatalf("posted body = %v", got)
	}
	if err := c.UpdateComment(t.Context(), "acme", "widgets", 9, "edited"); err != nil {
		t.Fatal(err)
	}
	if err := c.UpdateComment(t.Context(), "acme", "widgets", 10, "x"); !errors.Is(err, forge.ErrNotFound) {
		t.Fatalf("editing a deleted comment = %v, want ErrNotFound", err)
	}
	cm, err := c.GetComment(t.Context(), "acme", "widgets", 1, 3, false)
	if err != nil || cm.Author != "dev" || cm.Inline {
		t.Fatalf("GetComment = %+v, %v", cm, err)
	}
}

// reviews is a pull request with two reviews: the bot's, holding two code
// comments, one resolved, and dev's reply to the first in that review, and
// dev's own single-comment review on the same line, a conversation of its
// own.
func reviews(f *fakeForge) {
	f.json("GET", "/api/v1/user", 200, map[string]any{"login": "kritika-bot"})
	f.json("GET", repoAPI+"/pulls/7/reviews", 200, []map[string]any{
		{"id": 100, "state": "COMMENT", "user": map[string]any{"login": "kritika-bot"}, "commit_id": "head1", "submitted_at": "2026-10-20T10:00:00Z"},
		{"id": 101, "state": "COMMENT", "user": map[string]any{"login": "dev"}, "commit_id": "head1", "submitted_at": "2026-10-20T11:00:00Z"},
	})
	f.json("GET", repoAPI+"/pulls/7/reviews/100/comments", 200, []map[string]any{
		{"id": 11, "pull_request_review_id": 100, "body": "b", "path": "b.go", "position": 5, "user": map[string]any{"login": "kritika-bot"}, "created_at": "2026-10-20T10:00:01Z"},
		{"id": 10, "pull_request_review_id": 100, "body": "a", "path": "a.go", "position": 3, "user": map[string]any{"login": "kritika-bot"}, "created_at": "2026-10-20T10:00:00Z",
			"resolver": map[string]any{"login": "dev"}},
		{"id": 13, "pull_request_review_id": 100, "body": "why?", "path": "a.go", "position": 3, "user": map[string]any{"login": "dev"}, "created_at": "2026-10-20T11:30:00Z"},
	})
	f.json("GET", repoAPI+"/pulls/7/reviews/101/comments", 200, []map[string]any{
		{"id": 12, "pull_request_review_id": 101, "body": "another", "path": "a.go", "position": 3, "user": map[string]any{"login": "dev"}, "created_at": "2026-10-20T11:00:00Z"},
		{"id": 14, "pull_request_review_id": 101, "body": "gone", "path": "a.go", "original_position": 8, "user": map[string]any{"login": "dev"}, "created_at": "2026-10-20T11:00:01Z"},
		{"id": 15, "pull_request_review_id": 101, "body": "span", "path": "a.go", "position": 20, "extra_lines_count": 2, "user": map[string]any{"login": "dev"}, "created_at": "2026-10-20T11:00:02Z"},
		{"id": 16, "pull_request_review_id": 101, "body": "longer span", "path": "a.go", "position": 20, "extra_lines_count": 4, "user": map[string]any{"login": "dev"}, "created_at": "2026-10-20T11:00:03Z"},
	})
}

func TestInlineComments(t *testing.T) {
	f := newFake(t)
	reviews(f)
	f.mux.HandleFunc("GET "+repoAPI+"/issues/comments/13", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	f.json("GET", repoAPI+"/pulls", 200, []map[string]any{{"number": 7, "updated_at": "2026-10-20T11:00:00Z", "user": map[string]any{"login": "x"}}})
	c := f.client(t)
	inline, err := c.ListInline(t.Context(), "acme", "widgets", 7)
	if err != nil || len(inline) != 7 || inline[0].ID != 10 || inline[2].ID != 12 || inline[6].ID != 13 || !inline[1].Inline || inline[1].Path != "b.go" || inline[1].Line != 5 {
		t.Fatalf("ListInline = %+v, %v; want oldest first across reviews", inline, err)
	}
	// dev's comment in the bot's review on a.go line 3 replies to the
	// bot's, the oldest of that conversation; dev's own review on the same
	// line is a conversation of its own, as are a comment on the old side
	// and two ranges from one line to different last lines.
	for i, want := range map[int]int64{0: 0, 1: 0, 2: 0, 3: 0, 4: 0, 5: 0, 6: 10} {
		if inline[i].InReplyTo != want {
			t.Fatalf("comment %d replies to %d, want %d; a conversation is one review's comments on one line of one side", inline[i].ID, inline[i].InReplyTo, want)
		}
	}
	// A reply in a code conversation arrives as a conversation comment,
	// which the conversation endpoint answers with 204: it is a code comment.
	cm, err := c.GetComment(t.Context(), "acme", "widgets", 1, 13, false)
	if err != nil || !cm.Inline || cm.Path != "a.go" || cm.Author != "dev" || cm.InReplyTo != 10 {
		t.Fatalf("GetComment of a code comment = %+v, %v", cm, err)
	}
	if cm, err = c.GetComment(t.Context(), "acme", "widgets", 1, 11, true); err != nil || cm.ID != 11 {
		t.Fatalf("GetComment inline = %+v, %v", cm, err)
	}
	if _, err := c.GetComment(t.Context(), "acme", "widgets", 1, 99, true); !errors.Is(err, forge.ErrNotFound) {
		t.Fatalf("an unknown code comment = %v, want ErrNotFound", err)
	}
	for id, want := range map[int64][2]bool{10: {true, true}, 11: {false, true}, 99: {false, false}} {
		resolved, found, err := c.ThreadResolved(t.Context(), "acme", "widgets", 7, id)
		if err != nil || resolved != want[0] || found != want[1] {
			t.Fatalf("ThreadResolved(%d) = %v %v %v, want %v", id, resolved, found, err, want)
		}
	}
	if _, err := c.ResolveThread(t.Context(), "acme", "widgets", 7, 10, true); !errors.Is(err, forge.ErrUnsupported) {
		t.Fatalf("ResolveThread = %v, want ErrUnsupported", err)
	}
}

func TestCreateReviewAndReply(t *testing.T) {
	f := newFake(t)
	f.json("GET", "/api/v1/user", 200, map[string]any{"login": "kritika-bot"})
	f.json("POST", repoAPI+"/pulls/7/reviews", 200, map[string]any{"id": 200})
	f.json("GET", repoAPI+"/pulls/7/reviews/200/comments", 200, []map[string]any{
		{"id": 21, "body": "second", "path": "b.go", "position": 9, "user": map[string]any{"login": "kritika-bot"}},
		{"id": 20, "body": "first", "path": "a.go", "position": 3, "user": map[string]any{"login": "kritika-bot"}},
	})
	c := f.client(t)
	ids, err := c.CreateReview(t.Context(), "acme", "widgets", 7, "head1", []forge.InlineComment{
		{Path: "a.go", Line: 3, Body: "first"}, {Path: "b.go", Line: 9, StartLine: 7, Body: "second"}, {Path: "c.go", Line: 1, Body: "lost"},
	})
	if err != nil || !slices.Equal(ids, []int64{20, 21, 0}) {
		t.Fatalf("CreateReview ids = %v, %v", ids, err)
	}
	body := f.body("POST " + repoAPI + "/pulls/7/reviews")
	comments := body["comments"].([]any)
	one, span := comments[0].(map[string]any), comments[1].(map[string]any)
	if body["event"] != "COMMENT" || body["commit_id"] != "head1" || len(comments) != 3 || one["new_position"] != float64(3) || one["extra_lines_count"] != nil ||
		span["new_position"] != float64(7) || span["extra_lines_count"] != float64(2) || span["path"] != "b.go" {
		t.Fatalf("review request = %v; a range starts at its first line and counts the lines after it", body)
	}
	if ids, err := c.CreateReview(t.Context(), "acme", "widgets", 7, "head1", nil); err != nil || len(ids) != 0 || f.called("POST "+repoAPI+"/pulls/7/reviews") != 1 {
		t.Fatalf("an empty review must post nothing: %v %v", ids, err)
	}
}

func TestReplyInline(t *testing.T) {
	f := newFake(t)
	reviews(f)
	f.json("POST", repoAPI+"/pulls/7/reviews/100/comments", 200, map[string]any{"id": 21, "pull_request_review_id": 100})
	c := f.client(t)
	// The reply to dev's question goes into the bot's review, the
	// conversation the question is in, on its line.
	id, err := c.ReplyInline(t.Context(), "acme", "widgets", 7, forge.Comment{ID: 13, Inline: true, Path: "a.go", Line: 3, InReplyTo: 10}, "answer")
	if err != nil || id != 21 {
		t.Fatalf("ReplyInline = %d, %v; want the posted comment's id", id, err)
	}
	reply := f.body("POST " + repoAPI + "/pulls/7/reviews/100/comments")
	if reply["new_position"] != float64(3) || reply["old_position"] != nil || reply["path"] != "a.go" || reply["body"] != "answer" {
		t.Fatalf("reply request = %v", reply)
	}
	// A reply to a comment on the old side, or on a range, sits where it
	// does.
	f.json("POST", repoAPI+"/pulls/7/reviews/101/comments", 200, map[string]any{"id": 22})
	if _, err := c.ReplyInline(t.Context(), "acme", "widgets", 7, forge.Comment{ID: 14, Inline: true}, "old side"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ReplyInline(t.Context(), "acme", "widgets", 7, forge.Comment{ID: 16, Inline: true}, "range"); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	sent := f.bodies["POST "+repoAPI+"/pulls/7/reviews/101/comments"]
	f.mu.Unlock()
	if len(sent) != 2 || !strings.Contains(sent[0], `"old_position":8`) || strings.Contains(sent[0], "new_position") ||
		!strings.Contains(sent[1], `"new_position":20`) || !strings.Contains(sent[1], `"extra_lines_count":4`) {
		t.Fatalf("replies = %q", sent)
	}
	if _, err := c.ReplyInline(t.Context(), "acme", "widgets", 7, forge.Comment{ID: 99, Inline: true}, "x"); !errors.Is(err, forge.ErrNotFound) {
		t.Fatalf("a reply to an unknown comment = %v, want ErrNotFound", err)
	}
}

func TestApprovals(t *testing.T) {
	f := newFake(t)
	f.json("GET", "/api/v1/user", 200, map[string]any{"login": "kritika-bot"})
	f.json("GET", repoAPI+"/pulls/7/reviews", 200, []map[string]any{
		{"id": 1, "state": "APPROVED", "user": map[string]any{"login": "kritika-bot"}, "commit_id": "old"},
		{"id": 2, "state": "APPROVED", "user": map[string]any{"login": "kritika-bot"}, "commit_id": "head1", "dismissed": true},
		{"id": 3, "state": "REQUEST_CHANGES", "user": map[string]any{"login": "dev"}, "commit_id": "head1"},
		{"id": 4, "state": "APPROVED", "user": map[string]any{"login": "dev"}, "commit_id": "head1"},
		{"id": 5, "state": "REQUEST_CHANGES", "user": map[string]any{"login": "lead"}, "commit_id": "head1", "dismissed": true},
	})
	f.json("POST", repoAPI+"/pulls/7/reviews", 200, map[string]any{"id": 6})
	f.json("POST", repoAPI+"/pulls/7/reviews/1/dismissals", 200, map[string]any{"id": 1})
	c := f.client(t)
	// dev's latest standing review approves and lead's request was
	// dismissed, so no change request stands.
	if requested, err := c.ChangesRequested(t.Context(), "acme", "widgets", 7); err != nil || requested {
		t.Fatalf("ChangesRequested = %v, %v", requested, err)
	}
	// The bot's approval of head1 was dismissed, so a new one is posted.
	posted, err := c.Approve(t.Context(), "acme", "widgets", 7, "head1", "LGTM")
	if err != nil || !posted {
		t.Fatalf("Approve = %v, %v", posted, err)
	}
	if body := f.body("POST " + repoAPI + "/pulls/7/reviews"); body["event"] != "APPROVED" || body["body"] != "LGTM" || body["commit_id"] != "head1" {
		t.Fatalf("approval request = %v", body)
	}
	if posted, _ := c.Approve(t.Context(), "acme", "widgets", 7, "old", "LGTM"); posted {
		t.Fatal("an approval of the head that stands must not be repeated")
	}
	// The one standing approval, of old, is dismissed; the dismissed one is not.
	if n, err := c.DismissApprovals(t.Context(), "acme", "widgets", 7, "stale"); err != nil || n != 1 {
		t.Fatalf("DismissApprovals = %d, %v", n, err)
	}
	if body := f.body("POST " + repoAPI + "/pulls/7/reviews/1/dismissals"); body["message"] != "stale" {
		t.Fatalf("dismissal = %v", body)
	}
}

func TestStatusAndPermission(t *testing.T) {
	f := newFake(t)
	f.json("POST", repoAPI+"/statuses/head1", 201, map[string]any{"id": 1})
	f.json("GET", repoAPI+"/collaborators/owner/permission", 200, map[string]any{"permission": "owner"})
	f.json("GET", repoAPI+"/collaborators/dev/permission", 200, map[string]any{"permission": "write"})
	f.json("GET", repoAPI+"/collaborators/odd/permission", 200, map[string]any{"permission": "maintainer"})
	f.json("GET", repoAPI+"/collaborators/anyone/permission", 403, map[string]any{"message": "admins only"})
	c := f.client(t)
	long := strings.Repeat("x", 200)
	if err := c.SetStatus(t.Context(), "acme", "widgets", "head1", forge.StatusSuccess, long); err != nil {
		t.Fatal(err)
	}
	body := f.body("POST " + repoAPI + "/statuses/head1")
	if body["state"] != "success" || body["context"] != forge.StatusContext || len([]rune(body["description"].(string))) != forge.MaxStatusDescription {
		t.Fatalf("status = %v", body)
	}
	for login, want := range map[string]forge.Permission{"owner": forge.PermissionAdmin, "dev": forge.PermissionWrite} {
		if got, err := c.Permission(t.Context(), "acme", "widgets", login); err != nil || got != want {
			t.Fatalf("Permission(%s) = %q, %v", login, got, err)
		}
	}
	if _, err := c.Permission(t.Context(), "acme", "widgets", "odd"); err == nil {
		t.Fatal("an unknown level must be an error")
	}
	if _, err := c.Permission(t.Context(), "acme", "widgets", "anyone"); err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("a refused lookup = %v", err)
	}
}

func TestReactions(t *testing.T) {
	f := newFake(t)
	f.json("GET", "/api/v1/user", 200, map[string]any{"login": "kritika-bot"})
	f.json("POST", repoAPI+"/issues/comments/5/reactions", 201, map[string]any{"content": "eyes"})
	f.json("DELETE", repoAPI+"/issues/comments/5/reactions", 200, nil)
	f.json("POST", repoAPI+"/issues/7/reactions", 201, map[string]any{"content": "+1"})
	f.json("DELETE", repoAPI+"/issues/7/reactions", 200, nil)
	f.json("GET", repoAPI+"/issues/comments/5/reactions", 200, []map[string]any{
		{"content": "eyes", "user": map[string]any{"login": "dev"}},
		{"content": "confused", "user": map[string]any{"login": "kritika-bot"}},
	})
	f.json("GET", repoAPI+"/issues/7/reactions", 200, []map[string]any{{"content": "+1", "user": map[string]any{"login": "kritika-bot"}}})
	c := f.client(t)
	cm := forge.Comment{ID: 5, Inline: true}
	id, err := c.React(t.Context(), "acme", "widgets", cm, forge.ReactionEyes)
	if err != nil || id == 0 {
		t.Fatalf("React = %d, %v", id, err)
	}
	if body := f.body("POST " + repoAPI + "/issues/comments/5/reactions"); body["content"] != "eyes" {
		t.Fatalf("reaction = %v", body)
	}
	if err := c.Unreact(t.Context(), "acme", "widgets", cm, id); err != nil {
		t.Fatal(err)
	}
	if body := f.body("DELETE " + repoAPI + "/issues/comments/5/reactions"); body["content"] != "eyes" {
		t.Fatalf("the reaction removed is the one the id stands for: %v", body)
	}
	if _, err := c.React(t.Context(), "acme", "widgets", cm, "rocket"); err == nil {
		t.Fatal("a reaction kritika does not leave has no id")
	}
	if err := c.Unreact(t.Context(), "acme", "widgets", cm, 99); err == nil {
		t.Fatal("an unknown reaction id must be an error")
	}
	if got, err := c.Reaction(t.Context(), "acme", "widgets", cm, forge.ReactionEyes); err != nil || got != 0 {
		t.Fatalf("Reaction(eyes) = %d, %v; dev's eyes are not the bot's", got, err)
	}
	want, _ := reactionID(forge.ReactionFailed)
	if got, _ := c.Reaction(t.Context(), "acme", "widgets", cm, forge.ReactionFailed); got != want {
		t.Fatalf("Reaction(confused) = %d, want %d", got, want)
	}
	prID, err := c.ReactToPullRequest(t.Context(), "acme", "widgets", 7, forge.ReactionDone)
	if err != nil || prID == 0 {
		t.Fatalf("ReactToPullRequest = %d, %v", prID, err)
	}
	if got, _ := c.PullRequestReaction(t.Context(), "acme", "widgets", 7, forge.ReactionDone); got != prID {
		t.Fatalf("PullRequestReaction = %d, want %d", got, prID)
	}
	if err := c.UnreactToPullRequest(t.Context(), "acme", "widgets", 7, prID); err != nil {
		t.Fatal(err)
	}
}

func TestListOpenPullRequests(t *testing.T) {
	f := newFake(t)
	f.mux.HandleFunc("GET "+repoAPI+"/pulls", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("state") != "open" || q.Get("sort") != "recentupdate" || q.Get("limit") != fmt.Sprint(pageSize) {
			t.Errorf("query = %v", q)
		}
		var page []map[string]any
		switch q.Get("page") {
		case "1":
			for i := range pageSize {
				page = append(page, map[string]any{"number": 100 - i, "updated_at": time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC).Add(-time.Duration(i) * time.Minute).Format(time.RFC3339),
					"user": map[string]any{"login": "x"}})
			}
		case "2":
			page = []map[string]any{{"number": 1, "updated_at": "2026-10-19T00:00:00Z", "user": map[string]any{"login": "x"}}}
		}
		_ = json.NewEncoder(w).Encode(page)
	})
	c := f.client(t)
	all, err := c.ListOpenPullRequests(t.Context(), "acme", "widgets", time.Time{})
	if err != nil || len(all) != pageSize+1 || all[0].Number != 100 || all[pageSize].Number != 1 {
		t.Fatalf("ListOpenPullRequests(all) = %d, %v", len(all), err)
	}
	since := time.Date(2026, 10, 20, 11, 50, 0, 0, time.UTC)
	recent, err := c.ListOpenPullRequests(t.Context(), "acme", "widgets", since)
	if err != nil || len(recent) != 11 || f.called("GET "+repoAPI+"/pulls") != 3 {
		t.Fatalf("ListOpenPullRequests(since) = %d, %v, calls %d; want the walk to stop at the first older one", len(recent), err, f.called("GET "+repoAPI+"/pulls"))
	}
}

func TestRepositories(t *testing.T) {
	f := newFake(t)
	// The instance caps a page at 2, under what is asked, and says so
	// with a Link header on every page but the last: a short page is not
	// the end.
	f.mux.HandleFunc("GET /api/v1/users/acme/repos", func(w http.ResponseWriter, r *http.Request) {
		var page []map[string]any
		switch r.URL.Query().Get("page") {
		case "1":
			w.Header().Set("Link", `<`+f.srv.URL+`/api/v1/users/acme/repos?limit=2&page=2>; rel="next",<`+f.srv.URL+`/api/v1/users/acme/repos?limit=2&page=2>; rel="last"`)
			page = []map[string]any{
				{"full_name": "acme/widgets", "default_branch": "main", "private": true},
				{"full_name": "acme/old", "default_branch": "main", "archived": true, "internal": true},
			}
		case "2":
			page = []map[string]any{{"full_name": "acme/fork", "default_branch": "main", "fork": true}}
		}
		_ = json.NewEncoder(w).Encode(page)
	})
	// A total count alone paces the walk by pages, measured by the largest
	// page seen: the instance caps a page at 2 here, under what is asked,
	// and leaves one page empty of what the bot cannot see; the walk reads
	// past the emptied page and ends once the pages read cover the count.
	f.mux.HandleFunc("GET /api/v1/users/dev/repos", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Total-Count", "6")
		page := []map[string]any{}
		switch r.URL.Query().Get("page") {
		case "1":
			page = []map[string]any{{"full_name": "dev/tools", "default_branch": "trunk"}, {"full_name": "dev/more", "default_branch": "trunk"}}
		case "3":
			page = []map[string]any{{"full_name": "dev/last", "default_branch": "trunk"}}
		}
		_ = json.NewEncoder(w).Encode(page)
	})
	f.json("GET", "/api/v1/users/nobody/repos", 404, nil)
	c := f.client(t)
	repos, err := c.Repositories(t.Context(), "acme")
	if err != nil || len(repos) != 3 || repos[0].Visibility != "private" || repos[1].Visibility != "internal" || !repos[1].Archived ||
		repos[2].Visibility != "public" || !repos[2].Fork {
		t.Fatalf("Repositories(acme) = %+v, %v", repos, err)
	}
	if repos, err = c.Repositories(t.Context(), "dev"); err != nil || len(repos) != 3 || repos[2].FullName != "dev/last" || f.called("GET /api/v1/users/dev/repos") != 3 {
		t.Fatalf("Repositories(dev) = %+v, %v, %d calls; want an emptied page read past and the walk to end at the count", repos, err, f.called("GET /api/v1/users/dev/repos"))
	}
	if _, err := c.Repositories(t.Context(), "nobody"); !errors.Is(err, forge.ErrNotFound) {
		t.Fatalf("Repositories(nobody) = %v, want ErrNotFound", err)
	}
}

func TestErrorsCarryTheStatus(t *testing.T) {
	f := newFake(t)
	f.mux.HandleFunc("GET "+repoAPI+"/issues/7/comments", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, strings.Repeat("<html>", 200))
	})
	c := f.client(t)
	_, err := c.ListConversation(t.Context(), "acme", "widgets", 7)
	if err == nil || !strings.Contains(err.Error(), "500") || len(err.Error()) > maxErrorBody+200 {
		t.Fatalf("err = %v; want the status and a bounded body", err)
	}
	if _, ok := errors.AsType[*apiError](err); !ok || errors.Is(err, forge.ErrNotFound) {
		t.Fatalf("err = %v; a 500 is no ErrNotFound", err)
	}
}

// TestPullRequestDiffCatchesAPush: a push between the read of the pull
// request and of its diff leaves the diff the new head's, which the
// second read catches.
func TestPullRequestDiffCatchesAPush(t *testing.T) {
	f := newFake(t)
	heads := []string{"h1", "h2"}
	f.mux.HandleFunc("GET "+repoAPI+"/pulls/10", func(w http.ResponseWriter, r *http.Request) {
		head := heads[0]
		if len(heads) > 1 {
			heads = heads[1:]
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"number": 10, "merge_base": "b", "head": map[string]any{"sha": head}, "user": map[string]any{"login": "x"}})
	})
	f.mux.HandleFunc("GET "+repoAPI+"/pulls/10.diff", func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "diff --git a/x b/x\n") })
	c := f.client(t)
	if _, err := c.PullRequestDiff(t.Context(), "acme", "widgets", 10, "b", "h1"); err == nil || !strings.Contains(err.Error(), "moved") {
		t.Fatalf("PullRequestDiff across a push = %v, want a refusal", err)
	}
	if _, err := c.PullRequestDiff(t.Context(), "acme", "widgets", 10, "other", "h2"); err == nil || !strings.Contains(err.Error(), "merges from") {
		t.Fatalf("PullRequestDiff with another merge base = %v, want a refusal", err)
	}
}

// TestBearerIsReused: one token serves the calls within its life and is
// replaced a minute before it ends.
func TestBearerIsReused(t *testing.T) {
	f := newFake(t)
	f.json("GET", "/api/v1/user", 200, map[string]any{"login": "kritika-bot"})
	c := f.client(t)
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	c.now = func() time.Time { return now }
	first, err := c.bearer()
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(tokenTTL - 2*time.Minute)
	if again, _ := c.bearer(); again != first {
		t.Fatal("a token with two minutes left is kept")
	}
	now = now.Add(time.Minute)
	if again, _ := c.bearer(); again == first {
		t.Fatal("a token with a minute left is replaced")
	}
	if _, err := c.BotLogin(t.Context()); err != nil {
		t.Fatal(err)
	}
}
