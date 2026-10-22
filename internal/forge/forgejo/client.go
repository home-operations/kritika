// Package forgejo implements forge.Client against a Forgejo instance's REST
// API, https://<host>/api/v1, with the standard library's HTTP client:
// requests and responses are written out for the subset of the API kritika
// needs, which keeps a context on every call.
package forgejo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/home-operations/kritika/internal/forge"
	"github.com/home-operations/kritika/internal/webhook"
)

// maxErrorBody bounds how much of an error response an apiError quotes.
const maxErrorBody = 512

// maxDiffBytes bounds a pull request diff read whole.
const maxDiffBytes = 8 << 20

// pageSize is how many items a paginated listing asks for per page. A
// short page is the last: the page size is sent, so the server's own
// default never makes a full page look short.
const pageSize = 50

// errNoContent is a 204 answering a request that expected a body.
var errNoContent = errors.New("forgejo: no content")

// apiError is any non-2xx response.
type apiError struct {
	method, path string
	status       int
	body         string
}

func (e *apiError) Error() string {
	return fmt.Sprintf("forgejo: %s %s: %d: %s", e.method, e.path, e.status, e.body)
}

// Unwrap makes a 404 wrap forge.ErrNotFound.
func (e *apiError) Unwrap() error {
	if e.status == http.StatusNotFound {
		return forge.ErrNotFound
	}
	return nil
}

// Client is one bot account's access to a Forgejo instance.
type Client struct {
	http     *http.Client
	api, web string
	token    string
	// gitToken is what GitToken hands a runner: a read-only token, so the
	// pod that reads untrusted content never holds one that can write.
	gitToken string

	mu    sync.Mutex
	login string
}

// NewClient builds a Client for host, a hostname reached over HTTPS, or a
// URL with its scheme, which tests point at a server of their own. token
// calls the API; gitToken, or token when it is empty, is what runners
// fetch with. A nil httpClient is http.DefaultClient.
func NewClient(host, token, gitToken string, httpClient *http.Client) (*Client, error) {
	if host == "" {
		return nil, errors.New("forgejo: host is required")
	}
	web := host
	if !strings.Contains(web, "://") {
		web = "https://" + web
	}
	web = strings.TrimSuffix(web, "/")
	if u, err := url.Parse(web); err != nil || u.Host == "" {
		return nil, fmt.Errorf("forgejo: invalid host %q", host)
	}
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	if gitToken == "" {
		gitToken = token
	}
	return &Client{http: httpClient, api: web + "/api/v1", web: web, token: token, gitToken: gitToken}, nil
}

// do sends an API request and decodes its JSON answer into out, when out
// is not nil; a *string takes the body as text. body, when not nil, is
// sent as JSON.
func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	_, err := c.request(ctx, method, path, body, out)
	return err
}

// request is do, returning the answer's headers too.
func (c *Client) request(ctx context.Context, method, path string, body, out any) (http.Header, error) {
	var reader io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("forgejo: encode request: %w", err)
		}
		reader = bytes.NewReader(buf)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.api+path, reader)
	if err != nil {
		return nil, fmt.Errorf("forgejo: build request: %w", err)
	}
	req.Header.Set("Authorization", "token "+c.token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("forgejo: %s %s: %w", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
		return resp.Header, &apiError{method: method, path: path, status: resp.StatusCode, body: string(raw)}
	}
	if out == nil {
		return resp.Header, nil
	}
	if resp.StatusCode == http.StatusNoContent {
		return resp.Header, fmt.Errorf("forgejo: %s %s: %w", method, path, errNoContent)
	}
	if text, ok := out.(*string); ok {
		raw, err := io.ReadAll(io.LimitReader(resp.Body, maxDiffBytes+1))
		if err != nil {
			return resp.Header, fmt.Errorf("forgejo: read %s %s: %w", method, path, err)
		}
		if len(raw) > maxDiffBytes {
			return resp.Header, fmt.Errorf("forgejo: %s %s: response is over %d bytes", method, path, maxDiffBytes)
		}
		*text = string(raw)
		return resp.Header, nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return resp.Header, fmt.Errorf("forgejo: decode %s %s: %w", method, path, err)
	}
	return resp.Header, nil
}

