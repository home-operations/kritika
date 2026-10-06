package worker

import (
	"testing"

	"github.com/home-operations/kritika/internal/review"
	"github.com/home-operations/kritika/internal/store"
)

func TestAlreadyInline(t *testing.T) {
	seen := review.Finding{Path: "main.go", Line: 3, Title: "Nil  map write"}
	moved := review.Finding{Path: "main.go", Line: 9, Title: "nil map write"}
	fresh := review.Finding{Path: "main.go", Line: 5, Title: "unchecked error"}
	notPosted := review.Finding{Path: "util.go", Line: 1, Title: "slow loop"}
	prior := []priorFinding{
		{Finding: seen, postedInline: true, commentID: 55},
		{Finding: notPosted, postedInline: false},
	}
	none := store.InlinePosted{}
	cases := []struct {
		name        string
		findings    []review.Finding
		prior       []priorFinding
		wantCarried []store.InlinePosted
	}{
		{name: "no prior review", findings: []review.Finding{seen, fresh}, wantCarried: []store.InlinePosted{none, none}},
		{
			// The fingerprint ignores the line and title case and spacing,
			// so a finding that moved is still the one already posted, and
			// keeps its thread.
			name: "posted before", findings: []review.Finding{moved, fresh}, prior: prior,
			wantCarried: []store.InlinePosted{{Posted: true, ID: 55}, none},
		},
		{name: "found before but never posted", findings: []review.Finding{notPosted}, prior: prior, wantCarried: []store.InlinePosted{none}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := alreadyInline(tc.findings, tc.prior)
			if len(got) != len(tc.wantCarried) {
				t.Fatalf("alreadyInline = %v", got)
			}
			for i := range got {
				if got[i] != tc.wantCarried[i] {
					t.Fatalf("alreadyInline = %v, want %v", got, tc.wantCarried)
				}
			}
		})
	}
}

func TestCarriedDiagram(t *testing.T) {
	const prior, drawn = "flowchart LR\n  A --> B", "flowchart LR\n  A --> C"
	tests := []struct {
		name               string
		drawn, prior, want string
		incremental        bool
	}{
		{name: "a re-review's dropped diagram is carried", prior: prior, incremental: true, want: prior},
		{name: "a re-review's own diagram stands", drawn: drawn, prior: prior, incremental: true, want: drawn},
		{name: "a full review's answer stands", prior: prior},
		{name: "nothing to carry", incremental: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := carriedDiagram(tt.drawn, tt.prior, tt.incremental); got != tt.want {
				t.Fatalf("carriedDiagram = %q, want %q", got, tt.want)
			}
		})
	}
}
