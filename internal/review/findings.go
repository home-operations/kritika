// Package review turns a context pack into a prompt, a model answer into
// findings, and findings into the comments kritika posts. It knows nothing
// about forges or models beyond their interfaces.
//
// # Templates
//
// Comments are rendered from Go text/template templates that a repository
// may replace, with the go-sprout/sprout helpers that tuppr and chaski
// expose: the std, strings, conversion, encoding, numeric, slices, maps,
// regex, time, semver and reflect registries, less set and unset. Not
// available: env, filesystem, network, random, uniqueid, checksum and
// crypto, and the template, define and block actions, so a template can
// read no file and call no other template. Rendering is bounded, and a
// template that steps outside a bound falls back to kritika's default with
// a note:
//
//   - output of 64 KiB, marker included;
//   - 20,000 loop iterations per render, charged when a loop starts;
//   - 256 KiB per function call, counting its arguments, its result and,
//     for repeat, indent, nindent, join, replace, regexReplaceAll,
//     regexReplaceAllLiteral, seq, until, untilStep and printf, an estimate
//     of what it allocates; printf refuses a width or precision given as *;
//   - two seconds per render; identifiers starting with __kritika_ are
//     reserved.
//
// The summary template's dot is a RenderData, the inline template's a
// Finding; see templates/ for the defaults.
package review

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/home-operations/kritika/internal/contextpack"
)

// Severity of a finding, in the order the summary lists them.
type Severity string

// Severities a finding may carry.
const (
	SeverityBlocking  Severity = "blocking"
	SeverityImportant Severity = "important"
	SeverityNit       Severity = "nit"
)

var severities = []Severity{SeverityBlocking, SeverityImportant, SeverityNit}

// Valid reports whether s is one of the severities.
func (s Severity) Valid() bool { return slices.Contains(severities, s) }

// Rank orders severities most serious first; an invalid one sorts last.
func (s Severity) Rank() int {
	if i := slices.Index(severities, s); i >= 0 {
		return i
	}
	return len(severities)
}

// Category is what kind of problem a finding is: the dimension of the
// review it comes from, apart from how serious it is.
type Category string

// Categories a finding may carry.
const (
	CategoryCorrectness     Category = "correctness"
	CategorySecurity        Category = "security"
	CategoryPerformance     Category = "performance"
	CategoryReliability     Category = "reliability"
	CategoryMaintainability Category = "maintainability"
	CategoryTests           Category = "tests"
)

var categories = []Category{
	CategoryCorrectness, CategorySecurity, CategoryPerformance, CategoryReliability, CategoryMaintainability, CategoryTests,
}

// Categories lists the categories, in the order the dashboard shows them.
func Categories() []Category { return slices.Clone(categories) }

// Valid reports whether c is one of the categories.
func (c Category) Valid() bool { return slices.Contains(categories, c) }

// Summary is the review's overall judgement for the sticky comment.
type Summary struct {
	// Headline is one sentence on what the change does, the summary
	// comment's first line; "" from a review made before it was asked for.
	Headline string   `json:"headline,omitempty"`
	Take     string   `json:"take"`
	Praise   []string `json:"praise"`
	// Diagram is Mermaid source for the flow the change adds or alters,
	// without fences; "" when the change has no flow worth drawing or the
	// model's diagram was not one Parse keeps.
	Diagram string `json:"diagram,omitempty"`
	// Checked are the review's notes for the next review of the pull
	// request on what it checked and found sound, one line each; no
	// comment shows them.
	Checked []string `json:"checked,omitempty"`
}

// maxPraise bounds Summary.Praise; the schema says so and Parse enforces it.
const maxPraise = 3

// maxChecked bounds Summary.Checked; the schema says so and Parse enforces
// it, as it cuts each note to one line.
const maxChecked = 10

// maxDiagramBytes bounds Summary.Diagram. A diagram over it is dropped
// rather than cut, since a cut one would not render.
const maxDiagramBytes = 4 << 10

