package tuning

import (
	"fmt"
	"time"
)

// Review holds the tunable values of package review.
type Review struct {
	// HeldBackLines is how far from every line changed since the last
	// review an incremental re-review's new finding can point before it is
	// held back.
	HeldBackLines int `env:"KRITIKA_TUNING_REVIEW_HELD_BACK_LINES" envDefault:"3" yaml:"heldBackLines,omitempty"`
	// MaxLinkedIssues bounds the issues a review reads: a description that
	// closes more is a batch, and the first few say what it is for.
	MaxLinkedIssues int `env:"KRITIKA_TUNING_REVIEW_MAX_LINKED_ISSUES" envDefault:"3" yaml:"maxLinkedIssues,omitempty"`
	// MaxConfidenceFinding bounds one finding in the scorer's prompt and the
	// review's summary.
	MaxConfidenceFinding int `env:"KRITIKA_TUNING_REVIEW_MAX_CONFIDENCE_FINDING" envDefault:"1500" yaml:"maxConfidenceFinding,omitempty"`
	// MaxConfidenceSources bounds the sources the scorer's prompt lists.
	MaxConfidenceSources int `env:"KRITIKA_TUNING_REVIEW_MAX_CONFIDENCE_SOURCES" envDefault:"20" yaml:"maxConfidenceSources,omitempty"`
	// MaxConfidenceReason bounds the reason kept of the scorer's answer.
	MaxConfidenceReason int `env:"KRITIKA_TUNING_REVIEW_MAX_CONFIDENCE_REASON" envDefault:"500" yaml:"maxConfidenceReason,omitempty"`
	// MaxPraise bounds a summary's praise. The schema says so and the parse
	// enforces it.
	MaxPraise int `env:"KRITIKA_TUNING_REVIEW_MAX_PRAISE" envDefault:"3" yaml:"maxPraise,omitempty"`
	// MaxChecked bounds a summary's checked notes. The schema says so and
	// the parse enforces it.
	MaxChecked int `env:"KRITIKA_TUNING_REVIEW_MAX_CHECKED" envDefault:"10" yaml:"maxChecked,omitempty"`
	// MaxDiagramBytes bounds a summary's diagram. A diagram over it is
	// dropped rather than cut, since a cut one does not render.
	MaxDiagramBytes ByteSize `env:"KRITIKA_TUNING_REVIEW_MAX_DIAGRAM_BYTES" envDefault:"4Ki" yaml:"maxDiagramBytes,omitempty"`
	// MaxMessageChars bounds one thread message in a follow-up's prompt.
	MaxMessageChars int `env:"KRITIKA_TUNING_REVIEW_MAX_MESSAGE_CHARS" envDefault:"2000" yaml:"maxMessageChars,omitempty"`
	// OneLineBytes bounds a text that a prompt or a summary shows on one
	// line: a title, a checked note, an answer, a reason.
	OneLineBytes int `env:"KRITIKA_TUNING_REVIEW_ONE_LINE_BYTES" envDefault:"200" yaml:"oneLineBytes,omitempty"`
	// MaxVersionBytes is the longest token that reads as a version a
	// change moves.
	MaxVersionBytes int `env:"KRITIKA_TUNING_REVIEW_MAX_VERSION_BYTES" envDefault:"200" yaml:"maxVersionBytes,omitempty"`
	// MaxMergePaths is how many of a part's files the merge call is shown
	// before it counts the rest.
	MaxMergePaths int `env:"KRITIKA_TUNING_REVIEW_MAX_MERGE_PATHS" envDefault:"20" yaml:"maxMergePaths,omitempty"`
	// MaxMergeTake is how much of a part's take the merge call is shown.
	MaxMergeTake int `env:"KRITIKA_TUNING_REVIEW_MAX_MERGE_TAKE" envDefault:"1500" yaml:"maxMergeTake,omitempty"`
	// PartBytes is the most diff one part of a split review holds: about
	// thirty files of an ordinary change at the default, few enough for one
	// agent to read each with care. A review whose diff is larger is split
	// into parts.
	PartBytes ByteSize `env:"KRITIKA_TUNING_REVIEW_PART_BYTES" envDefault:"64Ki" yaml:"partBytes,omitempty"`
	// MaxBumpsBytes bounds the list of the versions a change moves, which is
	// written before the budgeted sections: a digest pinned beside a tag
	// takes a line of its own, and the diff shows the rest.
	MaxBumpsBytes int `env:"KRITIKA_TUNING_REVIEW_MAX_BUMPS_BYTES" envDefault:"2000" yaml:"maxBumpsBytes,omitempty"`
	// DefaultBudgetTokens bounds a prompt's user message when nothing sets
	// a budget, and is the agent's prompt budget when the configuration
	// file sets none.
	DefaultBudgetTokens int `env:"KRITIKA_TUNING_REVIEW_DEFAULT_BUDGET_TOKENS" envDefault:"64000" yaml:"defaultBudgetTokens,omitempty"`
	// CharsPerToken is the conservative approximation of a token's size
	// used for budgeting.
	CharsPerToken int `env:"KRITIKA_TUNING_REVIEW_CHARS_PER_TOKEN" envDefault:"4" yaml:"charsPerToken,omitempty"`
	// BodyShare divides the budget into the share that the description can
	// take and the share that the linked issues can take together:
	// 1/BodyShare each, which leaves at least half of it to the diff at the
	// default. A body over its share is cut there, with a note of how much
	// was left out.
	BodyShare int `env:"KRITIKA_TUNING_REVIEW_BODY_SHARE" envDefault:"4" yaml:"bodyShare,omitempty"`
	// MinUserBudget is the least a user message is given, in tokens,
	// whatever the system prompt and what follows the diff take of the
	// budget: room for the pull request's head and a small diff.
	MinUserBudget int `env:"KRITIKA_TUNING_REVIEW_MIN_USER_BUDGET" envDefault:"2000" yaml:"minUserBudget,omitempty"`
	// NoteRoom is kept free for the note on delta files or prior findings
	// that did not fit.
	NoteRoom int `env:"KRITIKA_TUNING_REVIEW_NOTE_ROOM" envDefault:"128" yaml:"noteRoom,omitempty"`
	// OmissionRoom is kept free, before a diff is fitted, for the note on
	// the files that did not fit, which the note keeps within.
	OmissionRoom int `env:"KRITIKA_TUNING_REVIEW_OMISSION_ROOM" envDefault:"512" yaml:"omissionRoom,omitempty"`
	// MaxFocusRunes bounds a review's focus: a line of a comment, not a
	// brief.
	MaxFocusRunes int `env:"KRITIKA_TUNING_REVIEW_MAX_FOCUS_RUNES" envDefault:"500" yaml:"maxFocusRunes,omitempty"`
	// MaxQuoteRefusals is how many times a run's submission is sent back
	// for its quotes before one is taken as it is, its misquoted findings
	// dropped: a model that does not correct a quote twice over will not,
	// and the rest of its review is worth more than the citations.
	MaxQuoteRefusals int `env:"KRITIKA_TUNING_REVIEW_MAX_QUOTE_REFUSALS" envDefault:"2" yaml:"maxQuoteRefusals,omitempty"`
	// MaxRenderBytes bounds a rendered comment, marker included. GitHub
	// rejects comment bodies over 65,536 characters, and a character is at
	// least a byte, so Validate refuses more.
	MaxRenderBytes ByteSize `env:"KRITIKA_TUNING_REVIEW_MAX_RENDER_BYTES" envDefault:"64Ki" yaml:"maxRenderBytes,omitempty"`
	// RenderTimeout bounds one render of a repository's template, parse
	// included.
	RenderTimeout time.Duration `env:"KRITIKA_TUNING_REVIEW_RENDER_TIMEOUT" envDefault:"2s" yaml:"renderTimeout,omitempty"`
	// MaxNamedOverlaps is how many overlapping pull requests the default
	// template names before it counts the rest: a monorepo can have many
	// open pull requests on one directory, and a comment is not where to
	// list them.
	MaxNamedOverlaps int `env:"KRITIKA_TUNING_REVIEW_MAX_NAMED_OVERLAPS" envDefault:"5" yaml:"maxNamedOverlaps,omitempty"`
	// MaxNamedShared is how many shared paths of each it names.
	MaxNamedShared int `env:"KRITIKA_TUNING_REVIEW_MAX_NAMED_SHARED" envDefault:"3" yaml:"maxNamedShared,omitempty"`
	// MaxNamedUsed is how many used names the default template writes
	// before it counts the rest. A repository can offer far more commands
	// than a comment needs to list.
	MaxNamedUsed int `env:"KRITIKA_TUNING_REVIEW_MAX_NAMED_USED" envDefault:"10" yaml:"maxNamedUsed,omitempty"`
	// FooterSubjectRunes is how much of a commit subject the footer shows.
	FooterSubjectRunes int `env:"KRITIKA_TUNING_REVIEW_FOOTER_SUBJECT_RUNES" envDefault:"40" yaml:"footerSubjectRunes,omitempty"`
	// MaxIterations is a template render's budget of loop iterations,
	// charged in full when a loop starts, so that nested loops cannot
	// multiply it.
	MaxIterations int `env:"KRITIKA_TUNING_REVIEW_MAX_ITERATIONS" envDefault:"20000" yaml:"maxIterations,omitempty"`
	// MaxMeasureDepth bounds how deep the template sandbox measures a value.
	// Anything deeper counts as one node.
	MaxMeasureDepth int `env:"KRITIKA_TUNING_REVIEW_MAX_MEASURE_DEPTH" envDefault:"64" yaml:"maxMeasureDepth,omitempty"`
}

