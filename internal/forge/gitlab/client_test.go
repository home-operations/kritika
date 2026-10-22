package gitlab

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	gl "gitlab.com/gitlab-org/api/client-go/v3"

	"github.com/home-operations/kritika/internal/forge"
)

var _ forge.Client = (*Client)(nil)

// fakeLab is enough of a GitLab instance for the client: a mux of the
// routes a test registers, and a log of what was called.
type fakeLab struct {
	t   *testing.T
	mux *http.ServeMux
	srv *httptest.Server

	mu     sync.Mutex
	calls  []string
	bodies map[string][]string
}

func newFake(t *testing.T) *fakeLab {
	t.Helper()
	f := &fakeLab{t: t, mux: http.NewServeMux(), bodies: map[string][]string{}}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("PRIVATE-TOKEN") != "glpat-api" {
			t.Errorf("%s %s: PRIVATE-TOKEN = %q", r.Method, r.URL.Path, r.Header.Get("PRIVATE-TOKEN"))
		}
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		key := r.Method + " " + r.URL.EscapedPath()
		f.calls = append(f.calls, key)
		f.bodies[key] = append(f.bodies[key], string(body))
		f.mu.Unlock()
		r.Body = io.NopCloser(strings.NewReader(string(body)))
		f.mux.ServeHTTP(w, r)
	}))
	t.Cleanup(f.srv.Close)
	f.json("GET", "/api/v4/user", 200, map[string]any{"id": 7, "username": "kritika", "bot": true})
	f.json("GET", "/api/v4/users/7", 200, map[string]any{"id": 7, "username": "kritika", "bot": true})
	return f
}

func (f *fakeLab) client(t *testing.T) *Client {
	t.Helper()
	c, err := NewClient(f.srv.URL, "glpat-api", "glpat-read", nil)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// json registers a route answering v, or a status alone.
func (f *fakeLab) json(method, path string, status int, v any) {
	f.mux.HandleFunc(method+" "+path, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if v != nil {
			_ = json.NewEncoder(w).Encode(v)
		}
	})
}

func (f *fakeLab) called(key string) int {
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

// body is the i-th request body sent to key.
func (f *fakeLab) body(key string, i int) map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out map[string]any
	if err := json.Unmarshal([]byte(f.bodies[key][i]), &out); err != nil {
		f.t.Fatalf("body of %s: %v", key, err)
	}
	return out
}

// The project path is one escaped segment of the API path.
const repoAPI = "/api/v4/projects/acme%2Fwidgets"

func TestNewClient(t *testing.T) {
	for _, tt := range []struct{ host, web string }{
		{"gitlab.example.com", "https://gitlab.example.com"},
		{"http://127.0.0.1:8080/", "http://127.0.0.1:8080"},
	} {
		c, err := NewClient(tt.host, "tok", "", nil)
		if err != nil || c.web != tt.web || c.runnerToken != "tok" {
			t.Fatalf("NewClient(%q) = %+v, %v", tt.host, c, err)
		}
	}
	for _, tt := range []struct{ host, token string }{{"", "tok"}, {"://", "tok"}, {"gitlab.example.com", ""}} {
		if _, err := NewClient(tt.host, tt.token, "", nil); err == nil {
			t.Fatalf("NewClient(%q, %q) must fail", tt.host, tt.token)
		}
	}
}

func TestURLs(t *testing.T) {
	c, _ := NewClient("gitlab.example.com", "tok", "read", nil)
	if got := c.CloneURL("acme", "sub/widgets"); got != "https://gitlab.example.com/acme/sub/widgets.git" {
		t.Errorf("CloneURL = %s", got)
	}
	if got := c.FileURL("acme", "widgets", "abc", "dir/a b.go", 3, 5); got != "https://gitlab.example.com/acme/widgets/-/blob/abc/dir/a%20b.go#L3-5" {
		t.Errorf("FileURL = %s", got)
	}
	if got := c.FileURL("acme", "widgets", "abc", "a.go", 3, 0); got != "https://gitlab.example.com/acme/widgets/-/blob/abc/a.go#L3" {
		t.Errorf("FileURL = %s", got)
	}
	if got := c.CommitURL("acme", "widgets", "abc"); got != "https://gitlab.example.com/acme/widgets/-/commit/abc" {
		t.Errorf("CommitURL = %s", got)
	}
	if got := c.ThreadURL("acme", "widgets", 4, 99); got != "https://gitlab.example.com/acme/widgets/-/merge_requests/4#note_99" {
		t.Errorf("ThreadURL = %s", got)
	}
	if tok, _ := c.GitToken(context.Background(), "acme/widgets"); tok != "read" {
		t.Errorf("GitToken = %s", tok)
	}
}