// diagramKinds are the Mermaid diagram types a summary may draw, by the
// keyword its source opens with.
var diagramKinds = []string{"flowchart", "graph", "sequenceDiagram"}

// Finding is one thing the reviewer wants a human to look at, anchored to a
// line on the head side of the diff, or to the range Line through EndLine.
type Finding struct {
	Path     string   `json:"path"`
	Line     int      `json:"line"`
	Severity Severity `json:"severity"`
	// Category is what kind of problem it is; see Category.
	Category     Category `json:"category"`
	Title        string   `json:"title"`
	Explanation  string   `json:"explanation"`
	SuggestedFix string   `json:"suggested_fix,omitempty"`
	// EndLine is the last line of the range the finding covers, 0 when it
	// covers Line alone.
	EndLine int `json:"end_line,omitempty"`
	// Replacement is what lines Line through EndLine should read instead,
	// raw code the forge offers as a one-click suggestion.
	Replacement string `json:"replacement,omitempty"`
	// InsertAfter is the lines a fix adds right after Line, changing none.
	// Parse folds it into Replacement, Line kept as the diff shows it, so
	// the model never reproduces an existing line; it is empty after Parse.
	InsertAfter string `json:"insert_after,omitempty"`
	// AgentPrompt is one paragraph telling a coding agent how to apply the
	// fix.
	AgentPrompt string `json:"agent_prompt,omitempty"`
	// Rules are the ids of the review rules the finding enforces; Parse
	// keeps only those the review was given.
	Rules []string `json:"rules,omitempty"`
	// Prior is the id of the last review's finding this one reports again,
	// as the prompt listed it; Parse resolves it into Fingerprint and
	// clears it.
	Prior string `json:"prior,omitempty"`
	// Fingerprint is the finding's fingerprint when it is known apart from
	// its path and title: Parse sets it when the finding carries on a
	// prior one's, the worker when it reads a finding back. Fingerprint()
	// prefers it, so a finding reported again in other words keeps the
	// thread its first wording opened.
	Fingerprint string `json:"fingerprint,omitempty"`
	// URL links the finding's lines at the head commit. kritika sets it
	// when rendering; the model never does.
	URL string `json:"-"`
	// ThreadURL links the finding's inline comment thread on the forge,
	// "" when it has none. kritika sets it when rendering.
	ThreadURL string `json:"-"`
}

// PriorFinding is a finding the last review made and this one did not
// report again, as the summary of a review that builds on it lists them.
type PriorFinding struct {
	Finding
	// Resolved is whether this review, asked to report the finding again
	// only if still present, did not.
	Resolved bool
	// Dismissed is whether a maintainer dismissed the finding, with
	// DismissReason the reason they gave, "" for none.
	Dismissed     bool
	DismissReason string
}

// DismissedFinding is a finding a maintainer dismissed on the pull
// request, with their reason, which a review is told not to raise again.
type DismissedFinding struct {
	Finding
	Reason string `json:"reason,omitempty"`
}

// AgentPromptFence is a code fence longer than any backtick run in
// AgentPrompt, so the prompt renders as one block whatever it contains.
func (f Finding) AgentPromptFence() string {
	longest, run := 0, 0
	for _, r := range f.AgentPrompt {
		if r == '`' {
			run++
			longest = max(longest, run)
		} else {
			run = 0
		}
	}
	return strings.Repeat("`", max(3, longest+1))
}

// Result is the whole answer.
type Result struct {
	Summary  Summary   `json:"summary"`
	Findings []Finding `json:"findings"`
}

// Counts is the number of findings at each severity.
type Counts struct {
	Blocking, Important, Nit int
}

// Counts tallies the findings by severity.
func (r Result) Counts() Counts {
	var c Counts
	for _, f := range r.Findings {
		switch f.Severity {
		case SeverityBlocking:
			c.Blocking++
		case SeverityImportant:
			c.Important++
		case SeverityNit:
			c.Nit++
		}
	}
	return c
}

