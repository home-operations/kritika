package forgejo

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/home-operations/kritika/internal/forge"
)

func TestNewGiteaClient(t *testing.T) {
	if _, err := NewGiteaClient("gitea.example.com", "", "", nil); err == nil || !strings.Contains(err.Error(), "token") {
		t.Fatalf("without a token = %v", err)
	}
	if _, err := NewGiteaClient("", "t", "", nil); err == nil {
		t.Fatal("without a host must fail")
	}
	// A runner fetches with the read-only token when there is one, else
	// with the API's.
	c, err := NewGiteaClient("gitea.example.com", "api", "read", nil)
	if err != nil || c.api != "https://gitea.example.com/api/v1" {
		t.Fatalf("NewGiteaClient = %+v, %v", c, err)
	}
	if tok, err := c.GitToken(t.Context(), "widgets"); err != nil || tok != "read" {
		t.Fatalf("GitToken = %q, %v", tok, err)
	}
	c, _ = NewGiteaClient("gitea.example.com", "api", "", nil)
	if tok, _ := c.GitToken(t.Context(), "widgets"); tok != "api" {
		t.Fatalf("GitToken without a read-only token = %q", tok)
	}
}

func TestGiteaReads(t *testing.T) {
	f := newGiteaFake(t)
	f.json("GET", repoAPI+"/pulls/7", 200, map[string]any{
		"number": 7, "user": map[string]any{"login": "deps", "type": "Bot"},
		"head": map[string]any{"sha": "head1", "repo": map[string]any{"full_name": "acme/widgets"}},
		"base": map[string]any{"ref": "main", "repo": map[string]any{"full_name": "acme/widgets"}},
	})
	c := f.giteaClient(t)
	pr, err := c.PullRequest(t.Context(), "acme", "widgets", 7)
	if err != nil || !pr.AuthorIsBot || pr.Fork {
		t.Fatalf("PullRequest = %+v, %v; Gitea types a bot user Bot", pr, err)
	}
}

func TestGiteaReplyInline(t *testing.T) {
	f := newGiteaFake(t)
	f.json("POST", repoAPI+"/pulls/7/comments/13/replies", 201, map[string]any{"id": 30, "pull_request_review_id": 100})
	f.json("POST", repoAPI+"/pulls/7/comments/99/replies", 404, map[string]any{"message": "not found"})
	c := f.giteaClient(t)
	// Gitea's own route posts the reply into the conversation the comment
	// is in; nothing is listed first.
	id, err := c.ReplyInline(t.Context(), "acme", "widgets", 7, forge.Comment{ID: 13, Inline: true, InReplyTo: 10}, "answer")
	if err != nil || id != 30 {
		t.Fatalf("ReplyInline = %d, %v", id, err)
	}
	if got := f.body("POST " + repoAPI + "/pulls/7/comments/13/replies"); len(got) != 1 || got["body"] != "answer" {
		t.Fatalf("reply request = %v", got)
	}
	if f.called("GET "+repoAPI+"/pulls/7/reviews") != 0 {
		t.Fatal("a Gitea reply needs no listing")
	}
	if _, err := c.ReplyInline(t.Context(), "acme", "widgets", 7, forge.Comment{ID: 99, Inline: true}, "x"); !errors.Is(err, forge.ErrNotFound) {
		t.Fatalf("a reply to an unknown comment = %v, want ErrNotFound", err)
	}
}

// TestGiteaReplyToMention: a reply in a code conversation arrives as a
// conversation comment, which Gitea's comment route answers with 204; it
// is found among the code comments, and answered in its conversation.
func TestGiteaReplyToMention(t *testing.T) {
	f := newGiteaFake(t)
	reviews(f)
	f.json("GET", repoAPI+"/issues/comments/13", 204, nil)
	f.json("GET", repoAPI+"/pulls", 200, []map[string]any{{"number": 7, "updated_at": "2026-10-20T11:00:00Z", "user": map[string]any{"login": "x"}}})
	f.json("POST", repoAPI+"/pulls/7/comments/13/replies", 201, map[string]any{"id": 31, "pull_request_review_id": 100})
	c := f.giteaClient(t)
	cm, err := c.GetComment(t.Context(), "acme", "widgets", 7, 13, false)
	if err != nil || !cm.Inline || cm.InReplyTo != 10 || cm.Author != "dev" {
		t.Fatalf("GetComment = %+v, %v; want dev's reply in the bot's conversation", cm, err)
	}
	if id, err := c.ReplyInline(t.Context(), "acme", "widgets", 7, cm, "answer"); err != nil || id != 31 {
		t.Fatalf("ReplyInline = %d, %v", id, err)
	}
}

