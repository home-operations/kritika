package gitlab

import (
	"context"
	"fmt"

	gl "gitlab.com/gitlab-org/api/client-go/v3"

	"github.com/home-operations/kritika/internal/forge"
)

// emoji names a reaction as GitLab does: GitHub's "+1" is "thumbsup".
func emoji(content string) string {
	switch content {
	case "+1":
		return "thumbsup"
	case "-1":
		return "thumbsdown"
	}
	return content
}

// awards is how reactions on one thing, a merge request or a note, are
// listed, added and removed.
type awards struct {
	list   func(gl.ListOptions) ([]*gl.AwardEmoji, *gl.Response, error)
	create func(*gl.CreateAwardEmojiOptions) (*gl.AwardEmoji, *gl.Response, error)
	remove func(int64) (*gl.Response, error)
}

func (c *Client) pullAwards(ctx context.Context, pid string, number int) awards {
	iid := int64(number)
	return awards{
		list: func(lo gl.ListOptions) ([]*gl.AwardEmoji, *gl.Response, error) {
			return c.api.AwardEmoji.ListMergeRequestAwardEmoji(pid, iid, &gl.ListAwardEmojiOptions{ListOptions: lo}, gl.WithContext(ctx))
		},
		create: func(o *gl.CreateAwardEmojiOptions) (*gl.AwardEmoji, *gl.Response, error) {
			return c.api.AwardEmoji.CreateMergeRequestAwardEmoji(pid, iid, o, gl.WithContext(ctx))
		},
		remove: func(id int64) (*gl.Response, error) {
			return c.api.AwardEmoji.DeleteMergeRequestAwardEmoji(pid, iid, id, gl.WithContext(ctx))
		},
	}
}

func (c *Client) noteAwards(ctx context.Context, pid string, number int, note int64) awards {
	iid := int64(number)
	return awards{
		list: func(lo gl.ListOptions) ([]*gl.AwardEmoji, *gl.Response, error) {
			return c.api.AwardEmoji.ListMergeRequestAwardEmojiOnNote(pid, iid, note, &gl.ListAwardEmojiOptions{ListOptions: lo}, gl.WithContext(ctx))
		},
		create: func(o *gl.CreateAwardEmojiOptions) (*gl.AwardEmoji, *gl.Response, error) {
			return c.api.AwardEmoji.CreateMergeRequestAwardEmojiOnNote(pid, iid, note, o, gl.WithContext(ctx))
		},
		remove: func(id int64) (*gl.Response, error) {
			return c.api.AwardEmoji.DeleteMergeRequestAwardEmojiOnNote(pid, iid, note, id, gl.WithContext(ctx))
		},
	}
}

// own is the id of the bot's reaction of content, 0 when it has none.
func (c *Client) own(ctx context.Context, a awards, content string) (int64, error) {
	login, err := c.BotLogin(ctx)
	if err != nil {
		return 0, err
	}
	reactions, err := list(a.list)
	if err != nil {
		return 0, apiError(err)
	}
	for _, r := range reactions {
		if r.Name == emoji(content) && r.User.Username == login {
			return r.ID, nil
		}
	}
	return 0, nil
}

// react adds the bot's reaction, or returns the one already there: GitLab
// refuses a second of the same name.
func (c *Client) react(ctx context.Context, a awards, content string) (int64, error) {
	if id, err := c.own(ctx, a, content); err != nil || id != 0 {
		return id, err
	}
	r, _, err := a.create(&gl.CreateAwardEmojiOptions{Name: emoji(content)})
	if err != nil {
		return 0, apiError(err)
	}
	return r.ID, nil
}

// noteAwardsOf is the awards of note cm, under the merge request it names,
// else the one pullOf finds.
func (c *Client) noteAwardsOf(ctx context.Context, owner, repo string, cm forge.Comment) (awards, error) {
	number := cm.Number
	if number == 0 {
		var err error
		if number, err = c.pullOf(ctx, owner, repo, cm.ID); err != nil {
			return awards{}, err
		}
	}
	return c.noteAwards(ctx, project(owner, repo), number, cm.ID), nil
}

// React implements forge.Client.
func (c *Client) React(ctx context.Context, owner, repo string, to forge.Comment, content string) (int64, error) {
	a, err := c.noteAwardsOf(ctx, owner, repo, to)
	if err != nil {
		return 0, err
	}
	id, err := c.react(ctx, a, content)
	if err != nil {
		return 0, fmt.Errorf("gitlab: react to note %d: %w", to.ID, err)
	}
	return id, nil
}

// Unreact implements forge.Client.
func (c *Client) Unreact(ctx context.Context, owner, repo string, from forge.Comment, id int64) error {
	a, err := c.noteAwardsOf(ctx, owner, repo, from)
	if err != nil {
		return err
	}
	if _, err := a.remove(id); err != nil {
		return fmt.Errorf("gitlab: remove reaction %d from note %d: %w", id, from.ID, apiError(err))
	}
	return nil
}

// ReactToPullRequest implements forge.Client.
func (c *Client) ReactToPullRequest(ctx context.Context, owner, repo string, number int, content string) (int64, error) {
	id, err := c.react(ctx, c.pullAwards(ctx, project(owner, repo), number), content)
	if err != nil {
		return 0, fmt.Errorf("gitlab: react to !%d: %w", number, err)
	}
	return id, nil
}

// UnreactToPullRequest implements forge.Client.
func (c *Client) UnreactToPullRequest(ctx context.Context, owner, repo string, number int, id int64) error {
	if _, err := c.pullAwards(ctx, project(owner, repo), number).remove(id); err != nil {
		return fmt.Errorf("gitlab: remove reaction %d from !%d: %w", id, number, apiError(err))
	}
	return nil
}

// Reaction implements forge.Client.
func (c *Client) Reaction(ctx context.Context, owner, repo string, on forge.Comment, content string) (int64, error) {
	a, err := c.noteAwardsOf(ctx, owner, repo, on)
	if err != nil {
		return 0, err
	}
	id, err := c.own(ctx, a, content)
	if err != nil {
		return 0, fmt.Errorf("gitlab: list reactions on note %d: %w", on.ID, err)
	}
	return id, nil
}

// PullRequestReaction implements forge.Client.
func (c *Client) PullRequestReaction(ctx context.Context, owner, repo string, number int, content string) (int64, error) {
	id, err := c.own(ctx, c.pullAwards(ctx, project(owner, repo), number), content)
	if err != nil {
		return 0, fmt.Errorf("gitlab: list reactions on !%d: %w", number, err)
	}
	return id, nil
}
