package webapi

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/riverqueue/river/rivertype"

	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/model"
	"github.com/home-operations/kritika/internal/repoconfig"
	"github.com/home-operations/kritika/internal/review"
	"github.com/home-operations/kritika/internal/store"
	"github.com/home-operations/kritika/internal/transcript"
)

var update = flag.Bool("update", false, "rewrite testdata/*.golden.json")

var (
	t0 = time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	t1 = t0.Add(90 * time.Second)
)

var goldenReview = Review{
	ID: "rev-1", Status: store.ReviewCompleted, Trigger: "push", Scope: review.ScopeIncremental,
	Model: "acme/large", HeadSHA: "abc123", CostUSD: 0.42, Tokens: TokenCounts{Input: 1000, Output: 200}, DurationMs: new(int64(90000)),
	CreatedAt: t0, FinishedAt: &t1, SkipReason: string(repoconfig.SkipFiltered), Error: "",
	Confidence: &Confidence{Score: 3, Threshold: 5, Reason: "The unchecked error stands.", Risk: review.RiskMedium, Model: "acme/judge"},
}

var goldenJob = Job{
	ID: 42, Kind: "review", State: rivertype.JobStateRetryable, Attempt: 2, MaxAttempts: 5, CreatedAt: t0, ScheduledAt: t1,
	AttemptedAt: &t0, FinalizedAt: nil, LastError: "github: list files: context deadline exceeded", Cause: CauseForgeUnavailable,
	Args: JobArgs{Repository: "alpha/one", Number: 7, Head: "abc123", Trigger: "push", CommentID: 0},
}

var goldenPullRef = PullRef{Repository: "alpha/one", Number: 7, Title: "Add widgets", URL: "https://git.example/alpha/one/pulls/7"}

var goldenSummary = AccountSummary{
	Slug: "github/alpha", Connection: "alpha-bot", Repositories: 3, Reviews7d: 9,
	Usage: MonthUsage{
		Tokens: 5000, CostUSD: 1.5, TokensPerMonth: 1000000, ReviewsToday: 2, ReviewsPerDay: 50,
		Reviews: 12, ReviewCostUSD: 1.2, MedianReviewCostUSD: new(0.08),
	},
	Attention: Attention{Failed: 1, Blocking: 2}, LastWebhookAt: &t0, LastPolledAt: &t1,
}

var goldenRepo = Repository{
	ID: "repo-1", FullName: "alpha/one", Enabled: true, ManagedBy: "file", DefaultBranch: "main",
	Index:      IndexState{ActiveCommit: "def456", ActiveAt: &t0, LastRunStatus: store.IndexCompleted, LastRunAt: &t1},
	LastReview: &ReviewRef{ID: "rev-1", Status: store.ReviewCompleted, CreatedAt: t0},
}

var goldenRepoSettings = RepoSettings{
	Enabled: true, Models: configfile.Models{Review: "openrouter/acme-large", Effort: "high"}, Filters: configfile.Filters{
		Include: []configfile.Filter{{Expr: `pr.author.startsWith("renovate")`}, {Name: "wanted", Expr: `pr.labels.exists(l, l.name == "needs-review")`}},
		Exclude: []configfile.Filter{{Name: "drafts", Expr: "pr.draft"}},
	},
	Ignore: []string{"vendor/**"}, SettleSeconds: 30, MaxDeltaFiles: 40,
	Confidence: configfile.Confidence{Model: "openrouter/acme-judge", Effort: "low", Threshold: 5, Risk: review.RiskLow, Instructions: "Image bumps are low."},
	Review: configfile.Review{
		RequireSuggestedFix: true,
		Templates:           configfile.ReviewTemplates{Summary: "docs/summary.tmpl"}, InlineComments: true,
		Context: []configfile.ContextFile{{Path: "db/schema.sql", Description: "the schema", Paths: []string{"**/*.sql"}}},
	},
	Agent: configfile.AgentSettings{
		MaxSteps: 60, MaxToolOutputBytes: 32768, MaxTokens: 4000000, MaxPromptTokens: 24000, MaxParts: 8, Timeout: 20 * time.Minute,
		Commands: []string{"go"}, CommandTimeout: 30 * time.Second,
	},
	Limits: configfile.Limits{Concurrency: 2},
	Skills: configfile.Skills{
		Paths: []string{".agents/skills", ".claude/skills"},
		Scope: map[string]configfile.SkillScope{"review-renovate-pr": {When: []configfile.When{{Expr: `pr.headRef.startsWith("renovate/")`}}}},
	},
}

