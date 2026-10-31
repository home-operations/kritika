package gitlab

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	gl "gitlab.com/gitlab-org/api/client-go/v3"

	"github.com/home-operations/kritika/internal/forge"
	"github.com/home-operations/kritika/internal/udiff"
)

// Note types as GitLab reports them, and the reviewer state that stands
// for changes requested.
const (
	noteDiff         = "DiffNote"
	requestedChanges = "requested_changes"
)

// suggestionFence opens a suggestion block as kritika writes one, without
// the line offsets GitLab's form of the fence takes.
var suggestionFence = regexp.MustCompile("(?m)^```suggestion$")

// lineAt is where a line of the head side of a diff is on the base side:
// zero for a line the push added, else its base-side number, which a
// position on an unchanged line must name too.
func lineAt(files []udiff.File, path string, line int) (old int, found bool) {
	for _, f := range files {
		if f.NewPath != path {
			continue
		}
		for _, h := range f.Hunks {
			for _, l := range h.Lines {
				if l.NewLine == line && (l.Kind == '+' || l.Kind == ' ') {
					if l.Kind == ' ' {
						return l.OldLine, true
					}
					return 0, true
				}
			}
		}
	}
	return 0, false
}

// position is a text position on the head side of the version's diff.
func position(v *diffVersion, files []udiff.File, path string, line int) *gl.PositionOptions {
	p := &gl.PositionOptions{
		BaseSHA: new(v.BaseCommitSHA), StartSHA: new(v.StartCommitSHA), HeadSHA: new(v.HeadCommitSHA),
		PositionType: new("text"), NewPath: new(path), OldPath: new(path), NewLine: new(int64(line)),
	}
	for _, d := range v.Diffs {
		if d.NewPath == path && d.OldPath != "" {
			p.OldPath = new(d.OldPath)
		}
	}
	if old, ok := lineAt(files, path, line); ok && old > 0 {
		p.OldLine = new(int64(old))
	}
	return p
}

// inlineBody is the comment's body for a range. GitLab anchors a comment on
// one line, so a range is anchored on its last. A suggestion fence is given
// the lines above it to cover, as GitLab's syntax has it.
func inlineBody(cm forge.InlineComment) string {
	if cm.StartLine <= 0 || cm.StartLine >= cm.Line {
		return cm.Body
	}
	return suggestionFence.ReplaceAllLiteralString(cm.Body, fmt.Sprintf("```suggestion:-%d+0", cm.Line-cm.StartLine))
}

// normalized is a note's body as GitLab stores one: without carriage
// returns or trailing whitespace.
func normalized(body string) string {
	return strings.TrimRight(strings.ReplaceAll(body, "\r", ""), " \t\n")
}

// CreateReview implements forge.Client with draft notes published at once,
// which GitLab notifies of as one review. Publishing answers with no notes.
// So they are listed after and matched by path and body, in order, among the
// ones that were not there before. Publishing publishes every pending draft
// of the bot's on the merge request. So any left by a review that failed
// before are discarded first. Two reviews of one merge request at once
// publish each other's, which the worker's publication lock rules out.
func (c *Client) CreateReview(
	ctx context.Context, owner, repo string, number int, headSHA string, comments []forge.InlineComment,
) ([]int64, error) {
	if len(comments) == 0 {
		return []int64{}, nil
	}
	pid := project(owner, repo)
	v, err := c.versionAt(ctx, pid, number, headSHA)
	if err != nil {
		return nil, fmt.Errorf("gitlab: review !%d: %w", number, err)
	}
	full, err := c.version(ctx, pid, number, v.ID)
	if err != nil {
		return nil, fmt.Errorf("gitlab: review !%d: %w", number, err)
	}
	files := udiff.Parse(unifiedDiff(full.Diffs))
	login, err := c.BotLogin(ctx)
	if err != nil {
		return nil, err
	}
	before, _, err := c.inlineComments(ctx, owner, repo, number)
	if err != nil {
		return nil, err
	}
	stale, err := list(func(lo gl.ListOptions) ([]*gl.DraftNote, *gl.Response, error) {
		return c.api.DraftNotes.ListDraftNotes(pid, int64(number), &gl.ListDraftNotesOptions{ListOptions: lo}, gl.WithContext(ctx))
	})
	if err != nil {
		return nil, fmt.Errorf("gitlab: review !%d: list drafts: %w", number, apiError(err))
	}
	for _, d := range stale {
		if _, err := c.api.DraftNotes.DeleteDraftNote(pid, int64(number), d.ID, gl.WithContext(ctx)); err != nil {
			return nil, fmt.Errorf("gitlab: review !%d: discard draft %d: %w", number, d.ID, apiError(err))
		}
	}
	var drafts []int64
	for _, cm := range comments {
		opts := &gl.CreateDraftNoteOptions{Note: new(inlineBody(cm)), Position: position(full, files, cm.Path, cm.Line)}
		d, _, err := c.api.DraftNotes.CreateDraftNote(pid, int64(number), opts, gl.WithContext(ctx))
		if err != nil {
			c.discardDrafts(ctx, pid, number, drafts)
			return nil, fmt.Errorf("gitlab: review !%d: draft note on %s:%d: %w", number, cm.Path, cm.Line, apiError(err))
		}
		drafts = append(drafts, d.ID)
	}
	if _, err := c.api.DraftNotes.PublishAllDraftNotes(pid, int64(number), gl.WithContext(ctx)); err != nil {
		c.discardDrafts(ctx, pid, number, drafts)
		return nil, fmt.Errorf("gitlab: review !%d: publish: %w", number, apiError(err))
	}
	ids := make([]int64, len(comments))
	posted, _, err := c.inlineComments(ctx, owner, repo, number)
	if err != nil {
		return ids, err
	}
	known := map[int64]bool{}
	for _, cm := range before {
		known[cm.ID] = true
	}
	for _, cm := range posted {
		if known[cm.ID] || cm.Author != login || cm.InReplyTo != 0 {
			continue
		}
		for i, sent := range comments {
			if ids[i] == 0 && cm.Path == sent.Path && cm.Body == normalized(inlineBody(sent)) {
				ids[i] = cm.ID
				break
			}
		}
	}
	return ids, nil
}