// Total is the number of findings at any severity.
func (c Counts) Total() int { return c.Blocking + c.Important + c.Nit }

// Approvable reports whether the result lets kritika approve the pull
// request: nothing blocking and nothing important, so nits alone do not
// withhold an approval.
func (c Counts) Approvable() bool { return c.Blocking == 0 && c.Important == 0 }

// DropReason says why Parse discarded a finding.
type DropReason string

// Reasons a finding is dropped.
const (
	DropUnanchored  DropReason = "unanchored"
	DropIncomplete  DropReason = "incomplete"
	DropNoFix       DropReason = "no_suggested_fix"
	DropBadSeverity DropReason = "bad_severity"
	DropBadCategory DropReason = "bad_category"
)

// Dropped is a finding Parse discarded, with the reason.
type Dropped struct {
	Finding Finding
	Reason  DropReason
}

// ParseOptions tune what Parse accepts.
type ParseOptions struct {
	// RequireSuggestedFix drops findings that carry no suggested fix.
	RequireSuggestedFix bool
	// Diagram keeps the summary's diagram; without it the diagram is
	// dropped, whatever the model sent.
	Diagram bool
	// Rules are the ids of the rules the review was given, the only ones a
	// finding may cite.
	Rules []string
	// Repository is the "owner/repo" under review, whose references the
	// model's text keeps; RedirectReferences rewrites the others.
	Repository string
	// Prior are the last review's findings, as the prompt listed them: a
	// finding on one's path that names its id, or that has its path and
	// title, carries its fingerprint on, as the first such finding only.
	Prior []Finding
}

// Field names of the contract, shared by its JSON Schema and the template
// context, so a template sees the names the model was asked for.
const (
	keySummary      = "summary"
	keyHeadline     = "headline"
	keyTake         = "take"
	keyPraise       = "praise"
	keyDiagram      = "diagram"
	keyChecked      = "checked"
	keyFindings     = "findings"
	keyPath         = "path"
	keyLine         = "line"
	keySeverity     = "severity"
	keyCategory     = "category"
	keyTitle        = "title"
	keyExplanation  = "explanation"
	keySuggestedFix = "suggested_fix"
	keyEndLine      = "end_line"
	keyReplacement  = "replacement"
	keyInsertAfter  = "insert_after"
	keyAgentPrompt  = "agent_prompt"
	keyRules        = "rules"
	keyPrior        = "prior"
)

// JSON Schema types the answer shapes use more than once.
const schemaObject, schemaString, schemaArray = "object", "string", "array"

// jsonSchema is the subset of JSON Schema kritika's answer shapes use.
type jsonSchema struct {
	Type        string                 `json:"type"`
	Description string                 `json:"description,omitempty"`
	Enum        []string               `json:"enum,omitempty"`
	Properties  map[string]*jsonSchema `json:"properties,omitempty"`
	Items       *jsonSchema            `json:"items,omitempty"`
	Required    []string               `json:"required,omitempty"`
	MaxItems    int                    `json:"maxItems,omitempty"`
	// AdditionalProperties is false on an object that takes no key beyond
	// Properties, so a provider that enforces the schema refuses a
	// misspelled key before Check has to.
	AdditionalProperties *bool `json:"additionalProperties,omitempty"`
}

func (s jsonSchema) mustMarshal() json.RawMessage {
	b, err := json.Marshal(s)
	if err != nil {
		panic(fmt.Sprintf("review: schema does not marshal: %v", err))
	}
	return b
}

