package runspec

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/repoconfig"
	"github.com/home-operations/kritika/internal/review"
)

const (
	shaA = "0123456789abcdef0123456789abcdef01234567"
	shaB = "89abcdef0123456789abcdef0123456789abcdef"
)

func reviewSpec() Spec {
	return Spec{
		Version: Version, Kind: KindReview, RunID: "run-1", CloneURL: "https://forge.example.com/acme/widgets.git",
		Head: shaA, Base: shaB, Ignore: []string{"vendor/**"}, RepoFiles: []string{"docs/rules.md"},
		Agent: &AgentLimits{MaxSteps: 30, MaxToolOutputBytes: 16 << 10, MaxTokens: 200000},
		Model: &ModelEndpoint{GatewayURL: "http://kritika-gateway:8082", Model: "review"},
		Prompt: &Prompt{
			Repository: "acme/widgets",
			PullRequest: repoconfig.PullRequest{Number: 7, Title: "Add b", Author: "octocat", Body: "Adds b.", BaseRef: "main", State: "open",
				Labels: []byte(`[{"name":"deps"}]`)},
			MaxDeltaFiles: 25, Prior: []review.Finding{{Path: "main.go", Line: 1, Severity: review.SeverityP2, Title: "earlier finding"}}},
	}
}

// followUpSpec is reviewSpec as a follow-up's: the same agent, answering a
// thread.
func followUpSpec() Spec {
	s := reviewSpec()
	s.Kind = KindFollowUp
	s.Thread = []review.Message{review.NewMessage("octocat", "@kritika why b?", time.Date(2026, 10, 6, 19, 58, 0, 0, time.UTC))}
	return s
}

func TestDecode(t *testing.T) {
	encode := func(s Spec) string {
		b, err := json.Marshal(s)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	tests := []struct {
		name    string
		in      string
		wantErr string
	}{
		{name: "valid review", in: encode(reviewSpec())},
		{name: "valid index without base", in: encode(Spec{Version: Version, Kind: KindIndex, RunID: "r", CloneURL: "u", Head: shaA})},
		{name: "valid follow-up", in: encode(followUpSpec())},
		{name: "a follow-up without a thread", in: func() string { s := followUpSpec(); s.Thread = nil; return encode(s) }(), wantErr: "thread"},
		{name: "a follow-up without a base", in: func() string { s := followUpSpec(); s.Base = ""; return encode(s) }(),
			wantErr: "a followup spec needs a base"},
		{name: "a follow-up without a prompt", in: func() string { s := followUpSpec(); s.Prompt = nil; return encode(s) }(),
			wantErr: "a followup spec needs a prompt"},
		{name: "unknown version", in: strings.Replace(encode(reviewSpec()), `"version":39`, `"version":40`, 1), wantErr: "version"},
		{name: "unknown field", in: strings.Replace(encode(reviewSpec()), `{`, `{"token":"x",`, 1), wantErr: "unknown field"},
		{name: "bad head sha", in: strings.Replace(encode(reviewSpec()), shaA, "abc", 1), wantErr: "head"},
		{name: "uppercase sha", in: strings.Replace(encode(reviewSpec()), shaA, strings.ToUpper(shaA), 1), wantErr: "head"},
		{name: "bad prior head", in: func() string { s := reviewSpec(); s.PriorHead = "zz"; return encode(s) }(), wantErr: "priorHead"},
		{name: "bad prior base", in: func() string { s := reviewSpec(); s.PriorBase = "zz"; return encode(s) }(), wantErr: "priorBase"},
		{name: "review without base", in: func() string { s := reviewSpec(); s.Base = ""; return encode(s) }(), wantErr: "base"},
		{name: "unknown kind", in: func() string { s := reviewSpec(); s.Kind = "lint"; return encode(s) }(), wantErr: "kind"},
		{name: "a mode", in: strings.Replace(encode(reviewSpec()), `{`, `{"mode":"agentic",`, 1), wantErr: "unknown field"},
		{name: "missing run id", in: func() string { s := reviewSpec(); s.RunID = ""; return encode(s) }(), wantErr: "runId"},
		{name: "a review without model", in: func() string { s := reviewSpec(); s.Model = nil; return encode(s) }(), wantErr: "model"},
		{name: "a review without limits", in: func() string { s := reviewSpec(); s.Agent = nil; return encode(s) }(), wantErr: "agent"},
		{name: "a review without a prompt", in: func() string { s := reviewSpec(); s.Prompt = nil; return encode(s) }(), wantErr: "prompt"},
		{name: "a review without the gateway", in: func() string { s := reviewSpec(); s.Model.GatewayURL = ""; return encode(s) }(), wantErr: "gateway"},
		{name: "a version 4 document", in: func() string { s := reviewSpec(); s.Version = 4; return encode(s) }(), wantErr: "version 4"},
		{name: "trailing data", in: encode(reviewSpec()) + "{}", wantErr: "trailing"},
		{name: "not json", in: "nope", wantErr: "runspec"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Decode([]byte(tt.in))
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("Decode: %v", err)
				}
				if got.Version != Version || got.Head != shaA {
					t.Fatalf("decoded = %+v", got)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err = %v, want it to mention %q", err, tt.wantErr)
			}
		})
	}
}