var goldenIndexRun = IndexRun{
	ID: "ix-1", Repository: "alpha/one", CommitSHA: "def456", BaseSHA: "", EmbedModel: "embed", Mode: "full",
	Status: store.IndexCompleted, Trigger: "push", ChunkCount: 12, Error: "", CreatedAt: t0, FinishedAt: &t1,
}

var goldenPull = Pull{
	Repository: "alpha/one", Number: 7, Title: "Add widgets", Author: "ada", State: "open", Draft: false, Merged: false,
	HeadSHA: "abc123", HeadRef: "widgets", BaseRef: "main", URL: "https://git.example/alpha/one/pulls/7", OpenedAt: &t0,
	UpdatedAt: t1, Labels: []Label{{Name: "bug", Color: "ff0000"}},
	LastReview: &ReviewBrief{
		ID: "rev-1", Status: store.ReviewCompleted, Scope: review.ScopeFull,
		Findings: SeverityCounts{Blocking: 1, Important: 2, Nit: 3}, CreatedAt: t0,
	},
	ReviewCount: 2, CostUSD: 0.84,
}

var goldenFollowup = Followup{
	ID: "fu-1", CommentID: 99, Repository: "alpha/one", Number: 7, PullURL: goldenPullRef.URL, Author: "bob", Inline: true, Path: "a.go", Line: 4,
	Status: store.FollowupAnswered, Reason: "", ReplyCommentID: new(int64(100)), Model: "acme/large", CreatedAt: t0,
}

var goldenTools = []ToolDef{{Name: "grep", Description: "search", InputSchema: json.RawMessage(`{"type":"object"}`)}}

