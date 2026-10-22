package webhook

import (
	"cmp"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Event names as GitLab puts them in X-Gitlab-Event.
const (
	glMergeRequest = "Merge Request Hook"
	glNote         = "Note Hook"
	glPush         = "Push Hook"
)

// glBotLogin matches the users GitLab makes for project and group access
// tokens, which a webhook does not mark as bots.
var glBotLogin = regexp.MustCompile(`^(project|group)_\d+_bot_`)

type glUser struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
}

func (u glUser) isBot() bool { return IsBot("", u.Username) || glBotLogin.MatchString(u.Username) }

// glProject is a project as GitLab payloads carry one. A payload says
// nothing of whether a project is archived or a fork.
type glProject struct {
	PathWithNamespace string `json:"path_with_namespace"`
	DefaultBranch     string `json:"default_branch"`
}

func (p glProject) event() *Repository {
	if p.PathWithNamespace == "" {
		return nil
	}
	return &Repository{FullName: p.PathWithNamespace, DefaultBranch: p.DefaultBranch, NoTraits: true}
}

// account is the top-level group or user a project is under, which a
// connection serves.
func (p glProject) account() string {
	account, _, _ := strings.Cut(p.PathWithNamespace, "/")
	return account
}

// glDraftChange is a change to a merge request's draft state.
type glDraftChange struct {
	Previous, Current bool
}

// glMergeRequestAction is the GitHub action a GitLab merge request event
// is, which the rest of kritika acts on: an update is a push when it says
// what the head was before, else a change of draft state or of labels, or
// an edit.
func glMergeRequestAction(action, oldrev string, draft *glDraftChange, labels bool) string {
	switch action {
	case "open":
		return "opened"
	case "reopen":
		return "reopened"
	case "close", "merge":
		return "closed"
	case "update":
		switch {
		case oldrev != "":
			return "synchronize"
		case draft != nil && draft.Previous && !draft.Current:
			return "ready_for_review"
		case draft != nil && !draft.Previous && draft.Current:
			return "converted_to_draft"
		case labels:
			return "labeled"
		}
		return "edited"
	}
	return action
}

// parseGitLab takes merge request, comment and push events. Project
// events, which a group webhook can send for a project created or
// deleted, are ignored: polling finds projects, renamed and moved ones
// too. A thread resolved sends no event.
func parseGitLab(event, delivery string, body []byte) (Event, error) {
	switch event {
	case glMergeRequest:
		return parseGitLabMergeRequest(delivery, body)
	case glNote:
		return parseGitLabNote(delivery, body)
	case glPush:
		return parseGitLabPush(delivery, body)
	}
	return Event{Kind: KindIgnored, Delivery: delivery, Action: event}, nil
}

