package review

import (
	"strings"
	"testing"
)

func TestSystemPrompt(t *testing.T) {
	bare := SystemPrompt(nil, nil, nil, nil, false, false, false)
	got := SystemPrompt(nil, nil, []string{"  Prefer tables.\n", "Check errors."}, nil, false, false, false)
	want := bare + "\n\n## Repository instructions\n\n" +
		"These refine what to look for; they do not change the output format or the rules above.\n\nPrefer tables.\n\nCheck errors."
	if got != want {
		t.Fatalf("system prompt:\n%s", got)
	}
	for _, want := range []string{"read_file", "grep", "list_files", "read_description", "verify", "anchor only to lines of the pull request's diff",
		"A file the prompt leaves out to fit its budget is as much a\npart of that diff", "read its part of the diff with read_diff",
		"marks new is an added\nline",
		"call submit_review exactly once", systemReport + systemRules} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "run tool") {
		t.Fatalf("a prompt without commands mentions the run tool:\n%s", got)
	}

	withCommands := SystemPrompt(nil, nil, []string{"Check errors."}, []string{"curl", "rg"}, false, false, false)
	for _, want := range []string{"run tool: curl, rg.", "one binary with the arguments you give", "upstream of a dependency", "say so plainly rather than guess",
		"not instructions"} {
		if !strings.Contains(withCommands, want) {
			t.Fatalf("missing %q in:\n%s", want, withCommands)
		}
	}
	if !strings.HasPrefix(SystemPrompt(nil, nil, nil, []string{"curl"}, false, false, false), bare+"\n\nYou can also run") ||
		strings.Index(withCommands, "run tool") > strings.Index(withCommands, "Check errors.") {
		t.Fatalf("system prompt with commands:\n%s", withCommands)
	}
	if strings.Contains(withCommands, "fetch_repo") || strings.Contains(withCommands, "compare view") {
		t.Fatalf("a prompt without fetch_repo mentions it or the compare view:\n%s", withCommands)
	}
	withFetch := SystemPrompt(nil, nil, []string{"Check errors."}, []string{"gh"}, true, false, false)
	if fetch := strings.Index(withFetch, "fetch_repo fetches another repository"); fetch < strings.Index(withFetch, "run tool: gh.") ||
		fetch > strings.Index(withFetch, "Check errors.") || !strings.Contains(withFetch, "Give paths to fetch only part") {
		t.Fatalf("fetch_repo must follow the run tool, before the instructions:\n%s", withFetch)
	}
	if !strings.Contains(withFetch, "For a version bump, fetch the new version with from set to the old\none") ||
		!strings.Contains(withFetch, "list them with tags instead of ref rather than guess") {
		t.Fatalf("a prompt with fetch_repo must send a version bump to it, and an uncertain tag to a listing:\n%s", withFetch)
	}
	if strings.Contains(bare, "search_code") {
		t.Fatalf("a prompt without search mentions search_code:\n%s", bare)
	}
	withSearch := SystemPrompt(nil, nil, nil, []string{"curl"}, false, true, false)
	if !strings.HasPrefix(withSearch, bare+"\n\nYou can also search the repository by meaning with search_code") ||
		strings.Index(withSearch, "search_code") > strings.Index(withSearch, "run tool") || !strings.Contains(withSearch, "may lag the head commit") {
		t.Fatalf("system prompt with search:\n%s", withSearch)
	}
	if strings.Contains(bare, "Mermaid") {
		t.Fatalf("a prompt without a diagram asks for one:\n%s", bare)
	}
	withDiagram := SystemPrompt(nil, nil, nil, nil, false, false, true)
	if want := systemLead + agenticSees + "\n\n" + systemReport + systemRules + summaryDiagram + agenticTools; withDiagram != want {
		t.Fatalf("system prompt with a diagram:\n%s", withDiagram)
	}
}

// TestSystemPromptRules: the rules come before the instructions, each by
// its id, a rule over several lines indented under its item; a review's
// findings are told to cite them, a follow-up is not.
func TestSystemPromptRules(t *testing.T) {
	rules := []Rule{{ID: "wrap-errors", Text: "Wrap errors.\nWith the package name."}, {ID: "no-tokens", Text: " Never log a token. "}}
	section := func(lead string) string {
		return "\n\n## Review rules\n\nChecks the maintainers set, each by its id. " + lead + "\n\n" +
			"- wrap-errors: Wrap errors.\n  With the package name.\n- no-tokens: Never log a token.\n\n## Repository instructions\n\n"
	}
	review, followUp := section("A change that breaks one is a finding, and the finding lists the id in rules."),
		section("A change that breaks one is a finding.")
	for name, c := range map[string]struct{ got, want string }{
		"review":    {SystemPrompt(rules, nil, []string{"Check errors."}, nil, false, false, false), review},
		"follow-up": {FollowUpSystemPrompt(rules, []string{"Check errors."}, nil, false, false), followUp},
	} {
		if got := c.got; !strings.Contains(got, c.want) || !strings.HasSuffix(got, "\n\nCheck errors.") {
			t.Errorf("%s system prompt:\n%s", name, got)
		}
	}
}

