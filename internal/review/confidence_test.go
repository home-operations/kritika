package review

import (
	"cmp"
	"encoding/json"
	"fmt"
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
		{name: "nits set no ceiling", raw: `{"score": 5, "reason": "ok"}`, counts: Counts{P2: 3}, wantScore: 5, wantReason: "ok"},
		{name: "an important finding holds it to 3", raw: `{"score": 5, "reason": "ok"}`, counts: Counts{P1: 1}, wantScore: 3, wantReason: "ok"},
		{name: "a blocking finding holds it to 2", raw: `{"score": 4, "reason": "ok"}`, counts: Counts{P0: 1, P1: 2}, wantScore: 2, wantReason: "ok"},
		{name: "a score under the ceiling stands", raw: `{"score": 1, "reason": "ok"}`, counts: Counts{P0: 1}, wantScore: 1, wantReason: "ok"},
		{name: "the reason is one line", raw: `{"score": 4, "reason": " two\n lines "}`, wantScore: 4, wantReason: "two lines"},
		{name: "a long reason is cut between words", raw: `{"score": 4, "reason": "` + strings.Repeat("word ", 120) + `"}`, wantScore: 4,
			wantReason: strings.TrimSpace(strings.Repeat("word ", 100)) + " …"},
		{name: "a word on the cap is kept", raw: `{"score": 4, "reason": "` + strings.Repeat("a", 496) + ` bcd efg"}`, wantScore: 4,
			wantReason: strings.Repeat("a", 496) + " bcd …"},
		{name: "a link over the cap goes whole", raw: `{"score": 4, "reason": "` + strings.Repeat("a", 480) + ` up/stream#12 more"}`, wantScore: 4,
			wantReason: strings.Repeat("a", 480) + " …"},
		{name: "a reason of one long word is cut in it", raw: `{"score": 4, "reason": "` + strings.Repeat("x", 600) + `"}`, wantScore: 4, wantReason: strings.Repeat("x", 500) + " …"},
		{name: "a reference to another repository does not link", raw: `{"score": 4, "reason": "See up/stream#12."}`, wantScore: 4,
			wantReason: RedirectReferences("See up/stream#12.", "o/r")},
		{name: "the risk it gives", raw: `{"score": 5, "risk": "medium", "reason": "ok"}`, wantScore: 5, wantRisk: RiskMedium, wantReason: "ok"},
		{name: "a risk that is no level is the highest", raw: `{"score": 5, "risk": "none", "reason": "ok"}`, wantScore: 5, wantReason: "ok"},
		{name: "a risk in another case is no level", raw: `{"score": 5, "risk": "Critical", "reason": "ok"}`, wantScore: 5, wantReason: "ok"},
		{name: "a score at the ceiling is not held", raw: `{"score": 3, "reason": "ok"}`, counts: Counts{P1: 1}, wantScore: 3, wantReason: "ok"},
		{name: "a score past the scale", raw: `{"score": 6, "reason": "ok"}`, wantErr: "no score from 0 to 5"},
		{name: "a negative score", raw: `{"score": -1, "reason": "ok"}`, wantErr: "no score from 0 to 5"},
		{name: "no score", raw: `{"reason": "ok"}`, wantErr: "no score from 0 to 5"},
		{name: "not JSON", raw: `five`, wantErr: "not the expected JSON"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := ParseConfidence(tt.raw, "o/r", tt.counts)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want it to mention %q", err, tt.wantErr)
				}
				return
			}
			if err != nil || p.Score != tt.wantScore || p.Reason != tt.wantReason || p.Risk != cmp.Or(tt.wantRisk, RiskCritical) {
				t.Fatalf("ParseConfidence = %d, %s, %q, %v; want %d, %s, %q", p.Score, p.Risk, p.Reason, err, tt.wantScore,
					cmp.Or(tt.wantRisk, RiskCritical), tt.wantReason)
			}
			// What the scorer gave is read back from its answer, apart from
			// what stands.
			var given struct {
				Score int    `json:"score"`
				Risk  string `json:"risk"`
			}
			if err := json.Unmarshal([]byte(tt.raw), &given); err != nil {
				t.Fatal(err)
			}
			if p.Given != given.Score || p.RiskGiven != given.Risk || p.Clamped() != (given.Score > tt.wantScore) ||
				p.Defaulted() != (given.Risk != string(cmp.Or(tt.wantRisk, RiskCritical))) {
				t.Fatalf("given %d (clamped %v), risk given %q (defaulted %v); the answer gave %d and %q", p.Given, p.Clamped(), p.RiskGiven,
					p.Defaulted(), given.Score, given.Risk)
			}
		})
	}
}