// versions registers two diff versions of !4, the current head and one
// pushed over, and the detail of the latter.
// mergeRequest registers !4 as it stands, at head1.
func mergeRequest(f *fakeLab) {
	f.json("GET", repoAPI+"/merge_requests/4", 200, map[string]any{
		"iid": 4, "state": "opened", "sha": "head1", "diff_refs": map[string]any{"base_sha": "base1", "start_sha": "start1", "head_sha": "head1"},
	})
}

func versions(f *fakeLab) {
	mergeRequest(f)
	f.json("GET", repoAPI+"/merge_requests/4/versions", 200, []map[string]any{
		{"id": 2, "head_commit_sha": "head2", "base_commit_sha": "base2", "start_commit_sha": "start2", "state": "collected"},
		{"id": 1, "head_commit_sha": "head1", "base_commit_sha": "base1", "start_commit_sha": "start1", "state": "collected"},
	})
	f.json("GET", repoAPI+"/merge_requests/4/versions/1", 200, map[string]any{
		"id": 1, "head_commit_sha": "head1", "base_commit_sha": "base1", "start_commit_sha": "start1", "state": "collected",
		"diffs": []map[string]any{
			{"old_path": "a.go", "new_path": "a.go", "a_mode": "100644", "b_mode": "100644",
				"diff": "--- a/a.go\n+++ b/a.go\n@@ -1,2 +1,3 @@\n x\n+y\n z\n"},
			{"old_path": "", "new_path": "b.go", "b_mode": "100644", "new_file": true, "diff": "@@ -0,0 +1 @@\n+b\n"},
		},
	})
}

func TestMergeBaseAndDiff(t *testing.T) {
	f := newFake(t)
	versions(f)
	c := f.client(t)
	ctx := context.Background()
	if base, err := c.MergeBase(ctx, "acme", "widgets", 4, "main", "head1"); err != nil || base != "base1" {
		t.Fatalf("MergeBase = %q, %v", base, err)
	}
	if _, err := c.MergeBase(ctx, "acme", "widgets", 4, "main", "head9"); err == nil {
		t.Fatal("a head no version has must fail")
	}
	diff, err := c.PullRequestDiff(ctx, "acme", "widgets", 4, "base1", "head1")
	if err != nil {
		t.Fatal(err)
	}
	want := "diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n@@ -1,2 +1,3 @@\n x\n+y\n z\n" +
		"diff --git a/b.go b/b.go\nnew file mode 100644\n--- /dev/null\n+++ b/b.go\n@@ -0,0 +1 @@\n+b\n"
	if diff != want {
		t.Fatalf("diff =\n%s\nwant\n%s", diff, want)
	}
	if _, err := c.PullRequestDiff(ctx, "acme", "widgets", 4, "base2", "head1"); err == nil || !strings.Contains(err.Error(), "merges from base1") {
		t.Fatalf("a base the version is not from = %v", err)
	}
}

func TestPullRequestDiffOverflow(t *testing.T) {
	f := newFake(t)
	f.json("GET", repoAPI+"/merge_requests/4/versions", 200, []map[string]any{{"id": 1, "head_commit_sha": "h", "base_commit_sha": "b"}})
	f.json("GET", repoAPI+"/merge_requests/4/versions/1", 200, map[string]any{"id": 1, "state": "overflow"})
	f.json("GET", repoAPI+"/merge_requests/5/versions", 200, []map[string]any{{"id": 2, "head_commit_sha": "h", "base_commit_sha": "b"}})
	f.json("GET", repoAPI+"/merge_requests/5/versions/2", 200, map[string]any{"id": 2, "state": "collected", "diffs": []map[string]any{
		{"new_path": "a.go", "diff": "@@ -1 +1 @@\n-a\n+b\n"}, {"new_path": "big.bin", "too_large": true},
	}})
	c := f.client(t)
	if _, err := c.PullRequestDiff(context.Background(), "acme", "widgets", 4, "b", "h"); err == nil || !strings.Contains(err.Error(), "diff limits") {
		t.Fatalf("overflow = %v", err)
	}
	if _, err := c.PullRequestDiff(context.Background(), "acme", "widgets", 5, "b", "h"); err == nil || !strings.Contains(err.Error(), "leaves out big.bin") {
		t.Fatalf("a file left out = %v", err)
	}
}

func TestDiscussionResolved(t *testing.T) {
	note := func(resolvable, resolved bool) *gl.Note { return &gl.Note{Resolvable: resolvable, Resolved: resolved} }
	for name, tt := range map[string]struct {
		notes []*gl.Note
		want  bool
	}{
		"none resolvable":          {[]*gl.Note{note(false, false)}, false},
		"root resolved":            {[]*gl.Note{note(true, true)}, true},
		"root resolved reply open": {[]*gl.Note{note(true, true), note(true, false)}, false},
		"all resolved":             {[]*gl.Note{note(true, true), note(true, true), note(false, false)}, true},
	} {
		if got := discussionResolved(&gl.Discussion{Notes: tt.notes}); got != tt.want {
			t.Errorf("%s: resolved = %v, want %v", name, got, tt.want)
		}
	}
}