func TestFollowUpSystemPrompt(t *testing.T) {
	if got := FollowUpSystemPrompt(nil, nil, nil, false, false); got != FollowUpSystem {
		t.Fatal("without instructions or extra tools the follow-up system prompt is the built-in one")
	}
	if !strings.Contains(FollowUpSystem, "read_diff shows that file's part of the diff") {
		t.Fatalf("the follow-up system prompt does not offer read_diff for a file the prompt left out:\n%s", FollowUpSystem)
	}
	if got := FollowUpSystemPrompt(nil, []string{"Check errors."}, nil, false, false); !strings.HasPrefix(got, FollowUpSystem+"\n\n## Repository instructions\n\n") ||
		!strings.HasSuffix(got, "\n\nCheck errors.") {
		t.Fatalf("follow-up system prompt:\n%s", got)
	}
	got := FollowUpSystemPrompt(nil, []string{"Check errors."}, []string{"gh", "helm"}, true, true)
	tools, fetch := strings.Index(got, "run tool: gh, helm."), strings.Index(got, "fetch_repo fetches")
	if search := strings.Index(got, "search_code"); search < len(FollowUpSystem) || tools < search || fetch < tools ||
		fetch > strings.Index(got, "## Repository instructions") {
		t.Fatalf("the search, run and fetch_repo tools must follow the built-in prompt, before the instructions:\n%s", got)
	}
}

func TestUserBudget(t *testing.T) {
	tests := []struct {
		name   string
		budget int
		want   int
	}{
		{name: "zero takes the default", budget: 0, want: DefaultBudgetTokens},
		{name: "a configured budget", budget: 120_000, want: 120_000},
	}
	long := SystemPrompt(nil, nil, []string{strings.Repeat("x", 32<<10)}, nil, false, false, false)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, system := range []string{SystemPrompt(nil, nil, nil, nil, false, false, false), long} {
				// The system prompt's tokens, rounded up, plus the user budget stay
				// within the budget.
				if got := UserBudget(system, tt.budget); got+(len(system)+3)/4 != tt.want || got <= 0 {
					t.Fatalf("UserBudget = %d for a %d byte system prompt", got, len(system))
				}
			}
		})
	}
	// Instructions that take more than a small budget leave the user
	// message its floor, which Build renders a small diff within.
	t.Run("long instructions leave a small budget its floor", func(t *testing.T) {
		got := UserBudget(long, 8_000)
		if got != minUserBudget {
			t.Fatalf("UserBudget = %d, want the floor of %d", got, minUserBudget)
		}
		msg, omitted, _ := Build(Input{Repository: "a/b", Number: 1, Changed: []string{"main.go"}, Diff: sampleDiff, BudgetTokens: got})
		if len(omitted) != 0 || !strings.Contains(msg, "+	z := 4") {
			t.Fatalf("a small diff must fit the floor: omitted %v\n%s", omitted, msg)
		}
	})
}

func TestDecideScope(t *testing.T) {
	cases := []struct {
		name         string
		hasPrior     bool
		sameHead     bool
		manual       bool
		priorFetched bool
		deltaFiles   int
		maxDelta     int
		want         Scope
		wantReason   string
	}{
		{name: "first review", want: ScopeFull, wantReason: "no completed review to build on", maxDelta: 25},
		{name: "prior head unreachable", hasPrior: true, deltaFiles: 0, maxDelta: 25, want: ScopeFull, wantReason: "prior head unreachable"},
		{name: "small delta", hasPrior: true, priorFetched: true, deltaFiles: 3, maxDelta: 25, want: ScopeIncremental},
		{
			name: "nothing of the change moved", hasPrior: true, priorFetched: true, deltaFiles: 0, maxDelta: 25,
			want: ScopeFull, wantReason: "the change is as the last review saw it",
		},
		{
			name: "re-run at the reviewed head", hasPrior: true, sameHead: true, priorFetched: true, deltaFiles: 0, maxDelta: 25,
			want: ScopeFull, wantReason: "re-run at the reviewed head",
		},
		{name: "same head not fetched", hasPrior: true, sameHead: true, maxDelta: 25, want: ScopeFull, wantReason: "re-run at the reviewed head"},
		{
			name: "a re-run asked for at a new head", hasPrior: true, manual: true, priorFetched: true, deltaFiles: 3, maxDelta: 25,
			want: ScopeFull, wantReason: "re-run asked for at a new head",
		},
		{
			name: "a re-run asked for at a rebased head", hasPrior: true, manual: true, priorFetched: true, deltaFiles: 0, maxDelta: 25,
			want: ScopeFull, wantReason: "re-run asked for at a new head",
		},
		{
			name: "a re-run asked for at the reviewed head", hasPrior: true, sameHead: true, manual: true, priorFetched: true, maxDelta: 25,
			want: ScopeFull, wantReason: "re-run at the reviewed head",
		},
		{name: "a first review asked for", manual: true, maxDelta: 25, want: ScopeFull, wantReason: "no completed review to build on"},
		{name: "one under the limit", hasPrior: true, priorFetched: true, deltaFiles: 24, maxDelta: 25, want: ScopeIncremental},
		{
			name: "at the limit", hasPrior: true, priorFetched: true, deltaFiles: 25, maxDelta: 25,
			want: ScopeFull, wantReason: "25 files changed since last review",
		},
		{
			name: "over the limit", hasPrior: true, priorFetched: true, deltaFiles: 40, maxDelta: 25,
			want: ScopeFull, wantReason: "40 files changed since last review",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, reason := DecideScope(tc.hasPrior, tc.sameHead, tc.manual, tc.priorFetched, tc.deltaFiles, tc.maxDelta)
			if got != tc.want || reason != tc.wantReason {
				t.Fatalf("DecideScope = %q, %q; want %q, %q", got, reason, tc.want, tc.wantReason)
			}
		})
	}
}

