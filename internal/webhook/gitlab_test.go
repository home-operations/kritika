package webhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/home-operations/kritika/internal/configfile"
)

const glToken = "whsec_c2lnbmluZy1rZXk="

func gl(event string) http.Header {
	h := http.Header{}
	h.Set("X-Gitlab-Event", event)
	h.Set("webhook-id", "msg-1")
	h.Set("Content-Type", "application/json")
	return h
}

// glSign signs body as GitLab does with glToken at stamp.
func glSign(h http.Header, body string, stamp time.Time) {
	key, _ := base64.StdEncoding.DecodeString("c2lnbmluZy1rZXk=")
	ts := strconv.FormatInt(stamp.Unix(), 10)
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(h.Get("webhook-id") + "." + ts + "." + body))
	h.Set("webhook-timestamp", ts)
	h.Set("webhook-signature", "v1,"+base64.StdEncoding.EncodeToString(mac.Sum(nil)))
}

func TestVerifyGitLab(t *testing.T) {
	now := time.Now()
	body := `{"object_kind":"push"}`
	tests := []struct {
		name string
		edit func(h http.Header)
		want error
	}{
		{"signed now", func(http.Header) {}, nil},
		{"one of several signatures", func(h http.Header) { h.Set("webhook-signature", "v1,AAAA "+h.Get("webhook-signature")) }, nil},
		{"unsigned", func(h http.Header) { h.Del("webhook-signature") }, ErrMissingSignature},
		{"another body's", func(h http.Header) { glSign(h, `{}`, now) }, ErrSignatureMismatch},
		{"another id", func(h http.Header) { h.Set("webhook-id", "msg-2") }, ErrSignatureMismatch},
		{"no id", func(h http.Header) { h.Del("webhook-id") }, ErrSignatureMismatch},
		{"too old", func(h http.Header) { glSign(h, body, now.Add(-10*time.Minute)) }, ErrSignatureMismatch},
		{"from the future", func(h http.Header) { glSign(h, body, now.Add(10*time.Minute)) }, ErrSignatureMismatch},
		{"another version", func(h http.Header) { h.Set("webhook-signature", "v2,"+h.Get("webhook-signature")[3:]) }, ErrSignatureMismatch},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := gl("Push Hook")
			glSign(h, body, now)
			tt.edit(h)
			if err := Verify(configfile.ForgeGitLab, glToken, h, []byte(body)); !errors.Is(err, tt.want) {
				t.Fatalf("Verify = %v, want %v", err, tt.want)
			}
		})
	}
	h := gl("Push Hook")
	glSign(h, body, now)
	if err := Verify(configfile.ForgeGitLab, "plain-secret", h, []byte(body)); err == nil {
		t.Fatal("a secret that is no signing token must not verify")
	}
	if !Delivered(configfile.ForgeGitLab, gl("Push Hook")) || Delivered(configfile.ForgeGitLab, http.Header{}) {
		t.Fatal("Delivered must read GitLab's own headers")
	}
}

const glProjectJSON = `"project": {"id": 1, "path_with_namespace": "acme/platform/widgets", "default_branch": "main", "visibility_level": 0}`

