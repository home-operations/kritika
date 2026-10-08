package worker

import (
	"log/slog"
	"testing"

	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/forge"
	"github.com/home-operations/kritika/internal/jobs"
	"github.com/home-operations/kritika/internal/review"
)

func TestVerdict(t *testing.T) {
	tests := []struct {
		name       string
		confidence *review.Confidence
		unscored   bool
		gate       bool
		unfinished int
		findings   int
		wantState  forge.StatusState
		wantDesc   string
	}{
		{name: "no score asked for, no findings", wantState: forge.StatusSuccess, wantDesc: "no findings"},
		{name: "no score asked for, findings", findings: 2, wantState: forge.StatusSuccess, wantDesc: "2 finding(s)"},
		{
			name: "a score that reaches the threshold", confidence: &review.Confidence{Score: 4, Threshold: 4}, gate: true, findings: 1,
			wantState: forge.StatusSuccess, wantDesc: "confidence 4/5, 1 finding(s)",
		},
		{
			name: "a score under the threshold, gated", confidence: &review.Confidence{Score: 3, Threshold: 5}, gate: true, findings: 1,
			wantState: forge.StatusFailure, wantDesc: "confidence 3/5, below 5, 1 finding(s)",
		},
		{
			name: "a score under the threshold, not gated", confidence: &review.Confidence{Score: 3, Threshold: 5}, findings: 1,
			wantState: forge.StatusSuccess, wantDesc: "confidence 3/5, 1 finding(s)",
		},
		{name: "a score asked for and not given, gated", unscored: true, gate: true, wantState: forge.StatusError, wantDesc: "confidence not scored, no findings"},
		{name: "a score asked for and not given, not gated", unscored: true, wantState: forge.StatusSuccess, wantDesc: "confidence not scored, no findings"},
		{
			name: "a split review a part of which went unreviewed", confidence: &review.Confidence{Score: 5, Threshold: 4}, unfinished: 1,
			findings: 2, wantState: forge.StatusError, wantDesc: "review incomplete, 2 finding(s)",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &publishPhase{confidence: tt.confidence, unscored: tt.unscored, unfinished: tt.unfinished,
				settings: configfile.Settings{Confidence: configfile.Confidence{Gate: tt.gate}}}
			if state, desc := p.verdict(tt.findings); state != tt.wantState || desc != tt.wantDesc {
				t.Fatalf("verdict = %s %q, want %s %q", state, desc, tt.wantState, tt.wantDesc)
			}
		})
	}
}

func TestSkipVerdict(t *testing.T) {
	tests := []struct {
		name      string
		carried   *review.Confidence
		gate      bool
		wantState forge.StatusState
		wantDesc  string
	}{
		{name: "nothing carried", wantState: forge.StatusSuccess, wantDesc: "skipped (why)"},
		{name: "a carried score that passes", carried: &review.Confidence{Score: 5, Threshold: 5}, gate: true,
			wantState: forge.StatusSuccess, wantDesc: "confidence 5/5, skipped (why)"},
		{name: "a carried score that does not, gated", carried: &review.Confidence{Score: 2, Threshold: 5}, gate: true,
			wantState: forge.StatusFailure, wantDesc: "confidence 2/5, below 5, skipped (why)"},
		{name: "a carried score that does not, not gated", carried: &review.Confidence{Score: 2, Threshold: 5},
			wantState: forge.StatusSuccess, wantDesc: "confidence 2/5, skipped (why)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if state, desc := skipVerdict(tt.carried, tt.gate, "why"); state != tt.wantState || desc != tt.wantDesc {
				t.Fatalf("skipVerdict = %s %q, want %s %q", state, desc, tt.wantState, tt.wantDesc)
			}
		})
	}
}

// TestCarryApproval: an unchanged patch's carried score is held to the
// threshold and the risk ceiling asked for now, and decides kritika's
// approval only where the repository has it approve.
func TestCarryApproval(t *testing.T) {
	settings := func(approve bool, threshold int) configfile.Settings {
		return configfile.Settings{
			Review:     configfile.Review{Approve: approve},
			Confidence: configfile.Confidence{Model: "p/judge", Threshold: threshold, Risk: review.RiskLow},
		}
	}
	carried := func(threshold int) *review.Confidence {
		return &review.Confidence{Score: 4, Threshold: threshold, Risk: review.RiskLow}
	}
	tests := []struct {
		name                string
		settings            configfile.Settings
		carried             *review.Confidence
		approved, dismissed string
	}{
		{name: "nothing carried", settings: settings(true, 4)},
		{name: "approvals off", settings: settings(false, 4), carried: carried(4)},
		{name: "a score that still passes approves the new head", settings: settings(true, 4), carried: carried(4),
			approved: "o/r#7@abcdef1234: kritika: confidence 4/5 with low risk at abcdef1."},
		{name: "a threshold raised since withdraws", settings: settings(true, 5), carried: carried(5),
			dismissed: "o/r#7: kritika: confidence 4/5 is below the threshold of 5 at abcdef1."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &approvalForge{}
			carryApproval(t.Context(), slog.New(slog.DiscardHandler), f, &pullRequest{repository: "o/r", number: 7, headSHA: "abcdef1234"},
				tt.settings, tt.carried)
			if f.approved != tt.approved || f.dismissed != tt.dismissed {
				t.Fatalf("approved %q dismissed %q, want %q and %q", f.approved, f.dismissed, tt.approved, tt.dismissed)
			}
		})
	}
}

// TestEarlierRisk: a review of a new head is given the risk the last
// review was rated under the same instructions; a re-run of the same head,
// a re-run asked for and a rating under other instructions start afresh.
func TestEarlierRisk(t *testing.T) {
	rated := func(rubric string) *review.Confidence {
		return &review.Confidence{Score: 4, Risk: review.RiskHigh, Reason: "A major bump.", Rubric: rubric}
	}
	tests := []struct {
		name    string
		prior   priorReview
		trigger string
		want    *review.EarlierRisk
	}{
		{name: "no earlier review"},
		{name: "an earlier review left unscored", prior: priorReview{id: "r1", headSHA: "old"}},
		{name: "a re-run of the head it rated", prior: priorReview{id: "r1", headSHA: "new", confidence: rated("rubric")}},
		{name: "a rating under other instructions", prior: priorReview{id: "r1", headSHA: "old", confidence: rated("other")}},
		{name: "a rating from before rubrics", prior: priorReview{id: "r1", headSHA: "old", confidence: rated("")}},
		{name: "a re-run asked for at a new head", prior: priorReview{id: "r1", headSHA: "old", confidence: rated("rubric")}, trigger: jobs.TriggerManual},
		{name: "a new head under the same instructions", prior: priorReview{id: "r1", headSHA: "old", confidence: rated("rubric")},
			want: &review.EarlierRisk{HeadSHA: "old", Risk: review.RiskHigh, Reason: "A major bump."}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := earlierRisk(tt.prior, "new", "rubric", tt.trigger)
			if (got == nil) != (tt.want == nil) || (got != nil && *got != *tt.want) {
				t.Fatalf("earlierRisk = %+v, want %+v", got, tt.want)
			}
		})
	}
}