// discardTimeout bounds the discarding of a failed review's drafts, which
// runs on even after the review's context ends.
const discardTimeout = 30 * time.Second

// discardDrafts deletes the draft notes of a review that was not
// published, so a later review does not publish them with its own. Best
// effort: a draft left behind is discarded by the next review.
func (c *Client) discardDrafts(ctx context.Context, pid string, number int, ids []int64) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), discardTimeout)
	defer cancel()
	for _, id := range ids {
		_, _ = c.api.DraftNotes.DeleteDraftNote(pid, int64(number), id, gl.WithContext(ctx))
	}
}

// approved is whether the bot's approval of the merge request stands.
func (c *Client) approved(ctx context.Context, pid string, number int) (bool, error) {
	login, err := c.BotLogin(ctx)
	if err != nil {
		return false, err
	}
	a, _, err := c.api.MergeRequestApprovals.GetConfiguration(pid, int64(number), gl.WithContext(ctx))
	if err != nil {
		return false, fmt.Errorf("gitlab: approvals of !%d: %w", number, apiError(err))
	}
	return slices.ContainsFunc(a.ApprovedBy, func(u *gl.MergeRequestApproverUser) bool {
		return u.User != nil && u.User.Username == login
	}), nil
}

// Approve implements forge.Client. An approval names the head it is of, so a
// push after it is not approved. One standing is left as it is, since GitLab
// withdraws approvals on push unless the project keeps them. GitLab's
// approval carries no body, so one is posted as a note first.
func (c *Client) Approve(ctx context.Context, owner, repo string, number int, headSHA, body string) (bool, error) {
	pid := project(owner, repo)
	standing, err := c.approved(ctx, pid, number)
	if err != nil || standing {
		return false, err
	}
	if body != "" {
		if _, err := c.CreateComment(ctx, owner, repo, number, body); err != nil {
			return false, err
		}
	}
	_, _, err = c.api.MergeRequestApprovals.ApproveMergeRequest(pid, int64(number),
		&gl.ApproveMergeRequestOptions{SHA: new(headSHA)}, gl.WithContext(ctx))
	if err != nil {
		return false, fmt.Errorf("gitlab: approve !%d: %w", number, apiError(err))
	}
	return true, nil
}

// DismissApprovals implements forge.Client. GitLab holds one approval per
// user, withdrawn without a word, so the message is posted as a note.
func (c *Client) DismissApprovals(ctx context.Context, owner, repo string, number int, message string) (int, error) {
	pid := project(owner, repo)
	standing, err := c.approved(ctx, pid, number)
	if err != nil || !standing {
		return 0, err
	}
	if _, err := c.api.MergeRequestApprovals.UnapproveMergeRequest(pid, int64(number), gl.WithContext(ctx)); err != nil {
		return 0, fmt.Errorf("gitlab: unapprove !%d: %w", number, apiError(err))
	}
	if message != "" {
		if _, err := c.CreateComment(ctx, owner, repo, number, message); err != nil {
			return 1, err
		}
	}
	return 1, nil
}