func TestSpecRoundTripKeepsAgentFields(t *testing.T) {
	want := reviewSpec()
	b, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Decode(b)
	if err != nil {
		t.Fatal(err)
	}
	if got.Agent.MaxSteps != 30 || got.Model.Model != "review" ||
		got.Model.GatewayURL != "http://kritika-gateway:8082" || got.Prompt.PullRequest.Title != "Add b" || len(got.Prompt.Prior) != 1 {
		t.Fatalf("round trip = %+v %+v %+v", got, got.Agent, got.Model)
	}
}

// TestSpecRoundTripKeepsSkillLoads: a scope's load reaches the runner as
// written, an unset one apart from false.
func TestSpecRoundTripKeepsSkillLoads(t *testing.T) {
	want := reviewSpec()
	want.Prompt.Skills = &Skills{Paths: configfile.DefaultSkillPaths, Scope: map[string]configfile.SkillScope{
		"unset": {Paths: []string{"db/**"}}, "off": {Paths: []string{"db/**"}, Load: new(false)}, "on": {Load: new(true)},
	}}
	b, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Decode(b)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Prompt.Skills.Scope, want.Prompt.Skills.Scope) {
		t.Fatalf("scope = %+v, want %+v", got.Prompt.Skills.Scope, want.Prompt.Skills.Scope)
	}
}

