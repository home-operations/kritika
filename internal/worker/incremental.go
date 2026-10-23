package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"slices"

	"github.com/jackc/pgx/v5"

	"github.com/home-operations/kritika/internal/review"
	"github.com/home-operations/kritika/internal/store"
)

// priorReview is a pull request's last completed review; id is "" when it
// has none. Its findings leave out the ones maintainers dismissed, which
// dismissed holds: their threads are resolved already, and the next
// review is told of them apart.
type priorReview struct {
	id, headSHA, trigger string
	// mergeBase is the merge base it was reviewed against.
	mergeBase string
	// changed are the paths the change touched at headSHA, as its context
	// pack recorded them.
	changed   []string
	findings  []priorFinding
	dismissed []store.Dismissal
	// diagram is its summary's diagram, "" when it drew none.
	diagram string
	// checked are its notes on what it checked and found sound.
	checked []string
	// confidence is its confidence score and risk, nil when it was not
	// scored.
	confidence *review.Confidence
	// reviewers are the reviewers whose agent submitted for it.
	reviewers []string
}

type priorFinding struct {
	review.Finding
	postedInline bool
	// commentID is the inline comment's id on the forge, 0 when unknown.
	commentID int64
}

// lastCompleted loads the pull request's last completed review and its
// findings.
func lastCompleted(ctx context.Context, tx pgx.Tx, prID string) (priorReview, error) {
	row, err := store.LastCompletedReview(ctx, tx, prID)
	if err != nil {
		return priorReview{}, err
	}
	p := priorReview{id: row.ID, headSHA: row.HeadSHA, trigger: row.Trigger, mergeBase: row.MergeBase,
		changed: row.Changed, diagram: row.Diagram, checked: row.Checked, confidence: row.Confidence, reviewers: row.Reviewers}
	p.findings = slices.Grow(p.findings, len(row.Findings))
	for _, f := range row.Findings {
		p.findings = append(p.findings, priorFinding{Finding: f.Finding, postedInline: f.PostedInline, commentID: f.CommentID})
	}
	return p, nil
}

// withDismissals gives p the pull request's dismissals and drops the
// findings they name from its findings.
func (p *priorReview) withDismissals(dismissed []store.Dismissal) {
	p.dismissed = dismissed
	gone := make(map[string]bool, len(dismissed))
	for _, d := range dismissed {
		gone[d.Fingerprint] = true
	}
	p.findings = slices.DeleteFunc(p.findings, func(f priorFinding) bool { return gone[review.Fingerprint(f.Finding)] })
}

// dismissedFindings is what the prompt is told of the dismissals.
func dismissedFindings(dismissed []store.Dismissal) []review.DismissedFinding {
	out := make([]review.DismissedFinding, len(dismissed))
	for i, d := range dismissed {
		out[i] = review.DismissedFinding{Finding: d.Finding, Reason: d.Reason}
	}
	return out
}

// dropDismissed leaves out the findings maintainers dismissed on the pull
// request, should the model raise one again, and counts them. A dismissed
// finding is not among the prior findings a raised one carries a
// fingerprint on from, so it is also known by the path and title it was
// dismissed under, which may not be the ones its fingerprint was made of.
func dropDismissed(findings []review.Finding, dismissed []store.Dismissal) ([]review.Finding, int) {
	if len(dismissed) == 0 {
		return findings, 0
	}
	gone := make(map[string]bool, 2*len(dismissed))
	for _, d := range dismissed {
		gone[d.Fingerprint] = true
		gone[review.Fingerprint(review.Finding{Path: d.Finding.Path, Title: d.Finding.Title})] = true
	}
	kept := slices.DeleteFunc(slices.Clone(findings), func(f review.Finding) bool { return gone[review.Fingerprint(f)] })
	return kept, len(findings) - len(kept)
}

// carriedDiagram is the diagram a review's summary keeps: drawn, what
// Parse kept of answer's, or prior when an incremental re-review's answer
// has no diagram at all. The re-review is asked to keep or update the last
// diagram, but it looks mostly at the commits since, and may not have been
// shown the last one, so it may still leave out one that describes the
// whole change. An empty diagram is its answer that the flow is gone, and
// stands, as a full review's does whatever it is.
func carriedDiagram(answer json.RawMessage, drawn, prior string, incremental bool) string {
	if drawn != "" || !incremental {
		return drawn
	}
	var a struct {
		Summary struct {
			Diagram *string `json:"diagram"`
		} `json:"summary"`
	}
	if err := json.NewDecoder(bytes.NewReader(answer)).Decode(&a); err != nil || a.Summary.Diagram != nil {
		return drawn
	}
	return prior
}

// reviewFindings drops the bookkeeping from prior findings.
func reviewFindings(prior []priorFinding) []review.Finding {
	out := make([]review.Finding, len(prior))
	for i, f := range prior {
		out[i] = f.Finding
	}
	return out
}

// alreadyInline reports, per finding, the inline comment a prior finding
// with the same fingerprint already has on the forge, in which case it is
// not posted again and its thread carries on.
func alreadyInline(findings []review.Finding, prior []priorFinding) []store.InlinePosted {
	posted := map[string]store.InlinePosted{}
	for _, p := range prior {
		if p.postedInline {
			posted[review.Fingerprint(p.Finding)] = store.InlinePosted{Posted: true, ID: p.commentID}
		}
	}
	out := make([]store.InlinePosted, len(findings))
	for i, f := range findings {
		out[i] = posted[review.Fingerprint(f)]
	}
	return out
}
