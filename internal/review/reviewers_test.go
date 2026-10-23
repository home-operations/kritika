package review

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

func TestMergeReviewers(t *testing.T) {
	f := func(line, end int, sev Severity, cat Category, title string, reviewers ...string) Finding {
		return Finding{Path: "main.go", Line: line, EndLine: end, Severity: sev, Category: cat, Title: title, Agents: reviewers}
	}
	for _, tt := range []struct {
		name string
		in   []Finding
		want []Finding
	}{
		{
			name: "one reviewer's findings are never merged with each other",
			in:   []Finding{f(11, 0, SeverityP1, CategorySecurity, "a"), f(11, 0, SeverityP2, CategorySecurity, "b")},
			want: []Finding{f(11, 0, SeverityP1, CategorySecurity, "a"), f(11, 0, SeverityP2, CategorySecurity, "b")},
		},
		{
			name: "the same category on overlapping lines merges, keeping the more severe and crediting both",
			in:   []Finding{f(11, 12, SeverityP2, CategorySecurity, "a"), f(12, 0, SeverityP0, CategorySecurity, "b", "security")},
			want: []Finding{f(12, 0, SeverityP0, CategorySecurity, "b", "security", DefaultReviewer)},
		},
		{
			name: "as severe, the first is kept",
			in:   []Finding{f(11, 0, SeverityP1, CategorySecurity, "a", "security"), f(11, 0, SeverityP1, CategorySecurity, "b")},
			want: []Finding{f(11, 0, SeverityP1, CategorySecurity, "a", "security", DefaultReviewer)},
		},
		{
			name: "one title in another category merges",
			in:   []Finding{f(11, 0, SeverityP1, CategoryCorrectness, "Nil deref"), f(11, 0, SeverityP1, CategorySecurity, "nil  DEREF", "security")},
			want: []Finding{f(11, 0, SeverityP1, CategoryCorrectness, "Nil deref", DefaultReviewer, "security")},
		},
		{
			name: "lines apart stay apart",
			in:   []Finding{f(11, 0, SeverityP1, CategorySecurity, "a"), f(13, 0, SeverityP1, CategorySecurity, "a", "security")},
			want: []Finding{f(11, 0, SeverityP1, CategorySecurity, "a"), f(13, 0, SeverityP1, CategorySecurity, "a", "security")},
		},
		{
			name: "another category and title stay apart",
			in:   []Finding{f(11, 0, SeverityP1, CategoryCorrectness, "a"), f(11, 0, SeverityP1, CategorySecurity, "b", "security")},
			want: []Finding{f(11, 0, SeverityP1, CategoryCorrectness, "a"), f(11, 0, SeverityP1, CategorySecurity, "b", "security")},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := MergeReviewers(tt.in)
			if len(got) != len(tt.want) {
				t.Fatalf("got %d findings, want %d: %+v", len(got), len(tt.want), got)
			}
			for i := range got {
				g, w := got[i], tt.want[i]
				if g.Line != w.Line || g.Severity != w.Severity || g.Title != w.Title || !slices.Equal(g.Agents, w.Agents) {
					t.Errorf("finding %d = %+v, want %+v", i, g, w)
				}
			}
		})
	}
}

// TestMergeReviewersFingerprint: the thread a finding carried on goes on
// in the finding it merged into.
func TestMergeReviewersFingerprint(t *testing.T) {
	got := MergeReviewers([]Finding{
		{Path: "a.go", Line: 1, Severity: SeverityP0, Category: CategorySecurity, Title: "new", Agents: []string{"security"}},
		{Path: "a.go", Line: 1, Severity: SeverityP2, Category: CategorySecurity, Title: "old", Fingerprint: "fp-old"},
	})
	if len(got) != 1 || got[0].Fingerprint != "fp-old" || got[0].Title != "new" {
		t.Fatalf("merged = %+v, want the p0 finding on the p2's thread", got)
	}
}

