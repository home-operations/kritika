package worker

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/forge"
	"github.com/home-operations/kritika/internal/review"
)

// approvalForge records the approval calls a publish makes.
type approvalForge struct {
	forge.Client
	approved, dismissed string
	requested           bool
	err, reviewsErr     error
}

func (f *approvalForge) Approve(_ context.Context, owner, repo string, number int, headSHA, body string) (bool, error) {
	f.approved = owner + "/" + repo + "#" + string(rune('0'+number)) + "@" + headSHA + ": " + body
	return f.err == nil, f.err
}

func (f *approvalForge) ChangesRequested(context.Context, string, string, int) (bool, error) {
	return f.requested, f.reviewsErr
}

func (f *approvalForge) DismissApprovals(_ context.Context, owner, repo string, number int, message string) (int, error) {
	f.dismissed = owner + "/" + repo + "#" + string(rune('0'+number)) + ": " + message
	return 1, f.err
}

func TestApprove(t *testing.T) {
	t.Parallel()
	scored := configfile.Confidence{Model: "p/judge", Threshold: 4, Risk: review.RiskMedium}
	const approves, withdraws = "o/r#7@abcdef1234: kritika: ", "o/r#7: kritika: "
	tests := []struct {
		name                string
		counts              review.Counts
		want                configfile.Confidence
		confidence          *review.Confidence
		unscored, requested bool
		moved               bool
		err, reviewsErr     error
		approved, dismissed string
	}{
		{name: "nothing found approves", approved: approves + "nothing blocking or important found at abcdef1."},
		{name: "nits alone approve", counts: review.Counts{Nit: 3}, approved: approves + "nothing blocking or important found at abcdef1."},
		{name: "an important finding withdraws", counts: review.Counts{Important: 1, Nit: 1},
			dismissed: withdraws + "0 blocking and 1 important finding(s) at abcdef1."},
		{name: "a blocking finding withdraws", counts: review.Counts{Blocking: 2},
			dismissed: withdraws + "2 blocking and 0 important finding(s) at abcdef1."},
		{name: "a forge error is logged, not raised", err: errors.New("forbidden"),
			approved: approves + "nothing blocking or important found at abcdef1."},
		{name: "a score and a risk within bounds approve", want: scored,
			confidence: &review.Confidence{Score: 4, Threshold: 4, Risk: review.RiskMedium},
			approved:   approves + "confidence 4/5 with medium risk at abcdef1."},
		{name: "a score under the threshold withdraws, whatever was found", want: scored,
			confidence: &review.Confidence{Score: 3, Threshold: 4, Risk: review.RiskLow},
			dismissed:  withdraws + "confidence 3/5 is below the threshold of 4 at abcdef1."},
		{name: "a risk above the ceiling withdraws, whatever the score", want: scored,
			confidence: &review.Confidence{Score: 5, Threshold: 4, Risk: review.RiskHigh},
			dismissed:  withdraws + "high risk is above the medium this repository approves at abcdef1."},
		{name: "a review left unscored withdraws", want: scored, unscored: true, dismissed: withdraws + "confidence was not scored at abcdef1."},
		{name: "a reviewer's request for changes withdraws", requested: true, dismissed: withdraws + "a reviewer has requested changes at abcdef1."},
		{name: "reviews that cannot be read approve nothing and withdraw nothing", reviewsErr: errors.New("forbidden")},
		{name: "a head that moved is not approved", moved: true},
		{name: "a head that moved still loses an approval its findings forbid", moved: true, counts: review.Counts{Blocking: 1},
			dismissed: withdraws + "1 blocking and 0 important finding(s) at abcdef1."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := &approvalForge{err: tt.err, requested: tt.requested, reviewsErr: tt.reviewsErr}
			p := &publishPhase{
				client: f, pr: &pullRequest{repository: "o/r", number: 7, headSHA: "abcdef1234"}, logger: slog.New(slog.DiscardHandler),
				settings: configfile.Settings{Confidence: tt.want}, confidence: tt.confidence, unscored: tt.unscored,
			}
			p.approve(t.Context(), tt.counts, !tt.moved)
			if f.approved != tt.approved || f.dismissed != tt.dismissed {
				t.Fatalf("approved %q dismissed %q, want %q and %q", f.approved, f.dismissed, tt.approved, tt.dismissed)
			}
		})
	}
}