// Descriptions of the fix fields, which the model must get exactly right
// for a one-click suggestion to be worth offering.
const (
	describeEndLine = "Last line of the range the finding covers, in the new version of the file; " +
		"omit when it covers line alone."
	describeReplacement = "What lines line through end_line should read instead, complete and exactly as they " +
		"should be committed: raw code, no fences, no commentary. Only when the fix is a change to those lines; " +
		"lines to add after line go in insert_after."
	describeInsertAfter = "Lines the fix adds right after line, which itself stays as it is: raw code, no fences, " +
		"no commentary, indented as the file is, exactly as they should be committed. Omit it when the fix changes " +
		"existing lines; replacement covers that."
	describeAgentPrompt = "One plain-text paragraph telling a coding agent how to apply the fix: the file, " +
		"the lines, the symbols and the exact change."
	describeRules = "Ids of the review rules this finding enforces, as the Review rules section lists them; " +
		"omit when it enforces none."
	describePrior = "Id of the last review's finding this one reports again, as its listing opens each line with, " +
		"whatever the wording now; the finding then keeps that one's thread. Omit it for a new finding."
	describeDiagram = "Mermaid source, raw with no fences, opening with flowchart or sequenceDiagram, of the flow the " +
		"change adds or alters as the head commit has it, drawn as the instructions say. Omit it when the change has no " +
		"such flow."
	describeChecked = "For the next review of this pull request, which starts without your reading: what you read " +
		"beyond the diff and found sound, one short line each naming the file or symbol and what you verified. " +
		"No comment shows it; leave it empty when there is nothing to pass on."
	describeCategory = "What kind of problem it is. correctness: wrong behaviour, a bug, a broken contract. " +
		"security: exposure, injection, secrets, unsafe defaults, data loss. performance: cost in time, memory or calls. " +
		"reliability: error handling, retries, timeouts, concurrency, resource leaks. maintainability: structure, " +
		"naming, clarity, duplication, dead code. tests: a test the change needs or a test that is wrong."
)

// contractSchema is kept minimal on purpose: every extra field is something
// a model can get wrong. diagram adds summary.diagram, which a review is
// asked for only where the repository opts in.
func contractSchema(requireFix, diagram bool) jsonSchema {
	required := []string{keyPath, keyLine, keySeverity, keyCategory, keyTitle, keyExplanation}
	fix := "A concrete fix: replacement code or a precise instruction. Markdown allowed, no headings."
	if requireFix {
		required = append(required, keySuggestedFix)
	} else {
		fix += " Omit it when there is no concrete fix."
	}
	enum := make([]string, len(severities))
	for i, s := range severities {
		enum[i] = string(s)
	}
	kinds := make([]string, len(categories))
	for i, c := range categories {
		kinds[i] = string(c)
	}
	summary := summaryProperties(diagram)
	summary[keyChecked] = &jsonSchema{
		Type: schemaArray, Description: describeChecked, Items: &jsonSchema{Type: schemaString}, MaxItems: maxChecked,
	}
	return jsonSchema{
		Type: schemaObject,
		Properties: map[string]*jsonSchema{
			keySummary: {
				Type:                 schemaObject,
				Properties:           summary,
				Required:             []string{keyHeadline, keyTake, keyPraise},
				AdditionalProperties: new(false),
			},
			keyFindings: {
				Type: schemaArray,
				Items: &jsonSchema{
					Type: schemaObject,
					Properties: map[string]*jsonSchema{
						keyPath: {Type: schemaString, Description: "Path of the changed file, exactly as it appears in the diff header."},
						keyLine: {Type: "integer", Description: "Line number in the new version of the file (a + or context line inside a hunk)."},
						keySeverity: {Type: schemaString, Enum: enum,
							Description: "blocking: must be fixed before merging. important: should be fixed. nit: optional polish."},
						keyCategory:     {Type: schemaString, Enum: kinds, Description: describeCategory},
						keyTitle:        {Type: schemaString, Description: "One line, under 80 characters."},
						keyExplanation:  {Type: schemaString, Description: "Why it matters. Markdown allowed, no headings."},
						keySuggestedFix: {Type: schemaString, Description: fix},
						keyEndLine:      {Type: "integer", Description: describeEndLine},
						keyReplacement:  {Type: schemaString, Description: describeReplacement},
						keyInsertAfter:  {Type: schemaString, Description: describeInsertAfter},
						keyAgentPrompt:  {Type: schemaString, Description: describeAgentPrompt},
						keyRules:        {Type: schemaArray, Description: describeRules, Items: &jsonSchema{Type: schemaString}},
						keyPrior:        {Type: schemaString, Description: describePrior},
					},
					Required:             required,
					AdditionalProperties: new(false),
				},
			},
		},
		Required:             []string{keySummary, keyFindings},
		AdditionalProperties: new(false),
	}
}

