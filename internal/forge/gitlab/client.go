// Package gitlab implements forge.Client against a GitLab instance's REST
// API, through gitlab.com/gitlab-org/api/client-go. kritika calls the API
// as a service account with a personal access token, and runners fetch
// with a second token of that account that can only read repositories.
// GitLab 19 or later is assumed.
package gitlab

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	gl "gitlab.com/gitlab-org/api/client-go/v3"

	"github.com/home-operations/kritika/internal/forge"
	"github.com/home-operations/kritika/internal/webhook"
)

// pageSize is how many items a paginated listing asks for per page, the
// most GitLab serves.
const pageSize = 100

// maxDiffBytes bounds a pull request diff read whole.
const maxDiffBytes = 8 << 20

// Client is one service account's access to a GitLab instance.
type Client struct {
	api         *gl.Client
	web         string
	runnerToken string

	mu    sync.Mutex
	login string
	// bots is whether a user, by id, is one GitLab marks as a bot: a
	// merge request's author comes without the flag, so it is read once
	// per author.
	bots map[int64]bool
	// pulls is the pull request, by note id, of every note the client has
	// read or posted: GitLab addresses a note under its merge request,
	// and the forge interface names a comment by its id alone.
	pulls map[int64]int
}

// NewClient builds a Client for host, a hostname reached over HTTPS, or a
// URL with its scheme, which tests point at a server of their own, calling
// the API with token and runners fetching with runnerToken, token when it
// is empty. A nil httpClient is the SDK's default.
func NewClient(host, token, runnerToken string, httpClient *http.Client) (*Client, error) {
	if host == "" {
		return nil, errors.New("gitlab: host is required")
	}
	if token == "" {
		return nil, errors.New("gitlab: token is required")
	}
	web := host
	if !strings.Contains(web, "://") {
		web = "https://" + web
	}
	web = strings.TrimSuffix(web, "/")
	if u, err := url.Parse(web); err != nil || u.Host == "" {
		return nil, fmt.Errorf("gitlab: invalid host %q", host)
	}
	opts := []gl.ClientOptionFunc{gl.WithBaseURL(web + "/api/v4")}
	if httpClient != nil {
		opts = append(opts, gl.WithHTTPClient(httpClient))
	}
	api, err := gl.NewClient(token, opts...)
	if err != nil {
		return nil, fmt.Errorf("gitlab: client: %w", err)
	}
	if runnerToken == "" {
		runnerToken = token
	}
	return &Client{
		api: api, web: web, runnerToken: runnerToken,
		bots: map[int64]bool{}, pulls: map[int64]int{},
	}, nil
}

// diffVersion is a diff version as the API answers it, with its files.
// The SDK's type drops the flags GitLab sets on a patch it left out of
// the version, so the answer is read without it.
type diffVersion struct {
	ID             int64      `json:"id"`
	HeadCommitSHA  string     `json:"head_commit_sha"`
	BaseCommitSHA  string     `json:"base_commit_sha"`
	StartCommitSHA string     `json:"start_commit_sha"`
	State          string     `json:"state"`
	Diffs          []fileDiff `json:"diffs"`
}

// fileDiff is one file of a diff version. Collapsed and TooLarge mark a
// patch GitLab left out, each for a size of its own.
type fileDiff struct {
	Diff        string `json:"diff"`
	OldPath     string `json:"old_path"`
	NewPath     string `json:"new_path"`
	AMode       string `json:"a_mode"`
	BMode       string `json:"b_mode"`
	NewFile     bool   `json:"new_file"`
	RenamedFile bool   `json:"renamed_file"`
	DeletedFile bool   `json:"deleted_file"`
	Collapsed   bool   `json:"collapsed"`
	TooLarge    bool   `json:"too_large"`
}

// version reads diff version id of the merge request with its files, in
// unified form. A version GitLab cut short, as a whole or by a file, is
// an error: a diff is read whole or not at all.
func (c *Client) version(ctx context.Context, pid string, number int, id int64) (*diffVersion, error) {
	path := fmt.Sprintf("projects/%s/merge_requests/%d/versions/%d", gl.PathEscape(pid), number, id)
	req, err := c.api.NewRequest(http.MethodGet, path, &gl.GetSingleMergeRequestDiffVersionOptions{Unidiff: new(true)},
		[]gl.RequestOptionFunc{gl.WithContext(ctx)})
	if err != nil {
		return nil, err
	}
	var v diffVersion
	if _, err := c.api.Do(req, &v); err != nil {
		return nil, apiError(err)
	}
	if v.State == "overflow" {
		return nil, fmt.Errorf("gitlab: diff version %d of %s!%d is over GitLab's diff limits", id, pid, number)
	}
	for _, d := range v.Diffs {
		if d.Collapsed || d.TooLarge {
			return nil, fmt.Errorf("gitlab: diff version %d of %s!%d leaves out %s, over GitLab's diff limits", id, pid, number, d.NewPath)
		}
	}
	return &v, nil
}