func TestInlineBody(t *testing.T) {
	body := "Use y.\n```suggestion\ny := 1\n```\n"
	if got := inlineBody(forge.InlineComment{Line: 5, Body: body}); got != body {
		t.Errorf("a line alone keeps the fence: %q", got)
	}
	if got := inlineBody(forge.InlineComment{StartLine: 3, Line: 5, Body: body}); got != "Use y.\n```suggestion:-2+0\ny := 1\n```\n" {
		t.Errorf("a range widens the fence: %q", got)
	}
}

func TestCreateReview(t *testing.T) {
	f := newFake(t)
	versions(f)
	drafts := 0
	f.mux.HandleFunc("POST "+repoAPI+"/merge_requests/4/draft_notes", func(w http.ResponseWriter, r *http.Request) {
		drafts++
		w.WriteHeader(201)
		_ = json.NewEncoder(w).Encode(map[string]any{"id": 100 + drafts})
	})
	// A draft left by a review that failed before is discarded first.
	f.json("GET", repoAPI+"/merge_requests/4/draft_notes", 200, []map[string]any{{"id": 50, "note": "stale"}})
	f.json("DELETE", repoAPI+"/merge_requests/4/draft_notes/50", 204, nil)
	published := false
	f.mux.HandleFunc("POST "+repoAPI+"/merge_requests/4/draft_notes/bulk_publish", func(w http.ResponseWriter, r *http.Request) {
		published = true
		w.WriteHeader(204)
	})
	// An earlier review's finding with the same body is not the new one's.
	f.mux.HandleFunc("GET "+repoAPI+"/merge_requests/4/discussions", func(w http.ResponseWriter, r *http.Request) {
		discussions := []map[string]any{
			{"id": "d0", "individual_note": false, "notes": []map[string]any{
				{"id": 10, "type": "DiffNote", "body": "Added line.", "author": map[string]any{"id": 7, "username": "kritika"},
					"position": map[string]any{"new_path": "a.go", "new_line": 2}, "created_at": "2026-10-22T09:00:00Z"},
			}},
		}
		if published {
			discussions = append(discussions,
				map[string]any{"id": "d1", "individual_note": false, "notes": []map[string]any{
					{"id": 11, "type": "DiffNote", "body": "Added line.", "author": map[string]any{"id": 7, "username": "kritika"},
						"position": map[string]any{"new_path": "a.go", "new_line": 2}, "created_at": "2026-10-22T10:00:00Z"},
				}},
				map[string]any{"id": "d2", "individual_note": false, "notes": []map[string]any{
					{"id": 12, "type": "DiffNote", "body": "Context line.\n```suggestion:-1+0\nzz\n```", "author": map[string]any{"id": 7, "username": "kritika"},
						"position": map[string]any{"new_path": "a.go", "new_line": 3}, "created_at": "2026-10-22T10:00:01Z"},
				}})
		}
		_ = json.NewEncoder(w).Encode(discussions)
	})
	c := f.client(t)
	ids, err := c.CreateReview(context.Background(), "acme", "widgets", 4, "head1", []forge.InlineComment{
		{Path: "a.go", Line: 2, Body: "Added line."},
		{Path: "a.go", StartLine: 2, Line: 3, Body: "Context line.\n```suggestion\nzz\n```\n"},
		{Path: "c.go", Line: 1, Body: "Not in the diff."},
	})
	if err != nil || !slices.Equal(ids, []int64{11, 12, 0}) {
		t.Fatalf("ids = %v, %v", ids, err)
	}
	key := "POST " + repoAPI + "/merge_requests/4/draft_notes"
	added := f.body(key, 0)["position"].(map[string]any)
	if added["new_line"] != 2.0 || added["old_line"] != nil || added["base_sha"] != "base1" || added["head_sha"] != "head1" || added["start_sha"] != "start1" || added["position_type"] != "text" {
		t.Errorf("an added line's position = %v", added)
	}
	ctxLine := f.body(key, 1)
	if pos := ctxLine["position"].(map[string]any); pos["new_line"] != 3.0 || pos["old_line"] != 2.0 {
		t.Errorf("a context line names both sides: %v", pos)
	}
	if ctxLine["note"] != "Context line.\n```suggestion:-1+0\nzz\n```\n" {
		t.Errorf("note = %q", ctxLine["note"])
	}
	if pos := f.body(key, 2)["position"].(map[string]any); pos["new_line"] != 1.0 || pos["old_line"] != nil {
		t.Errorf("a line outside the diff is sent as it is: %v", pos)
	}
	if f.called("POST "+repoAPI+"/merge_requests/4/draft_notes/bulk_publish") != 1 {
		t.Error("the drafts must be published once")
	}
	if f.called("DELETE "+repoAPI+"/merge_requests/4/draft_notes/50") != 1 {
		t.Error("the stale draft must be discarded")
	}
}