func TestParseReviewers(t *testing.T) {
	raw := `{"summary": {"take": "T."}, "findings": [
	  {"path": "main.go", "line": 11, "severity": "p1", "category": "correctness", "title": "a", "explanation": "e", "agents": ["security"]},
	  {"path": "main.go", "line": 12, "severity": "p1", "category": "correctness", "title": "b", "explanation": "e", "agents": ["ghost"]},
	  {"path": "main.go", "line": 13, "severity": "p1", "category": "security", "title": "c", "explanation": "e", "agents": ["security"]},
	  {"path": "main.go", "line": 14, "severity": "p1", "category": "tests", "title": "d", "explanation": "e", "agents": ["default"]}
	]}`
	res, dropped, err := Parse(raw, Anchors(sampleDiff), ParseOptions{
		Reviewers:  []string{DefaultReviewer, "security"},
		Categories: map[string][]Category{"security": {CategorySecurity}},
	})
	if err != nil {
		t.Fatal(err)
	}
	kept := make([]string, 0, len(res.Findings))
	for _, f := range res.Findings {
		kept = append(kept, f.Title+":"+strings.Join(f.Agents, ","))
	}
	if want := []string{"b:", "c:security", "d:"}; !slices.Equal(kept, want) {
		t.Errorf("kept %q, want %q: a reviewer the review did not run is no one's, and the default reviewer's names none", kept, want)
	}
	if len(dropped) != 1 || dropped[0].Finding.Title != "a" || dropped[0].Reason != DropBadCategory {
		t.Errorf("dropped %+v, want the security reviewer's correctness finding as a bad category", dropped)
	}
}

