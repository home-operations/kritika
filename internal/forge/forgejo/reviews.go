package forgejo

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"time"

	"github.com/home-operations/kritika/internal/forge"
)

// Review states as Forgejo reports them.
const (
	stateApproved         = "APPROVED"
	stateChangesRequested = "REQUEST_CHANGES"
)

// CreateReview implements forge.Client with a COMMENT review. A range is
// sent as Forgejo takes one, its first line and the count of lines after
// it, which an instance without range comments reads as the first line
// alone. The review's answer carries no comments, so they are listed
// after and matched by path and body, in order.
func (c *Client) CreateReview(
	ctx context.Context, owner, repo string, number int, headSHA string, comments []forge.InlineComment,
) ([]int64, error) {
	if len(comments) == 0 {
		return []int64{}, nil
	}
	req := createReview{CommitID: headSHA, Event: "COMMENT"}
	for _, cm := range comments {
		rc := createReviewComment{Path: cm.Path, Body: cm.Body, NewPosition: cm.Line}
		if cm.StartLine > 0 && cm.StartLine < cm.Line {
			rc.NewPosition, rc.ExtraLinesCount = cm.StartLine, cm.Line-cm.StartLine
		}
		req.Comments = append(req.Comments, rc)
	}
	var review pullReview
	if err := c.do(ctx, http.MethodPost, fmt.Sprintf("%s/pulls/%d/reviews", repoPath(owner, repo), number), req, &review); err != nil {
		return nil, fmt.Errorf("forgejo: review #%d: %w", number, err)
	}
	ids := make([]int64, len(comments))
	posted, err := c.reviewComments(ctx, owner, repo, number, review.ID)
	if err != nil {
		return ids, err
	}
	for _, cm := range posted {
		for i, sent := range comments {
			if ids[i] == 0 && cm.Path == sent.Path && cm.Body == sent.Body {
				ids[i] = cm.ID
				break
			}
		}
	}
	return ids, nil
}

// Approve implements forge.Client. Forgejo keeps every review submitted,
// so one of the bot's already approving the head stands as it is.
func (c *Client) Approve(ctx context.Context, owner, repo string, number int, headSHA, body string) (bool, error) {
	approvals, err := c.approvals(ctx, owner, repo, number)
	if err != nil {
		return false, err
	}
	if slices.ContainsFunc(approvals, func(r pullReview) bool { return r.CommitID == headSHA }) {
		return false, nil
	}
	req := createReview{CommitID: headSHA, Event: stateApproved, Body: body}
	if err := c.do(ctx, http.MethodPost, fmt.Sprintf("%s/pulls/%d/reviews", repoPath(owner, repo), number), req, nil); err != nil {
		return false, fmt.Errorf("forgejo: approve #%d: %w", number, err)
	}
	return true, nil
}

// DismissApprovals implements forge.Client. Forgejo lets a repository
// admin dismiss a review, so a bot without that access dismisses none.
func (c *Client) DismissApprovals(ctx context.Context, owner, repo string, number int, message string) (int, error) {
	approvals, err := c.approvals(ctx, owner, repo, number)
	if err != nil {
		return 0, err
	}
	for i, r := range approvals {
		path := fmt.Sprintf("%s/pulls/%d/reviews/%d/dismissals", repoPath(owner, repo), number, r.ID)
		if err := c.do(ctx, http.MethodPost, path, dismissReview{Message: message}, nil); err != nil {
			return i, fmt.Errorf("forgejo: dismiss review %d on #%d: %w", r.ID, number, err)
		}
	}
	return len(approvals), nil
}

// ChangesRequested implements forge.Client. Forgejo keeps every review
// submitted: where a reviewer stands is their latest one that approved or
// requested changes and was not dismissed.
func (c *Client) ChangesRequested(ctx context.Context, owner, repo string, number int) (bool, error) {
	login, err := c.BotLogin(ctx)
	if err != nil {
		return false, err
	}
	reviews, err := c.listReviews(ctx, owner, repo, number)
	if err != nil {
		return false, err
	}
	stands := map[string]string{}
	for _, r := range reviews {
		if r.Dismissed || r.User.Login == login {
			continue
		}
		switch r.State {
		case stateApproved, stateChangesRequested:
			stands[r.User.Login] = r.State
		}
	}
	for _, state := range stands {
		if state == stateChangesRequested {
			return true, nil
		}
	}
	return false, nil
}

// approvals lists the bot's reviews of the pull request that approve it
// and stand.
func (c *Client) approvals(ctx context.Context, owner, repo string, number int) ([]pullReview, error) {
	login, err := c.BotLogin(ctx)
	if err != nil {
		return nil, err
	}
	reviews, err := c.listReviews(ctx, owner, repo, number)
	if err != nil {
		return nil, err
	}
	var out []pullReview
	for _, r := range reviews {
		if r.User.Login == login && r.State == stateApproved && !r.Dismissed {
			out = append(out, r)
		}
	}
	return out, nil
}