// summaryProperties are a summary's headline, take and praise, and its
// diagram when diagram is set, as a schema states them.
func summaryProperties(diagram bool) map[string]*jsonSchema {
	summary := map[string]*jsonSchema{
		keyHeadline: {
			Type:        schemaString,
			Description: "One sentence, under twelve words, on what the change does; it opens the comment. No markdown.",
		},
		keyTake: {
			Type:        schemaString,
			Description: "Two to four sentences: what the change does and the overall assessment. No markdown headings.",
		},
		keyPraise: {
			Type:        schemaArray,
			Description: "Up to three specific things the change does well; empty when nothing stands out.",
			Items:       &jsonSchema{Type: schemaString},
			MaxItems:    maxPraise,
		},
	}
	if diagram {
		summary[keyDiagram] = &jsonSchema{Type: schemaString, Description: describeDiagram}
	}
	return summary
}

// contract picks one of the schemas contractSchema builds.
type contract struct{ strict, diagram bool }

var contracts = map[contract]json.RawMessage{
	{false, false}: contractSchema(false, false).mustMarshal(),
	{false, true}:  contractSchema(false, true).mustMarshal(),
	{true, false}:  contractSchema(true, false).mustMarshal(),
	{true, true}:   contractSchema(true, true).mustMarshal(),
}

// The keys the fullest contract defines at each level. Check holds every
// submission to them whichever schema it was asked with: a diagram it was
// not asked for is Parse's to drop, not a misspelling to send back.
var topKeys, summaryKeys, findingKeys = contractKeys()

func contractKeys() (top, summary, finding []string) {
	s := contractSchema(true, true)
	return slices.Sorted(maps.Keys(s.Properties)),
		slices.Sorted(maps.Keys(s.Properties[keySummary].Properties)),
		slices.Sorted(maps.Keys(s.Properties[keyFindings].Items.Properties))
}

// Schema is the JSON Schema of the answer the model must produce, with
// summary.diagram when diagram is set.
func Schema(diagram bool) json.RawMessage { return slices.Clone(contracts[contract{false, diagram}]) }

// SchemaStrict is Schema with suggested_fix required on every finding.
func SchemaStrict(diagram bool) json.RawMessage {
	return slices.Clone(contracts[contract{true, diagram}])
}

// Check says why raw is not a review in the contract's shape: a field of
// the wrong type, a key the contract does not define, or no summary take.
// It is what the agent loop answers a submit_review call with, so the
// model can correct it before the review ends; Parse applies the rest of
// the contract to the submission that ended it.
func Check(raw json.RawMessage) error {
	var res Result
	if err := json.Unmarshal(raw, &res); err != nil {
		return fmt.Errorf("review: %w; the input is an object with a summary object {headline, take, praise} and a findings array", err)
	}
	if unknown := unknownKeys(raw); len(unknown) > 0 {
		return fmt.Errorf("review: %w %s; the input takes %s, the summary takes %s and a finding takes %s", errUnknownKeys,
			strings.Join(unknown, ", "), strings.Join(topKeys, ", "), strings.Join(summaryKeys, ", "), strings.Join(findingKeys, ", "))
	}
	if strings.TrimSpace(res.Summary.Take) == "" {
		return errors.New("review: summary.take is required: two to four sentences on the change")
	}
	if d := strings.TrimSpace(res.Summary.Diagram); d != "" && diagram(d) == "" {
		return fmt.Errorf("review: summary.diagram must be Mermaid source under %d bytes opening with %s, or be left out",
			maxDiagramBytes, strings.Join(diagramKinds, ", "))
	}
	return nil
}

