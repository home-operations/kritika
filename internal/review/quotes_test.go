package review

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestQuotes(t *testing.T) {
	text := "## Errors\n\n- Wrap errors with `fmt.Errorf(\"<package>: %w\", err)`, so a caller keeps the *original* error.\n" +
		"- See [the guide](https://example.test/guide) for sentinel errors."
	for _, tt := range []struct {
		name, quote string
		want        bool
	}{
		{"the rule's words", "Wrap errors with fmt.Errorf", true},
		{"with its markdown", "- Wrap errors with `fmt.Errorf(\"<package>: %w\", err)`", true},
		{"other case and spacing", "wrap  errors\nWITH fmt.errorf", true},
		{"a link by its text", "See the guide for sentinel errors.", true},
		{"curly quotes", "fmt.Errorf(“<package>: %w”, err)", true},
		{"too short", "Wrap errors", false},
		{"not in the rule", "Wrap errors with errors.Join", false},
		{"blank", " \n", false},
		{"an identifier's underscores are not emphasis", "callers keep the original_error", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := quotes(text, tt.quote); got != tt.want {
				t.Errorf("quotes(%q) = %v, want %v", tt.quote, got, tt.want)
			}
		})
	}
	code := "Always _wrap errors_ with **context**; pass `*User` to Save, never `User`, and keep ~~old~~ get_user_id. " +
		"Call `__init__`, **return `err`** and reject `*secret*` paths."
	for quote, want := range map[string]bool{
		"Always wrap errors with context":        true,
		"always _wrap errors_ with **context**":  true,
		"pass *User to Save":                     true,
		"pass User to Save":                      false,
		"keep old get_user_id":                   true,
		"keep old getuserid":                     false,
		"with *context*; pass":                   true,
		"Call `__init__`, return err":            true,
		"Call init, return err":                  false,
		"return err and reject `*secret*` paths": true,
		"reject secret paths":                    false,
	} {
		if got := quotes(code, quote); got != want {
			t.Errorf("quotes(code, %q) = %v, want %v", quote, got, want)
		}
	}
	spans := "Validate with `[a-z](foo)`; write `` `*User*` `` in docs, and never a lone ` backtick."
	for quote, want := range map[string]bool{
		"Validate with `[a-z](foo)`":   true,
		"Validate with `a-z`":          false,
		"write `*User*` in docs":       true,
		"write User in docs":           false,
		"never a lone ` backtick":      true,
		"never a lone backtick":        true,
		"never a lone \uE000 backtick": true,
	} {
		if got := quotes(spans, quote); got != want {
			t.Errorf("quotes(spans, %q) = %v, want %v", quote, got, want)
		}
	}
	if !quotes("x \uE000 y and more words here", "x \uE000 y and more words") {
		t.Error("a stand-in rune in the text is dropped, never a span")
	}
	if !quotes("Use `rg`.", "use rg") {
		t.Error("a short rule is quoted whole")
	}
	if quotes("Use `rg`, never grep.", "never grep") {
		t.Error("a part of a short rule is too short to quote")
	}
	if !quotes("> - Never log tokens.\n> - Always redact passwords.", "Never log tokens.\nAlways redact passwords.") {
		t.Error("nested markers are stripped")
	}
	if quotes("禁止在日志中记录令牌或密码。", "禁止在日志中记录") {
		t.Error("the minimum counts characters, not bytes")
	}
	if !quotes("禁止在日志中记录令牌或密码。", "禁止在日志中记录令牌或密码") {
		t.Error("twelve characters of a rule quote it")
	}
}