func TestCreateReviewDiscardsDraftsOnFailure(t *testing.T) {
	f := newFake(t)
	versions(f)
	f.json("GET", repoAPI+"/merge_requests/4/discussions", 200, []map[string]any{})
	f.json("GET", repoAPI+"/merge_requests/4/draft_notes", 200, []map[string]any{})
	f.json("POST", repoAPI+"/merge_requests/4/draft_notes", 201, map[string]any{"id": 101})
	f.json("POST", repoAPI+"/merge_requests/4/draft_notes/bulk_publish", 422, map[string]any{"message": "boom"})
	f.json("DELETE", repoAPI+"/merge_requests/4/draft_notes/101", 204, nil)
	ids, err := f.client(t).CreateReview(context.Background(), "acme", "widgets", 4, "head1", []forge.InlineComment{{Path: "a.go", Line: 2, Body: "x"}})
	if err == nil || ids != nil {
		t.Fatalf("ids = %v, err = %v", ids, err)
	}
	if f.called("DELETE "+repoAPI+"/merge_requests/4/draft_notes/101") != 1 {
		t.Error("the draft must be discarded")
	}
}

// discussions registers the notes of !4: a conversation note, a code
// thread with a reply, and a system note.
func discussions(f *fakeLab) {
	mergeRequest(f)
	f.json("GET", repoAPI+"/merge_requests/4/discussions", 200, []map[string]any{
		{"id": "d0", "individual_note": true, "notes": []map[string]any{
			{"id": 20, "body": "assigned to @x", "system": true, "author": map[string]any{"id": 1, "username": "ann"}, "created_at": "2026-10-22T09:00:00Z"},
		}},
		{"id": "d1", "individual_note": true, "notes": []map[string]any{
			{"id": 21, "body": "<!-- kritika -->\nSummary", "author": map[string]any{"id": 7, "username": "kritika"}, "created_at": "2026-10-22T10:00:00Z"},
		}},
		{"id": "d2", "individual_note": false, "notes": []map[string]any{
			{"id": 22, "type": "DiffNote", "body": "Finding", "resolvable": true, "resolved": false, "author": map[string]any{"id": 7, "username": "kritika"},
				"position": map[string]any{"new_path": "a.go", "new_line": 2, "base_sha": "base1", "start_sha": "start1", "head_sha": "head1"}, "created_at": "2026-10-22T10:00:01Z"},
			{"id": 23, "type": "DiffNote", "body": "@kritika why?", "author": map[string]any{"id": 1, "username": "ann"},
				"position": map[string]any{"new_path": "a.go", "new_line": 2, "base_sha": "base1", "start_sha": "start1", "head_sha": "head0"}, "created_at": "2026-10-22T10:05:00Z"},
		}},
	})
}

func TestComments(t *testing.T) {
	f := newFake(t)
	discussions(f)
	f.json("GET", "/api/v4/users/1", 200, map[string]any{"id": 1, "username": "ann", "bot": false})
	c := f.client(t)
	ctx := context.Background()
	conv, err := c.ListConversation(ctx, "acme", "widgets", 4)
	if err != nil || len(conv) != 1 || conv[0].ID != 21 || conv[0].Number != 4 || !conv[0].AuthorIsBot || conv[0].Inline {
		t.Fatalf("conversation = %+v, %v", conv, err)
	}
	inline, err := c.ListInline(ctx, "acme", "widgets", 4)
	if err != nil || len(inline) != 2 {
		t.Fatalf("inline = %+v, %v", inline, err)
	}
	if root := inline[0]; root.ID != 22 || !root.Inline || root.Path != "a.go" || root.Line != 2 || root.InReplyTo != 0 || root.Outdated {
		t.Errorf("root = %+v", root)
	}
	if reply := inline[1]; reply.ID != 23 || reply.InReplyTo != 22 || reply.Author != "ann" || reply.AuthorIsBot || !reply.Outdated {
		t.Errorf("reply = %+v", reply)
	}
	if id, err := c.FindComment(ctx, "acme", "widgets", 4, "kritika", "<!-- kritika -->"); err != nil || id != 21 {
		t.Errorf("FindComment = %d, %v", id, err)
	}
	// A note read once is addressed under its merge request from then on.
	cm, err := c.GetComment(ctx, "acme", "widgets", 4, 23, true)
	if err != nil || cm.InReplyTo != 22 {
		t.Fatalf("GetComment = %+v, %v", cm, err)
	}
	// A note asked for as the wrong kind is still found, as what it is.
	if cm, err := c.GetComment(ctx, "acme", "widgets", 4, 23, false); err != nil || !cm.Inline {
		t.Fatalf("GetComment as a conversation note = %+v, %v", cm, err)
	}
	if cm, err := c.GetComment(ctx, "acme", "widgets", 4, 21, true); err != nil || cm.Inline {
		t.Fatalf("GetComment as a diff note = %+v, %v", cm, err)
	}
	f.json("PUT", repoAPI+"/merge_requests/4/notes/21", 200, map[string]any{"id": 21})
	if err := c.UpdateComment(ctx, "acme", "widgets", 21, "new"); err != nil || f.body("PUT "+repoAPI+"/merge_requests/4/notes/21", 0)["body"] != "new" {
		t.Fatalf("UpdateComment: %v", err)
	}
}