func TestSchemaFor(t *testing.T) {
	var s struct {
		Properties struct {
			Findings struct {
				Items struct {
					Properties map[string]struct {
						Enum []string `json:"enum"`
					} `json:"properties"`
					Required []string `json:"required"`
				} `json:"items"`
			} `json:"findings"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(SchemaFor(true, false, []Category{CategorySecurity, CategoryReliability}), &s); err != nil {
		t.Fatal(err)
	}
	items := s.Properties.Findings.Items
	if got := items.Properties[keyCategory].Enum; !slices.Equal(got, []string{"security", "reliability"}) {
		t.Errorf("category enum = %q, want the reviewer's", got)
	}
	if !slices.Contains(items.Required, keySuggestedFix) {
		t.Error("a strict schema does not require a suggested fix")
	}
	if got := SchemaFor(false, true, nil); string(got) != string(contracts[contract{false, true}]) {
		t.Error("no categories is not the plain schema")
	}
}

func TestCheckCategories(t *testing.T) {
	sub := func(cat string) json.RawMessage {
		return json.RawMessage(`{"summary":{"headline":"H","take":"T.","praise":[]},"findings":[{"path":"a.go","line":1,` +
			`"severity":"p2","category":"` + cat + `","title":"t","explanation":"e"}]}`)
	}
	check := CheckCategories(Check, []Category{CategorySecurity})
	if err := check(sub("security")); err != nil {
		t.Errorf("a security finding was refused: %v", err)
	}
	if err := check(sub("tests")); err == nil || !strings.Contains(err.Error(), "only security") {
		t.Errorf("a tests finding = %v, want it refused naming the categories", err)
	}
	if err := CheckCategories(Check, nil)(sub("tests")); err != nil {
		t.Errorf("no categories refused a finding: %v", err)
	}
}

func TestWithReviewer(t *testing.T) {
	if got := WithReviewer("S", nil); got != "S" {
		t.Errorf("no reviewer = %q", got)
	}
	if got := WithReviewer("S", &Reviewer{Name: DefaultReviewer, Instructions: "x"}); got != "S" {
		t.Errorf("the default reviewer = %q, want nothing added", got)
	}
	got := WithReviewer("S", &Reviewer{Name: "security", Instructions: " Exploit paths first. ", Categories: []Category{CategorySecurity}})
	for _, want := range []string{"you are the security reviewer", "Report only security findings", "Exploit paths first."} {
		if !strings.Contains(got, want) {
			t.Errorf("system prompt lacks %q:\n%s", want, got)
		}
	}
}

func TestPartLead(t *testing.T) {
	for _, tt := range []struct {
		part PartInput
		want string
	}{
		{PartInput{Index: 2, Count: 3}, "reviewed in 3 parts, each by its own reviewer, and this is part 2"},
		{PartInput{Index: 1, Count: 1, Reviewer: "security"}, "you review it as the security reviewer: the files"},
		{PartInput{Index: 2, Count: 3, Reviewer: "security"}, "as the security reviewer, in 3 parts, this one part 2: the files"},
	} {
		if got := tt.part.lead(); !strings.Contains(got, tt.want) {
			t.Errorf("lead of %+v = %q, want it to say %q", tt.part, got, tt.want)
		}
	}
}

func TestAgentNames(t *testing.T) {
	for _, tt := range []struct {
		reviewers []string
		want      string
	}{
		{nil, ""},
		{[]string{DefaultReviewer}, ""},
		{[]string{"security"}, "security"},
		{[]string{DefaultReviewer, "security"}, "default, security"},
	} {
		f := Finding{Agents: tt.reviewers}
		if got := f.AgentNames(); got != tt.want {
			t.Errorf("AgentNames(%q) = %q, want %q", tt.reviewers, got, tt.want)
		}
	}
	if !(Finding{}).RaisedBy(DefaultReviewer) || (Finding{}).RaisedBy("security") {
		t.Error("a finding naming no reviewer is not the default reviewer's alone")
	}
}

// TestRenderReviewers: the default templates credit a finding's reviewers
// and list the reviewers a review ran, and say nothing of either for the
// default reviewer alone.
func TestRenderReviewers(t *testing.T) {
	f := Finding{Path: "a.go", Line: 1, Severity: SeverityP1, Category: CategorySecurity, Title: "Open redirect", Explanation: "e",
		Agents: []string{"security"}}
	inline, _ := RenderInline(context.Background(), Templates{}, InlineData{Finding: f})
	if !strings.Contains(inline, "· security · security") {
		t.Errorf("inline comment does not credit the reviewer:\n%s", inline)
	}
	data := RenderData{Number: 1, HeadSHA: "abcdef1234", Result: Result{Findings: []Finding{f}}, Agents: []string{DefaultReviewer, "security"}}
	summary, _ := RenderSummary(context.Background(), Templates{}, data)
	for _, want := range []string{"Open redirect · security · security", "2 agents", "**Agents:** `default` `security`"} {
		if !strings.Contains(summary, want) {
			t.Errorf("summary lacks %q:\n%s", want, summary)
		}
	}
	// A specialist that ran alone, finding nothing, is still named.
	lone := RenderData{Number: 1, HeadSHA: "abcdef1234", Agents: []string{"security"}}
	summary, _ = RenderSummary(context.Background(), Templates{}, lone)
	for _, want := range []string{"1 agent</summary>", "**Agents:** `security`"} {
		if !strings.Contains(summary, want) {
			t.Errorf("a lone specialist's summary lacks %q:\n%s", want, summary)
		}
	}
	f.Agents, data.Agents = nil, []string{DefaultReviewer}
	data.Result.Findings = []Finding{f}
	summary, _ = RenderSummary(context.Background(), Templates{}, data)
	if strings.Contains(summary, "Agents:") || strings.Contains(summary, " agents") {
		t.Errorf("a review by the default reviewer alone mentions reviewers:\n%s", summary)
	}
}

// TestParseReviewersKeepPriorThread: a finding that repeats a prior one's
// title carries its thread into the finding of another reviewer it is
// merged into, though the other's title wins.
func TestParseReviewersKeepPriorThread(t *testing.T) {
	prior := Finding{Path: "main.go", Line: 11, Severity: SeverityP2, Category: CategorySecurity, Title: "Unchecked input"}
	raw := `{"summary": {"take": "T."}, "findings": [
	  {"path": "main.go", "line": 11, "severity": "p2", "category": "security", "title": "Unchecked input", "explanation": "e"},
	  {"path": "main.go", "line": 11, "severity": "p0", "category": "security", "title": "SQL injection", "explanation": "e",
	   "agents": ["security"]}
	]}`
	res, _, err := Parse(raw, Anchors(sampleDiff), ParseOptions{Reviewers: []string{DefaultReviewer, "security"}, Prior: []Finding{prior}})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Findings) != 1 || res.Findings[0].Title != "SQL injection" || Fingerprint(res.Findings[0]) != Fingerprint(prior) {
		t.Fatalf("findings = %+v, want the p0 on the prior finding's thread", res.Findings)
	}
}