// unknownKeys lists the keys raw carries that the contract does not
// define, each with its path, so one error names them all and the model
// can correct every one in a single resubmission. A value under a
// misspelled key is otherwise dropped on decoding and the finding posts
// without it, with nothing to say so.
func unknownKeys(raw json.RawMessage) []string {
	var out []string
	eachLevel(raw, func(name string, m map[string]json.RawMessage, defined []string) {
		for _, k := range slices.Sorted(maps.Keys(m)) {
			if !defines(defined, k) {
				out = append(out, fmt.Sprintf("%s%q", name, k))
			}
		}
	})
	return out
}

// knownKeys is raw without the keys unknownKeys lists, or raw itself when
// it is not an object.
func knownKeys(raw json.RawMessage) json.RawMessage {
	return eachLevel(raw, func(_ string, m map[string]json.RawMessage, defined []string) {
		maps.DeleteFunc(m, func(k string, _ json.RawMessage) bool { return !defines(defined, k) })
	})
}

// eachLevel calls visit with each object of raw the contract defines keys
// at, the top, then the summary, then each finding, named as unknownKeys
// names a key under it, with the keys defined there, and returns raw with
// what visit did to them encoded back; raw itself when it is not an
// object.
func eachLevel(raw json.RawMessage, visit func(name string, m map[string]json.RawMessage, defined []string)) json.RawMessage {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil || top == nil {
		return raw
	}
	visit("", top, topKeys)
	for _, k := range fields(top, keySummary) {
		var summary map[string]json.RawMessage
		if json.Unmarshal(top[k], &summary) == nil && summary != nil {
			visit(keySummary+".", summary, summaryKeys)
			top[k], _ = json.Marshal(summary)
		}
	}
	for _, k := range fields(top, keyFindings) {
		var findings []map[string]json.RawMessage
		if json.Unmarshal(top[k], &findings) == nil {
			for i, f := range findings {
				visit(fmt.Sprintf("%s[%d].", keyFindings, i), f, findingKeys)
			}
			top[k], _ = json.Marshal(findings)
		}
	}
	out, _ := json.Marshal(top)
	return out
}

// fields is the keys of m that fill the field named name on decoding,
// compared regardless of case, sorted: decoding applies every one of them,
// so each is walked, in an order that does not change between runs.
func fields(m map[string]json.RawMessage, name string) []string {
	return slices.DeleteFunc(slices.Sorted(maps.Keys(m)), func(k string) bool { return !strings.EqualFold(k, name) })
}

// defines says whether keys holds k, compared regardless of case as
// decoding compares a key with a field's name: a key that differs only in
// case still fills its field.
func defines(keys []string, k string) bool {
	return slices.ContainsFunc(keys, func(d string) bool { return strings.EqualFold(d, k) })
}

// errUnknownKeys is what Check's refusal of keys the contract does not
// define wraps.
var errUnknownKeys = errors.New("unknown keys")

// Lenient says whether check refused raw for keys the contract does not
// define and accepts it once they are dropped, as Parse drops them on
// decoding: a submission refused for those keys alone is still a review,
// which the agent loop falls back on rather than end with none. The
// refusal must be for those keys because knownKeys keeps only the last
// of a repeated key, which can hide a type error Check found in an
// earlier one, and Check reports a type error before any unknown key.
func Lenient(check func(json.RawMessage) error) func(json.RawMessage) bool {
	return func(raw json.RawMessage) bool {
		return errors.Is(check(raw), errUnknownKeys) && check(knownKeys(raw)) == nil
	}
}