func TestThreads(t *testing.T) {
	f := newFake(t)
	discussions(f)
	f.json("GET", "/api/v4/users/1", 200, map[string]any{"id": 1, "username": "ann", "bot": false})
	c := f.client(t)
	ctx := context.Background()
	inline, err := c.ListInline(ctx, "acme", "widgets", 4)
	if err != nil || len(inline) != 2 {
		t.Fatalf("inline = %+v, %v", inline, err)
	}
	if resolved, found, err := c.ThreadResolved(ctx, "acme", "widgets", 4, 22); err != nil || resolved || !found {
		t.Errorf("ThreadResolved = %v, %v, %v", resolved, found, err)
	}
	if _, found, _ := c.ThreadResolved(ctx, "acme", "widgets", 4, 23); found {
		t.Error("a reply opens no thread")
	}
	f.json("POST", repoAPI+"/merge_requests/4/discussions/d2/notes", 201, map[string]any{"id": 24})
	if id, err := c.ReplyInline(ctx, "acme", "widgets", 4, inline[1], "Because."); err != nil || id != 24 {
		t.Fatalf("ReplyInline = %d, %v", id, err)
	}
	// The thread has ann's reply in it, so it is hers to resolve unless asked.
	f.json("PUT", repoAPI+"/merge_requests/4/discussions/d2", 200, map[string]any{"id": "d2"})
	if done, err := c.ResolveThread(ctx, "acme", "widgets", 4, 22, true); err != nil || done {
		t.Errorf("ResolveThread(onlyOwn) = %v, %v", done, err)
	}
	if done, err := c.ResolveThread(ctx, "acme", "widgets", 4, 22, false); err != nil || !done || f.body("PUT "+repoAPI+"/merge_requests/4/discussions/d2", 0)["resolved"] != true {
		t.Errorf("ResolveThread = %v, %v", done, err)
	}
}

// TestGetCommentWalksOpenPullRequests: a note never read is found under
// the open merge requests.
func TestUpdateCommentWalksOpenPullRequests(t *testing.T) {
	f := newFake(t)
	f.json("GET", repoAPI, 200, map[string]any{"default_branch": "main"})
	f.json("GET", repoAPI+"/merge_requests", 200, []map[string]any{{"iid": 4, "state": "opened", "author": map[string]any{"id": 7, "username": "kritika[bot]"}}})
	f.json("PUT", repoAPI+"/merge_requests/4/notes/21", 200, map[string]any{"id": 21})
	discussions(f)
	if err := f.client(t).UpdateComment(context.Background(), "acme", "widgets", 21, "new"); err != nil {
		t.Fatalf("UpdateComment = %v", err)
	}
	if err := f.client(t).UpdateComment(context.Background(), "acme", "widgets", 99, "new"); !errors.Is(err, forge.ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
}

// TestGetCommentOnAClosedMergeRequest: a note is read under the merge
// request it is on, open or not, with no walk of the open ones.
func TestGetCommentOnAClosedMergeRequest(t *testing.T) {
	f := newFake(t)
	discussions(f)
	cm, err := f.client(t).GetComment(context.Background(), "acme", "widgets", 4, 21, false)
	if err != nil || cm.ID != 21 || f.called("GET "+repoAPI+"/merge_requests") != 0 {
		t.Fatalf("GetComment = %+v, %v", cm, err)
	}
	if _, err := f.client(t).GetComment(context.Background(), "acme", "widgets", 4, 99, false); !errors.Is(err, forge.ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
}

func TestApprovals(t *testing.T) {
	f := newFake(t)
	approved := []map[string]any{}
	f.mux.HandleFunc("GET "+repoAPI+"/merge_requests/4/approvals", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"approved_by": approved})
	})
	f.json("POST", repoAPI+"/merge_requests/4/approve", 201, map[string]any{})
	f.json("POST", repoAPI+"/merge_requests/4/unapprove", 201, nil)
	f.json("POST", repoAPI+"/merge_requests/4/notes", 201, map[string]any{"id": 30})
	c := f.client(t)
	ctx := context.Background()
	if posted, err := c.Approve(ctx, "acme", "widgets", 4, "head1", "Looks good."); err != nil || !posted {
		t.Fatalf("Approve = %v, %v", posted, err)
	}
	if f.body("POST "+repoAPI+"/merge_requests/4/approve", 0)["sha"] != "head1" || f.body("POST "+repoAPI+"/merge_requests/4/notes", 0)["body"] != "Looks good." {
		t.Error("the approval names the head and the body is a note")
	}
	approved = []map[string]any{{"user": map[string]any{"username": "kritika"}}}
	if posted, err := c.Approve(ctx, "acme", "widgets", 4, "head1", ""); err != nil || posted {
		t.Fatalf("a standing approval is kept: %v, %v", posted, err)
	}
	if n, err := c.DismissApprovals(ctx, "acme", "widgets", 4, "Stale."); err != nil || n != 1 || f.called("POST "+repoAPI+"/merge_requests/4/unapprove") != 1 {
		t.Fatalf("DismissApprovals = %d, %v", n, err)
	}
	approved = nil
	if n, err := c.DismissApprovals(ctx, "acme", "widgets", 4, "Stale."); err != nil || n != 0 {
		t.Fatalf("nothing to dismiss = %d, %v", n, err)
	}
	f.json("GET", repoAPI+"/merge_requests/4/reviewers", 200, []map[string]any{
		{"user": map[string]any{"username": "kritika"}, "state": "requested_changes"},
		{"user": map[string]any{"username": "ann"}, "state": "reviewed"},
	})
	if yes, err := c.ChangesRequested(ctx, "acme", "widgets", 4); err != nil || yes {
		t.Fatalf("the bot's own request does not count: %v, %v", yes, err)
	}
	f.json("GET", repoAPI+"/merge_requests/5/reviewers", 200, []map[string]any{{"user": map[string]any{"username": "ann"}, "state": "requested_changes"}})
	if yes, err := c.ChangesRequested(ctx, "acme", "widgets", 5); err != nil || !yes {
		t.Fatalf("ChangesRequested = %v, %v", yes, err)
	}
}