func parseGitLabMergeRequest(delivery string, body []byte) (Event, error) {
	var p struct {
		User             glUser    `json:"user"`
		Project          glProject `json:"project"`
		ObjectAttributes struct {
			IID             int        `json:"iid"`
			Title           string     `json:"title"`
			Description     string     `json:"description"`
			State           string     `json:"state"`
			Action          string     `json:"action"`
			Draft           bool       `json:"draft"`
			AuthorID        int64      `json:"author_id"`
			SourceBranch    string     `json:"source_branch"`
			TargetBranch    string     `json:"target_branch"`
			SourceProjectID int64      `json:"source_project_id"`
			TargetProjectID int64      `json:"target_project_id"`
			URL             string     `json:"url"`
			CreatedAt       time.Time  `json:"created_at"`
			UpdatedAt       time.Time  `json:"updated_at"`
			MergedAt        *time.Time `json:"merged_at"`
			Oldrev          string     `json:"oldrev"`
			LastCommit      struct {
				ID string `json:"id"`
			} `json:"last_commit"`
		} `json:"object_attributes"`
		Labels []struct {
			Title string `json:"title"`
			Color string `json:"color"`
		} `json:"labels"`
		Changes struct {
			Draft  *glDraftChange  `json:"draft"`
			Labels json.RawMessage `json:"labels"`
		} `json:"changes"`
	}
	if err := json.Unmarshal(body, &p); err != nil {
		return Event{}, fmt.Errorf("webhook: merge request payload: %w", err)
	}
	a := p.ObjectAttributes
	pr := &PullRequest{
		Number: a.IID, Title: a.Title, Body: a.Description, Draft: a.Draft,
		State: stateOpen, Merged: a.State == "merged",
		HeadRef: a.SourceBranch, HeadSHA: a.LastCommit.ID, BaseRef: a.TargetBranch, Fork: a.SourceProjectID != a.TargetProjectID,
		URL: a.URL, CreatedAt: a.CreatedAt, UpdatedAt: a.UpdatedAt,
	}
	if a.State != "opened" {
		pr.State = "closed"
		closed := a.UpdatedAt
		if a.MergedAt != nil {
			closed = *a.MergedAt
		}
		pr.ClosedAt = &closed
	}
	// The payload names the author by id alone: the user is who acted,
	// which is the author only when the ids agree. Otherwise the author is
	// left for the forge to name.
	if p.User.ID != 0 && p.User.ID == a.AuthorID {
		pr.Author, pr.AuthorIsBot = p.User.Username, p.User.isBot()
	}
	for _, l := range p.Labels {
		pr.Labels = append(pr.Labels, Label{Name: l.Title, Color: strings.TrimPrefix(l.Color, "#")})
	}
	labels := len(p.Changes.Labels) > 0 && string(p.Changes.Labels) != "null"
	return Event{
		Kind: KindPullRequest, Action: glMergeRequestAction(a.Action, a.Oldrev, p.Changes.Draft, labels), Delivery: delivery,
		Repository: p.Project.event(), Account: p.Project.account(), PullRequest: pr,
	}, nil
}

// glNoteActions are GitLab's note actions by GitHub's name for them.
var glNoteActions = map[string]string{"create": "created", "update": "edited"}

func parseGitLabNote(delivery string, body []byte) (Event, error) {
	var p struct {
		User             glUser    `json:"user"`
		Project          glProject `json:"project"`
		ObjectAttributes struct {
			ID           int64           `json:"id"`
			Note         string          `json:"note"`
			NoteableType string          `json:"noteable_type"`
			Type         string          `json:"type"`
			LineCode     string          `json:"line_code"`
			Position     json.RawMessage `json:"position"`
			System       bool            `json:"system"`
			Action       string          `json:"action"`
		} `json:"object_attributes"`
		MergeRequest struct {
			IID int `json:"iid"`
		} `json:"merge_request"`
	}
	if err := json.Unmarshal(body, &p); err != nil {
		return Event{}, fmt.Errorf("webhook: note payload: %w", err)
	}
	a := p.ObjectAttributes
	// Notes on issues, commits and snippets are not review follow-ups, nor
	// are the notes GitLab writes to record an event.
	if a.NoteableType != "MergeRequest" || a.System {
		return Event{Kind: KindIgnored, Action: glNote, Delivery: delivery}, nil
	}
	// A note on a diff line is a diff note, a reply in its discussion too;
	// the payload says nothing of which note a reply answers.
	inline := a.Type == "DiffNote" || a.LineCode != "" || len(a.Position) > 0 && string(a.Position) != "null"
	return Event{
		Kind: KindComment, Action: cmp.Or(glNoteActions[a.Action], a.Action), Delivery: delivery,
		Repository: p.Project.event(), Account: p.Project.account(),
		Comment: &Comment{
			ID: a.ID, Number: p.MergeRequest.IID, Author: p.User.Username, AuthorIsBot: p.User.isBot(),
			Body: a.Note, Inline: inline,
		},
	}, nil
}

func parseGitLabPush(delivery string, body []byte) (Event, error) {
	var p struct {
		Ref     string    `json:"ref"`
		After   string    `json:"after"`
		Project glProject `json:"project"`
	}
	if err := json.Unmarshal(body, &p); err != nil {
		return Event{}, fmt.Errorf("webhook: push payload: %w", err)
	}
	return Event{
		Kind: KindPush, Delivery: delivery,
		Repository: p.Project.event(), Account: p.Project.account(),
		Push: &Push{Ref: p.Ref, After: p.After},
	}, nil
}