func TestBuildConfidence(t *testing.T) {
	diff := "diff --git a/main.go b/main.go\n--- a/main.go\n+++ b/main.go\n@@ -1,1 +1,2 @@\n package main\n+func f() {}\n"
	in := Input{Repository: "o/r", Number: 7, Title: "Add f", Author: "dev", BaseRef: "main", Changed: ChangedPaths(diff), Diff: diff}
	many := make([]string, 25)
	for i := range many {
		many[i] = fmt.Sprintf("https://example.com/%d", i)
	}
	// Five ordinary paths run past the line a source is cut to.
	longPaths := strings.Repeat("kubernetes/apps/default/some-application/app/helmrelease.yaml, ", 4) + "kubernetes/apps/default/other/app/kustomization.yaml"
	if len(longPaths) <= 200 {
		t.Fatal("the unreviewed note must outrun a line")
	}
	tests := []struct {
		name    string
		body    string
		res     Result
		rec     Record
		earlier *EarlierRisk
		want    []string
	}{
		{name: "no findings", want: []string{
			"Pull request #7: Add f", "- main.go\n", "+func f() {}", "The review's account of itself:\n\nIt lists nothing it checked beyond the diff.\n",
			"\nIt read no sources outside the repository.\n", "The review reported no findings.",
		}},
		{
			name:    "the risk the last review was rated follows the findings",
			earlier: &EarlierRisk{HeadSHA: "abcdef1234", Risk: RiskHigh, Reason: "A major bump\nof the ingress chart."},
			want: []string{"The review reported no findings.\n\n\nThe last review of this pull request, at abcdef1, rated its risk high. " +
				"Its reason: A major bump of the ingress chart.\n"},
		},
		{
			name: "a rating without a reason", earlier: &EarlierRisk{HeadSHA: "abcdef1234", Risk: RiskLow},
			want: []string{"at abcdef1, rated its risk low.\n"},
		},
		{
			name: "findings follow the diff, whole",
			res: Result{Findings: []Finding{{
				Path: "main.go", Line: 2, Severity: SeverityP0, Title: "f does nothing", Explanation: "It is empty.\nCallers expect a result.",
			}}},
			want: []string{"Findings the review reported (1):", "- [p0] main.go:2 f does nothing\n  It is empty.\n  Callers expect a result.\n"},
		},
		{
			name: "the description and the review's account",
			body: "Release notes: f is new.",
			res: Result{Summary: Summary{
				Take:    "Adds f.\nIt is unused.",
				Checked: []string{"main.go: no caller of f yet", strings.Repeat("y", 400)},
			}},
			rec: Record{Sources: []string{"https://github.com/up/stream/releases/tag/v2"}},
			want: []string{
				"<description>\nRelease notes: f is new.\n</description>", "\nIts summary: Adds f. It is unused.\n",
				"\nWhat it checked beyond the diff and found sound:\n- main.go: no caller of f yet\n- " + strings.Repeat("y", 200) + " …\n",
				"\nThe sources it read outside the repository:\n- https://github.com/up/stream/releases/tag/v2\n\n\nThe review reported no findings.",
			},
		},
		{
			name: "past 20 sources, a count", rec: Record{Sources: many},
			want: []string{"- https://example.com/19\n- and 5 more\n"},
		},
		{
			name: "the record of the run follows the sources",
			rec: Record{Sources: []string{"oci://ghcr.io/x/y:1"}, CommandsRun: []string{"gh", "rg"}, CommandErrors: []string{"helm: not found\nat all"},
				Unfinished: []string{"Part 2 of 2 ended before it submitted, so its files went unreviewed: " + longPaths}},
			want: []string{
				"- oci://ghcr.io/x/y:1\n\nThe commands it ran: gh, rg.\n\nA command failed to run: helm: not found at all\n" +
					"\nUnreviewed: Part 2 of 2 ended before it submitted, so its files went unreviewed: " + longPaths + "\n\n\nThe review reported no findings.",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := in
			in.Body = tt.body
			msg := BuildConfidence(in, tt.res, tt.rec, tt.earlier, ConfidenceSystem, 0)
			for _, want := range tt.want {
				if !strings.Contains(msg, want) {
					t.Errorf("message lacks %q:\n%s", want, msg)
				}
			}
			if tt.earlier == nil && strings.Contains(msg, "The last review of this pull request") {
				t.Errorf("a rating is given with none earlier:\n%s", msg)
			}
			if len(tt.rec.CommandsRun) == 0 && strings.Contains(msg, "commands it ran") {
				t.Errorf("commands are named with none run:\n%s", msg)
			}
			if strings.Contains(msg, "https://example.com/20") {
				t.Errorf("the 21st source is listed:\n%s", msg)
			}
		})
	}
}