// Parse decodes the model's JSON and drops findings kritika cannot post: an
// unknown severity or category, a missing field, a missing fix when opts
// require one, or a line the diff
// does not add or keep. Dropped findings are returned
// with the reason so they can be logged and counted, never silently lost.
// anchors maps a path to the head-side lines the diff covers, each with
// its text. A range or a replacement the diff does not wholly cover is
// cleared rather than the finding dropped, and an insertion becomes a
// replacement of its line by that line plus the added ones. Kept findings
// are ordered most severe first, then by path and line. The summary keeps
// its diagram only when opts ask for one and it is one diagram allows.
func Parse(raw string, anchors map[string]map[int]string, opts ParseOptions) (Result, []Dropped, error) {
	var res Result
	dec := json.NewDecoder(strings.NewReader(strings.TrimSpace(raw)))
	if err := dec.Decode(&res); err != nil {
		return Result{}, nil, fmt.Errorf("review: model output is not the expected JSON: %w", err)
	}
	res.Summary = normalizeSummary(res.Summary, opts)
	kept := make([]Finding, 0, len(res.Findings))
	var dropped []Dropped
	prior := indexPrior(opts.Prior)
	for _, f := range res.Findings {
		f.Path = strings.TrimSpace(f.Path)
		f.Title = severityPrefix.ReplaceAllString(prose(f.Title, opts.Repository), "")
		f.Explanation = prose(f.Explanation, opts.Repository)
		f.SuggestedFix = prose(f.SuggestedFix, opts.Repository)
		f.Replacement = stripFences(f.Replacement)
		f.InsertAfter = stripFences(f.InsertAfter)
		f.AgentPrompt = strings.TrimSpace(f.AgentPrompt)
		f.Rules = citedRules(f.Rules, opts.Rules)
		f.Prior = strings.TrimSpace(f.Prior)
		var reason DropReason
		switch {
		case !f.Severity.Valid():
			reason = DropBadSeverity
		case !f.Category.Valid():
			reason = DropBadCategory
		case f.Path == "" || f.Line <= 0 || f.Title == "" || f.Explanation == "":
			reason = DropIncomplete
		case opts.RequireSuggestedFix && f.SuggestedFix == "" && f.Replacement == "" && f.InsertAfter == "":
			reason = DropNoFix
		}
		text, anchored := anchors[f.Path][f.Line]
		if reason == "" && !anchored {
			reason = DropUnanchored
		}
		// A finding off the diff is reported all the same and keeps its
		// thread; one dropped as malformed claims no prior finding's, which
		// a later finding may then carry on.
		if reason == "" || reason == DropUnanchored {
			f.Fingerprint = prior.inherit(f)
		}
		f.Prior = ""
		if reason != "" {
			dropped = append(dropped, Dropped{Finding: f, Reason: reason})
			continue
		}
		if f.InsertAfter != "" {
			// A replacement given too says what the line becomes; an
			// insertion alone keeps the line as the diff shows it.
			if f.Replacement == "" {
				f.EndLine, f.Replacement = 0, text+"\n"+f.InsertAfter
			}
			f.InsertAfter = ""
		}
		if f.EndLine <= f.Line {
			f.EndLine = 0
		}
		for l := f.Line + 1; l <= f.EndLine; l++ {
			if _, ok := anchors[f.Path][l]; !ok {
				f.EndLine, f.Replacement = 0, ""
				break
			}
		}
		kept = append(kept, f)
	}
	slices.SortStableFunc(kept, func(a, b Finding) int {
		return cmp.Or(
			cmp.Compare(a.Severity.Rank(), b.Severity.Rank()),
			strings.Compare(a.Path, b.Path),
			cmp.Compare(a.Line, b.Line),
		)
	})
	res.Findings = kept
	return res, dropped, nil
}

