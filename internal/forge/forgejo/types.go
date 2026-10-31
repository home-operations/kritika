package forgejo

import "time"

// user is a user as the REST API carries one. Gitea types a bot user Bot, as
// GitHub does. Forgejo types no account, so its bots are told by the
// login-suffix heuristic alone.
type user struct {
	Login string `json:"login"`
	Type  string `json:"type"`
}

type label struct {
	Name  string `json:"name"`
	Color string `json:"color"`
}

// repository is what kritika reads of a repository. Forgejo says private
// and internal rather than a visibility.
type repository struct {
	FullName      string `json:"full_name"`
	DefaultBranch string `json:"default_branch"`
	Archived      bool   `json:"archived"`
	Fork          bool   `json:"fork"`
	Private       bool   `json:"private"`
	Internal      bool   `json:"internal"`
}

// visibility is the repository's visibility as GitHub names one.
func (r repository) visibility() string {
	switch {
	case r.Internal:
		return "internal"
	case r.Private:
		return "private"
	}
	return "public"
}

// branchInfo is a pull request's head or base. Repo is a pointer because a
// deleted fork leaves head.repo null.
type branchInfo struct {
	Ref  string      `json:"ref"`
	SHA  string      `json:"sha"`
	Repo *repository `json:"repo"`
}

type pullRequest struct {
	Number       int        `json:"number"`
	Title        string     `json:"title"`
	Body         string     `json:"body"`
	User         user       `json:"user"`
	State        string     `json:"state"`
	Merged       bool       `json:"merged"`
	Draft        bool       `json:"draft"`
	HTMLURL      string     `json:"html_url"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
	ClosedAt     *time.Time `json:"closed_at"`
	MergeBase    string     `json:"merge_base"`
	Head         branchInfo `json:"head"`
	Base         branchInfo `json:"base"`
	Labels       []label    `json:"labels"`
	Additions    int        `json:"additions"`
	Deletions    int        `json:"deletions"`
	ChangedFiles int        `json:"changed_files"`
}

type repoBranch struct {
	Commit struct {
		ID string `json:"id"`
	} `json:"commit"`
}

// commit is GET /git/commits/{sha}: the git commit is under commit.
type commit struct {
	Commit struct {
		Message string `json:"message"`
	} `json:"commit"`
}

// issue is GET /issues/{index}. pull_request is set when it is a pull
// request, which Forgejo files among the issues.
type issue struct {
	Number      int             `json:"number"`
	Title       string          `json:"title"`
	Body        string          `json:"body"`
	HTMLURL     string          `json:"html_url"`
	PullRequest *map[string]any `json:"pull_request"`
}

// comment is a conversation comment.
type comment struct {
	ID        int64     `json:"id"`
	Body      string    `json:"body"`
	User      user      `json:"user"`
	CreatedAt time.Time `json:"created_at"`
}

type commentBody struct {
	Body string `json:"body"`
}

// pullReview is one review as listed under a pull request.
type pullReview struct {
	ID        int64  `json:"id"`
	User      user   `json:"user"`
	State     string `json:"state"`
	CommitID  string `json:"commit_id"`
	Dismissed bool   `json:"dismissed"`
}

// pullReviewComment is one code comment under a review. position is the line
// on the new side of the diff, and original_position is the line on the old
// side. resolver is who resolved its conversation, null while it is open.
// There is no reply relation. Forgejo shows the code comments of one review
// on one line as one conversation, and a reply joins the root's review.
type pullReviewComment struct {
	ID               int64     `json:"id"`
	ReviewID         int64     `json:"pull_request_review_id"`
	Body             string    `json:"body"`
	Path             string    `json:"path"`
	Position         int       `json:"position"`
	OriginalPosition int       `json:"original_position"`
	ExtraLinesCount  int       `json:"extra_lines_count"`
	User             user      `json:"user"`
	Resolver         *user     `json:"resolver"`
	CreatedAt        time.Time `json:"created_at"`
}

// createReviewComment is one code comment of a review: on line new_position
// of the new side, and the extra_lines_count lines after it. A Forgejo that
// takes the field shows those lines as a range and applies a suggestion to
// them. An older one comments the one line.
type createReviewComment struct {
	Path            string `json:"path"`
	Body            string `json:"body"`
	NewPosition     int    `json:"new_position,omitempty"`
	OldPosition     int    `json:"old_position,omitempty"`
	ExtraLinesCount int    `json:"extra_lines_count,omitempty"`
}

type createReview struct {
	CommitID string                `json:"commit_id,omitempty"`
	Event    string                `json:"event"`
	Body     string                `json:"body,omitempty"`
	Comments []createReviewComment `json:"comments,omitempty"`
}

type dismissReview struct {
	Message string `json:"message"`
}

type collaboratorPermission struct {
	Permission string `json:"permission"`
}

type createStatus struct {
	State       string `json:"state"`
	Context     string `json:"context"`
	Description string `json:"description"`
}

type reaction struct {
	Content string `json:"content"`
	User    user   `json:"user"`
}

type reactionContent struct {
	Content string `json:"content"`
}