// goldens is one populated instance of every DTO, by golden file name.
var goldens = map[string]any{
	"page": Page[Repository]{Items: []Repository{goldenRepo}, NextCursor: new("opaque")},
	"me": Me{
		User:  User{ID: "acct-1", DisplayName: "Ada", Email: "ada@example.com", AvatarURL: "https://img.example/a.png"},
		Admin: true, Accounts: []string{"github/alpha"},
		Settings: UserSettings{TimeZone: "Europe/Amsterdam", Clock: "24", Theme: "dark"},
	},
	"account_summary": goldenSummary,
	"admin_account":   AdminAccount{AccountSummary: goldenSummary, Live: false, Conflict: "no connection serves this account"},
	"account_detail": AccountDetail{
		Slug: "github/alpha",
		Connection: Connection{
			Name: "alpha-bot", Forge: configfile.ForgeGitHub, Accounts: []string{"alpha"},
			Credentials: CredentialsSet{ClientID: true, PrivateKey: true, WebhookSecret: true},
			HookPath:    "/hooks/alpha-bot", LastWebhookAt: &t0,
		},
		Models: configfile.Models{Review: "openrouter/acme-large", Fallback: "openrouter/acme-small", Effort: "high"},
		Limits: configfile.Limits{Concurrency: 2, ReviewsPerDay: 50, TokensPerMonth: 1000000}, Filters: configfile.Filters{Include: []configfile.Filter{}, Exclude: []configfile.Filter{{Name: "drafts", Expr: "pr.draft"}}},
		Usage: goldenSummary.Usage, LastPolledAt: &t1,
	},
	"repository":       goldenRepo,
	"instance_setting": InstanceSetting{Section: "connections", Key: "alpha-bot", Value: "alpha, webhook /hooks/alpha-bot", Source: configfile.SourceFile},
	"repo_detail": RepoDetail{
		Repository: goldenRepo,
		Settings:   goldenRepoSettings,
		Sources: map[string]configfile.Source{
			"trigger.exclude": configfile.SourceAccount, "review.model": configfile.SourceDefaults, "trigger.settle": configfile.SourceDefault,
		},
		RepoConfig: &RepoConfig{
			ReviewID: "rev-1", Commit: "def456", Found: true,
			Settings: func() RepoSettings {
				s := goldenRepoSettings
				s.Models.Review = "openrouter/acme-small"
				return s
			}(),
			Filters: configfile.Filters{Include: []configfile.Filter{}, Exclude: []configfile.Filter{{Name: "skip-label", Expr: `pr.labels.exists(l, l.name == "skip-review")`}}},
			Dropped: []string{`.kritika.yaml: review.model "q/big" was dropped; allowed: a model of openrouter`},
		},
		IndexRuns: []IndexRun{goldenIndexRun},
	},
	"index_run": goldenIndexRun,
	"pull":      goldenPull,
	"review":    goldenReview,
	"followup":  goldenFollowup,
	"pull_detail": PullDetail{
		Pull: goldenPull, Reviews: []Review{goldenReview}, Followups: []Followup{goldenFollowup}, Job: &goldenJob,
	},
	"review_detail": ReviewDetail{
		Review: ReviewInfo{
			Review: goldenReview, Pull: goldenPullRef, PullState: "open", ScopeReason: "delta",
			MergeBaseSHA: "base1", PatchID: "patch1", PriorReviewID: new("rev-0"), CancelRequestedAt: nil,
		},
		Summary: &Summary{Take: "Looks fine.", Praise: []string{"tests"}, Diagram: "flowchart TD\n  A --> B"},
		Findings: []Finding{{
			ID: "f-1", Path: "a.go", Line: 3, EndLine: 5, Severity: review.SeverityBlocking, Title: "nil deref",
			Explanation: "x may be nil", SuggestedFix: "check x", Replacement: "if x != nil {}", AgentPrompt: "fix it",
			Fingerprint: "fp", PostedInline: true, ForgeCommentID: new(int64(55)), CreatedAt: t0, ReactionsUp: 2, ReactionsDown: 1,
			Rules: []string{"wrap-errors"}, Status: store.FindingOpen,
		}},
		RunnerRun: &RunnerRun{
			ID: "run-1", Phase: "done", JobName: "job", PodName: "pod", NodeName: "node", CreatedAt: t0, ScheduledAt: &t0,
			StartedAt: &t0, FinishedAt: &t1, HeartbeatAt: &t1, ExitCode: new(0), TerminationReason: "Completed",
			DeadlineExceeded: false, Error: "", LogTail: "ok\n",
		},
		AgentRun: &AgentRun{
			StopReason: "submitted", Steps: 3, ToolCalls: map[string]int{"grep": 2},
			Timeline: []TimelineStep{{Index: 0, Part: 1, Tools: []string{"grep"}, DurationMs: 1200, OutputBytes: 300, InputTokens: 100, OutputTokens: 20}},
			Sources:  []string{"https://docs.example"}, SkillsOffered: []string{"renovate-review", "go-style"}, SkillsOpened: []string{"renovate-review"},
			CommandsOffered: []string{"gh", "helm"}, CommandsRun: []string{"helm"},
			Usage:   Usage{Input: 100, CacheRead: 50, CacheWrite: 10, Output: 20},
			CostUSD: 0.1, Model: "acme/large", Error: "", CreatedAt: t1, Result: json.RawMessage(`{"findings":[]}`),
			CarriedReviewID: new("rev-0"),
			Parts:           []AgentPart{{Paths: []string{"a.go"}, Stop: "submitted", Error: "", Steps: 3}},
		},
		Usage: []UsageRow{{Role: "review", Model: "acme/large", Upstream: "acme", InputTokens: 100, OutputTokens: 20, CostUSD: 0.1, CreatedAt: t1}},
		ContextPack: &ContextPack{
			HeadSHA: "abc123", BaseSHA: "base1", PatchID: "patch1", ChangedPaths: []string{"a.go"}, DeltaPaths: []string{"a.go"},
			PriorHeadSHA: new("abc000"), RuleIDs: []string{"wrap-errors", "no-tokens"},
			Stages: []Stage{{
				Stage: "definitions", Path: "b.go", Language: "go", Symbol: "F", Kind: "func", Scope: "pkg", StartLine: 1,
				EndLine: 9, Ref: "F", Bytes: 120,
			}},
			RepoNotes: []string{"docs/missing.md: not found"}, RepoFiles: []RepoFile{{Path: ".kritika.yaml", Size: 42}}, CreatedAt: t0,
		},
	},
	"account_finding": AccountFinding{
		Finding: Finding{
			ID: "f-1", Path: "a.go", Line: 3, EndLine: 5, Severity: review.SeverityBlocking, Title: "nil deref",
			Explanation: "x may be nil", SuggestedFix: "check x", Replacement: "", AgentPrompt: "", Fingerprint: "fp",
			PostedInline: true, ForgeCommentID: new(int64(55)), CreatedAt: t0, ReactionsUp: 2, ReactionsDown: 1,
			Rules: []string{"wrap-errors"}, Status: store.FindingAddressed,
		},
		ReviewID: "rev-1", Pull: goldenPullRef, FirstSeenAt: t0, LastSeenAt: t1,
	},
	"review_diff": ReviewDiff{Diff: "diff --git a/a.go b/a.go\n", DeltaDiff: ""},
	"review_raw": ReviewRaw{
		RepoFiles: map[string]string{".kritika.yaml": "review: { approve: true }\n"},
		Stages: []ContextChunk{{
			Stage: "definitions", Path: "b.go", Language: "go", Symbol: "F", Kind: "func", Scope: "pkg", StartLine: 1, EndLine: 9,
			Ref: "F", Text: "func F() {}",
		}},
		Result: json.RawMessage(`{"findings":[]}`), LogTail: "ok\n",
	},
	"transcript": Transcript{
		System: "You review code.", Tools: goldenTools,
		Turns: []Turn{{
			Index: 0, ID: "mc-1", Kind: transcript.KindAgentStep, Step: 0, Model: "acme/large", Upstream: "acme",
			System: new("You review code, again."), Tools: goldenTools, Reset: false, MessagesFrom: 0,
			Messages: []Message{{
				Role: model.RoleUser, Text: "review this",
				ToolCalls:   []ToolCall{{ID: "c1", Name: "grep", Input: json.RawMessage(`{"q":"x"}`)}},
				ToolResults: []ToolResult{{CallID: "c0", Content: "hit", IsError: false, TruncatedBytes: 10}},
			}},
			Response: Response{Text: "done", ToolCalls: []ToolCall{}, Stop: model.StopEndTurn},
			Usage:    Usage{Input: 10, CacheRead: 5, CacheWrite: 1, Output: 2}, CostUSD: 0.01, DurationMs: 1500, Error: "",
			Truncated: false, CreatedAt: t0, RunnerRunID: "run-1",
		}},
	},
	"usage_series": UsageSeries{
		Group: store.UsageByDay, From: t0, To: t1,
		Rows: []UsagePoint{{Key: "2026-09-01", InputTokens: 100, CacheReadTokens: 50, CacheWriteTokens: 5, OutputTokens: 20, CostUSD: 0.1, Calls: 2}},
	},
	"attention": Attention{Failed: 2, Capped: 1, Blocking: 3, Paused: 1},
	"analytics": Analytics{
		Group: store.AnalyticsByDay, From: t0, To: t1,
		Current: AnalyticsTotals{
			PullRequests: 3, Reviews: 5, Failed: 1, Findings: SeverityCounts{Blocking: 1, Important: 2, Nit: 3}, Addressed: 2,
			Categories:  map[review.Category]int{review.CategoryCorrectness: 2, review.CategorySecurity: 1, review.CategoryPerformance: 0, review.CategoryReliability: 1, review.CategoryMaintainability: 2, review.CategoryTests: 0},
			ReactionsUp: 4, ReactionsDown: 1, CostUSD: 1.25, MedianReviewMs: new(int64(90000)), MedianMergeMs: new(int64(129600000)),
		},
		Previous: AnalyticsTotals{Findings: SeverityCounts{}, Categories: categoryCounts(nil)},
		Series:   []AnalyticsPoint{{Key: "2026-09-01", Reviews: 5, Findings: SeverityCounts{Blocking: 1, Important: 2, Nit: 3}, CostUSD: 1.25}},
		Repositories: []RepoActivity{
			{Repository: "alpha/one", Reviews: 5, Findings: SeverityCounts{Blocking: 1, Important: 2, Nit: 3}, Addressed: 2},
		},
	},
	"rule": Rule{
		Kind: RuleWritten, ID: "wrap-errors", Text: "Wrap an error with the package name before returning it.", Paths: []string{"**/*.go"},
		When:   []configfile.When{},
		Source: RuleFromEntry, Repositories: []string{"alpha/one"}, Findings: 3, Addressed: 1,
	},
	"job": goldenJob,
	"instance_queue": InstanceQueue{
		Jobs:  []InstanceJob{{Job: goldenJob, Account: "github/alpha"}},
		Slots: []ModelSlots{{Account: "github/alpha", Model: "openrouter/acme-large", Held: 2, Slots: 2}},
	},
	"event": Event{Kind: store.EventReview, Account: "alpha", ID: "rev-1", ReviewID: new("rev-1")},
	"error": ErrorBody{Code: CodeNotFound, Message: "account not found"},
}

func TestDTOGolden(t *testing.T) {
	for name, v := range goldens {
		t.Run(name, func(t *testing.T) {
			got, err := json.MarshalIndent(v, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			got = append(got, '\n')
			path := filepath.Join("testdata", name+".golden.json")
			if *update {
				if err := os.WriteFile(path, got, 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("%v (run with -update to create it)", err)
			}
			// Compared compacted: the repository's JSON formatter re-wraps
			// the files, which changes no name, order or value.
			var gotC, wantC bytes.Buffer
			if err := json.Compact(&gotC, got); err != nil {
				t.Fatal(err)
			}
			if err := json.Compact(&wantC, want); err != nil {
				t.Fatalf("%s: %v", path, err)
			}
			if !bytes.Equal(gotC.Bytes(), wantC.Bytes()) {
				t.Errorf("%s changed; the UI's types.ts mirrors it. got:\n%s", path, got)
			}
		})
	}
}
