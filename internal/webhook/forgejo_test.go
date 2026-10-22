package webhook

import (
	"errors"
	"net/http"
	"testing"

	"github.com/home-operations/kritika/internal/configfile"
)

func fj(event string) http.Header {
	h := http.Header{}
	h.Set("X-Forgejo-Event", event)
	h.Set("X-Forgejo-Delivery", "fj-1")
	h.Set("Content-Type", "application/json")
	return h
}

const fjRepo = `"repository": {"full_name": "acme/widgets", "default_branch": "main", "private": true, "internal": false,
    "owner": {"login": "acme"}}`

func TestParseForgejoPullRequest(t *testing.T) {
	body := `{"action": "synchronized", "number": 7, "pull_request": {
    "number": 7, "title": "feat", "body": "b", "state": "open", "draft": false, "merged": false,
    "html_url": "https://forge.example.com/acme/widgets/pulls/7", "created_at": "2026-10-20T10:00:00Z", "updated_at": "2026-10-20T11:00:00Z",
    "user": {"login": "renovate-bot"},
    "head": {"ref": "renovate/x", "sha": "aaa111", "repo": {"full_name": "acme/widgets"}},
    "base": {"ref": "main", "sha": "bbb222", "repo": {"full_name": "acme/widgets"}},
    "labels": [{"name": "deps", "color": "0e8a16"}]}, ` + fjRepo + `}`
	ev, err := Parse(configfile.ForgeForgejo, fj("pull_request"), []byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if ev.Kind != KindPullRequest || ev.Action != "synchronize" || ev.Delivery != "fj-1" || ev.Account != "acme" {
		t.Fatalf("event = %+v; Forgejo's synchronized is GitHub's synchronize", ev)
	}
	if ev.PullRequest.Number != 7 || ev.PullRequest.HeadSHA != "aaa111" || ev.PullRequest.AuthorIsBot || ev.PullRequest.Fork {
		t.Fatalf("pr = %+v", ev.PullRequest)
	}
	if ev.Repository.Visibility != "private" || ev.Repository.DefaultBranch != "main" {
		t.Fatalf("repo = %+v; a private repository's visibility is read from its flag", ev.Repository)
	}
	for action, want := range map[string]string{"opened": "opened", "label_updated": "labeled", "label_cleared": "unlabeled", "closed": "closed", "reviewed": "reviewed"} {
		ev, err := Parse(configfile.ForgeForgejo, fj("pull_request"), []byte(`{"action": "`+action+`", "pull_request": {"number": 1, "user": {"login": "x"}}, `+fjRepo+`}`))
		if err != nil || ev.Action != want {
			t.Fatalf("action %s = %q, %v; want %q", action, ev.Action, err, want)
		}
	}
	internal := `{"action": "opened", "pull_request": {"number": 1, "user": {"login": "x"}},
	  "repository": {"full_name": "acme/widgets", "private": false, "internal": true, "owner": {"login": "acme"}}}`
	if ev, _ := Parse(configfile.ForgeForgejo, fj("pull_request"), []byte(internal)); ev.Repository.Visibility != "internal" {
		t.Fatalf("visibility = %q, want internal", ev.Repository.Visibility)
	}
	public := `{"action": "opened", "pull_request": {"number": 1, "user": {"login": "x"}},
	  "repository": {"full_name": "acme/widgets", "owner": {"login": "acme"}}}`
	if ev, _ := Parse(configfile.ForgeForgejo, fj("pull_request"), []byte(public)); ev.Repository.Visibility != "public" {
		t.Fatalf("visibility = %q, want public", ev.Repository.Visibility)
	}
}

func TestParseForgejoComments(t *testing.T) {
	onPull := `{"action": "created", "is_pull": true, "issue": {"number": 7},
	  "comment": {"id": 55, "body": "@kritika review", "user": {"login": "dev"}}, ` + fjRepo + `}`
	ev, err := Parse(configfile.ForgeForgejo, fj("issue_comment"), []byte(onPull))
	if err != nil || ev.Kind != KindComment || ev.Comment.ID != 55 || ev.Comment.Number != 7 || ev.Comment.Inline || ev.Comment.Author != "dev" {
		t.Fatalf("event = %+v, %v; is_pull says the comment is on a pull request", ev, err)
	}
	onIssue := `{"action": "created", "is_pull": false, "issue": {"number": 3},
	  "comment": {"id": 56, "body": "hi", "user": {"login": "dev"}}, ` + fjRepo + `}`
	if ev, err := Parse(configfile.ForgeForgejo, fj("issue_comment"), []byte(onIssue)); err != nil || ev.Kind != KindIgnored {
		t.Fatalf("a comment on an issue = %+v, %v; want it ignored", ev, err)
	}
	// Review events carry the review's body and none of its code
	// comments, and thread events do not exist.
	for _, event := range []string{"pull_request_comment", "pull_request_approved", "pull_request_rejected", "pull_request_review_thread", "wiki"} {
		ev, err := Parse(configfile.ForgeForgejo, fj(event), []byte(`{"action": "reviewed", `+fjRepo+`}`))
		if err != nil || ev.Kind != KindIgnored || ev.Action != event {
			t.Fatalf("%s = %+v, %v; want it ignored", event, ev, err)
		}
	}
}

func TestParseForgejoPushAndRepository(t *testing.T) {
	push := `{"ref": "refs/heads/main", "before": "aaa", "after": "bbb", ` + fjRepo + `}`
	ev, err := Parse(configfile.ForgeForgejo, fj("push"), []byte(push))
	if err != nil || ev.Kind != KindPush || ev.Push.Ref != "refs/heads/main" || ev.Push.After != "bbb" || ev.Account != "acme" {
		t.Fatalf("push = %+v, %v", ev, err)
	}
	created := `{"action": "created", ` + fjRepo + `, "organization": {"login": "acme"}, "sender": {"login": "dev"}}`
	ev, err = Parse(configfile.ForgeForgejo, fj("repository"), []byte(created))
	if err != nil || ev.Kind != KindRepository || ev.Action != "created" || ev.Repository.FullName != "acme/widgets" || ev.Previous != "" {
		t.Fatalf("repository = %+v, %v", ev, err)
	}
	if ev, _ := Parse(configfile.ForgeForgejo, fj("repository"), []byte(`{"action": "starred", `+fjRepo+`}`)); ev.Kind != KindIgnored {
		t.Fatalf("an unknown repository action = %+v", ev)
	}
	if _, err := Parse(configfile.ForgeForgejo, fj("push"), []byte(`{"ref": 1}`)); err == nil {
		t.Fatal("a malformed payload must fail")
	}
}

func TestVerifyForgejo(t *testing.T) {
	const secret = "It's a Secret to Everybody"
	sig := hmacSHA256Hex(secret)
	body := []byte(canonicalBody)
	for _, tt := range []struct {
		name    string
		header  http.Header
		wantErr error
	}{
		{"valid", http.Header{"X-Forgejo-Signature": {sig}}, nil},
		{"wrong secret", http.Header{"X-Forgejo-Signature": {hmacSHA256Hex("x")}}, ErrSignatureMismatch},
		{"missing header", http.Header{"X-Hub-Signature-256": {"sha256=" + sig}}, ErrMissingSignature},
		{"github's prefix", http.Header{"X-Forgejo-Signature": {"sha256=" + sig}}, ErrSignatureMismatch},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if err := Verify(configfile.ForgeForgejo, secret, tt.header, body); !errors.Is(err, tt.wantErr) {
				t.Fatalf("Verify = %v, want %v", err, tt.wantErr)
			}
		})
	}
	if !Delivered(configfile.ForgeForgejo, fj("ping")) || Delivered(configfile.ForgeForgejo, http.Header{"X-Forgejo-Delivery": {"d"}}) ||
		Delivered(configfile.ForgeForgejo, gh("ping")) {
		t.Fatal("a Forgejo delivery carries its own delivery and event headers")
	}
}