func TestReactions(t *testing.T) {
	f := newFake(t)
	f.json("GET", repoAPI, 200, map[string]any{"default_branch": "main"})
	f.json("GET", repoAPI+"/merge_requests", 200, []map[string]any{{"iid": 4, "state": "opened"}})
	discussions(f)
	f.json("GET", repoAPI+"/merge_requests/4/award_emoji", 200, []map[string]any{
		{"id": 1, "name": "eyes", "user": map[string]any{"username": "ann"}},
		{"id": 2, "name": "thumbsup", "user": map[string]any{"username": "kritika"}},
	})
	f.json("POST", repoAPI+"/merge_requests/4/award_emoji", 201, map[string]any{"id": 3, "name": "eyes"})
	f.json("DELETE", repoAPI+"/merge_requests/4/award_emoji/3", 204, nil)
	f.json("GET", repoAPI+"/merge_requests/4/notes/23/award_emoji", 200, []map[string]any{})
	f.json("POST", repoAPI+"/merge_requests/4/notes/23/award_emoji", 201, map[string]any{"id": 9, "name": "eyes"})
	c := f.client(t)
	ctx := context.Background()
	if id, err := c.ReactToPullRequest(ctx, "acme", "widgets", 4, forge.ReactionDone); err != nil || id != 2 {
		t.Fatalf("a reaction already there is returned: %d, %v", id, err)
	}
	if id, err := c.ReactToPullRequest(ctx, "acme", "widgets", 4, forge.ReactionEyes); err != nil || id != 3 || f.body("POST "+repoAPI+"/merge_requests/4/award_emoji", 0)["name"] != "eyes" {
		t.Fatalf("ReactToPullRequest = %d, %v", id, err)
	}
	if err := c.UnreactToPullRequest(ctx, "acme", "widgets", 4, 3); err != nil {
		t.Fatal(err)
	}
	if id, err := c.PullRequestReaction(ctx, "acme", "widgets", 4, forge.ReactionDone); err != nil || id != 2 {
		t.Fatalf("PullRequestReaction = %d, %v", id, err)
	}
	cm, err := c.GetComment(ctx, "acme", "widgets", 4, 23, true)
	if err != nil {
		t.Fatal(err)
	}
	if id, err := c.React(ctx, "acme", "widgets", cm, forge.ReactionEyes); err != nil || id != 9 {
		t.Fatalf("React = %d, %v", id, err)
	}
	if id, err := c.Reaction(ctx, "acme", "widgets", cm, forge.ReactionEyes); err != nil || id != 0 {
		t.Fatalf("Reaction = %d, %v", id, err)
	}
}