// project is the path GitLab identifies a project by, which the SDK
// escapes into one path segment. A project under a subgroup is
// "group/sub/project", which kritika splits as owner "group" and name
// "sub/project".
func project(owner, repo string) string { return owner + "/" + repo }

// apiError wraps a 404 as forge.ErrNotFound, so a caller can tell a
// missing merge request, note or file from a failed call.
func apiError(err error) error {
	if resp, ok := errors.AsType[*gl.ErrorResponse](err); ok && resp.StatusCode == http.StatusNotFound {
		return fmt.Errorf("%w: %w", forge.ErrNotFound, err)
	}
	return err
}

// list reads every page of a listing, whose page function is called with
// the page to read and answers it with what GitLab says of the next.
func list[T any](page func(gl.ListOptions) ([]T, *gl.Response, error)) ([]T, error) {
	var out []T
	for p := int64(1); ; {
		batch, resp, err := page(gl.ListOptions{Page: p, PerPage: pageSize})
		if err != nil {
			return nil, err
		}
		out = append(out, batch...)
		if resp == nil || resp.NextPage == 0 {
			return out, nil
		}
		p = resp.NextPage
	}
}

// versionAt is the merge request's diff version whose head is head: GitLab
// keeps one per push, so the diff and merge base of a head the pull
// request has moved on from are still its own.
func (c *Client) versionAt(ctx context.Context, pid string, number int, head string) (*gl.MergeRequestDiffVersion, error) {
	versions, err := list(func(lo gl.ListOptions) ([]*gl.MergeRequestDiffVersion, *gl.Response, error) {
		return c.api.MergeRequests.GetMergeRequestDiffVersions(pid, int64(number),
			&gl.GetMergeRequestDiffVersionsOptions{ListOptions: lo}, gl.WithContext(ctx))
	})
	if err != nil {
		return nil, apiError(err)
	}
	for _, v := range versions {
		if v.HeadCommitSHA == head {
			return v, nil
		}
	}
	return nil, fmt.Errorf("gitlab: no diff version of %s!%d has head %s", pid, number, head)
}

// MergeBase implements forge.Client from the diff version of head, which
// records the merge base GitLab computed for it; base goes unused.
func (c *Client) MergeBase(ctx context.Context, owner, repo string, number int, _, head string) (string, error) {
	v, err := c.versionAt(ctx, project(owner, repo), number, head)
	if err != nil {
		return "", fmt.Errorf("gitlab: merge base of %s/%s!%d: %w", owner, repo, number, err)
	}
	if v.BaseCommitSHA == "" {
		return "", fmt.Errorf("gitlab: %s/%s!%d at %s reports no merge base", owner, repo, number, head)
	}
	return v.BaseCommitSHA, nil
}

// PullRequestDiff implements forge.Client from the diff version of head,
// which must be from base: the version is the diff as GitLab computed it
// at that push. A version GitLab cut short is an error.
func (c *Client) PullRequestDiff(ctx context.Context, owner, repo string, number int, base, head string) (string, error) {
	pid := project(owner, repo)
	v, err := c.versionAt(ctx, pid, number, head)
	if err != nil {
		return "", fmt.Errorf("gitlab: diff of %s/%s!%d: %w", owner, repo, number, err)
	}
	if v.BaseCommitSHA != base {
		return "", fmt.Errorf("gitlab: %s/%s!%d at %s merges from %s, not %s", owner, repo, number, head, v.BaseCommitSHA, base)
	}
	full, err := c.version(ctx, pid, number, v.ID)
	if err != nil {
		return "", fmt.Errorf("gitlab: diff of %s/%s!%d: %w", owner, repo, number, err)
	}
	diff := unifiedDiff(full.Diffs)
	if len(diff) > maxDiffBytes {
		return "", fmt.Errorf("gitlab: diff of %s/%s!%d is over %d bytes", owner, repo, number, maxDiffBytes)
	}
	return diff, nil
}

