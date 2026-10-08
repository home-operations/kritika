package webapi

import (
	"time"

	"github.com/home-operations/kritika/internal/review"
	"github.com/home-operations/kritika/internal/store"
)

// The shapes of a pull request and the reviews and follow-ups it had, which
// routes_pulls.go serves.

// ReviewBrief is a pull request's newest review.
type ReviewBrief struct {
	ID        string             `json:"id"`
	Status    store.ReviewStatus `json:"status"`
	Scope     review.Scope       `json:"scope"`
	Findings  SeverityCounts     `json:"findings"`
	CreatedAt time.Time          `json:"createdAt"`
}

// Pull is one pull request.
type Pull struct {
	Repository string `json:"repository"`
	Number     int    `json:"number"`
	Title      string `json:"title"`
	Author     string `json:"author"`
	State      string `json:"state"`
	Draft      bool   `json:"draft"`
	Fork       bool   `json:"fork"`
	Merged     bool   `json:"merged"`
	// Paused is whether its automatic reviews are paused: a push is
	// recorded, not reviewed, until someone asks for a review or resumes
	// them.
	Paused     bool         `json:"paused"`
	HeadSHA    string       `json:"headSha"`
	HeadRef    string       `json:"headRef"`
	BaseRef    string       `json:"baseRef"`
	URL        string       `json:"url"`
	OpenedAt   *time.Time   `json:"openedAt"`
	UpdatedAt  time.Time    `json:"updatedAt"`
	Labels     []Label      `json:"labels"`
	LastReview *ReviewBrief `json:"lastReview"`
	// ReviewCount is how many of its reviews completed; CostUSD is what
	// all of them spent, the ones that did not complete included.
	ReviewCount int     `json:"reviewCount"`
	CostUSD     float64 `json:"costUsd"`
}

// Attention counts the account's open pull requests that want a look, by
// why: their newest review failed, hit a cap or found something blocking,
// or their automatic reviews are paused. One may count under several.
type Attention struct {
	Failed   int `json:"failed"`
	Capped   int `json:"capped"`
	Blocking int `json:"blocking"`
	Paused   int `json:"paused"`
}

// TokenCounts are input and output tokens.
type TokenCounts struct {
	Input  int64 `json:"input"`
	Output int64 `json:"output"`
}

// Confidence is the score a second model gave a reviewed pull request, out
// of 5, against the threshold its repository set.
type Confidence struct {
	Score     int    `json:"score"`
	Threshold int    `json:"threshold"`
	Passed    bool   `json:"passed"`
	Reason    string `json:"reason"`
	// Risk is how much a mistake in the change would cost: low, medium,
	// high or critical.
	Risk  review.Risk `json:"risk"`
	Model string      `json:"model"`
}

// Followup is one @-mention of the bot and what came of it.
type Followup struct {
	ID         string `json:"id"`
	CommentID  int64  `json:"commentId"`
	Repository string `json:"repository"`
	Number     int    `json:"number"`
	// PullURL is the pull request on the forge, where its comments are.
	PullURL        string               `json:"pullUrl"`
	Author         string               `json:"author"`
	Inline         bool                 `json:"inline"`
	Path           string               `json:"path"`
	Line           int                  `json:"line"`
	Status         store.FollowupStatus `json:"status"`
	Reason         string               `json:"reason"`
	ReplyCommentID *int64               `json:"replyCommentId"`
	Model          string               `json:"model"`
	CreatedAt      time.Time            `json:"createdAt"`
}

// PullDetail is a pull request with every review and follow-up. Job is
// its review job that has not finished, nil when there is none: a review
// row only exists once a job got as far as starting the review, so a job
// that waits or keeps failing before that shows nowhere else.
type PullDetail struct {
	Pull      Pull       `json:"pull"`
	Reviews   []Review   `json:"reviews"`
	Followups []Followup `json:"followups"`
	Job       *Job       `json:"job"`
}

// PullRef names a pull request, and where it is on the forge.
type PullRef struct {
	Repository string `json:"repository"`
	Number     int    `json:"number"`
	Title      string `json:"title"`
	URL        string `json:"url"`
}