func TestPermission(t *testing.T) {
	f := newFake(t)
	f.json("GET", "/api/v4/users", 200, []map[string]any{{"id": 1, "username": "ann"}})
	f.json("GET", repoAPI+"/members/all/1", 200, map[string]any{"access_level": 30})
	c := f.client(t)
	ctx := context.Background()
	if p, err := c.Permission(ctx, "acme", "widgets", "ann"); err != nil || p != forge.PermissionWrite {
		t.Fatalf("Permission = %s, %v", p, err)
	}
	for level, want := range map[int]forge.Permission{5: forge.PermissionNone, 10: forge.PermissionRead, 15: forge.PermissionRead, 20: forge.PermissionTriage, 25: forge.PermissionTriage, 30: forge.PermissionWrite, 40: forge.PermissionMaintain, 50: forge.PermissionAdmin} {
		if got := permission(gl.AccessLevelValue(level)); got != want {
			t.Errorf("permission(%d) = %s, want %s", level, got, want)
		}
	}
}

func TestPermissionNone(t *testing.T) {
	f := newFake(t)
	f.json("GET", "/api/v4/users", 200, []map[string]any{{"id": 2, "username": "bob"}})
	f.json("GET", repoAPI+"/members/all/2", 404, map[string]any{"message": "404 Not found"})
	if p, err := f.client(t).Permission(context.Background(), "acme", "widgets", "bob"); err != nil || p != forge.PermissionNone {
		t.Fatalf("no membership = %s, %v", p, err)
	}
}

func TestStatusAndFiles(t *testing.T) {
	f := newFake(t)
	f.json("POST", repoAPI+"/statuses/abc", 201, map[string]any{})
	f.mux.HandleFunc("GET "+repoAPI+"/repository/files/{path}/raw", func(w http.ResponseWriter, r *http.Request) {
		switch r.PathValue("path") {
		case "dir/a.go":
			_, _ = io.WriteString(w, "package a\n")
		case "big":
			_, _ = w.Write(make([]byte, forge.MaxFileBytes+1))
		default:
			w.WriteHeader(404)
		}
	})
	c := f.client(t)
	ctx := context.Background()
	for state, want := range map[forge.StatusState]string{forge.StatusPending: "pending", forge.StatusSuccess: "success", forge.StatusFailure: "failed", forge.StatusError: "canceled"} {
		if err := c.SetStatus(ctx, "acme", "widgets", "abc", state, "d"); err != nil {
			t.Fatal(err)
		}
		if got := f.body("POST "+repoAPI+"/statuses/abc", f.called("POST "+repoAPI+"/statuses/abc")-1); got["state"] != want || got["name"] != forge.StatusContext {
			t.Errorf("status %s = %v", state, got)
		}
	}
	if b, err := c.FileAt(ctx, "acme", "widgets", "abc", "dir/a.go"); err != nil || string(b) != "package a\n" {
		t.Fatalf("FileAt = %q, %v", b, err)
	}
	if _, err := c.FileAt(ctx, "acme", "widgets", "abc", "missing"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing = %v", err)
	}
	if _, err := c.FileAt(ctx, "acme", "widgets", "abc", "big"); !errors.Is(err, forge.ErrFileTooLarge) {
		t.Fatalf("big = %v", err)
	}
}

func TestPullRequests(t *testing.T) {
	f := newFake(t)
	f.json("GET", repoAPI, 200, map[string]any{"default_branch": "main"})
	f.mux.HandleFunc("GET "+repoAPI+"/merge_requests", func(w http.ResponseWriter, r *http.Request) {
		if q := r.URL.Query(); q.Get("state") != "opened" || q.Get("order_by") != "updated_at" || q.Get("updated_after") == "" {
			t.Errorf("query = %v", q)
		}
		if r.URL.Query().Get("page") == "1" {
			w.Header().Set("X-Next-Page", "2")
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{{
			"iid": 4, "title": "T", "description": "B", "state": "opened", "draft": true, "sha": "head1",
			"source_branch": "feat", "target_branch": "main", "source_project_id": 9, "target_project_id": 1,
			"web_url": "https://gitlab.example.com/acme/widgets/-/merge_requests/4", "labels": []string{"bug"},
			"author": map[string]any{"id": 3, "username": "renovate"}, "created_at": "2026-10-22T09:00:00Z", "updated_at": "2026-10-22T10:00:00Z",
		}})
	})
	f.json("GET", "/api/v4/users/3", 200, map[string]any{"id": 3, "username": "renovate", "bot": true})
	f.json("GET", repoAPI+"/merge_requests/4", 200, map[string]any{"iid": 4, "state": "merged", "changes_count": "1000+", "author": map[string]any{"id": 3, "username": "renovate"}})
	c := f.client(t)
	ctx := context.Background()
	prs, err := c.ListOpenPullRequests(ctx, "acme", "widgets", time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC))
	if err != nil || len(prs) != 2 {
		t.Fatalf("prs = %+v, %v", prs, err)
	}
	pr := prs[0]
	if pr.Number != 4 || pr.State != "open" || !pr.Draft || !pr.Fork || pr.HeadSHA != "head1" || pr.HeadRef != "feat" || pr.BaseRef != "main" ||
		pr.DefaultBranch != "main" || !pr.AuthorIsBot || pr.Author != "renovate" || len(pr.Labels) != 1 || pr.Labels[0].Name != "bug" || pr.UpdatedAt.IsZero() {
		t.Fatalf("pr = %+v", pr)
	}
	if f.called("GET /api/v4/users/3") != 1 {
		t.Errorf("an author's bot flag is read once, got %d", f.called("GET /api/v4/users/3"))
	}
	one, err := c.PullRequest(ctx, "acme", "widgets", 4)
	if err != nil || one.State != "closed" || !one.Merged || one.ChangedFiles != 1000 {
		t.Fatalf("PullRequest = %+v, %v", one, err)
	}
	f.json("GET", repoAPI+"/merge_requests/5", 404, map[string]any{"message": "404 Not found"})
	if _, err := c.PullRequest(ctx, "acme", "widgets", 5); !errors.Is(err, forge.ErrNotFound) {
		t.Fatalf("missing = %v", err)
	}
}