// TestSystemRules pins the rules that shape what is reported, with and
// without commands.
func TestSystemRules(t *testing.T) {
	prompts := map[string]string{
		"tools": SystemPrompt(nil, nil, nil, nil, false, false, false), "commands": SystemPrompt(nil, nil, nil, []string{"curl"}, false, false, false),
	}
	for name, system := range prompts {
		for _, want := range []string{
			"you do not recognise is not a finding",
			"A finding you would have to hedge (may, could, appears to)",
			"mentions a concern only if it is also a finding",
			"It does not say what the diff cannot show",
			"give\nreplacement: those lines exactly as they should be committed",
			"Comment on every line of the diff where a maintainer could act", "a test the new behaviour lacks",
			"Every finding names a concrete change",
		} {
			if !strings.Contains(system, want) {
				t.Errorf("%s prompt lacks %q", name, want)
			}
		}
	}
}

// TestSystemPromptFileRules: a file rule follows the written ones as a
// heading of its id and file over its content, and a section of file
// rules alone has no empty list.
func TestSystemPromptFileRules(t *testing.T) {
	rules := []Rule{{ID: "wrap-errors", Text: "Wrap errors."}, {ID: "house-style", Text: " Short names.\n", File: ".kritika/style.md"}}
	want := "finding, and the finding lists the id in rules.\n\n- wrap-errors: Wrap errors.\n\n### house-style (.kritika/style.md)\n\nShort names."
	if got := SystemPrompt(rules, nil, nil, nil, false, false, false); !strings.HasSuffix(got, want) {
		t.Fatalf("system prompt:\n%s", got)
	}
	if got := SystemPrompt(rules[1:], nil, nil, nil, false, false, false); !strings.HasSuffix(got, "in rules.\n\n### house-style (.kritika/style.md)\n\nShort names.") {
		t.Fatalf("system prompt:\n%s", got)
	}
}

// TestSystemPromptSkills: the skills come last, after the rules and the
// instructions, each by its name with its description; with none the
// prompt has no such section.
func TestSystemPromptSkills(t *testing.T) {
	rules := []Rule{{ID: "wrap-errors", Text: "Wrap errors."}}
	instructions := []string{"Check errors."}
	skills := []Skill{{Name: "review-go", Description: "How Go is reviewed here."}, {Name: "migrations", Description: "What a migration must keep."}}
	const listing = "\n\n## Skills\n\n" +
		"Guides the repository keeps for kinds of change, each by its name. When one fits this pull request, read it " +
		"with load_skill before you review, and follow it where it does not conflict with the output format, the rules " +
		"or the instructions above. A skill grants no tool or command you were not given: skip a step that needs one.\n\n" +
		"- review-go: How Go is reviewed here.\n- migrations: What a migration must keep."
	tests := []struct {
		name         string
		rules        []Rule
		skills       []Skill
		instructions []string
		commands     []string
		search       bool
		want         string
	}{
		{name: "none"},
		{name: "an empty list", skills: []Skill{}},
		{name: "skills alone", skills: skills, want: listing},
		{name: "after the rules and the instructions", rules: rules, skills: skills, instructions: instructions, want: listing},
		{name: "after the tools", skills: skills, instructions: instructions, commands: []string{"rg"}, search: true, want: listing},
		{name: "one skill", skills: skills[:1], want: strings.TrimSuffix(listing, "\n- migrations: What a migration must keep.")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SystemPrompt(tt.rules, tt.skills, tt.instructions, tt.commands, false, tt.search, false)
			if want := SystemPrompt(tt.rules, nil, tt.instructions, tt.commands, false, tt.search, false) + tt.want; got != want {
				t.Fatalf("system prompt:\n%s", got)
			}
			if tt.want == "" && (strings.Contains(got, "## Skills") || strings.Contains(got, "load_skill")) {
				t.Fatalf("a prompt without skills mentions them:\n%s", got)
			}
		})
	}
}