func TestConfidenceHold(t *testing.T) {
	const at = "db/migrations/1.sql"
	for _, tt := range []struct {
		name  string
		c     Confidence
		floor Risk
		at    string
		want  Confidence
		held  bool
	}{
		{name: "a rating under the floor rises to it", c: Confidence{Risk: RiskLow, Reason: "Docs only."}, floor: RiskCritical, at: at,
			want: Confidence{Risk: RiskCritical, Rated: RiskLow, Held: at, Reason: "Docs only."}, held: true},
		{name: "a rating at the floor stands", c: Confidence{Risk: RiskHigh, Reason: "r"}, floor: RiskHigh, at: at, want: Confidence{Risk: RiskHigh, Reason: "r"}},
		{name: "a rating over the floor stands", c: Confidence{Risk: RiskCritical, Reason: "r"}, floor: RiskMedium, at: at, want: Confidence{Risk: RiskCritical, Reason: "r"}},
		{name: "no floor holds nothing", c: Confidence{Risk: RiskLow, Reason: "r"}, floor: RiskLow, want: Confidence{Risk: RiskLow, Reason: "r"}},
		{name: "a floor raised since holds the rating higher", c: Confidence{Risk: RiskHigh, Rated: RiskLow, Held: "charts/x.yaml", Reason: "r"},
			floor: RiskCritical, at: at, want: Confidence{Risk: RiskCritical, Rated: RiskLow, Held: at, Reason: "r"}, held: true},
		{name: "a floor lowered since lets the rating go", c: Confidence{Risk: RiskCritical, Rated: RiskLow, Held: at, Reason: "r"},
			floor: RiskLow, want: Confidence{Risk: RiskLow, Reason: "r"}},
		{name: "the same floor again changes nothing", c: Confidence{Risk: RiskCritical, Rated: RiskLow, Held: at, Reason: "r"},
			floor: RiskCritical, at: at, want: Confidence{Risk: RiskCritical, Rated: RiskLow, Held: at, Reason: "r"}, held: true},
		{name: "a risk that is no level is left as it is", c: Confidence{Reason: "r"}, floor: RiskLow, want: Confidence{Reason: "r"}},
		{name: "a risk that is no level is not held either", c: Confidence{Risk: "none", Reason: "r"}, floor: RiskCritical, at: at,
			want: Confidence{Risk: "none", Reason: "r"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c := tt.c
			if held := c.Hold(tt.floor, tt.at); held != tt.held || c != tt.want {
				t.Fatalf("Hold = %v, %+v; want %v, %+v", held, c, tt.held, tt.want)
			}
		})
	}
}