// ChangesRequested implements forge.Client from the reviewers' states.
func (c *Client) ChangesRequested(ctx context.Context, owner, repo string, number int) (bool, error) {
	login, err := c.BotLogin(ctx)
	if err != nil {
		return false, err
	}
	reviewers, _, err := c.api.MergeRequests.GetMergeRequestReviewers(project(owner, repo), int64(number), gl.WithContext(ctx))
	if err != nil {
		return false, fmt.Errorf("gitlab: reviewers of !%d: %w", number, apiError(err))
	}
	for _, r := range reviewers {
		if r.State == requestedChanges && (r.User == nil || r.User.Username != login) {
			return true, nil
		}
	}
	return false, nil
}

// discussions is every discussion of the merge request, as GitLab lists
// them, and records which pull request their notes are on.
func (c *Client) discussions(ctx context.Context, owner, repo string, number int) ([]*gl.Discussion, error) {
	out, err := list(func(lo gl.ListOptions) ([]*gl.Discussion, *gl.Response, error) {
		return c.api.Discussions.ListMergeRequestDiscussions(project(owner, repo), int64(number),
			&gl.ListMergeRequestDiscussionsOptions{ListOptions: lo}, gl.WithContext(ctx))
	})
	if err != nil {
		return nil, fmt.Errorf("gitlab: discussions of !%d: %w", number, apiError(err))
	}
	for _, d := range out {
		for _, n := range d.Notes {
			c.remember(number, n.ID)
		}
	}
	return out, nil
}

// comment maps a note that replies to root, 0 when it is one. A diff note is
// outdated when its position names other refs than the merge request now has.
// GitLab moves a note's position with a push that keeps its lines, so one
// left behind was on lines that the push changed.
func (c *Client) comment(ctx context.Context, n *gl.Note, number int, root int64, refs gl.MergeRequestDiffRefs) forge.Comment {
	out := forge.Comment{
		ID: n.ID, Number: number, Author: n.Author.Username, Body: n.Body, CreatedAt: timeOf(n.CreatedAt), InReplyTo: root,
		AuthorIsBot: c.isBot(ctx, &gl.BasicUser{ID: n.Author.ID, Username: n.Author.Username}),
	}
	if n.Type == noteDiff && n.Position != nil {
		p := n.Position
		out.Inline, out.Path, out.Line = true, p.NewPath, int(p.NewLine)
		out.Outdated = refs.HeadSha != "" && (p.BaseSHA != refs.BaseSha || p.StartSHA != refs.StartSha || p.HeadSHA != refs.HeadSha)
	}
	return out
}

// discussionResolved is whether a discussion stands resolved: every note
// of it that can be resolved is, as GitLab reckons it, since a note can
// be resolved alone through the API.
func discussionResolved(d *gl.Discussion) bool {
	resolvable := false
	for _, n := range d.Notes {
		if n.Resolvable && !n.Resolved {
			return false
		}
		resolvable = resolvable || n.Resolvable
	}
	return resolvable
}