func TestCheckQuotes(t *testing.T) {
	rules := []Rule{{ID: "wrap-errors", Text: "Wrap errors with fmt.Errorf and the package name."}, {ID: "no-tokens", Text: "Never log a token."}}
	sub := func(finding string) json.RawMessage {
		return json.RawMessage(`{"summary":{"headline":"H","take":"T.","praise":[]},"findings":[{"path":"a.go","line":1,` +
			`"severity":"p2","category":"correctness","title":"t","explanation":"e"` + finding + `}]}`)
	}
	for _, tt := range []struct {
		name, finding, wantErr string
	}{
		{name: "no rules cited", finding: ``},
		{name: "no rules cited and a quote", finding: `,"quote":"Never log a token."`},
		{name: "a rule quoted", finding: `,"rules":["wrap-errors"],"quote":"fmt.Errorf and the package name"`},
		{name: "one of two rules quoted", finding: `,"rules":["wrap-errors","no-tokens"],"quote":"never log a token."`},
		{name: "only a rule the review was not given", finding: `,"rules":["made-up"]`},
		{name: "a rule cited with no quote", finding: `,"rules":["wrap-errors"]`,
			wantErr: "a.go:1 cites rule wrap-errors but has no quote. Copy the passage"},
		{name: "a rule cited with a blank quote", finding: `,"rules":["wrap-errors"],"quote":" "`,
			wantErr: "a.go:1 cites rule wrap-errors but has no quote"},
		{name: "a quote the rules do not have", finding: `,"rules":["wrap-errors","no-tokens"],"quote":"Wrap errors with errors.Join."`,
			wantErr: "a.go:1's quote is not in rules wrap-errors, no-tokens. Copy the passage of the rule each finding enforces into " +
				"its quote, verbatim, as the Review rules section has it, at least 12 characters of it or the whole rule when it is shorter"},
		{name: "a quote too short to be sure of", finding: `,"rules":["wrap-errors"],"quote":"Wrap errors"`,
			wantErr: "a.go:1's quote is not in rule wrap-errors"},
		{name: "every refused finding named at once", finding: `,"rules":["wrap-errors"]},{"path":"b.go","line":2,"severity":"p2",` +
			`"category":"correctness","title":"t","explanation":"e","rules":["no-tokens"],"quote":"Log every token."`,
			wantErr: "a.go:1 cites rule wrap-errors but has no quote; b.go:2's quote is not in rule no-tokens."},
		{name: "the shape still checked first", finding: `,"rules":["wrap-errors"],"mood":"ok"`, wantErr: "unknown keys"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := CheckQuotes(Check, rules)(sub(tt.finding))
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("refused: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err = %v, want one containing %q", err, tt.wantErr)
			}
		})
	}
	if err := CheckQuotes(Check, nil)(sub(`,"rules":["wrap-errors"]`)); err != nil {
		t.Errorf("no rules refused a finding: %v", err)
	}
	if !Lenient(CheckQuotes(Check, rules))(sub(`,"rules":["wrap-errors"],"quote":"fmt.Errorf and the package name","mood":"ok"`)) {
		t.Error("a quoted finding with an unknown key is a lenient answer")
	}
	if Lenient(CheckQuotes(Check, rules))(sub(`,"rules":["wrap-errors"],"mood":"ok"`)) {
		t.Error("an unquoted citation is not a lenient answer")
	}
}

// TestQuoteGate: a gate refuses a misquoted submission MaxQuoteRefusals times
// and takes the next. Drop then removes the misquoted findings and nothing
// else, and leaves a clean submission as it is.
func TestQuoteGate(t *testing.T) {
	rules := []Rule{{ID: "wrap-errors", Text: "Wrap errors with fmt.Errorf and the package name."}}
	quoted := `{"path":"a.go","line":1,"severity":"p2","category":"correctness","title":"t","explanation":"e","rules":["wrap-errors"],"quote":"fmt.Errorf and the package name"}`
	unquoted := `{"path":"b.go","line":2,"severity":"p2","category":"correctness","title":"u","explanation":"e","rules":["wrap-errors"]}`
	raw := json.RawMessage(`{"summary":{"take":"T.","praise":[]},"findings":[` + quoted + `,` + unquoted + `]}`)
	g := NewQuoteGate(rules)
	check := g.Check(Check)
	for i := range MaxQuoteRefusals {
		if err := check(raw); err == nil || !strings.Contains(err.Error(), "b.go:2 cites rule wrap-errors but has no quote") {
			t.Fatalf("refusal %d = %v", i+1, err)
		}
	}
	if err := check(raw); err != nil {
		t.Fatalf("the submission after %d refusals = %v, want it taken", MaxQuoteRefusals, err)
	}
	// Peek refuses as Check does and spends nothing: the fallback judges a
	// submission that Check refused once more without costing a refusal.
	fresh := NewQuoteGate(rules)
	for range MaxQuoteRefusals + 1 {
		if err := fresh.Peek(Check)(raw); err == nil {
			t.Fatal("Peek took a misquoted submission")
		}
	}
	if fresh.refused != 0 || Lenient(fresh.Peek(Check))(raw) {
		t.Fatalf("Peek spent %d refusals, or held a misquoted submission", fresh.refused)
	}
	if err := fresh.Check(Check)(raw); err == nil || fresh.refused != 1 {
		t.Fatalf("Check after Peek = %v, refused %d; want one refusal", err, fresh.refused)
	}
	// Drop reads the findings as the worker does, and keeps the rest of
	// the submission, a key the contract does not define included.
	raw = json.RawMessage(`{"summary":{"take":"T.","praise":[]},"verdict":"x","Findings":[` + quoted + `,` + unquoted + `]}`)
	dropped, why, err := g.Drop(raw)
	if err != nil || len(why) != 1 || !strings.Contains(why[0], "b.go:2") {
		t.Fatalf("Drop = %s, %q, %v", dropped, why, err)
	}
	var res Result
	if err := json.Unmarshal(dropped, &res); err != nil || len(res.Findings) != 1 || res.Findings[0].Title != "t" ||
		!strings.Contains(string(dropped), `"verdict":"x"`) || strings.Contains(string(dropped), `"Findings"`) {
		t.Fatalf("dropped = %s, %v; want the quoted finding alone under findings, the rest kept", dropped, err)
	}
	clean := json.RawMessage(`{"summary":{"take":"T.","praise":[]},"findings":[` + quoted + `]}`)
	if same, why, err := g.Drop(clean); err != nil || why != nil || string(same) != string(clean) {
		t.Fatalf("a clean submission = %s, %q, %v; want it as it is", same, why, err)
	}
	if same, why, err := NewQuoteGate(nil).Drop(raw); err != nil || why != nil || string(same) != string(raw) || NewQuoteGate(nil) != nil {
		t.Fatalf("no rules = %s, %q, %v", same, why, err)
	}
}
