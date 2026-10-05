package review

import (
	"strings"
	"testing"
)

func TestParseConfidence(t *testing.T) {
	tests := []struct {
		name       string
		raw        string
		counts     Counts
		wantScore  int
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
		{name: "a score past the scale", raw: `{"score": 6, "reason": "ok"}`, wantErr: "no score from 0 to 5"},
		{name: "a negative score", raw: `{"score": -1, "reason": "ok"}`, wantErr: "no score from 0 to 5"},
		{name: "no score", raw: `{"reason": "ok"}`, wantErr: "no score from 0 to 5"},
		{name: "not JSON", raw: `five`, wantErr: "not the expected JSON"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			score, reason, err := ParseConfidence(tt.raw, tt.counts)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want it to mention %q", err, tt.wantErr)
				}
				return
			}
			if err != nil || score != tt.wantScore || reason != tt.wantReason {
				t.Fatalf("ParseConfidence = %d, %q, %v; want %d, %q", score, reason, err, tt.wantScore, tt.wantReason)
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
			msg := BuildConfidence(in, tt.findings)
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