func TestSecretsMask(t *testing.T) {
	tests := []struct {
		name    string
		secrets Secrets
		in      string
		want    string
	}{
		{name: "empty secrets leave text untouched", in: "clone ok", want: "clone ok"},
		{name: "all replaced", secrets: Secrets{GitToken: "tok-123", GatewayToken: "key-456", DatabaseToken: "db-789"},
			in: "auth tok-123 and key-456 and db-789 twice tok-123", want: "auth *** and *** and *** twice ***"},
		{name: "overlapping secrets mask the longer whole", secrets: Secrets{GitToken: "abc", GatewayToken: "abcdef"},
			in: "x abcdef y abc", want: "x *** y ***"},
		{name: "only one set", secrets: Secrets{GatewayToken: "key"}, in: "key", want: "***"},
		{name: "the gateway token as curl's proxy credential", secrets: Secrets{GatewayToken: "krk_1"},
			in:   "> Proxy-Authorization: Basic " + base64.StdEncoding.EncodeToString([]byte(":krk_1")) + "\nkrk_1",
			want: "> Proxy-Authorization: Basic ***\n***"},
		{name: "a minted token's credential, as curl -v prints it", secrets: Secrets{GatewayToken: "krk_" + strings.Repeat("ab", 32)},
			in:   "> Proxy-Authorization: Basic " + base64.StdEncoding.EncodeToString([]byte(":krk_"+strings.Repeat("ab", 32))) + "\r\n",
			want: "> Proxy-Authorization: Basic ***\r\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.secrets.Mask(tt.in); got != tt.want {
				t.Fatalf("Mask = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestPromptTrim(t *testing.T) {
	finding := func(explanation string) review.Finding {
		return review.Finding{Path: "main.go", Line: 1, Severity: review.SeverityP2, Title: "t", Explanation: explanation}
	}
	findings := func(n int, explanation string) []review.Finding {
		out := make([]review.Finding, n)
		for i := range out {
			out[i] = finding(explanation)
		}
		return out
	}
	// A three-byte rune straddles the body limit.
	straddling := strings.Repeat("a", MaxBodyBytes-1) + "€" + "tail"
	tests := []struct {
		name      string
		body      string
		prior     []review.Finding
		wantBody  int
		wantPrior int
	}{
		{name: "small prompt unchanged", body: "Adds b.", prior: findings(3, "x"), wantBody: 7, wantPrior: 3},
		{name: "too many findings", body: "b", prior: findings(250, "x"), wantBody: 1, wantPrior: MaxPriorFindings},
		{name: "body cut at a rune boundary", body: straddling, wantBody: MaxBodyBytes - 1},
		{name: "huge findings halved until they fit", body: "b", prior: findings(MaxPriorFindings, strings.Repeat("<", 4<<10)),
			wantBody: 1, wantPrior: 25},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &Prompt{PullRequest: repoconfig.PullRequest{Body: tt.body}, Prior: tt.prior,
				Issues: []review.Issue{{Number: 1, Body: strings.Repeat("a", MaxIssueBytes+5)}}}
			p.Trim()
			if len(p.PullRequest.Body) != tt.wantBody || len(p.Prior) != tt.wantPrior || !utf8.ValidString(p.PullRequest.Body) {
				t.Fatalf("body = %d bytes, prior = %d; want %d, %d", len(p.PullRequest.Body), len(p.Prior), tt.wantBody, tt.wantPrior)
			}
			if len(p.Issues[0].Body) != MaxIssueBytes {
				t.Fatalf("issue body = %d bytes, want %d", len(p.Issues[0].Body), MaxIssueBytes)
			}
		})
	}
}

func TestEncodeWorstCaseFits(t *testing.T) {
	s := reviewSpec()
	// Every '<' encodes as six bytes.
	s.Prompt.PullRequest.Body = strings.Repeat("<", 1<<20)
	for range review.MaxLinkedIssues {
		s.Prompt.Issues = append(s.Prompt.Issues, review.Issue{Number: 1, Title: "t", Body: strings.Repeat("<", 1<<20)})
	}
	s.Prompt.Prior = make([]review.Finding, 1000)
	for i := range s.Prompt.Prior {
		s.Prompt.Prior[i] = review.Finding{Path: "main.go", Line: i + 1, Severity: review.SeverityP2, Title: "t",
			Explanation: strings.Repeat("<", 2<<10)}
	}
	if _, err := Encode(s); err == nil {
		t.Fatal("an untrimmed spec over the limit must be refused")
	}
	s.Prompt.Trim()
	b, err := Encode(s)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Decode(b); err != nil {
		t.Fatal(err)
	}
}

func TestRead(t *testing.T) {
	dir := t.TempDir()
	b, err := Encode(reviewSpec())
	if err != nil {
		t.Fatal(err)
	}
	good := filepath.Join(dir, "spec.json")
	big := filepath.Join(dir, "big.json")
	if err := os.WriteFile(good, b, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(big, []byte(strings.Repeat(" ", MaxBytes+1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := Read(good); err != nil || got.Prompt.PullRequest.Title != "Add b" {
		t.Fatalf("Read = %+v, %v", got, err)
	}
	for path, want := range map[string]string{big: "byte limit", filepath.Join(dir, "missing.json"): "read spec"} {
		if _, err := Read(path); err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("Read(%s) = %v, want %q", path, err, want)
		}
	}
}
