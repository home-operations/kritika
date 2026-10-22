package webhook

import (
	"errors"
	"net/http"
	"testing"

	"github.com/home-operations/kritika/internal/configfile"
)

func gt(event string) http.Header {
	h := http.Header{}
	h.Set("X-Gitea-Event", event)
	h.Set("X-Gitea-Delivery", "gt-1")
	h.Set("Content-Type", "application/json")
	return h
}

func TestParseGiteaPullRequest(t *testing.T) {
	body := `{"action": "synchronized", "number": 7, "pull_request": {
    "number": 7, "title": "feat", "state": "open",
    "user": {"login": "deps", "type": "Bot"},
    "head": {"ref": "deps/x", "sha": "aaa111", "repo": {"full_name": "acme/widgets"}},
    "base": {"ref": "main", "sha": "bbb222", "repo": {"full_name": "acme/widgets"}}}, ` + fjRepo + `}`
	ev, err := Parse(configfile.ForgeGitea, gt("pull_request"), []byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if ev.Kind != KindPullRequest || ev.Action != "synchronize" || ev.Delivery != "gt-1" || ev.Account != "acme" {
		t.Fatalf("event = %+v; Gitea's synchronized is GitHub's synchronize", ev)
	}
	if ev.PullRequest.HeadSHA != "aaa111" || !ev.PullRequest.AuthorIsBot || ev.Repository.Visibility != "private" {
		t.Fatalf("pr = %+v, repo = %+v; Gitea types a bot user Bot", ev.PullRequest, ev.Repository)
	}
	if ev, _ := Parse(configfile.ForgeGitea, fj("pull_request"), []byte(body)); ev.Kind != KindIgnored {
		t.Fatalf("a delivery without Gitea's headers = %+v, want it ignored", ev)
	}
}

func TestParseGiteaComment(t *testing.T) {
	onPull := `{"action": "created", "is_pull": true, "issue": {"number": 7},
	  "comment": {"id": 55, "body": "@kritika review", "user": {"login": "dev"}}, ` + fjRepo + `}`
	ev, err := Parse(configfile.ForgeGitea, gt("issue_comment"), []byte(onPull))
	if err != nil || ev.Kind != KindComment || ev.Comment.ID != 55 || ev.Comment.Number != 7 || ev.Delivery != "gt-1" {
		t.Fatalf("event = %+v, %v", ev, err)
	}
}

// TestParseGiteaRename: Gitea names a renamed repository's old name at
// the top of its changes, not under changes.repository as GitHub does.
func TestParseGiteaRename(t *testing.T) {
	renamed := `{"action": "renamed", "changes": {"name": {"from": "gadgets"}}, ` + fjRepo + `}`
	ev, err := Parse(configfile.ForgeGitea, gt("repository"), []byte(renamed))
	if err != nil || ev.Kind != KindRepository || ev.Action != "renamed" || ev.Previous != "acme/gadgets" {
		t.Fatalf("repository = %+v, %v; want the old name acme/gadgets", ev, err)
	}
}

func TestVerifyGitea(t *testing.T) {
	const secret = "It's a Secret to Everybody"
	sig := hmacSHA256Hex(secret)
	body := []byte(canonicalBody)
	for _, tt := range []struct {
		name    string
		header  http.Header
		wantErr error
	}{
		{"valid", http.Header{"X-Gitea-Signature": {sig}}, nil},
		{"wrong secret", http.Header{"X-Gitea-Signature": {hmacSHA256Hex("x")}}, ErrSignatureMismatch},
		{"forgejo's header alone", http.Header{"X-Forgejo-Signature": {sig}}, ErrMissingSignature},
		{"github's prefix", http.Header{"X-Gitea-Signature": {"sha256=" + sig}}, ErrSignatureMismatch},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if err := Verify(configfile.ForgeGitea, secret, tt.header, body); !errors.Is(err, tt.wantErr) {
				t.Fatalf("Verify = %v, want %v", err, tt.wantErr)
			}
		})
	}
	if !Delivered(configfile.ForgeGitea, gt("push")) || Delivered(configfile.ForgeGitea, http.Header{"X-Gitea-Delivery": {"d"}}) ||
		Delivered(configfile.ForgeGitea, fj("push")) {
		t.Fatal("a Gitea delivery carries its own delivery and event headers")
	}
}
