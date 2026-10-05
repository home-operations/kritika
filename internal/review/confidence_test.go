package review

import (
	"cmp"
	"strings"
	"testing"
)

func TestParseConfidence(t *testing.T) {
	tests := []struct {
		name       string
		raw        string
		counts     Counts
		wantScore  int
		wantRisk   Risk
		wantReason string
		wantErr    string
	}{
		{name: "a clean review keeps its score", raw: `{"score": 5, "reason": "Nothing stands against it."}`, wantScore: 5, wantReason: "Nothing stands against it."},
		{name: "nits set no ceiling", raw: `{"score": 5, "reason": "ok"}`, counts: Counts{Nit: 3}, wantScore: 5, wantReason: "ok"},
		{name: "an important finding holds it to 3", raw: `{"score": 5, "reason": "ok"}`, counts: Counts{Important: 1}, wantScore: 3, wantReason: "ok"},
		{name: "a blocking finding holds it to 2", raw: `{"score": 4, "reason": "ok"}`, counts: Counts{Blocking: 1, Important: 2}, wantScore: 2, wantReason: "ok"},
		{name: "a score under the ceiling stands", raw: `{"score": 1, "reason": "ok"}`, counts: Counts{Blocking: 1}, wantScore: 1, wantReason: "ok"},
		{name: "the reason is one line", raw: `{"score": 4, "reason": " two\n lines "}`, wantScore: 4, wantReason: "two lines"},
		{name: "a long reason is cut", raw: `{"score": 4, "reason": "` + strings.Repeat("x", 600) + `"}`, wantScore: 4, wantReason: strings.Repeat("x", 500) + " …"},
		{name: "a reference to another repository does not link", raw: `{"score": 4, "reason": "See up/stream#12."}`, wantScore: 4,
			wantReason: RedirectReferences("See up/stream#12.", "o/r")},
		{name: "the risk it gives", raw: `{"score": 5, "risk": "medium", "reason": "ok"}`, wantScore: 5, wantRisk: RiskMedium, wantReason: "ok"},
		{name: "a risk that is no level is the highest", raw: `{"score": 5, "risk": "none", "reason": "ok"}`, wantScore: 5, wantReason: "ok"},
		{name: "a score past the scale", raw: `{"score": 6, "reason": "ok"}`, wantErr: "no score from 0 to 5"},
		{name: "a negative score", raw: `{"score": -1, "reason": "ok"}`, wantErr: "no score from 0 to 5"},
		{name: "no score", raw: `{"reason": "ok"}`, wantErr: "no score from 0 to 5"},
		{name: "not JSON", raw: `five`, wantErr: "not the expected JSON"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			score, risk, reason, err := ParseConfidence(tt.raw, "o/r", tt.counts)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want it to mention %q", err, tt.wantErr)
				}
				return
			}
			if err != nil || score != tt.wantScore || reason != tt.wantReason || risk != cmp.Or(tt.wantRisk, RiskCritical) {
				t.Fatalf("ParseConfidence = %d, %s, %q, %v; want %d, %s, %q", score, risk, reason, err, tt.wantScore,
					cmp.Or(tt.wantRisk, RiskCritical), tt.wantReason)
			}
		})
	}
}

func TestBuildConfidence(t *testing.T) {
	diff := "diff --git a/main.go b/main.go\n--- a/main.go\n+++ b/main.go\n@@ -1,1 +1,2 @@\n package main\n+func f() {}\n"
	in := Input{Repository: "o/r", Number: 7, Title: "Add f", Author: "dev", BaseRef: "main", Changed: ChangedPaths(diff), Diff: diff}
	tests := []struct {
		name     string
		findings []Finding
		want     []string
	}{
		{name: "no findings", want: []string{"Pull request #7: Add f", "- main.go\n", "+func f() {}", "The review reported no findings."}},
		{
			name: "findings follow the diff, whole",
			findings: []Finding{{
				Path: "main.go", Line: 2, Severity: SeverityBlocking, Title: "f does nothing", Explanation: "It is empty.\nCallers expect a result.",
			}},
			want: []string{"Findings the review reported (1):", "- [blocking] main.go:2 f does nothing\n  It is empty.\n  Callers expect a result.\n"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg := BuildConfidence(in, tt.findings, ConfidenceSystem)
			for _, want := range tt.want {
				if !strings.Contains(msg, want) {
					t.Errorf("message lacks %q:\n%s", want, msg)
				}
			}
		})
	}
}

func TestConfidencePassed(t *testing.T) {
	if (Confidence{Score: 4, Threshold: 5}).Passed() || !(Confidence{Score: 5, Threshold: 5}).Passed() || !(Confidence{Score: 3, Threshold: 0}).Passed() {
		t.Fatal("Passed must be true exactly when the score reaches the threshold")
	}
}

func TestRiskWithin(t *testing.T) {
	tests := []struct {
		risk, ceiling Risk
		want          bool
	}{
		{RiskLow, RiskLow, true},
		{RiskMedium, RiskLow, false},
		{RiskHigh, RiskCritical, true},
		{RiskCritical, RiskHigh, false},
		{"", RiskCritical, false},
		{"none", RiskCritical, false},
	}
	for _, tt := range tests {
		if got := tt.risk.Within(tt.ceiling); got != tt.want {
			t.Errorf("%q within %q = %v, want %v", tt.risk, tt.ceiling, got, tt.want)
		}
	}
}

func TestConfidenceSystemPrompt(t *testing.T) {
	if got := ConfidenceSystemPrompt("  \n"); got != ConfidenceSystem {
		t.Fatalf("blank guidance changed the prompt:\n%s", got)
	}
	got := ConfidenceSystemPrompt("Image bumps are low.\n")
	if !strings.HasPrefix(got, ConfidenceSystem+"\n\n") || !strings.HasSuffix(got, "\n\nImage bumps are low.") {
		t.Fatalf("the guidance does not follow the prompt:\n%s", got)
	}
}