// normalizeSummary applies the contract to a summary's text: one-line
// headline and checked notes, prose with its references redirected,
// praise and notes to their caps, and the diagram only where opts ask for
// one and it is one diagram allows.
func normalizeSummary(s Summary, opts ParseOptions) Summary {
	s.Headline = oneLine(prose(s.Headline, opts.Repository))
	s.Take = prose(s.Take, opts.Repository)
	praise := make([]string, 0, maxPraise)
	for _, p := range s.Praise {
		if p = prose(p, opts.Repository); p != "" && len(praise) < maxPraise {
			praise = append(praise, p)
		}
	}
	s.Praise = praise
	var checked []string
	for _, c := range s.Checked {
		if c = oneLine(c); c != "" && len(checked) < maxChecked {
			checked = append(checked, c)
		}
	}
	s.Checked = checked
	if opts.Diagram {
		s.Diagram = diagram(s.Diagram)
	} else {
		s.Diagram = ""
	}
	return s
}

// citedRules is the ids of cited that given lists, once each, in the
// order cited; nil for none.
func citedRules(cited, given []string) []string {
	var out []string
	for _, id := range cited {
		if id = strings.TrimSpace(id); slices.Contains(given, id) && !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	return out
}

// prose is a text field the model wrote for a human, trimmed, with its
// GitHub references redirected as RedirectReferences does. Code fields,
// the replacement and the agent prompt, are not prose: they render in
// code blocks, which the forge does not link.
func prose(s, repository string) string {
	return RedirectReferences(strings.TrimSpace(s), repository)
}

// stripFences drops the fence lines a model wraps replacement code in and
// any stray fence line inside it, since the template puts the code in a
// suggestion fence of its own.
func stripFences(code string) string {
	lines := strings.Split(strings.Trim(code, "\n"), "\n")
	kept := lines[:0]
	for _, l := range lines {
		if !strings.HasPrefix(strings.TrimSpace(l), "```") {
			kept = append(kept, l)
		}
	}
	return strings.Trim(strings.Join(kept, "\n"), "\n")
}

// diagram is the model's Mermaid source without its fences, or "" when it
// does not open with one of diagramKinds or is over maxDiagramBytes: the
// forge would show a render error, or a diagram too big to read, in its
// place. A titled front matter block and comment lines may precede the
// kind, as Mermaid allows; diagramHeader says what is refused.
func diagram(src string) string {
	src = stripFences(strings.TrimSpace(src))
	if len(src) > maxDiagramBytes {
		return ""
	}
	if kind := strings.Fields(diagramHeader(src)); len(kind) == 0 || !slices.Contains(diagramKinds, kind[0]) {
		return ""
	}
	return src
}

// diagramHeader is the line of src that names its diagram kind: the first
// past a leading "---" front matter block and any "%%" comment lines. It is
// "" when the front matter holds anything but a title, or a directive
// ("%%{...}%%") appears anywhere, since either can reconfigure the forge's
// rendering.
func diagramHeader(src string) string {
	if strings.Contains(src, "%%{") {
		return ""
	}
	lines := strings.Split(src, "\n")
	if len(lines) > 0 && strings.TrimSpace(lines[0]) == "---" {
		end := slices.IndexFunc(lines[1:], func(l string) bool { return strings.TrimSpace(l) == "---" })
		if end < 0 {
			return ""
		}
		for _, l := range lines[1 : end+1] {
			if t := strings.TrimSpace(l); t != "" && !strings.HasPrefix(t, "title:") {
				return ""
			}
		}
		lines = lines[end+2:]
	}
	for _, l := range lines {
		if t := strings.TrimSpace(l); t != "" && !strings.HasPrefix(t, "%%") {
			return t
		}
	}
	return ""
}

// Anchors reads a unified diff and returns, per head-side path, the
// new-file lines the diff shows (added and context lines) with their text.
// A finding may only be attached to one of these, which is also the set of
// lines GitHub accepts an inline comment on; the text is what an insertion
// keeps above the lines it adds.
func Anchors(diff string) map[string]map[int]string {
	return contextpack.ShownLines(diff)
}