// comments is the merge request's notes, oldest first, the inline ones or the
// conversation's, each with the root of its discussion. It also says which
// roots open a resolved discussion. System notes, which record events, are
// left out.
func (c *Client) comments(ctx context.Context, owner, repo string, number int, inline bool) ([]forge.Comment, map[int64]bool, error) {
	discussions, err := c.discussions(ctx, owner, repo, number)
	if err != nil {
		return nil, nil, err
	}
	var refs gl.MergeRequestDiffRefs
	if inline {
		mr, _, err := c.api.MergeRequests.GetMergeRequest(project(owner, repo), int64(number), nil, gl.WithContext(ctx))
		if err != nil {
			return nil, nil, fmt.Errorf("gitlab: merge request !%d: %w", number, apiError(err))
		}
		refs = mr.DiffRefs
	}
	var out []forge.Comment
	resolved := map[int64]bool{}
	for _, d := range discussions {
		var root int64
		for _, n := range d.Notes {
			if n.System || (n.Type == noteDiff) != inline {
				continue
			}
			cm := c.comment(ctx, n, number, root, refs)
			if root == 0 && !d.IndividualNote {
				root = n.ID
				resolved[n.ID] = discussionResolved(d)
			}
			out = append(out, cm)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, resolved, nil
}

func (c *Client) inlineComments(ctx context.Context, owner, repo string, number int) ([]forge.Comment, map[int64]bool, error) {
	return c.comments(ctx, owner, repo, number, true)
}

// GetComment implements forge.Client. It reads the note under merge request
// number. GitLab numbers every note of a merge request alike, so one not
// among the kind asked for is looked for among the other: a webhook tells a
// diff note by fields it can leave out.
func (c *Client) GetComment(ctx context.Context, owner, repo string, number int, id int64, inline bool) (forge.Comment, error) {
	for _, kind := range []bool{inline, !inline} {
		comments, _, err := c.comments(ctx, owner, repo, number, kind)
		if err != nil {
			return forge.Comment{}, err
		}
		if i := slices.IndexFunc(comments, func(cm forge.Comment) bool { return cm.ID == id }); i >= 0 {
			return comments[i], nil
		}
	}
	return forge.Comment{}, fmt.Errorf("gitlab: note %d of %s/%s: %w", id, owner, repo, forge.ErrNotFound)
}

// ListConversation implements forge.Client.
func (c *Client) ListConversation(ctx context.Context, owner, repo string, number int) ([]forge.Comment, error) {
	comments, _, err := c.comments(ctx, owner, repo, number, false)
	return comments, err
}

// ListInline implements forge.Client.
func (c *Client) ListInline(ctx context.Context, owner, repo string, number int) ([]forge.Comment, error) {
	comments, _, err := c.inlineComments(ctx, owner, repo, number)
	return comments, err
}

// discussionOf is the discussion of the merge request that note id opened
// or is in.
func (c *Client) discussionOf(ctx context.Context, owner, repo string, number int, id int64) (*gl.Discussion, error) {
	discussions, err := c.discussions(ctx, owner, repo, number)
	if err != nil {
		return nil, err
	}
	for _, d := range discussions {
		if slices.ContainsFunc(d.Notes, func(n *gl.Note) bool { return n.ID == id }) {
			return d, nil
		}
	}
	return nil, fmt.Errorf("gitlab: discussion of note %d on !%d: %w", id, number, forge.ErrNotFound)
}

// ReplyInline implements forge.Client.
func (c *Client) ReplyInline(ctx context.Context, owner, repo string, number int, to forge.Comment, body string) (int64, error) {
	d, err := c.discussionOf(ctx, owner, repo, number, to.ID)
	if err != nil {
		return 0, err
	}
	n, _, err := c.api.Discussions.AddMergeRequestDiscussionNote(project(owner, repo), int64(number), d.ID,
		&gl.AddMergeRequestDiscussionNoteOptions{Body: new(body)}, gl.WithContext(ctx))
	if err != nil {
		return 0, fmt.Errorf("gitlab: reply to note %d on !%d: %w", to.ID, number, apiError(err))
	}
	c.remember(number, n.ID)
	return n.ID, nil
}

// ThreadResolved implements forge.Client from the discussion's first note,
// which carries the discussion's state.
func (c *Client) ThreadResolved(ctx context.Context, owner, repo string, number int, id int64) (bool, bool, error) {
	_, resolved, err := c.inlineComments(ctx, owner, repo, number)
	if err != nil {
		return false, false, err
	}
	state, found := resolved[id]
	return state, found, nil
}

// ResolveThread implements forge.Client.
func (c *Client) ResolveThread(ctx context.Context, owner, repo string, number int, id int64, onlyOwn bool) (bool, error) {
	d, err := c.discussionOf(ctx, owner, repo, number, id)
	if err != nil {
		return false, err
	}
	if len(d.Notes) == 0 || d.Notes[0].ID != id || discussionResolved(d) {
		return false, nil
	}
	if onlyOwn {
		login, err := c.BotLogin(ctx)
		if err != nil {
			return false, err
		}
		if slices.ContainsFunc(d.Notes, func(n *gl.Note) bool { return !n.System && n.Author.Username != login }) {
			return false, nil
		}
	}
	_, _, err = c.api.Discussions.ResolveMergeRequestDiscussion(project(owner, repo), int64(number), d.ID,
		&gl.ResolveMergeRequestDiscussionOptions{Resolved: new(true)}, gl.WithContext(ctx))
	if err != nil {
		return false, fmt.Errorf("gitlab: resolve discussion of note %d on !%d: %w", id, number, apiError(err))
	}
	return true, nil
}