// list reads every page of path, whose query gains page and limit, as
// long as the answer says there is another: a Link header with rel="next",
// or an X-Total-Count the pages read fall short of. Without either, a page
// shorter than asked is the last. The page size asked for is capped by the
// instance, and a listing may leave out what the bot cannot see, so a
// short page alone does not say the listing is over. stop ends the walk
// early, after the page it was called on.
func list[T any](ctx context.Context, c *Client, path string, query url.Values, stop func(T) bool) ([]T, error) {
	var out []T
	largest := 0
	for page := 1; ; page++ {
		q := url.Values{}
		maps.Copy(q, query)
		q.Set("page", strconv.Itoa(page))
		q.Set("limit", strconv.Itoa(pageSize))
		var batch []T
		header, err := c.request(ctx, http.MethodGet, path+"?"+q.Encode(), nil, &batch)
		if err != nil {
			return nil, err
		}
		largest = max(largest, len(batch))
		for _, item := range batch {
			if stop != nil && stop(item) {
				return out, nil
			}
			out = append(out, item)
		}
		if !morePages(header, page, len(batch), largest) {
			return out, nil
		}
	}
}

// morePages reads whether a listing's answer has a page after page: a
// Link to the next, or a total the pages read so far fall short of. The
// total counts what a listing leaves out of a page too, so a page emptied
// that way does not end the walk, and the pages are measured by the
// largest page seen, which is at most the size the instance caps a page
// at, whatever was asked: a page is never taken for more than it held.
func morePages(header http.Header, page, got, largest int) bool {
	for _, link := range header.Values("Link") {
		if strings.Contains(link, `rel="next"`) {
			return true
		}
	}
	if total, err := strconv.Atoi(header.Get("X-Total-Count")); err == nil {
		return page*max(largest, 1) < total
	}
	return got >= pageSize
}

func repoPath(owner, repo string) string {
	return "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(repo)
}