func TestGiteaResolveThread(t *testing.T) {
	f := newGiteaFake(t)
	reviews(f)
	f.json("POST", repoAPI+"/pulls/comments/11/resolve", 204, nil)
	f.json("POST", repoAPI+"/pulls/comments/12/resolve", 204, nil)
	c := f.giteaClient(t)
	for _, tt := range []struct {
		name    string
		id      int64
		onlyOwn bool
		want    bool
	}{
		{"the bot's open thread", 11, true, true},
		{"a resolved thread", 10, false, false},
		{"a reply", 13, false, false},
		{"an unknown comment", 99, false, false},
		{"someone else's thread when only its own", 12, true, false},
		{"someone else's thread when asked", 12, false, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := c.ResolveThread(t.Context(), "acme", "widgets", 7, tt.id, tt.onlyOwn)
			if err != nil || got != tt.want {
				t.Fatalf("ResolveThread(%d, %v) = %v, %v; want %v", tt.id, tt.onlyOwn, got, err, tt.want)
			}
		})
	}
	if f.called("POST "+repoAPI+"/pulls/comments/11/resolve") != 1 || f.called("POST "+repoAPI+"/pulls/comments/12/resolve") != 1 {
		t.Fatal("each thread resolved is resolved once, by its root")
	}
}

// TestGiteaResolveRepliedThread: the bot's thread someone replied in is
// theirs to resolve, unless one of them asked, and a refusal is an error.
// An earlier review's comment on the same line is a conversation of its
// own, which resolving the bot's leaves as it is.
func TestGiteaResolveRepliedThread(t *testing.T) {
	f := newGiteaFake(t)
	f.json("GET", "/api/v1/user", 200, map[string]any{"login": "kritika-bot"})
	f.json("GET", repoAPI+"/pulls/7/reviews", 200, []map[string]any{
		{"id": 90, "state": "COMMENT", "user": map[string]any{"login": "dev"}},
		{"id": 100, "state": "COMMENT", "user": map[string]any{"login": "kritika-bot"}},
	})
	f.json("GET", repoAPI+"/pulls/7/reviews/90/comments", 200, []map[string]any{
		{"id": 5, "pull_request_review_id": 90, "body": "earlier", "path": "a.go", "position": 3, "user": map[string]any{"login": "dev"}, "created_at": "2026-10-20T09:00:00Z"},
	})
	f.json("GET", repoAPI+"/pulls/7/reviews/100/comments", 200, []map[string]any{
		{"id": 10, "pull_request_review_id": 100, "body": "a", "path": "a.go", "position": 3, "user": map[string]any{"login": "kritika-bot"}, "created_at": "2026-10-20T10:00:00Z"},
		{"id": 13, "pull_request_review_id": 100, "body": "why?", "path": "a.go", "position": 3, "user": map[string]any{"login": "dev"}, "created_at": "2026-10-20T11:30:00Z"},
		{"id": 20, "pull_request_review_id": 100, "body": "b", "path": "b.go", "position": 1, "user": map[string]any{"login": "kritika-bot"}, "created_at": "2026-10-20T10:00:01Z"},
	})
	f.json("POST", repoAPI+"/pulls/comments/10/resolve", 204, nil)
	f.json("POST", repoAPI+"/pulls/comments/20/resolve", 403, map[string]any{"message": "forbidden"})
	c := f.giteaClient(t)
	if got, err := c.ResolveThread(t.Context(), "acme", "widgets", 7, 10, true); err != nil || got {
		t.Fatalf("ResolveThread of a replied thread = %v, %v; want it left open", got, err)
	}
	if got, err := c.ResolveThread(t.Context(), "acme", "widgets", 7, 10, false); err != nil || !got {
		t.Fatalf("ResolveThread asked = %v, %v; want it resolved", got, err)
	}
	if _, err := c.ResolveThread(t.Context(), "acme", "widgets", 7, 20, true); err == nil {
		t.Fatal("a refused resolve must be an error")
	}
	if f.called("POST "+repoAPI+"/pulls/comments/5/resolve") != 0 {
		t.Fatal("the earlier review's conversation must be left as it is")
	}
}