func TestRepositories(t *testing.T) {
	f := newFake(t)
	f.json("GET", "/api/v4/groups/acme/projects", 200, []map[string]any{
		{"path_with_namespace": "acme/sub/widgets", "default_branch": "main", "archived": true, "visibility": "internal", "forked_from_project": map[string]any{"id": 1}},
	})
	f.json("GET", "/api/v4/groups/dev/projects", 404, map[string]any{"message": "404 Group Not Found"})
	f.json("GET", "/api/v4/users/dev/projects", 200, []map[string]any{{"path_with_namespace": "dev/tool", "default_branch": "trunk", "visibility": "public"}})
	c := f.client(t)
	ctx := context.Background()
	repos, err := c.Repositories(ctx, "acme")
	if err != nil || len(repos) != 1 || repos[0] != (Repository{FullName: "acme/sub/widgets", DefaultBranch: "main", Archived: true, Fork: true, Visibility: "internal"}) {
		t.Fatalf("group = %+v, %v", repos, err)
	}
	if repos, err = c.Repositories(ctx, "dev"); err != nil || len(repos) != 1 || repos[0].FullName != "dev/tool" || repos[0].Fork {
		t.Fatalf("user = %+v, %v", repos, err)
	}
}

func TestBranchAndCommit(t *testing.T) {
	f := newFake(t)
	f.json("GET", repoAPI, 200, map[string]any{"default_branch": "main"})
	f.json("GET", repoAPI+"/repository/branches/main", 200, map[string]any{"name": "main", "commit": map[string]any{"id": "abc"}})
	f.json("GET", repoAPI+"/repository/commits/abc", 200, map[string]any{"id": "abc", "title": "feat: x ", "message": "feat: x\n\nbody"})
	f.json("GET", repoAPI+"/issues/8", 200, map[string]any{"iid": 8, "title": "Bug", "description": "D", "web_url": "u"})
	c := f.client(t)
	ctx := context.Background()
	if sha, branch, err := c.BranchTip(ctx, "acme", "widgets", ""); err != nil || sha != "abc" || branch != "main" {
		t.Fatalf("BranchTip = %s, %s, %v", sha, branch, err)
	}
	if s, err := c.CommitSubject(ctx, "acme", "widgets", "abc"); err != nil || s != "feat: x" {
		t.Fatalf("CommitSubject = %q, %v", s, err)
	}
	if is, err := c.Issue(ctx, "acme", "widgets", 8); err != nil || is != (forge.Issue{Number: 8, Title: "Bug", Body: "D", URL: "u"}) {
		t.Fatalf("Issue = %+v, %v", is, err)
	}
	if login, err := c.BotLogin(ctx); err != nil || login != "kritika" || f.called("GET /api/v4/user") != 1 {
		t.Fatalf("BotLogin = %q, %v", login, err)
	}
}

// TestReactOnACommentNamingItsMergeRequest: a note whose merge request the
// caller names is reacted to under it, read before or not, open or not.
func TestReactOnACommentNamingItsMergeRequest(t *testing.T) {
	f := newFake(t)
	f.json("GET", repoAPI+"/merge_requests/9/notes/23/award_emoji", 200, []map[string]any{})
	f.json("POST", repoAPI+"/merge_requests/9/notes/23/award_emoji", 201, map[string]any{"id": 5, "name": "eyes"})
	id, err := f.client(t).React(context.Background(), "acme", "widgets", forge.Comment{ID: 23, Number: 9, Inline: true}, forge.ReactionEyes)
	if err != nil || id != 5 || f.called("GET "+repoAPI+"/merge_requests") != 0 {
		t.Fatalf("React = %d, %v", id, err)
	}
}