func escapePath(path string) string {
	segs := strings.Split(path, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return strings.Join(segs, "/")
}

// pullAt reads pull request number as it stands, and refuses one whose
// head is not head, or whose merge base is not base when one is given:
// Forgejo reports a pull request's merge base and diff as they are now,
// and a review of an earlier head, queued before a push, must not take
// the new head's.
func (c *Client) pullAt(ctx context.Context, owner, repo string, number int, base, head string) (pullRequest, error) {
	var pr pullRequest
	if err := c.do(ctx, http.MethodGet, fmt.Sprintf("%s/pulls/%d", repoPath(owner, repo), number), nil, &pr); err != nil {
		return pullRequest{}, err
	}
	if head != "" && pr.Head.SHA != head {
		return pullRequest{}, fmt.Errorf("forgejo: %s/%s#%d is at %s, not %s: the pull request moved", owner, repo, number, pr.Head.SHA, head)
	}
	if base != "" && pr.MergeBase != base {
		return pullRequest{}, fmt.Errorf("forgejo: %s/%s#%d merges from %s, not %s: the pull request moved",
			owner, repo, number, pr.MergeBase, base)
	}
	return pr, nil
}

// MergeBase implements forge.Client from the pull request, which reports
// the merge base Forgejo computed against its base branch, when head is
// still its head; base goes unused.
func (c *Client) MergeBase(ctx context.Context, owner, repo string, number int, _, head string) (string, error) {
	pr, err := c.pullAt(ctx, owner, repo, number, "", head)
	if err != nil {
		return "", fmt.Errorf("forgejo: merge base of %s/%s#%d: %w", owner, repo, number, err)
	}
	if pr.MergeBase == "" {
		return "", fmt.Errorf("forgejo: %s/%s#%d reports no merge base", owner, repo, number)
	}
	return pr.MergeBase, nil
}

// PullRequestDiff implements forge.Client. Forgejo diffs a pull request,
// not two commits: the diff runs from the pull request's current merge
// base to its current head, which must be base and head before the diff
// is read and still after, so a push between the two reads is caught.
func (c *Client) PullRequestDiff(ctx context.Context, owner, repo string, number int, base, head string) (string, error) {
	if _, err := c.pullAt(ctx, owner, repo, number, base, head); err != nil {
		return "", fmt.Errorf("forgejo: diff of %s/%s#%d: %w", owner, repo, number, err)
	}
	var diff string
	if err := c.do(ctx, http.MethodGet, fmt.Sprintf("%s/pulls/%d.diff", repoPath(owner, repo), number), nil, &diff); err != nil {
		return "", fmt.Errorf("forgejo: diff of %s/%s#%d: %w", owner, repo, number, err)
	}
	if _, err := c.pullAt(ctx, owner, repo, number, base, head); err != nil {
		return "", fmt.Errorf("forgejo: diff of %s/%s#%d: %w", owner, repo, number, err)
	}
	return diff, nil
}

// CloneURL implements forge.Client.
func (c *Client) CloneURL(owner, repo string) string {
	return c.web + "/" + owner + "/" + repo + ".git"
}

// GitToken implements forge.Client with the static token a runner fetches
// with.
func (c *Client) GitToken(context.Context, string) (string, error) { return c.gitToken, nil }

// BranchTip implements forge.Client.
func (c *Client) BranchTip(ctx context.Context, owner, repo, branch string) (string, string, error) {
	if branch == "" {
		var r repository
		if err := c.do(ctx, http.MethodGet, repoPath(owner, repo), nil, &r); err != nil {
			return "", "", fmt.Errorf("forgejo: repository %s/%s: %w", owner, repo, err)
		}
		branch = r.DefaultBranch
	}
	var b repoBranch
	if err := c.do(ctx, http.MethodGet, repoPath(owner, repo)+"/branches/"+url.PathEscape(branch), nil, &b); err != nil {
		return "", "", fmt.Errorf("forgejo: branch %s of %s/%s: %w", branch, owner, repo, err)
	}
	if b.Commit.ID == "" {
		return "", "", fmt.Errorf("forgejo: branch %s of %s/%s has no commit", branch, owner, repo)
	}
	return b.Commit.ID, branch, nil
}

// CommitSubject implements forge.Client.
func (c *Client) CommitSubject(ctx context.Context, owner, repo, sha string) (string, error) {
	var cm commit
	if err := c.do(ctx, http.MethodGet, repoPath(owner, repo)+"/git/commits/"+url.PathEscape(sha), nil, &cm); err != nil {
		return "", fmt.Errorf("forgejo: commit %s of %s/%s: %w", sha, owner, repo, err)
	}
	subject, _, _ := strings.Cut(cm.Commit.Message, "\n")
	return strings.TrimSpace(subject), nil
}

// FileAt implements forge.Client through the raw endpoint, read up to the
// size limit.
func (c *Client) FileAt(ctx context.Context, owner, repo, ref, path string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.api+repoPath(owner, repo)+"/raw/"+escapePath(path)+"?ref="+url.QueryEscape(ref), nil)
	if err != nil {
		return nil, fmt.Errorf("forgejo: build request: %w", err)
	}
	req.Header.Set("Authorization", "token "+c.token)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("forgejo: %s of %s/%s at %s: %w", path, owner, repo, ref, err)
	}
	defer func() { _ = resp.Body.Close() }()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return nil, fmt.Errorf("forgejo: %s of %s/%s at %s: %w", path, owner, repo, ref, fs.ErrNotExist)
	case resp.StatusCode < 200 || resp.StatusCode > 299:
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
		return nil, fmt.Errorf("forgejo: %s of %s/%s at %s: %w", path, owner, repo, ref,
			&apiError{method: http.MethodGet, path: path, status: resp.StatusCode, body: string(raw)})
	}
	content, err := io.ReadAll(io.LimitReader(resp.Body, forge.MaxFileBytes+1))
	if err != nil {
		return nil, fmt.Errorf("forgejo: %s of %s/%s at %s: %w", path, owner, repo, ref, err)
	}
	if len(content) > forge.MaxFileBytes {
		return nil, fmt.Errorf("forgejo: %s of %s/%s at %s: %w", path, owner, repo, ref, forge.ErrFileTooLarge)
	}
	return content, nil
}

// Issue implements forge.Client. Forgejo files pull requests among a
// repository's issues, so one is returned as such.
func (c *Client) Issue(ctx context.Context, owner, repo string, number int) (forge.Issue, error) {
	var is issue
	if err := c.do(ctx, http.MethodGet, fmt.Sprintf("%s/issues/%d", repoPath(owner, repo), number), nil, &is); err != nil {
		return forge.Issue{}, fmt.Errorf("forgejo: issue %d of %s/%s: %w", number, owner, repo, err)
	}
	return forge.Issue{Number: is.Number, Title: is.Title, Body: is.Body, URL: is.HTMLURL, PullRequest: is.PullRequest != nil}, nil
}