// MaxCallBytes bounds one template function call: its arguments plus an
// estimate of what it allocates, and its result. The estimate is computed
// from the arguments before the call runs, so that amplifying functions
// fail before they allocate.
func (r Review) MaxCallBytes() int { return 4 * int(r.MaxRenderBytes) }

// Transcript holds the tunable values of package transcript.
type Transcript struct {
	// ToolResultCap bounds one recorded tool result's content.
	ToolResultCap ByteSize `env:"KRITIKA_TUNING_TRANSCRIPT_TOOL_RESULT_CAP" envDefault:"64Ki" yaml:"toolResultCap,omitempty"`
	// RowCap bounds one row's encoded messages. A row over it keeps its
	// messages' structure with a placeholder for every text, content and
	// input.
	RowCap ByteSize `env:"KRITIKA_TUNING_TRANSCRIPT_ROW_CAP" envDefault:"1Mi" yaml:"rowCap,omitempty"`
	// RunCap bounds what a run records: once its rows exceed it, later rows
	// keep only the response and usage.
	RunCap ByteSize `env:"KRITIKA_TUNING_TRANSCRIPT_RUN_CAP" envDefault:"16Mi" yaml:"runCap,omitempty"`
}

// SystemCap bounds a stored system prompt: RowCap. One over it is cut, on
// a rune boundary.
func (t Transcript) SystemCap() int { return int(t.RowCap) }

// PRFilter holds the tunable values of package prfilter.
type PRFilter struct {
	// EvalCostLimit caps the CEL cost of a single filter evaluation. The
	// expression is the admin's, and its inputs that an attacker sways (a
	// title, labels) are bounded by the forge, so this is defense in depth
	// against a pathological expression: a ceiling that no reasonable
	// predicate approaches, which still bounds an accidental blow-up. CEL
	// has no loops, so a finite cost always exists.
	EvalCostLimit int64 `env:"KRITIKA_TUNING_PR_FILTER_EVAL_COST_LIMIT" envDefault:"1000000" yaml:"evalCostLimit,omitempty"`
}

func init() {
	crossField = append(crossField,
		func(v Values) error {
			if v.Review.MaxRenderBytes > GitHubCommentBytes {
				return fmt.Errorf("%s %w GitHub's comment cap, %d bytes", fieldLabel("review.maxRenderBytes"), errMustNotExceed, GitHubCommentBytes)
			}
			return nil
		},
	)
}
