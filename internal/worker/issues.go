package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	"github.com/home-operations/kritika/internal/forge"
	"github.com/home-operations/kritika/internal/linear"
	"github.com/home-operations/kritika/internal/repoconfig"
	"github.com/home-operations/kritika/internal/review"
)

// linkedIssues reads the issues of owner/repo that body, the pull request
// description, says the change closes, for the prompt. A number that the
// forge has no issue for, or that names a pull request, is left out. A number
// that the forge did not give is left out with a note the summary states. The
// reason is that the App can lack the permission to read it.
func linkedIssues(ctx context.Context, client forge.Client, owner, repo, body string, logger *slog.Logger) ([]review.Issue, []string) {
	numbers := review.LinkedIssues(body, owner+"/"+repo)
	issues := make([]review.Issue, 0, len(numbers))
	var notes []string
	for _, n := range numbers {
		is, err := client.Issue(ctx, owner, repo, n)
		switch {
		case errors.Is(err, forge.ErrNotFound), err == nil && is.PullRequest:
			continue
		case err != nil:
			logger.Warn("linked issue not read", "issue", n, "error", err)
			notes = append(notes, fmt.Sprintf("issue #%d, which the description says this closes, could not be read", n))
			continue
		}
		issues = append(issues, review.Issue{ID: review.ForgeIssueID(is.Number), Title: is.Title, Body: is.Body})
	}
	return issues, notes
}

// linearIssues reads, with key, the issues of the Linear teams that pull
// links, at most limit of them, for the prompt. An issue that Linear does not
// give, that moved to a team not among teams, or that a link names another
// workspace for, is left out without a note: a note would tell the pull
// request's author that the issue exists. One that Linear fails to give for
// another reason is left out with a note the summary states.
func linearIssues(
	ctx context.Context, client *linear.Client, key string, teams []string, pull repoconfig.PullRequest, limit int, logger *slog.Logger,
) ([]review.Issue, []string) {
	refs := linear.Linked(pull.Title, pull.HeadRef, pull.Body, teams, limit)
	issues := make([]review.Issue, 0, len(refs))
	read := make([]string, 0, len(refs))
	var notes []string
	for _, ref := range refs {
		id := ref.ID
		is, err := client.Issue(ctx, key, id)
		switch {
		case errors.Is(err, linear.ErrNotFound):
			logger.Debug("linked Linear issue not given", "issue", id, "error", err)
			continue
		case err != nil:
			logger.Warn("linked Linear issue not read", "issue", id, "error", err)
			notes = append(notes, fmt.Sprintf("Linear issue %s, which the pull request links, could not be read", id))
			continue
		case !slices.Contains(teams, strings.ToUpper(is.Team)), slices.Contains(read, is.ID),
			ref.Workspace != "" && !strings.EqualFold(ref.Workspace, is.Workspace):
			continue
		}
		read = append(read, is.ID)
		issues = append(issues, review.Issue{ID: id, Title: is.Title, Body: is.Description})
	}
	return issues, notes
}