func TestConfidenceExplanation(t *testing.T) {
	for _, tt := range []struct {
		name string
		c    Confidence
		want string
	}{
		{name: "the reason alone", c: Confidence{Risk: RiskLow, Reason: "Docs only."}, want: "Docs only."},
		{name: "how a floor held the risk", c: Confidence{Risk: RiskCritical, Rated: RiskLow, Held: "db/migrations/1.sql", Reason: "Docs only."},
			want: "Docs only. Held to critical risk by the instance's floor for db/migrations/1.sql."},
		{name: "no reason to follow", c: Confidence{Risk: RiskHigh, Rated: RiskLow, Held: "charts/x.yaml"},
			want: "Held to high risk by the instance's floor for charts/x.yaml."},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.c.Explanation(); got != tt.want {
				t.Fatalf("Explanation = %q, want %q", got, tt.want)
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

func TestConfidenceSystemConcerns(t *testing.T) {
	system := strings.Join(strings.Fields(ConfidenceSystem), " ")
	for _, rule := range []string{
		"score it as reported: a P2 you would rate higher still takes nothing off",
		"no reported finding raises, whatever its severity",
		"a prerequisite for merging that is unmet or that nothing in the description or the account shows was met",
		"their silence about a changed line is no sign that the review skipped it",
		"neither is a step the description sets for a release that comes after the merge",
		"Granting access to principals that did not have it, by binding them to a role or by widening a role they hold, rates at least high",
		"critical when the access is administrative, reaches secrets or production data, or cannot be quickly reverted",
		"A revert that removes the grant lowers nothing",
	} {
		t.Run(rule, func(t *testing.T) {
			if !strings.Contains(system, rule) {
				t.Error("the scorer's instructions do not say so")
			}
		})
	}
}

func TestConfidenceRubric(t *testing.T) {
	plain := ConfidenceRubric(ConfidenceSystemPrompt(""))
	if plain != ConfidenceRubric(ConfidenceSystem) || len(plain) != 16 {
		t.Fatalf("rubric = %q, want 16 hex digits the same for the same instructions", plain)
	}
	if ConfidenceRubric(ConfidenceSystemPrompt("Image bumps are low.")) == plain {
		t.Fatal("the instance's guidance does not change the rubric")
	}
}

func TestBuildConfidenceBudget(t *testing.T) {
	diff := "diff --git a/big.go b/big.go\n--- a/big.go\n+++ b/big.go\n@@ -0,0 +1,3000 @@\n" +
		strings.Repeat("+// a line of the large file under review\n", 3000)
	in := Input{Repository: "o/r", Number: 7, Title: "Add big", Author: "dev", BaseRef: "main", Changed: ChangedPaths(diff), Diff: diff}
	tests := []struct {
		name   string
		budget int
		kept   bool
	}{
		{name: "the default budget leaves the diff out", budget: 0},
		{name: "the passed budget keeps it", budget: 60_000, kept: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg := BuildConfidence(in, Result{}, Record{}, nil, ConfidenceSystem, tt.budget)
			if kept := strings.Contains(msg, "+// a line of the large file under review"); kept != tt.kept {
				t.Fatalf("diff in the message = %v, want %v", kept, tt.kept)
			}
			// The scorer has no tool to read a file left out, and is told so.
			if note := strings.Contains(msg, "[1 file(s) not shown, past the prompt budget: big.go; you cannot read them]"); note == tt.kept ||
				strings.Contains(msg, "read_diff") {
				t.Fatalf("omission note in the message = %v, want %v, never naming read_diff:\n%s", note, !tt.kept, msg[len(msg)-300:])
			}
		})
	}
}