// TestBots: the logins a connection lists are bots in everything the
// client reads, as Gitea's bot users are.
func TestBots(t *testing.T) {
	f := newFake(t)
	f.json("GET", repoAPI+"/pulls", 200, []map[string]any{{"number": 7, "user": map[string]any{"login": "renovate-bot"},
		"updated_at": "2026-10-20T11:00:00Z", "head": map[string]any{"repo": map[string]any{"full_name": "acme/widgets"}},
		"base": map[string]any{"repo": map[string]any{"full_name": "acme/widgets"}}}})
	f.json("GET", repoAPI+"/pulls/7/reviews", 200, []map[string]any{{"id": 100, "state": "COMMENT", "user": map[string]any{"login": "dev"}}})
	f.json("GET", repoAPI+"/pulls/7/reviews/100/comments", 200, []map[string]any{
		{"id": 10, "pull_request_review_id": 100, "body": "a", "path": "a.go", "position": 3, "user": map[string]any{"login": "dev"}, "created_at": "2026-10-20T10:00:00Z"},
		{"id": 11, "pull_request_review_id": 100, "body": "b", "path": "a.go", "position": 3, "user": map[string]any{"login": "renovate-bot"}, "created_at": "2026-10-20T10:00:01Z"},
	})
	f.json("GET", repoAPI+"/issues/comments/1", 200, map[string]any{"id": 1, "body": "digest", "user": map[string]any{"login": "renovate-bot"}})
	f.json("GET", repoAPI+"/pulls/7", 200, map[string]any{"number": 7, "user": map[string]any{"login": "Renovate-Bot"},
		"head": map[string]any{"sha": "h", "repo": map[string]any{"full_name": "acme/widgets"}}, "base": map[string]any{"repo": map[string]any{"full_name": "acme/widgets"}}})
	f.json("GET", repoAPI+"/issues/7/comments", 200, []map[string]any{
		{"id": 1, "body": "digest", "user": map[string]any{"login": "renovate-bot"}},
		{"id": 2, "body": "lgtm", "user": map[string]any{"login": "dev"}},
	})
	c := f.client(t)
	c.Bots = []string{"renovate-bot"}
	if pr, err := c.PullRequest(t.Context(), "acme", "widgets", 7); err != nil || !pr.AuthorIsBot {
		t.Fatalf("PullRequest = %+v, %v; want a listed bot's", pr, err)
	}
	comments, err := c.ListConversation(t.Context(), "acme", "widgets", 7)
	if err != nil || len(comments) != 2 || !comments[0].AuthorIsBot || comments[1].AuthorIsBot {
		t.Fatalf("ListConversation = %+v, %v", comments, err)
	}
	if prs, err := c.ListOpenPullRequests(t.Context(), "acme", "widgets", time.Time{}); err != nil || len(prs) != 1 || !prs[0].AuthorIsBot {
		t.Fatalf("ListOpenPullRequests = %+v, %v", prs, err)
	}
	inline, err := c.ListInline(t.Context(), "acme", "widgets", 7)
	if err != nil || len(inline) != 2 || inline[0].AuthorIsBot || !inline[1].AuthorIsBot {
		t.Fatalf("ListInline = %+v, %v", inline, err)
	}
	if cm, err := c.GetComment(t.Context(), "acme", "widgets", 7, 1, false); err != nil || !cm.AuthorIsBot {
		t.Fatalf("GetComment = %+v, %v", cm, err)
	}
	if cm, err := c.GetComment(t.Context(), "acme", "widgets", 7, 11, true); err != nil || !cm.AuthorIsBot {
		t.Fatalf("GetComment inline = %+v, %v", cm, err)
	}
}