// listReviews pages through the pull request's reviews, oldest first.
func (c *Client) listReviews(ctx context.Context, owner, repo string, number int) ([]pullReview, error) {
	reviews, err := list[pullReview](ctx, c, fmt.Sprintf("%s/pulls/%d/reviews", repoPath(owner, repo), number), nil, nil)
	if err != nil {
		return nil, fmt.Errorf("forgejo: list reviews on #%d: %w", number, err)
	}
	return reviews, nil
}

// reviewComments is the code comments of one review, which Forgejo does
// not paginate.
func (c *Client) reviewComments(ctx context.Context, owner, repo string, number int, reviewID int64) ([]pullReviewComment, error) {
	var out []pullReviewComment
	path := fmt.Sprintf("%s/pulls/%d/reviews/%d/comments", repoPath(owner, repo), number, reviewID)
	if err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, fmt.Errorf("forgejo: list the comments of review %d on #%d: %w", reviewID, number, err)
	}
	return out, nil
}

// rawInline is every code comment on the pull request, oldest first:
// Forgejo lists them under their reviews alone.
func (c *Client) rawInline(ctx context.Context, owner, repo string, number int) ([]pullReviewComment, error) {
	reviews, err := c.listReviews(ctx, owner, repo, number)
	if err != nil {
		return nil, err
	}
	var raw []pullReviewComment
	for _, r := range reviews {
		comments, err := c.reviewComments(ctx, owner, repo, number, r.ID)
		if err != nil {
			return nil, err
		}
		raw = append(raw, comments...)
	}
	sort.SliceStable(raw, func(i, j int) bool { return raw[i].CreatedAt.Before(raw[j].CreatedAt) })
	return raw, nil
}

// conversation is what Forgejo shows a code conversation as: the code
// comments of one review on one line of one side of the diff, a range
// counting as its last line.
type conversation struct {
	reviewID                   int64
	path                       string
	position, originalPosition int
}

func conversationOf(cm pullReviewComment) conversation {
	key := conversation{cm.ReviewID, cm.Path, cm.Position, cm.OriginalPosition}
	if key.position > 0 {
		key.position += cm.ExtraLinesCount
	}
	return key
}

// inlineComments is every code comment on the pull request, oldest first,
// each with the root of its conversation, and which of them are resolved.
// Forgejo keeps no reply relation: the oldest comment of a conversation is
// the root of the ones after it, which a reply joins by being posted into
// the root's review.
func (c *Client) inlineComments(ctx context.Context, owner, repo string, number int) ([]forge.Comment, map[int64]bool, error) {
	raw, err := c.rawInline(ctx, owner, repo, number)
	if err != nil {
		return nil, nil, err
	}
	roots := map[conversation]int64{}
	resolved := map[int64]bool{}
	out := make([]forge.Comment, 0, len(raw))
	for _, cm := range raw {
		key := conversationOf(cm)
		root, ok := roots[key]
		if !ok {
			roots[key] = cm.ID
			root = 0
		}
		resolved[cm.ID] = cm.Resolver != nil
		out = append(out, inlineComment(cm, root))
	}
	return out, resolved, nil
}

// GetComment implements forge.Client. A conversation comment is read by
// its id. A code comment has no endpoint of its own: the conversation
// endpoint answers one with 204, and Forgejo delivers a reply in a code
// conversation as a conversation comment, so a 204 is looked up among the
// code comments, as an inline comment is from the start.
func (c *Client) GetComment(ctx context.Context, owner, repo string, _ int, id int64, inline bool) (forge.Comment, error) {
	if !inline {
		var cm comment
		err := c.do(ctx, http.MethodGet, fmt.Sprintf("%s/issues/comments/%d", repoPath(owner, repo), id), nil, &cm)
		if err == nil {
			return conversationComment(cm), nil
		}
		if !errors.Is(err, errNoContent) {
			return forge.Comment{}, fmt.Errorf("forgejo: comment %d: %w", id, err)
		}
	}
	cm, _, err := c.findInlineComment(ctx, owner, repo, id)
	if err != nil {
		return forge.Comment{}, err
	}
	return cm, nil
}

// findInlineComment locates code comment id and the pull request it is
// on. Forgejo lists code comments under their reviews alone, and the id
// says nothing of the pull request, so the walk is over the open pull
// requests, the most recently updated first; a comment on a closed pull
// request is not found.
func (c *Client) findInlineComment(ctx context.Context, owner, repo string, id int64) (forge.Comment, int, error) {
	prs, err := c.ListOpenPullRequests(ctx, owner, repo, time.Time{})
	if err != nil {
		return forge.Comment{}, 0, err
	}
	for _, pr := range prs {
		comments, _, err := c.inlineComments(ctx, owner, repo, pr.Number)
		if err != nil {
			return forge.Comment{}, 0, err
		}
		for _, cm := range comments {
			if cm.ID == id {
				return cm, pr.Number, nil
			}
		}
	}
	return forge.Comment{}, 0, fmt.Errorf("forgejo: code comment %d of %s/%s: %w", id, owner, repo, forge.ErrNotFound)
}