// BotLogin implements forge.Client, cached: the account behind a token
// never changes.
func (c *Client) BotLogin(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.login != "" {
		return c.login, nil
	}
	var u user
	if err := c.do(ctx, http.MethodGet, "/user", nil, &u); err != nil {
		return "", fmt.Errorf("forgejo: bot login: %w", err)
	}
	c.login = u.Login
	return c.login, nil
}

// FindComment implements forge.Client.
func (c *Client) FindComment(ctx context.Context, owner, repo string, number int, login, marker string) (int64, error) {
	comments, err := c.ListConversation(ctx, owner, repo, number)
	if err != nil {
		return 0, err
	}
	for _, cm := range comments {
		if cm.Author == login && strings.Contains(cm.Body, marker) {
			return cm.ID, nil
		}
	}
	return 0, nil
}

// CreateComment implements forge.Client.
func (c *Client) CreateComment(ctx context.Context, owner, repo string, number int, body string) (int64, error) {
	var cm comment
	path := fmt.Sprintf("%s/issues/%d/comments", repoPath(owner, repo), number)
	if err := c.do(ctx, http.MethodPost, path, commentBody{Body: body}, &cm); err != nil {
		return 0, fmt.Errorf("forgejo: comment on #%d: %w", number, err)
	}
	return cm.ID, nil
}

// UpdateComment implements forge.Client.
func (c *Client) UpdateComment(ctx context.Context, owner, repo string, id int64, body string) error {
	path := fmt.Sprintf("%s/issues/comments/%d", repoPath(owner, repo), id)
	if err := c.do(ctx, http.MethodPatch, path, commentBody{Body: body}, nil); err != nil {
		return fmt.Errorf("forgejo: edit comment %d: %w", id, err)
	}
	return nil
}

// SetStatus implements forge.Client.
func (c *Client) SetStatus(ctx context.Context, owner, repo, sha string, state forge.StatusState, description string) error {
	status := createStatus{State: string(state), Context: forge.StatusContext, Description: forge.StatusDescription(description)}
	if err := c.do(ctx, http.MethodPost, repoPath(owner, repo)+"/statuses/"+url.PathEscape(sha), status, nil); err != nil {
		return fmt.Errorf("forgejo: status on %s: %w", sha, err)
	}
	return nil
}

// FileURL implements forge.Client.
func (c *Client) FileURL(owner, repo, sha, path string, line, endLine int) string {
	u := fmt.Sprintf("%s/%s/%s/src/commit/%s/%s#L%d", c.web, owner, repo, sha, escapePath(path), line)
	if endLine > line {
		u += fmt.Sprintf("-L%d", endLine)
	}
	return u
}

// CommitURL implements forge.Client.
func (c *Client) CommitURL(owner, repo, sha string) string {
	return fmt.Sprintf("%s/%s/%s/commit/%s", c.web, owner, repo, sha)
}

// ThreadURL implements forge.Client: a submitted review's code comment is
// anchored on the conversation tab like any comment.
func (c *Client) ThreadURL(owner, repo string, number int, id int64) string {
	return fmt.Sprintf("%s/%s/%s/pulls/%d#issuecomment-%d", c.web, owner, repo, number, id)
}

// Permission implements forge.Client. Forgejo reports owner for the
// repository's owner, above its admin collaborator level, and answers the
// lookup for a repository or site admin alone.
func (c *Client) Permission(ctx context.Context, owner, repo, login string) (forge.Permission, error) {
	var p collaboratorPermission
	path := repoPath(owner, repo) + "/collaborators/" + url.PathEscape(login) + "/permission"
	if err := c.do(ctx, http.MethodGet, path, nil, &p); err != nil {
		return "", fmt.Errorf("forgejo: permission of %s on %s/%s: %w", login, owner, repo, err)
	}
	level := forge.Permission(p.Permission)
	if p.Permission == "owner" {
		level = forge.PermissionAdmin
	}
	if !level.Valid() {
		return "", fmt.Errorf("forgejo: unrecognized permission %q for %s on %s/%s", p.Permission, login, owner, repo)
	}
	return level, nil
}

