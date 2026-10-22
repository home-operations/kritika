package forgejo

import (
	"context"
	"fmt"
	"net/http"

	"github.com/home-operations/kritika/internal/forge"
)

// reactionIDs stand in for the ids a forge gives reactions: Forgejo has
// none, it holds one reaction of a content per user, so the content is the
// identity, and the number React returns is the content's.
var reactionIDs = map[string]int64{forge.ReactionEyes: 1, forge.ReactionDone: 2, forge.ReactionFailed: 3}

func reactionID(content string) (int64, error) {
	id, ok := reactionIDs[content]
	if !ok {
		return 0, fmt.Errorf("forgejo: reaction %q is not one kritika leaves", content)
	}
	return id, nil
}

func reactionOf(id int64) (string, error) {
	for content, i := range reactionIDs {
		if i == id {
			return content, nil
		}
	}
	return "", fmt.Errorf("forgejo: reaction id %d is not one kritika leaves", id)
}

func commentReactionsPath(owner, repo string, id int64) string {
	return fmt.Sprintf("%s/issues/comments/%d/reactions", repoPath(owner, repo), id)
}

func pullReactionsPath(owner, repo string, number int) string {
	return fmt.Sprintf("%s/issues/%d/reactions", repoPath(owner, repo), number)
}

func (c *Client) react(ctx context.Context, path, content string) (int64, error) {
	id, err := reactionID(content)
	if err != nil {
		return 0, err
	}
	if err := c.do(ctx, http.MethodPost, path, reactionContent{Content: content}, nil); err != nil {
		return 0, err
	}
	return id, nil
}

func (c *Client) unreact(ctx context.Context, path string, id int64) error {
	content, err := reactionOf(id)
	if err != nil {
		return err
	}
	return c.do(ctx, http.MethodDelete, path, reactionContent{Content: content}, nil)
}

// ownReaction is the id of the bot's reaction of content at path, 0 when
// it has none.
func (c *Client) ownReaction(ctx context.Context, path, content string) (int64, error) {
	login, err := c.BotLogin(ctx)
	if err != nil {
		return 0, err
	}
	var reactions []reaction
	if err := c.do(ctx, http.MethodGet, path, nil, &reactions); err != nil {
		return 0, err
	}
	for _, r := range reactions {
		if r.Content == content && r.User.Login == login {
			return reactionID(content)
		}
	}
	return 0, nil
}

// React implements forge.Client. A code comment's reactions are a
// comment's on Forgejo, whichever kind it is.
func (c *Client) React(ctx context.Context, owner, repo string, to forge.Comment, content string) (int64, error) {
	id, err := c.react(ctx, commentReactionsPath(owner, repo, to.ID), content)
	if err != nil {
		return 0, fmt.Errorf("forgejo: react to comment %d: %w", to.ID, err)
	}
	return id, nil
}

// Unreact implements forge.Client.
func (c *Client) Unreact(ctx context.Context, owner, repo string, from forge.Comment, id int64) error {
	if err := c.unreact(ctx, commentReactionsPath(owner, repo, from.ID), id); err != nil {
		return fmt.Errorf("forgejo: remove reaction %d from comment %d: %w", id, from.ID, err)
	}
	return nil
}

// ReactToPullRequest implements forge.Client.
func (c *Client) ReactToPullRequest(ctx context.Context, owner, repo string, number int, content string) (int64, error) {
	id, err := c.react(ctx, pullReactionsPath(owner, repo, number), content)
	if err != nil {
		return 0, fmt.Errorf("forgejo: react to #%d: %w", number, err)
	}
	return id, nil
}

// UnreactToPullRequest implements forge.Client.
func (c *Client) UnreactToPullRequest(ctx context.Context, owner, repo string, number int, id int64) error {
	if err := c.unreact(ctx, pullReactionsPath(owner, repo, number), id); err != nil {
		return fmt.Errorf("forgejo: remove reaction %d from #%d: %w", id, number, err)
	}
	return nil
}

// Reaction implements forge.Client.
func (c *Client) Reaction(ctx context.Context, owner, repo string, on forge.Comment, content string) (int64, error) {
	id, err := c.ownReaction(ctx, commentReactionsPath(owner, repo, on.ID), content)
	if err != nil {
		return 0, fmt.Errorf("forgejo: list reactions on comment %d: %w", on.ID, err)
	}
	return id, nil
}

// PullRequestReaction implements forge.Client.
func (c *Client) PullRequestReaction(ctx context.Context, owner, repo string, number int, content string) (int64, error) {
	id, err := c.ownReaction(ctx, pullReactionsPath(owner, repo, number), content)
	if err != nil {
		return 0, fmt.Errorf("forgejo: list reactions on #%d: %w", number, err)
	}
	return id, nil
}