// unifiedDiff joins the files of a diff version into one unified diff.
// GitLab gives each file's hunks with their "---" and "+++" lines when
// asked for unidiff, and without them otherwise; the git header is added
// either way, and the file lines when they are missing.
func unifiedDiff(diffs []fileDiff) string {
	var b strings.Builder
	for _, d := range diffs {
		oldPath, newPath := d.OldPath, d.NewPath
		if oldPath == "" {
			oldPath = newPath
		}
		if newPath == "" {
			newPath = oldPath
		}
		fmt.Fprintf(&b, "diff --git a/%s b/%s\n", oldPath, newPath)
		switch {
		case d.NewFile:
			fmt.Fprintf(&b, "new file mode %s\n", d.BMode)
		case d.DeletedFile:
			fmt.Fprintf(&b, "deleted file mode %s\n", d.AMode)
		case d.RenamedFile:
			fmt.Fprintf(&b, "rename from %s\nrename to %s\n", oldPath, newPath)
		}
		if !strings.HasPrefix(d.Diff, "--- ") {
			a, bb := "a/"+oldPath, "b/"+newPath
			if d.NewFile {
				a = "/dev/null"
			}
			if d.DeletedFile {
				bb = "/dev/null"
			}
			fmt.Fprintf(&b, "--- %s\n+++ %s\n", a, bb)
		}
		b.WriteString(d.Diff)
		if d.Diff != "" && !strings.HasSuffix(d.Diff, "\n") {
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// CloneURL implements forge.Client.
func (c *Client) CloneURL(owner, repo string) string {
	return c.web + "/" + owner + "/" + repo + ".git"
}

// GitToken implements forge.Client with the runner's token, which GitLab
// takes as the password of a git fetch over HTTPS.
func (c *Client) GitToken(context.Context, string) (string, error) {
	return c.runnerToken, nil
}

// defaultBranch is the project's default branch, which a merge request
// does not carry.
func (c *Client) defaultBranch(ctx context.Context, pid string) (string, error) {
	p, _, err := c.api.Projects.GetProject(pid, nil, gl.WithContext(ctx))
	if err != nil {
		return "", fmt.Errorf("gitlab: project %s: %w", pid, apiError(err))
	}
	return p.DefaultBranch, nil
}

// BranchTip implements forge.Client.
func (c *Client) BranchTip(ctx context.Context, owner, repo, branch string) (string, string, error) {
	pid := project(owner, repo)
	if branch == "" {
		var err error
		if branch, err = c.defaultBranch(ctx, pid); err != nil {
			return "", "", err
		}
	}
	b, _, err := c.api.Branches.GetBranch(pid, branch, gl.WithContext(ctx))
	if err != nil {
		return "", "", fmt.Errorf("gitlab: branch %s of %s: %w", branch, pid, apiError(err))
	}
	if b.Commit == nil || b.Commit.ID == "" {
		return "", "", fmt.Errorf("gitlab: branch %s of %s has no commit", branch, pid)
	}
	return b.Commit.ID, branch, nil
}

// CommitSubject implements forge.Client.
func (c *Client) CommitSubject(ctx context.Context, owner, repo, sha string) (string, error) {
	cm, _, err := c.api.Commits.GetCommit(project(owner, repo), sha, nil, gl.WithContext(ctx))
	if err != nil {
		return "", fmt.Errorf("gitlab: commit %s of %s/%s: %w", sha, owner, repo, apiError(err))
	}
	return strings.TrimSpace(cm.Title), nil
}

// boundedFile takes a file's bytes up to forge.MaxFileBytes and refuses
// the first byte over, so a file too large is never read whole. It ends
// the request then: the SDK drains a body it did not finish reading.
type boundedFile struct {
	content []byte
	cancel  context.CancelFunc
}

func (b *boundedFile) Write(p []byte) (int, error) {
	if len(b.content)+len(p) > forge.MaxFileBytes {
		b.cancel()
		return 0, forge.ErrFileTooLarge
	}
	b.content = append(b.content, p...)
	return len(p), nil
}

// FileAt implements forge.Client through the raw file endpoint, read up
// to the size limit.
func (c *Client) FileAt(ctx context.Context, owner, repo, ref, path string) ([]byte, error) {
	apiPath := fmt.Sprintf("projects/%s/repository/files/%s/raw", gl.PathEscape(project(owner, repo)), gl.PathEscape(path))
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	req, err := c.api.NewRequest(http.MethodGet, apiPath, &gl.GetRawFileOptions{Ref: new(ref)}, []gl.RequestOptionFunc{gl.WithContext(ctx)})
	if err != nil {
		return nil, fmt.Errorf("gitlab: %s of %s/%s at %s: %w", path, owner, repo, ref, err)
	}
	file := boundedFile{cancel: cancel}
	if _, err := c.api.Do(req, &file); err != nil {
		if errors.Is(apiError(err), forge.ErrNotFound) {
			err = fs.ErrNotExist
		}
		return nil, fmt.Errorf("gitlab: %s of %s/%s at %s: %w", path, owner, repo, ref, err)
	}
	return file.content, nil
}

// Issue implements forge.Client. GitLab numbers issues and merge requests
// apart, so a number names an issue alone.
func (c *Client) Issue(ctx context.Context, owner, repo string, number int) (forge.Issue, error) {
	is, _, err := c.api.Issues.GetIssue(project(owner, repo), int64(number), gl.WithContext(ctx))
	if err != nil {
		return forge.Issue{}, fmt.Errorf("gitlab: issue %d of %s/%s: %w", number, owner, repo, apiError(err))
	}
	return forge.Issue{Number: int(is.IID), Title: is.Title, Body: is.Description, URL: is.WebURL}, nil
}

// BotLogin implements forge.Client, cached: the account behind a token
// never changes.
func (c *Client) BotLogin(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.login != "" {
		return c.login, nil
	}
	u, _, err := c.api.Users.CurrentUser(gl.WithContext(ctx))
	if err != nil {
		return "", fmt.Errorf("gitlab: bot login: %w", err)
	}
	c.login = u.Username
	return c.login, nil
}

// isBot is whether GitLab marks the user as a bot, read once per user,
// or whether its login says so as a GitHub bot's would.
func (c *Client) isBot(ctx context.Context, u *gl.BasicUser) bool {
	if u == nil {
		return false
	}
	if webhook.IsBot("", u.Username) {
		return true
	}
	c.mu.Lock()
	bot, ok := c.bots[u.ID]
	c.mu.Unlock()
	if ok {
		return bot
	}
	full, _, err := c.api.Users.GetUser(u.ID, nil, gl.WithContext(ctx))
	if err != nil {
		return false
	}
	c.mu.Lock()
	c.bots[u.ID] = full.Bot
	c.mu.Unlock()
	return full.Bot
}

// remember records which pull request a note is on.
func (c *Client) remember(number int, ids ...int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, id := range ids {
		c.pulls[id] = number
	}
}

// pullOf is the pull request note id is on: the one it was read or posted
// under, else found by walking the open pull requests, the most recently
// updated first, as a note's id says nothing of its merge request. A note
// on a closed pull request the client never read is not found.
func (c *Client) pullOf(ctx context.Context, owner, repo string, id int64) (int, error) {
	c.mu.Lock()
	number, ok := c.pulls[id]
	c.mu.Unlock()
	if ok {
		return number, nil
	}
	prs, err := c.ListOpenPullRequests(ctx, owner, repo, time.Time{})
	if err != nil {
		return 0, err
	}
	for _, pr := range prs {
		if _, err := c.discussions(ctx, owner, repo, pr.Number); err != nil {
			return 0, err
		}
		c.mu.Lock()
		number, ok = c.pulls[id]
		c.mu.Unlock()
		if ok {
			return number, nil
		}
	}
	return 0, fmt.Errorf("gitlab: note %d of %s/%s: %w", id, owner, repo, forge.ErrNotFound)
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
	n, _, err := c.api.Notes.CreateMergeRequestNote(project(owner, repo), int64(number),
		&gl.CreateMergeRequestNoteOptions{Body: new(body)}, gl.WithContext(ctx))
	if err != nil {
		return 0, fmt.Errorf("gitlab: comment on !%d: %w", number, apiError(err))
	}
	c.remember(number, n.ID)
	return n.ID, nil
}

// UpdateComment implements forge.Client.
func (c *Client) UpdateComment(ctx context.Context, owner, repo string, id int64, body string) error {
	number, err := c.pullOf(ctx, owner, repo, id)
	if err != nil {
		return fmt.Errorf("gitlab: edit note %d: %w", id, err)
	}
	_, _, err = c.api.Notes.UpdateMergeRequestNote(project(owner, repo), int64(number), id,
		&gl.UpdateMergeRequestNoteOptions{Body: new(body)}, gl.WithContext(ctx))
	if err != nil {
		return fmt.Errorf("gitlab: edit note %d: %w", id, apiError(err))
	}
	return nil
}

// buildState is the commit status state GitLab shows for one kritika
// reports. A review that reached no verdict is canceled: failed would read
// as one.
func buildState(state forge.StatusState) gl.BuildStateValue {
	switch state {
	case forge.StatusSuccess:
		return gl.Success
	case forge.StatusFailure:
		return gl.Failed
	case forge.StatusError:
		return gl.Canceled
	}
	return gl.Pending
}

// SetStatus implements forge.Client. GitLab files the status as a job of
// an external pipeline on the commit.
func (c *Client) SetStatus(ctx context.Context, owner, repo, sha string, state forge.StatusState, description string) error {
	opts := &gl.SetCommitStatusOptions{
		State: buildState(state), Name: new(forge.StatusContext), Description: new(forge.StatusDescription(description)),
	}
	if _, _, err := c.api.Commits.SetCommitStatus(project(owner, repo), sha, opts, gl.WithContext(ctx)); err != nil {
		return fmt.Errorf("gitlab: status on %s: %w", sha, apiError(err))
	}
	return nil
}

func escapePath(path string) string {
	segs := strings.Split(path, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return strings.Join(segs, "/")
}

// FileURL implements forge.Client.
func (c *Client) FileURL(owner, repo, sha, path string, line, endLine int) string {
	u := fmt.Sprintf("%s/%s/%s/-/blob/%s/%s#L%d", c.web, owner, repo, sha, escapePath(path), line)
	if endLine > line {
		u += fmt.Sprintf("-%d", endLine)
	}
	return u
}

// CommitURL implements forge.Client.
func (c *Client) CommitURL(owner, repo, sha string) string {
	return fmt.Sprintf("%s/%s/%s/-/commit/%s", c.web, owner, repo, sha)
}

// ThreadURL implements forge.Client.
func (c *Client) ThreadURL(owner, repo string, number int, id int64) string {
	return fmt.Sprintf("%s/%s/%s/-/merge_requests/%d#note_%d", c.web, owner, repo, number, id)
}

// permission is the level an access level grants: Developer and up can
// push, Reporter and Security Manager triage, Guest and Planner read.
func permission(level gl.AccessLevelValue) forge.Permission {
	switch {
	case level >= gl.OwnerPermissions:
		return forge.PermissionAdmin
	case level >= gl.MaintainerPermissions:
		return forge.PermissionMaintain
	case level >= gl.DeveloperPermissions:
		return forge.PermissionWrite
	case level >= gl.ReporterPermissions:
		return forge.PermissionTriage
	case level >= gl.GuestPermissions:
		return forge.PermissionRead
	}
	return forge.PermissionNone
}

// Permission implements forge.Client from the user's membership of the
// project, direct or inherited from its groups, which any member can
// read. A login no user has, or no membership, is none.
func (c *Client) Permission(ctx context.Context, owner, repo, login string) (forge.Permission, error) {
	users, _, err := c.api.Users.ListUsers(&gl.ListUsersOptions{Username: new(login)}, gl.WithContext(ctx))
	if err != nil {
		return "", fmt.Errorf("gitlab: user %s: %w", login, err)
	}
	if len(users) == 0 {
		return forge.PermissionNone, nil
	}
	m, _, err := c.api.ProjectMembers.GetInheritedProjectMember(project(owner, repo), users[0].ID, gl.WithContext(ctx))
	if err != nil {
		if errors.Is(apiError(err), forge.ErrNotFound) {
			return forge.PermissionNone, nil
		}
		return "", fmt.Errorf("gitlab: permission of %s on %s/%s: %w", login, owner, repo, err)
	}
	return permission(m.AccessLevel), nil
}

// ListOpenPullRequests implements forge.Client. GitLab filters by update
// time server-side.
func (c *Client) ListOpenPullRequests(ctx context.Context, owner, repo string, since time.Time) ([]forge.OpenPullRequest, error) {
	pid := project(owner, repo)
	branch, err := c.defaultBranch(ctx, pid)
	if err != nil {
		return nil, err
	}
	mrs, err := list(func(lo gl.ListOptions) ([]*gl.BasicMergeRequest, *gl.Response, error) {
		opts := &gl.ListProjectMergeRequestsOptions{ListOptions: lo, State: new("opened"), OrderBy: new("updated_at"), Sort: new("desc")}
		if !since.IsZero() {
			opts.UpdatedAfter = new(since)
		}
		return c.api.MergeRequests.ListProjectMergeRequests(pid, opts, gl.WithContext(ctx))
	})
	if err != nil {
		return nil, fmt.Errorf("gitlab: list open merge requests of %s: %w", pid, apiError(err))
	}
	out := make([]forge.OpenPullRequest, 0, len(mrs))
	for _, mr := range mrs {
		out = append(out, c.openPullRequest(ctx, mr, branch))
	}
	return out, nil
}

// PullRequest implements forge.Client.
func (c *Client) PullRequest(ctx context.Context, owner, repo string, number int) (forge.OpenPullRequest, error) {
	pid := project(owner, repo)
	branch, err := c.defaultBranch(ctx, pid)
	if err != nil {
		return forge.OpenPullRequest{}, err
	}
	mr, _, err := c.api.MergeRequests.GetMergeRequest(pid, int64(number), nil, gl.WithContext(ctx))
	if err != nil {
		return forge.OpenPullRequest{}, fmt.Errorf("gitlab: merge request %d of %s: %w", number, pid, apiError(err))
	}
	out := c.openPullRequest(ctx, &mr.BasicMergeRequest, branch)
	// GitLab counts files, capped as "1000+", and no lines.
	out.ChangedFiles, _ = strconv.Atoi(strings.TrimSuffix(mr.ChangesCount, "+"))
	return out, nil
}

// Repository is one project an account has, as Reach lists them.
type Repository struct {
	FullName, DefaultBranch string
	Archived, Fork          bool
	Visibility              string
}

// Repositories lists the projects of account, a group with its subgroups
// or a user, that the bot can see.
func (c *Client) Repositories(ctx context.Context, account string) ([]Repository, error) {
	projects, err := list(func(lo gl.ListOptions) ([]*gl.Project, *gl.Response, error) {
		opts := &gl.ListGroupProjectsOptions{ListOptions: lo, IncludeSubGroups: new(true), WithShared: new(false)}
		return c.api.Groups.ListGroupProjects(account, opts, gl.WithContext(ctx))
	})
	if errors.Is(apiError(err), forge.ErrNotFound) {
		// Not a group: a user's namespace.
		projects, err = list(func(lo gl.ListOptions) ([]*gl.Project, *gl.Response, error) {
			return c.api.Projects.ListUserProjects(account, &gl.ListProjectsOptions{ListOptions: lo}, gl.WithContext(ctx))
		})
	}
	if err != nil {
		return nil, fmt.Errorf("gitlab: list projects of %s: %w", account, apiError(err))
	}
	out := make([]Repository, 0, len(projects))
	for _, p := range projects {
		out = append(out, Repository{
			FullName: p.PathWithNamespace, DefaultBranch: p.DefaultBranch, Archived: p.Archived,
			Fork: p.ForkedFromProject != nil, Visibility: string(p.Visibility),
		})
	}
	return out, nil
}

func timeOf(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return *t
}

// openPullRequest maps a merge request. GitLab's states are opened,
// closed, merged and locked; kritika's are open and closed.
func (c *Client) openPullRequest(ctx context.Context, mr *gl.BasicMergeRequest, defaultBranch string) forge.OpenPullRequest {
	out := forge.OpenPullRequest{DefaultBranch: defaultBranch}
	out.Number, out.Title, out.Body = int(mr.IID), mr.Title, mr.Description
	if mr.Author != nil {
		out.Author, out.AuthorIsBot = mr.Author.Username, c.isBot(ctx, mr.Author)
	}
	out.State = "closed"
	if mr.State == "opened" {
		out.State = "open"
	}
	out.Merged, out.Draft = mr.State == "merged", mr.Draft
	out.Fork = mr.SourceProjectID != mr.TargetProjectID
	out.HeadRef, out.HeadSHA, out.BaseRef = mr.SourceBranch, mr.SHA, mr.TargetBranch
	out.URL, out.CreatedAt, out.UpdatedAt, out.ClosedAt = mr.WebURL, timeOf(mr.CreatedAt), timeOf(mr.UpdatedAt), mr.ClosedAt
	for _, l := range mr.Labels {
		out.Labels = append(out.Labels, webhook.Label{Name: l})
	}
	return out
}