// ListOpenPullRequests implements forge.Client. Forgejo sorts by update
// time server-side, so the walk stops at the first one older than since.
func (c *Client) ListOpenPullRequests(ctx context.Context, owner, repo string, since time.Time) ([]forge.OpenPullRequest, error) {
	q := url.Values{"state": {"open"}, "sort": {"recentupdate"}}
	prs, err := list(ctx, c, repoPath(owner, repo)+"/pulls", q, func(pr pullRequest) bool { return pr.UpdatedAt.Before(since) })
	if err != nil {
		return nil, fmt.Errorf("forgejo: list open pull requests of %s/%s: %w", owner, repo, err)
	}
	out := make([]forge.OpenPullRequest, 0, len(prs))
	for _, pr := range prs {
		out = append(out, openPullRequest(pr))
	}
	return out, nil
}

// PullRequest implements forge.Client.
func (c *Client) PullRequest(ctx context.Context, owner, repo string, number int) (forge.OpenPullRequest, error) {
	var pr pullRequest
	if err := c.do(ctx, http.MethodGet, fmt.Sprintf("%s/pulls/%d", repoPath(owner, repo), number), nil, &pr); err != nil {
		return forge.OpenPullRequest{}, fmt.Errorf("forgejo: pull request %d of %s/%s: %w", number, owner, repo, err)
	}
	out := openPullRequest(pr)
	out.ClosedAt = pr.ClosedAt
	out.Additions, out.Deletions, out.ChangedFiles = pr.Additions, pr.Deletions, pr.ChangedFiles
	return out, nil
}

// Repository is one repository an account has, as Reach lists them.
type Repository struct {
	FullName, DefaultBranch string
	Archived, Fork          bool
	// Visibility is public, private or internal, as GitHub would name
	// what Forgejo's private and internal flags say.
	Visibility string
}

// Repositories lists the repositories of account, an organization or a
// user, that the bot can see. The users route lists an organization's
// too, and asks the token for the repository scope alone, where the
// organizations route would ask for the organization scope as well.
func (c *Client) Repositories(ctx context.Context, account string) ([]Repository, error) {
	repos, err := list[repository](ctx, c, "/users/"+url.PathEscape(account)+"/repos", nil, nil)
	if err != nil {
		return nil, fmt.Errorf("forgejo: list repositories of %s: %w", account, err)
	}
	out := make([]Repository, 0, len(repos))
	for _, r := range repos {
		out = append(out, Repository{
			FullName: r.FullName, DefaultBranch: r.DefaultBranch, Archived: r.Archived, Fork: r.Fork, Visibility: r.visibility(),
		})
	}
	return out, nil
}

func conversationComment(cm comment) forge.Comment {
	return forge.Comment{
		ID: cm.ID, Author: cm.User.Login, AuthorIsBot: isBot(cm.User.Login), Body: cm.Body, CreatedAt: cm.CreatedAt,
	}
}

// inlineComment maps a code comment, replying to root, 0 when it is one.
func inlineComment(cm pullReviewComment, root int64) forge.Comment {
	return forge.Comment{
		ID: cm.ID, Author: cm.User.Login, AuthorIsBot: isBot(cm.User.Login), Body: cm.Body, CreatedAt: cm.CreatedAt,
		Inline: true, Path: cm.Path, Line: cm.Position, InReplyTo: root,
	}
}

func fullName(r *repository) string {
	if r == nil {
		return ""
	}
	return r.FullName
}

func openPullRequest(pr pullRequest) forge.OpenPullRequest {
	out := forge.OpenPullRequest{UpdatedAt: pr.UpdatedAt}
	out.Number, out.Title, out.Body = pr.Number, pr.Title, pr.Body
	out.Author, out.AuthorIsBot = pr.User.Login, isBot(pr.User.Login)
	out.State, out.Merged, out.Draft = pr.State, pr.Merged, pr.Draft
	// A fork PR's head lives in another repository than its base; a deleted
	// fork leaves head.repo null, which is not the base repository either.
	out.Fork = pr.Head.Repo == nil || fullName(pr.Head.Repo) != fullName(pr.Base.Repo)
	out.HeadRef, out.HeadSHA, out.BaseRef = pr.Head.Ref, pr.Head.SHA, pr.Base.Ref
	out.URL, out.CreatedAt = pr.HTMLURL, pr.CreatedAt
	if pr.Base.Repo != nil {
		out.DefaultBranch = pr.Base.Repo.DefaultBranch
	}
	for _, l := range pr.Labels {
		out.Labels = append(out.Labels, webhook.Label{Name: l.Name, Color: l.Color})
	}
	return out
}

// isBot is the login-suffix half of webhook.IsBot: Forgejo's user model
// has no type.
func isBot(login string) bool { return webhook.IsBot("", login) }
