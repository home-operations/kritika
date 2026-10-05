package worker

import (
	"testing"

	"github.com/home-operations/kritika/internal/forge"
	"github.com/home-operations/kritika/internal/review"
)

func TestVerdict(t *testing.T) {
	tests := []struct {
		name       string
		confidence *review.Confidence
		unscored   bool
		findings   int
		wantState  forge.StatusState
		wantDesc   string
	}{
		{name: "no score asked for, no findings", wantState: forge.StatusSuccess, wantDesc: "no findings"},
		{name: "no score asked for, findings", findings: 2, wantState: forge.StatusSuccess, wantDesc: "2 finding(s)"},
		{
			name: "a score that reaches the threshold", confidence: &review.Confidence{Score: 4, Threshold: 4}, findings: 1,
			wantState: forge.StatusSuccess, wantDesc: "confidence 4/5, 1 finding(s)",
		},
		{
			name: "a score under the threshold", confidence: &review.Confidence{Score: 3, Threshold: 5}, findings: 1,
			wantState: forge.StatusFailure, wantDesc: "confidence 3/5, below 5, 1 finding(s)",
		},
		{name: "a score asked for and not given", unscored: true, wantState: forge.StatusError, wantDesc: "confidence not scored, no findings"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &publishPhase{confidence: tt.confidence, unscored: tt.unscored}
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
		wantState forge.StatusState
		wantDesc  string
	}{
		{name: "nothing carried", wantState: forge.StatusSuccess, wantDesc: "skipped (why)"},
		{name: "a carried score that passes", carried: &review.Confidence{Score: 5, Threshold: 5},
			wantState: forge.StatusSuccess, wantDesc: "confidence 5/5, skipped (why)"},
		{name: "a carried score that does not", carried: &review.Confidence{Score: 2, Threshold: 5},
			wantState: forge.StatusFailure, wantDesc: "confidence 2/5, below 5, skipped (why)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if state, desc := skipVerdict(tt.carried, "why"); state != tt.wantState || desc != tt.wantDesc {
				t.Fatalf("skipVerdict = %s %q, want %s %q", state, desc, tt.wantState, tt.wantDesc)
			}
		})
	}
}