func TestParseGitLabMergeRequest(t *testing.T) {
	body := `{"object_kind": "merge_request", "user": {"id": 3, "username": "project_9_bot_abc"}, ` + glProjectJSON + `,
	  "object_attributes": {"iid": 7, "title": "feat", "description": "b", "state": "opened", "action": "update", "draft": false,
	    "author_id": 3, "source_branch": "feat", "target_branch": "main", "source_project_id": 2, "target_project_id": 1,
	    "url": "https://gitlab.example.com/acme/platform/widgets/-/merge_requests/7", "oldrev": "000aaa",
	    "created_at": "2026-10-20T10:00:00.000Z", "updated_at": "2026-10-20T11:00:00.000Z", "last_commit": {"id": "aaa111"}},
	  "labels": [{"title": "deps", "color": "#0e8a16"}], "changes": {}}`
	ev, err := Parse(configfile.ForgeGitLab, gl("Merge Request Hook"), []byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if ev.Kind != KindPullRequest || ev.Action != "synchronize" || ev.Delivery != "msg-1" || ev.Account != "acme" {
		t.Fatalf("event = %+v; an update with an oldrev is a push", ev)
	}
	pr := ev.PullRequest
	if pr.Number != 7 || pr.HeadSHA != "aaa111" || pr.State != "open" || !pr.Fork || pr.Author != "project_9_bot_abc" || !pr.AuthorIsBot ||
		pr.UpdatedAt.IsZero() || len(pr.Labels) != 1 || pr.Labels[0] != (Label{Name: "deps", Color: "0e8a16"}) {
		t.Fatalf("pr = %+v", pr)
	}
	if r := ev.Repository; r.FullName != "acme/platform/widgets" || r.DefaultBranch != "main" || !r.NoTraits {
		t.Fatalf("repo = %+v; a GitLab payload names no traits", r)
	}

	// Someone else acting leaves the author for the forge to name.
	other := `{"user": {"id": 4, "username": "ann"}, ` + glProjectJSON + `, "object_attributes": {"iid": 7, "action": "open", "state": "opened", "author_id": 3}}`
	if ev, _ := Parse(configfile.ForgeGitLab, gl("Merge Request Hook"), []byte(other)); ev.PullRequest.Author != "" || ev.Action != "opened" {
		t.Fatalf("pr = %+v, action %q", ev.PullRequest, ev.Action)
	}

	for _, tt := range []struct{ action, changes, state, want string }{
		{"reopen", `{}`, "opened", "reopened"},
		{"close", `{}`, "closed", "closed"},
		{"merge", `{}`, "merged", "closed"},
		{"update", `{"draft": {"previous": true, "current": false}}`, "opened", "ready_for_review"},
		{"update", `{"draft": {"previous": false, "current": true}}`, "opened", "converted_to_draft"},
		{"update", `{"labels": {"previous": [], "current": [{"title": "x"}]}}`, "opened", "labeled"},
		{"update", `{"title": {"previous": "a", "current": "b"}}`, "opened", "edited"},
		{"approved", `{}`, "opened", "approved"},
	} {
		body := `{"user": {"id": 3, "username": "bob"}, ` + glProjectJSON + `, "changes": ` + tt.changes + `,
		  "object_attributes": {"iid": 7, "action": "` + tt.action + `", "state": "` + tt.state + `", "author_id": 3,
		    "updated_at": "2026-10-20T11:00:00.000Z", "merged_at": null}}`
		ev, err := Parse(configfile.ForgeGitLab, gl("Merge Request Hook"), []byte(body))
		if err != nil || ev.Action != tt.want {
			t.Fatalf("%s %s = %q, %v; want %q", tt.action, tt.changes, ev.Action, err, tt.want)
		}
		if closed := tt.state != "opened"; closed != (ev.PullRequest.State == "closed") || closed != (ev.PullRequest.ClosedAt != nil) ||
			ev.PullRequest.Merged != (tt.state == "merged") {
			t.Fatalf("%s: pr = %+v", tt.action, ev.PullRequest)
		}
	}
}

func TestParseGitLabNote(t *testing.T) {
	note := func(attrs string) string {
		return `{"user": {"id": 4, "username": "ann"}, ` + glProjectJSON + `, "merge_request": {"iid": 7},
		  "object_attributes": {"id": 99, "note": "@kritika why?", "noteable_type": "MergeRequest", "action": "create", ` + attrs + `}}`
	}
	ev, err := Parse(configfile.ForgeGitLab, gl("Note Hook"), []byte(note(`"type": "DiffNote", "line_code": "abc_1_2"`)))
	if err != nil {
		t.Fatal(err)
	}
	c := ev.Comment
	if ev.Kind != KindComment || ev.Action != "created" || ev.Account != "acme" || c.ID != 99 || c.Number != 7 || c.Author != "ann" ||
		c.AuthorIsBot || !c.Inline || c.InReplyTo != 0 || c.Body != "@kritika why?" {
		t.Fatalf("event = %+v, comment = %+v", ev, c)
	}
	for attrs, inline := range map[string]bool{`"type": null`: false, `"line_code": "abc_1_2"`: true, `"position": {"new_line": 2}`: true, `"position": null`: false} {
		if ev, _ := Parse(configfile.ForgeGitLab, gl("Note Hook"), []byte(note(attrs))); ev.Comment.Inline != inline {
			t.Errorf("%s: inline = %v", attrs, ev.Comment.Inline)
		}
	}
	if ev, _ := Parse(configfile.ForgeGitLab, gl("Note Hook"), []byte(note(`"system": false`))); ev.Kind != KindComment {
		t.Fatal("a person's note is a comment")
	}
	for _, attrs := range []string{`"system": true`, `"noteable_type": "Issue"`} {
		body := `{"user": {"username": "ann"}, ` + glProjectJSON + `, "object_attributes": {"id": 1, "noteable_type": "MergeRequest", ` + attrs + `}}`
		if ev, _ := Parse(configfile.ForgeGitLab, gl("Note Hook"), []byte(body)); ev.Kind != KindIgnored {
			t.Errorf("%s: kind = %s", attrs, ev.Kind)
		}
	}
	if ev, _ := Parse(configfile.ForgeGitLab, gl("Note Hook"), []byte(note(`"action": "update"`))); ev.Action != "edited" {
		t.Fatalf("an update is an edit: %q", ev.Action)
	}
}

func TestParseGitLabPushAndOthers(t *testing.T) {
	body := `{"object_kind": "push", "ref": "refs/heads/main", "before": "a", "after": "b", ` + glProjectJSON + `}`
	ev, err := Parse(configfile.ForgeGitLab, gl("Push Hook"), []byte(body))
	if err != nil || ev.Kind != KindPush || ev.Push.Ref != "refs/heads/main" || ev.Push.After != "b" || ev.Account != "acme" ||
		ev.Repository.FullName != "acme/platform/widgets" {
		t.Fatalf("event = %+v, %v", ev, err)
	}
	for _, event := range []string{"Tag Push Hook", "Emoji Hook", "Pipeline Hook"} {
		if ev, err := Parse(configfile.ForgeGitLab, gl(event), []byte(`{}`)); err != nil || ev.Kind != KindIgnored {
			t.Errorf("%s = %+v, %v", event, ev, err)
		}
	}
}