// ListConversation implements forge.Client. Forgejo lists conversation
// comments alone here, code comments being their reviews'.
func (c *Client) ListConversation(ctx context.Context, owner, repo string, number int) ([]forge.Comment, error) {
	var raw []comment
	if err := c.do(ctx, http.MethodGet, fmt.Sprintf("%s/issues/%d/comments", repoPath(owner, repo), number), nil, &raw); err != nil {
		return nil, fmt.Errorf("forgejo: list comments on #%d: %w", number, err)
	}
	out := make([]forge.Comment, 0, len(raw))
	for _, cm := range raw {
		out = append(out, conversationComment(cm))
	}
	// Stable: Forgejo's times are whole seconds, and it lists comments made
	// in the same second in the order they were made.
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

// ListInline implements forge.Client.
func (c *Client) ListInline(ctx context.Context, owner, repo string, number int) ([]forge.Comment, error) {
	comments, _, err := c.inlineComments(ctx, owner, repo, number)
	return comments, err
}

// ReplyInline implements forge.Client. Forgejo has no reply relation: a
// reply is a code comment on the thread's line posted into the review of
// the comment replied to, which Forgejo shows in that conversation, as
// its own reply form does. Gitea has a route that posts the same comment.
func (c *Client) ReplyInline(ctx context.Context, owner, repo string, number int, to forge.Comment, body string) (int64, error) {
	if c.gitea {
		var posted pullReviewComment
		path := fmt.Sprintf("%s/pulls/%d/comments/%d/replies", repoPath(owner, repo), number, to.ID)
		if err := c.do(ctx, http.MethodPost, path, commentBody{Body: body}, &posted); err != nil {
			return 0, fmt.Errorf("forgejo: reply to code comment %d on #%d: %w", to.ID, number, err)
		}
		return posted.ID, nil
	}
	raw, err := c.rawInline(ctx, owner, repo, number)
	if err != nil {
		return 0, err
	}
	i := slices.IndexFunc(raw, func(cm pullReviewComment) bool { return cm.ID == to.ID })
	if i < 0 {
		return 0, fmt.Errorf("forgejo: reply to code comment %d on #%d: %w", to.ID, number, forge.ErrNotFound)
	}
	// The reply sits where the target does: its side, line and range.
	target := raw[i]
	req := createReviewComment{Path: target.Path, Body: body, NewPosition: target.Position, OldPosition: target.OriginalPosition,
		ExtraLinesCount: target.ExtraLinesCount}
	if target.Position > 0 {
		req.OldPosition = 0
	}
	var posted pullReviewComment
	path := fmt.Sprintf("%s/pulls/%d/reviews/%d/comments", repoPath(owner, repo), number, target.ReviewID)
	if err := c.do(ctx, http.MethodPost, path, req, &posted); err != nil {
		return 0, fmt.Errorf("forgejo: reply to code comment %d on #%d: %w", to.ID, number, err)
	}
	return posted.ID, nil
}

// ThreadResolved implements forge.Client from the code comment's resolver,
// who resolved its conversation, set while it is resolved.
func (c *Client) ThreadResolved(ctx context.Context, owner, repo string, number int, id int64) (bool, bool, error) {
	_, resolved, err := c.inlineComments(ctx, owner, repo, number)
	if err != nil {
		return false, false, err
	}
	state, found := resolved[id]
	return state, found, nil
}

// ResolveThread implements forge.Client. Forgejo's API has no call that
// resolves a conversation, so none is resolved there. Gitea resolves one
// by its root, the comment its timeline shows the conversation's state
// on; its diff view shows the oldest comment on the line instead, which
// is another review's when an earlier one commented there.
func (c *Client) ResolveThread(ctx context.Context, owner, repo string, number int, id int64, onlyOwn bool) (bool, error) {
	if !c.gitea {
		return false, forge.ErrUnsupported
	}
	comments, resolved, err := c.inlineComments(ctx, owner, repo, number)
	if err != nil {
		return false, err
	}
	i := slices.IndexFunc(comments, func(cm forge.Comment) bool { return cm.ID == id })
	if i < 0 || comments[i].InReplyTo != 0 || resolved[id] {
		return false, nil
	}
	if onlyOwn {
		login, err := c.BotLogin(ctx)
		if err != nil {
			return false, err
		}
		if slices.ContainsFunc(comments, func(cm forge.Comment) bool {
			return (cm.ID == id || cm.InReplyTo == id) && cm.Author != login
		}) {
			return false, nil
		}
	}
	if err := c.do(ctx, http.MethodPost, fmt.Sprintf("%s/pulls/comments/%d/resolve", repoPath(owner, repo), id), nil, nil); err != nil {
		return false, fmt.Errorf("forgejo: resolve the conversation of code comment %d on #%d: %w", id, number, err)
	}
	return true, nil
}
