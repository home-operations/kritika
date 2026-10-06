package github

import (
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	gh "github.com/google/go-github/v92/github"

	"github.com/home-operations/kritika/internal/forge"
)

// newTestClient builds a Client whose underlying go-github API calls, and
// whose installation-token minting, both hit srv. It mirrors
// TestInstallationTokensMintOnceAndRefresh's server setup but leaves the
// caller free to install its own handler for the actual API request under
// test; the token mint itself is answered directly here since callers don't
// care about it.
func newTestClient(t *testing.T, handler http.HandlerFunc) (*httptest.Server, *Client) {
	t.Helper()
	_, pemKey := testKeyPEM(t)
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v3/app/installations/42/access_tokens", func(w http.ResponseWriter, r *http.Request) {
		exp := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"token":"ghs_test","expires_at":"` + exp + `"}`))
	})
	mux.HandleFunc("/", handler)
	srv := httptest.NewServer(mux)

	app, err := NewApp("Iv1.abc", pemKey, srv.URL+"/api/v3")
	if err != nil {
		t.Fatal(err)
	}
	c, err := NewClient(app, 42)
	if err != nil {
		t.Fatal(err)
	}
	return srv, c
}

func TestFileURL(t *testing.T) {
	tests := []struct {
		path          string
		line, endLine int
		want          string
	}{
		{"cmd/main.go", 3, 0, "https://github.com/o/r/blob/abc/cmd/main.go#L3"},
		{"docs/a file#1?.md", 2, 5, "https://github.com/o/r/blob/abc/docs/a%20file%231%3F.md#L2-L5"},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			if got := (&Client{}).FileURL("o", "r", "abc", tt.path, tt.line, tt.endLine); got != tt.want {
				t.Fatalf("FileURL = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCommitAndThreadURL(t *testing.T) {
	c := &Client{}
	if got := c.CommitURL("o", "r", "abc"); got != "https://github.com/o/r/commit/abc" {
		t.Fatalf("CommitURL = %q", got)
	}
	if got := c.ThreadURL("o", "r", 7, 5_800_000_001); got != "https://github.com/o/r/pull/7#discussion_r5800000001" {
		t.Fatalf("ThreadURL = %q", got)
	}
}

func TestPermission(t *testing.T) {
	respond := func(body string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if !strings.HasSuffix(r.URL.Path, "/collaborators/alice/permission") {
				t.Errorf("path = %s", r.URL.Path)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(body))
		}
	}

	cases := []struct {
		name string
		body string
		want forge.Permission
	}{
		{"admin permission", `{"permission":"admin"}`, forge.PermissionAdmin},
		{"write permission", `{"permission":"write"}`, forge.PermissionWrite},
		{"read permission", `{"permission":"read"}`, forge.PermissionRead},
		{"none permission", `{"permission":"none"}`, forge.PermissionNone},
		{"maintain role_name overrides write permission", `{"permission":"write","role_name":"maintain"}`, forge.PermissionMaintain},
		{"triage role_name overrides read permission", `{"permission":"read","role_name":"triage"}`, forge.PermissionTriage},
		{"custom role falls back to its base permission", `{"permission":"write","role_name":"security-reviewer"}`, forge.PermissionWrite},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, c := newTestClient(t, respond(tc.body))
			defer srv.Close()
			got, err := c.Permission(t.Context(), "acme", "widgets", "alice")
			if err != nil {
				t.Fatalf("Permission: %v", err)
			}
			if got != tc.want {
				t.Fatalf("Permission = %q, want %q", got, tc.want)
			}
		})
	}

	t.Run("unrecognized permission is an error", func(t *testing.T) {
		srv, c := newTestClient(t, respond(`{"permission":"bogus"}`))
		defer srv.Close()
		if _, err := c.Permission(t.Context(), "acme", "widgets", "alice"); err == nil {
			t.Fatal("expected an error for an unrecognized permission string")
		}
	})
}

// fakeAPI serves canned GitHub responses and records requests.
type fakeAPI struct {
	t        *testing.T
	mux      *http.ServeMux
	requests []string
	bodies   map[string]any
}

func newFakeAPI(t *testing.T) (*fakeAPI, *Client) {
	t.Helper()
	f := &fakeAPI{t: t, mux: http.NewServeMux(), bodies: map[string]any{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.requests = append(f.requests, r.Method+" "+r.URL.Path+"?"+r.URL.RawQuery)
		if r.Body != nil {
			var body any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body != nil {
				f.bodies[r.Method+" "+r.URL.Path] = body
			}
		}
		f.mux.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	api, err := newClient(http.DefaultTransport, srv.URL+"/api/v3")
	if err != nil {
		t.Fatal(err)
	}
	return f, &Client{api: api, tokens: &InstallationTokens{tok: "ghs_test", exp: time.Now().Add(time.Hour)}}
}

func (f *fakeAPI) reply(pattern string, status int, body string) {
	f.mux.HandleFunc(pattern, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	})
}

func (f *fakeAPI) saw(prefix string) bool {
	for _, r := range f.requests {
		if strings.HasPrefix(r, prefix) {
			return true
		}
	}
	return false
}

func TestMergeBaseAndBranchTip(t *testing.T) {
	f, c := newFakeAPI(t)
	f.reply("GET /api/v3/repos/o/r/compare/main...abc", 200, `{"merge_base_commit":{"sha":"base123"}}`)
	f.reply("GET /api/v3/repos/o/r", 200, `{"default_branch":"trunk"}`)
	f.reply("GET /api/v3/repos/o/r/branches/trunk", 200, `{"name":"trunk","commit":{"sha":"tip456"}}`)
	sha, err := c.MergeBase(t.Context(), "o", "r", "main", "abc")
	if err != nil || sha != "base123" {
		t.Fatalf("MergeBase = %q, %v", sha, err)
	}
	tip, branch, err := c.BranchTip(t.Context(), "o", "r", "")
	if err != nil || tip != "tip456" || branch != "trunk" {
		t.Fatalf("BranchTip = %q %q, %v", tip, branch, err)
	}
	if c.CloneURL("o", "r") != "https://github.com/o/r.git" {
		t.Fatalf("CloneURL = %s", c.CloneURL("o", "r"))
	}
	f.reply("GET /api/v3/repos/o/r/compare/main...none", 200, `{}`)
	if _, err := c.MergeBase(t.Context(), "o", "r", "main", "none"); err == nil {
		t.Fatal("a compare without a merge base must error")
	}
}

func TestFileAt(t *testing.T) {
	f, c := newFakeAPI(t)
	f.reply("GET /api/v3/repos/o/r/contents/.kritika.yaml", 200, `{"type":"file","size":8,"encoding":"base64","content":"bW9kZTog\neA=="}`)
	f.reply("GET /api/v3/repos/o/r/contents/gone.yaml", 404, `{"message":"Not Found"}`)
	f.reply("GET /api/v3/repos/o/r/contents/docs", 200, `[{"type":"file","name":"a.md"}]`)
	f.reply("GET /api/v3/repos/o/r/contents/link", 200, `{"type":"symlink","target":"a.md"}`)
	f.reply("GET /api/v3/repos/o/r/contents/big.bin", 200, `{"type":"file","size":1048577,"encoding":"none","content":""}`)
	got, err := c.FileAt(t.Context(), "o", "r", "base123", ".kritika.yaml")
	if err != nil || string(got) != "mode: x" {
		t.Fatalf("FileAt = %q, %v", got, err)
	}
	if !f.saw("GET /api/v3/repos/o/r/contents/.kritika.yaml?ref=base123") {
		t.Fatalf("requests = %v, want the file at the ref", f.requests)
	}
	for _, path := range []string{"gone.yaml", "docs", "link"} {
		if _, err := c.FileAt(t.Context(), "o", "r", "base123", path); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("FileAt(%s) = %v, want fs.ErrNotExist", path, err)
		}
	}
	if _, err := c.FileAt(t.Context(), "o", "r", "base123", "big.bin"); !errors.Is(err, forge.ErrFileTooLarge) {
		t.Fatalf("FileAt of a file over the cap = %v, want forge.ErrFileTooLarge", err)
	}
}

func TestIssue(t *testing.T) {
	f, c := newFakeAPI(t)
	f.reply("GET /api/v3/repos/o/r/issues/12", 200, `{"number":12,"title":"Widgets leak","body":"Steps.","html_url":"https://github.com/o/r/issues/12"}`)
	f.reply("GET /api/v3/repos/o/r/issues/13", 200, `{"number":13,"title":"A pull request","pull_request":{"url":"https://api.github.com/repos/o/r/pulls/13"}}`)
	f.reply("GET /api/v3/repos/o/r/issues/14", 404, `{"message":"Not Found"}`)
	got, err := c.Issue(t.Context(), "o", "r", 12)
	want := forge.Issue{Number: 12, Title: "Widgets leak", Body: "Steps.", URL: "https://github.com/o/r/issues/12"}
	if err != nil || got != want {
		t.Fatalf("Issue = %+v, %v; want %+v", got, err, want)
	}
	if got, err := c.Issue(t.Context(), "o", "r", 13); err != nil || !got.PullRequest {
		t.Fatalf("Issue of a pull request = %+v, %v; want PullRequest set", got, err)
	}
	if _, err := c.Issue(t.Context(), "o", "r", 14); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Issue of an unknown number = %v, want fs.ErrNotExist", err)
	}
}

func TestPullRequestDiff(t *testing.T) {
	f, c := newFakeAPI(t)
	const diff = "diff --git a/a.go b/a.go\n+b\n"
	f.mux.HandleFunc("GET /api/v3/repos/o/r/compare/base123...abc", func(w http.ResponseWriter, r *http.Request) {
		if accept := r.Header.Get("Accept"); accept != "application/vnd.github.v3.diff" {
			t.Errorf("Accept = %q, want the diff media type", accept)
		}
		_, _ = w.Write([]byte(diff))
	})
	got, err := c.PullRequestDiff(t.Context(), "o", "r", "base123", "abc")
	if err != nil || got != diff {
		t.Fatalf("PullRequestDiff = %q, %v", got, err)
	}
	f.reply("GET /api/v3/repos/o/r/compare/base123...big", 406, `{"message":"diff too large"}`)
	if _, err := c.PullRequestDiff(t.Context(), "o", "r", "base123", "big"); err == nil {
		t.Fatal("a diff the forge refuses must be an error")
	}
}

func TestFindCommentPaginatesAndMatchesAuthorPlusMarker(t *testing.T) {
	f, c := newFakeAPI(t)
	f.mux.HandleFunc("GET /api/v3/repos/o/r/issues/7/comments", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("page") == "2" {
			_, _ = w.Write([]byte(`[{"id":30,"body":"<!-- kritika:pr-7 --> real","user":{"login":"bot[bot]","type":"Bot"}}]`))
			return
		}
		w.Header().Set("Link", `<`+"http://x"+r.URL.Path+`?page=2>; rel="next"`)
		_, _ = w.Write([]byte(`[{"id":10,"body":"<!-- kritika:pr-7 --> planted","user":{"login":"attacker","type":"User"}},
			{"id":20,"body":"unrelated","user":{"login":"bot[bot]","type":"Bot"}}]`))
	})
	id, err := c.FindComment(t.Context(), "o", "r", 7, "bot[bot]", "<!-- kritika:pr-7 -->")
	if err != nil || id != 30 {
		t.Fatalf("FindComment = %d, %v; a planted marker by another author must not match", id, err)
	}
}

func TestWriteBackCalls(t *testing.T) {
	f, c := newFakeAPI(t)
	f.reply("POST /api/v3/repos/o/r/issues/7/comments", 201, `{"id":100}`)
	f.reply("PATCH /api/v3/repos/o/r/issues/comments/100", 200, `{"id":100}`)
	f.reply("POST /api/v3/repos/o/r/pulls/7/reviews", 200, `{"id":5}`)
	f.reply("GET /api/v3/repos/o/r/pulls/7/reviews/5/comments", 200,
		`[{"id":301,"path":"a.go","line":3,"body":"b"},{"id":302,"path":"a.go","line":9,"body":"elsewhere"}]`)
	f.reply("POST /api/v3/repos/o/r/statuses/abc", 201, `{"state":"success"}`)
	f.reply("POST /api/v3/repos/o/r/pulls/7/comments", 201, `{"id":200}`)

	id, err := c.CreateComment(t.Context(), "o", "r", 7, "hello")
	if err != nil || id != 100 {
		t.Fatalf("CreateComment = %d, %v", id, err)
	}
	if err := c.UpdateComment(t.Context(), "o", "r", 100, "edited"); err != nil {
		t.Fatal(err)
	}
	f.reply("PATCH /api/v3/repos/o/r/issues/comments/101", 404, `{"message":"Not Found"}`)
	if err := c.UpdateComment(t.Context(), "o", "r", 101, "edited"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("UpdateComment of a deleted comment = %v, want fs.ErrNotExist", err)
	}
	if ids, err := c.CreateReview(t.Context(), "o", "r", 7, "abc", nil); err != nil || ids == nil || f.saw("POST /api/v3/repos/o/r/pulls/7/reviews") {
		t.Fatal("a review with no comments must not be posted")
	}
	sent := []forge.InlineComment{{Path: "a.go", Line: 3, Body: "b"}, {Path: "b.go", Line: 1, Body: "b"}}
	ids, err := c.CreateReview(t.Context(), "o", "r", 7, "abc", sent)
	if err != nil || !slices.Equal(ids, []int64{301, 0}) {
		t.Fatalf("CreateReview = %v, %v; want each comment's id matched by path and body, 0 where none", ids, err)
	}
	review := f.bodies["POST /api/v3/repos/o/r/pulls/7/reviews"].(map[string]any)
	if review["event"] != "COMMENT" || review["commit_id"] != "abc" {
		t.Fatalf("review body = %v; must be a COMMENT review pinned to the head", review)
	}
	if cm := review["comments"].([]any)[0].(map[string]any); cm["side"] != "RIGHT" || cm["line"] != float64(3) {
		t.Fatalf("inline comment = %v", cm)
	}
	long := strings.Repeat("x", 200)
	if err := c.SetStatus(t.Context(), "o", "r", "abc", forge.StatusSuccess, long); err != nil {
		t.Fatal(err)
	}
	status := f.bodies["POST /api/v3/repos/o/r/statuses/abc"].(map[string]any)
	// GitHub's limit is 140 characters, not bytes; the ellipsis is 3 bytes.
	if status["context"] != forge.StatusContext || utf8.RuneCountInString(status["description"].(string)) != 140 {
		t.Fatalf("status body = %v", status)
	}
	// A reply to a reply goes under the thread's root, as a reply to the root does.
	for _, to := range []forge.Comment{{ID: 50, Inline: true}, {ID: 51, Inline: true, InReplyTo: 50}} {
		rid, err := c.ReplyInline(t.Context(), "o", "r", 7, to, "reply")
		if err != nil || rid != 200 || f.bodies["POST /api/v3/repos/o/r/pulls/7/comments"].(map[string]any)["in_reply_to"] != float64(50) {
			t.Fatalf("ReplyInline(%+v) = %d, %v, body %v", to, rid, err, f.bodies["POST /api/v3/repos/o/r/pulls/7/comments"])
		}
	}
}

// TestChangesRequested: a reviewer stands where their latest approval,
// request for changes or dismissal left them; a comment after it changes
// nothing, and the bot's own reviews do not count.
func TestChangesRequested(t *testing.T) {
	tests := []struct {
		name    string
		reviews string
		want    bool
	}{
		{name: "no reviews", reviews: `[]`},
		{name: "a request for changes stands through a later comment", want: true, reviews: `[
			{"id":1,"user":{"login":"human"},"state":"CHANGES_REQUESTED"},
			{"id":2,"user":{"login":"human"},"state":"COMMENTED"},
			{"id":3,"user":{"login":"other"},"state":"APPROVED"}]`},
		{name: "a later approval lifts it", reviews: `[
			{"id":1,"user":{"login":"human"},"state":"CHANGES_REQUESTED"},
			{"id":2,"user":{"login":"human"},"state":"APPROVED"}]`},
		{name: "a dismissed one does not stand", reviews: `[{"id":1,"user":{"login":"human"},"state":"DISMISSED"}]`},
		{name: "the bot's own does not count", reviews: `[{"id":1,"user":{"login":"kritika[bot]"},"state":"CHANGES_REQUESTED"}]`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, c := newFakeAPI(t)
			c.login = "kritika[bot]"
			f.reply("GET /api/v3/repos/o/r/pulls/7/reviews", 200, tt.reviews)
			got, err := c.ChangesRequested(t.Context(), "o", "r", 7)
			if err != nil || got != tt.want {
				t.Fatalf("ChangesRequested = %v, %v; want %v", got, err, tt.want)
			}
		})
	}
}

func TestApproveAndDismissApprovals(t *testing.T) {
	f, c := newFakeAPI(t)
	c.login = "kritika[bot]"
	f.reply("GET /api/v3/repos/o/r/pulls/7/reviews", 200, `[
		{"id":1,"user":{"login":"kritika[bot]"},"state":"COMMENTED","commit_id":"abc"},
		{"id":2,"user":{"login":"human"},"state":"APPROVED","commit_id":"abc"},
		{"id":3,"user":{"login":"kritika[bot]"},"state":"DISMISSED","commit_id":"old"},
		{"id":4,"user":{"login":"kritika[bot]"},"state":"APPROVED","commit_id":"old"}]`)
	f.reply("POST /api/v3/repos/o/r/pulls/7/reviews", 200, `{"id":5}`)
	f.reply("PUT /api/v3/repos/o/r/pulls/7/reviews/4/dismissals", 200, `{"id":4,"state":"DISMISSED"}`)

	posted, err := c.Approve(t.Context(), "o", "r", 7, "abc", "clean")
	if err != nil || !posted {
		t.Fatalf("Approve = %v, %v; want an approval of a head the bot has not approved", posted, err)
	}
	review := f.bodies["POST /api/v3/repos/o/r/pulls/7/reviews"].(map[string]any)
	if review["event"] != "APPROVE" || review["commit_id"] != "abc" || review["body"] != "clean" {
		t.Fatalf("review body = %v; must be an APPROVE review pinned to the head", review)
	}
	delete(f.bodies, "POST /api/v3/repos/o/r/pulls/7/reviews")
	// The bot's approval of old stands; a human's of abc, and the bot's
	// dismissed and comment reviews, do not count.
	if posted, err := c.Approve(t.Context(), "o", "r", 7, "old", "clean"); err != nil || posted {
		t.Fatalf("Approve = %v, %v; want the standing approval left as it is", posted, err)
	}
	if _, ok := f.bodies["POST /api/v3/repos/o/r/pulls/7/reviews"]; ok {
		t.Fatal("a head the bot already approved was approved again")
	}
	n, err := c.DismissApprovals(t.Context(), "o", "r", 7, "stale")
	if err != nil || n != 1 {
		t.Fatalf("DismissApprovals = %d, %v; want the bot's one standing approval dismissed", n, err)
	}
	if body := f.bodies["PUT /api/v3/repos/o/r/pulls/7/reviews/4/dismissals"].(map[string]any); body["message"] != "stale" {
		t.Fatalf("dismissal body = %v", body)
	}
	if f.saw("PUT /api/v3/repos/o/r/pulls/7/reviews/2/") {
		t.Fatal("a human's approval was dismissed")
	}
}

func TestListInlineReactions(t *testing.T) {
	f, c := newFakeAPI(t)
	f.reply("GET /api/v3/repos/o/r/pulls/7/comments", 200,
		`[{"id":3,"body":"c","path":"a.go","line":1,"user":{"login":"u"},"reactions":{"+1":2,"-1":1,"heart":5}},
		  {"id":4,"body":"d","path":"a.go","line":2,"user":{"login":"u"}}]`)
	inl, err := c.ListInline(t.Context(), "o", "r", 7)
	if err != nil || len(inl) != 2 {
		t.Fatalf("ListInline = %+v, %v", inl, err)
	}
	if inl[0].ReactionsUp != 2 || inl[0].ReactionsDown != 1 || inl[1].ReactionsUp != 0 || inl[1].ReactionsDown != 0 {
		t.Fatalf("reactions = %+v; want 👍 and 👎 counted, other reactions and a missing rollup left out", inl)
	}
}

// openPullsJSON is a listing of two open pull requests, the newer one from
// a fork and a bot.
const openPullsJSON = `[
	{"number":2,"title":"new","state":"open","updated_at":"2026-09-24T22:00:00Z","user":{"login":"x[bot]","type":"Bot"},
	 "head":{"ref":"f","sha":"h2","repo":{"full_name":"fork/r"}},"base":{"ref":"main","sha":"b2","repo":{"full_name":"o/r","default_branch":"main"}},
	 "labels":[{"name":"l"}]},
	{"number":1,"title":"old","state":"open","updated_at":"2026-09-24T10:00:00Z","user":{"login":"u"},
	 "head":{"ref":"g","sha":"h1","repo":{"full_name":"o/r"}},"base":{"ref":"main","sha":"b1","repo":{"full_name":"o/r"}}}]`

func TestCommentsPermissionAndOpenPullRequests(t *testing.T) {
	f, c := newFakeAPI(t)
	f.reply("GET /api/v3/repos/o/r/issues/comments/1", 200, `{"id":1,"body":"hi","user":{"login":"u","type":"User"},"created_at":"2026-09-24T20:00:00Z"}`)
	f.reply("GET /api/v3/repos/o/r/pulls/comments/2", 200, `{"id":2,"body":"inline","path":"a.go","line":4,"commit_id":"c1","in_reply_to_id":1,"user":{"login":"b[bot]","type":"Bot"}}`)
	f.reply("GET /api/v3/repos/o/r/issues/7/comments", 200, `[{"id":1,"body":"a","user":{"login":"u"}},{"id":2,"body":"b","user":{"login":"v"}}]`)
	f.reply("GET /api/v3/repos/o/r/pulls/7/comments", 200, `[{"id":3,"body":"c","path":"a.go","line":1,"user":{"login":"u"}}]`)
	f.reply("GET /api/v3/repos/o/r/collaborators/u/permission", 200, `{"permission":"write","role_name":"maintain"}`)
	f.reply("GET /api/v3/repos/o/r/pulls", 200, openPullsJSON)

	cm, err := c.GetComment(t.Context(), "o", "r", 1, false)
	if err != nil || cm.Author != "u" || cm.AuthorIsBot || cm.CreatedAt.IsZero() {
		t.Fatalf("GetComment = %+v, %v", cm, err)
	}
	inline, err := c.GetComment(t.Context(), "o", "r", 2, true)
	if err != nil || !inline.Inline || inline.Path != "a.go" || inline.Line != 4 || inline.InReplyTo != 1 || !inline.AuthorIsBot {
		t.Fatalf("inline GetComment = %+v, %v", inline, err)
	}
	conv, err := c.ListConversation(t.Context(), "o", "r", 7)
	if err != nil || len(conv) != 2 || conv[1].Author != "v" {
		t.Fatalf("ListConversation = %+v, %v", conv, err)
	}
	inl, err := c.ListInline(t.Context(), "o", "r", 7)
	if err != nil || len(inl) != 1 || !inl[0].Inline {
		t.Fatalf("ListInline = %+v, %v", inl, err)
	}
	perm, err := c.Permission(t.Context(), "o", "r", "u")
	if err != nil || perm != forge.PermissionMaintain || !forge.CanWrite(perm) || forge.CanWrite(forge.PermissionRead) {
		t.Fatalf("Permission = %q, %v", perm, err)
	}
	since := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	prs, err := c.ListOpenPullRequests(t.Context(), "o", "r", since)
	if err != nil || len(prs) != 1 || prs[0].Number != 2 {
		t.Fatalf("ListOpenPullRequests = %+v, %v; the older PR is past since", prs, err)
	}
	pr := prs[0]
	if !pr.Fork || !pr.AuthorIsBot || pr.HeadSHA != "h2" || pr.DefaultBranch != "main" || len(pr.Labels) != 1 {
		t.Fatalf("open PR = %+v", pr)
	}
}

// TestPullRequestByNumber: the zero time lists every open pull request,
// and one asked for by number comes back as it stands, closed included, a
// missing one as fs.ErrNotExist.
func TestPullRequestByNumber(t *testing.T) {
	f, c := newFakeAPI(t)
	f.reply("GET /api/v3/repos/o/r/pulls", 200, openPullsJSON)
	if all, err := c.ListOpenPullRequests(t.Context(), "o", "r", time.Time{}); err != nil || len(all) != 2 {
		t.Fatalf("ListOpenPullRequests(zero) = %+v, %v; want every open pull request", all, err)
	}
	f.reply("GET /api/v3/repos/o/r/pulls/9", 200, `{"number":9,"state":"closed","merged":true,"closed_at":"2026-10-02T08:59:24Z",
		"head":{"ref":"f","sha":"h9","repo":{"full_name":"o/r"}},"base":{"ref":"main","repo":{"full_name":"o/r","default_branch":"main"}},
		"user":{"login":"u","type":"User"}}`)
	f.reply("GET /api/v3/repos/o/r/pulls/10", 404, `{"message":"Not Found"}`)
	closed, err := c.PullRequest(t.Context(), "o", "r", 9)
	if err != nil || closed.State != "closed" || !closed.Merged || closed.ClosedAt == nil ||
		!closed.ClosedAt.Equal(time.Date(2026, 10, 2, 8, 59, 24, 0, time.UTC)) {
		t.Fatalf("PullRequest(9) = %+v, %v; want it closed and merged at 08:59:24", closed, err)
	}
	if _, err := c.PullRequest(t.Context(), "o", "r", 10); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("PullRequest(10) = %v, want fs.ErrNotExist", err)
	}
}

func TestOpenPullRequestForkDetection(t *testing.T) {
	base := &gh.Repository{FullName: new("acme/widgets")}
	tests := []struct {
		name string
		head *gh.Repository
		want bool
	}{
		{"a deleted head repo is a fork", nil, true},
		{"the base repo is not", &gh.Repository{FullName: new("acme/widgets")}, false},
		{"another repo is", &gh.Repository{FullName: new("someone/widgets")}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pr := &gh.PullRequest{Head: &gh.PullRequestBranch{Repo: tt.head}, Base: &gh.PullRequestBranch{Repo: base}}
			if got := openPullRequest(pr).Fork; got != tt.want {
				t.Fatalf("Fork = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestCommentListingsAreBounded: a listing stops at maxListedComments
// rather than reading a pull request's every comment into memory.
func TestCommentListingsAreBounded(t *testing.T) {
	old := maxListedComments
	maxListedComments = 2
	t.Cleanup(func() { maxListedComments = old })
	f, c := newFakeAPI(t)
	f.reply("GET /api/v3/repos/o/r/issues/7/comments", 200,
		`[{"id":1,"body":"a","user":{"login":"u"}},{"id":2,"body":"b","user":{"login":"u"}},{"id":3,"body":"c","user":{"login":"u"}}]`)
	f.reply("GET /api/v3/repos/o/r/pulls/7/comments", 200,
		`[{"id":4,"body":"a","path":"a.go","line":1,"user":{"login":"u"}},{"id":5,"body":"b","path":"a.go","line":2,"user":{"login":"u"}},
		  {"id":6,"body":"c","path":"a.go","line":3,"user":{"login":"u"}}]`)
	conv, err := c.ListConversation(t.Context(), "o", "r", 7)
	if err != nil || len(conv) != 2 || conv[1].ID != 2 {
		t.Fatalf("ListConversation = %+v, %v; want the two oldest", conv, err)
	}
	inl, err := c.ListInline(t.Context(), "o", "r", 7)
	if err != nil || len(inl) != 2 || inl[1].ID != 5 {
		t.Fatalf("ListInline = %+v, %v; want the two oldest", inl, err)
	}
}

// TestReactions: a conversation comment and an inline one are reacted to,
// and the reaction taken off, in their own namespaces.
func TestReactions(t *testing.T) {
	tests := []struct {
		name    string
		comment forge.Comment
		path    string
	}{
		{name: "conversation comment", comment: forge.Comment{ID: 5}, path: "/api/v3/repos/o/r/issues/comments/5/reactions"},
		{name: "inline comment", comment: forge.Comment{ID: 6, Inline: true}, path: "/api/v3/repos/o/r/pulls/comments/6/reactions"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, c := newFakeAPI(t)
			f.reply("POST "+tt.path, 201, `{"id":77,"content":"eyes"}`)
			f.reply("DELETE "+tt.path+"/77", 204, ``)
			id, err := c.React(t.Context(), "o", "r", tt.comment, forge.ReactionEyes)
			if err != nil || id != 77 || f.bodies["POST "+tt.path].(map[string]any)["content"] != "eyes" {
				t.Fatalf("React = %d, %v, body %v", id, err, f.bodies["POST "+tt.path])
			}
			if err := c.Unreact(t.Context(), "o", "r", tt.comment, id); err != nil || !f.saw("DELETE "+tt.path+"/77") {
				t.Fatalf("Unreact = %v, requests %v", err, f.requests)
			}
		})
	}

	t.Run("a refused reaction is an error", func(t *testing.T) {
		f, c := newFakeAPI(t)
		f.reply("POST /api/v3/repos/o/r/issues/comments/5/reactions", 403, `{"message":"Resource not accessible by integration"}`)
		if _, err := c.React(t.Context(), "o", "r", forge.Comment{ID: 5}, forge.ReactionEyes); err == nil {
			t.Fatal("React = nil, want the forge's refusal")
		}
	})
}

// TestPullRequestReactions: a pull request's own reactions are its issue's.
func TestPullRequestReactions(t *testing.T) {
	f, c := newFakeAPI(t)
	f.reply("POST /api/v3/repos/o/r/issues/7/reactions", 201, `{"id":78,"content":"+1"}`)
	f.reply("DELETE /api/v3/repos/o/r/issues/7/reactions/78", 204, ``)
	id, err := c.ReactToPullRequest(t.Context(), "o", "r", 7, forge.ReactionDone)
	if err != nil || id != 78 || f.bodies["POST /api/v3/repos/o/r/issues/7/reactions"].(map[string]any)["content"] != "+1" {
		t.Fatalf("ReactToPullRequest = %d, %v, body %v", id, err, f.bodies["POST /api/v3/repos/o/r/issues/7/reactions"])
	}
	if err := c.UnreactToPullRequest(t.Context(), "o", "r", 7, id); err != nil || !f.saw("DELETE /api/v3/repos/o/r/issues/7/reactions/78") {
		t.Fatalf("UnreactToPullRequest = %v, requests %v", err, f.requests)
	}
}
