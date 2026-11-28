package tuning

import (
	"math"
	"strings"
	"testing"
	"time"
)

// TestCrossFieldEdges: each invariant between fields refuses values that
// break it, naming the fields by path and variable, and holds at its edge.
func TestCrossFieldEdges(t *testing.T) {
	for _, tt := range []struct {
		name   string
		breaks func(*Values)
		edge   func(*Values)
		says   []string
	}{
		{
			"similar answer within the runner's wait",
			func(v *Values) { v.Gateway.SimilarWithin = v.Runner.SimilarTimeout },
			func(v *Values) { v.Gateway.SimilarWithin = v.Runner.SimilarTimeout - time.Millisecond },
			[]string{"tuning.gateway.similarWithin: KRITIKA_TUNING_GATEWAY_SIMILAR_WITHIN", "tuning.runner.similarTimeout: KRITIKA_TUNING_RUNNER_SIMILAR_TIMEOUT"},
		},
		{
			"index body holds the encoded index",
			func(v *Values) { v.Gateway.MaxIndexBody = ByteSize(v.indexUpload() - 1) },
			func(v *Values) { v.Gateway.MaxIndexBody = ByteSize(v.indexUpload()) },
			[]string{"KRITIKA_TUNING_GATEWAY_MAX_INDEX_BODY", "KRITIKA_TUNING_GATEWAY_MAX_INDEX_LINE", "KRITIKA_TUNING_INDEXER_MAX_CHUNKS",
				"KRITIKA_TUNING_INDEXER_MAX_CHUNK_BYTES", "KRITIKA_TUNING_INDEXER_MAX_SIGNATURE_BYTES"},
		},
		{
			"index body holds more than one chunk's raw text",
			func(v *Values) {
				v.Indexer.MaxChunks, v.Gateway.MaxIndexLine = 1, ByteSize(v.indexChunkLine())
				v.Gateway.MaxIndexBody = v.Indexer.MaxChunkBytes
			},
			func(v *Values) {
				v.Indexer.MaxChunks, v.Gateway.MaxIndexLine = 1, ByteSize(v.indexChunkLine())
				v.Gateway.MaxIndexBody = ByteSize(v.indexUpload())
			},
			[]string{"tuning.gateway.maxIndexBody: KRITIKA_TUNING_GATEWAY_MAX_INDEX_BODY"},
		},
		{
			"index line holds an escaped chunk",
			func(v *Values) { v.Gateway.MaxIndexLine = 4 << 10 },
			func(v *Values) { v.Gateway.MaxIndexLine = ByteSize(v.indexChunkLine()) },
			[]string{"tuning.gateway.maxIndexLine: KRITIKA_TUNING_GATEWAY_MAX_INDEX_LINE", "tuning.indexer.maxChunkBytes",
				"tuning.indexer.maxSignatureBytes"},
		},
		{
			"report body holds an escaped context report",
			func(v *Values) {
				v.Runner.MaxPackDiffBytes = ((v.Gateway.MaxReportBody-reportEnvelope)/jsonGrowth-v.ContextPack.MaxPackBytes)/2 + 1
			},
			func(v *Values) {
				v.Runner.MaxPackDiffBytes = ((v.Gateway.MaxReportBody-reportEnvelope)/jsonGrowth - v.ContextPack.MaxPackBytes) / 2
			},
			[]string{"tuning.gateway.maxReportBody: KRITIKA_TUNING_GATEWAY_MAX_REPORT_BODY", "tuning.runner.maxPackDiffBytes",
				"tuning.contextPack.maxPackBytes"},
		},
		{
			"report body holds the conversation in base64",
			func(v *Values) { v.Gateway.MaxConversationBytes = 3*((v.Gateway.MaxReportBody-reportEnvelope)/4) + 1 },
			func(v *Values) { v.Gateway.MaxConversationBytes = 3 * ((v.Gateway.MaxReportBody - reportEnvelope) / 4) },
			[]string{"tuning.gateway.maxReportBody: KRITIKA_TUNING_GATEWAY_MAX_REPORT_BODY", "tuning.gateway.maxConversationBytes"},
		},
		{
			"similar request body holds its escaped queries",
			func(v *Values) {
				v.ContextPack.SimilarQueryChars = int((int64(v.Gateway.MaxSimilarRequest)-similarEnvelope)/int64(v.ContextPack.SimilarQueries)-
					similarLanguageBytes-6)/jsonGrowth + 1
			},
			func(v *Values) {
				v.ContextPack.SimilarQueryChars = int((int64(v.Gateway.MaxSimilarRequest)-similarEnvelope)/int64(v.ContextPack.SimilarQueries)-
					similarLanguageBytes-6) / jsonGrowth
			},
			[]string{"tuning.gateway.maxSimilarRequest", "KRITIKA_TUNING_CONTEXT_PACK_SIMILAR_QUERIES", "KRITIKA_TUNING_CONTEXT_PACK_SIMILAR_QUERY_CHARS"},
		},
		{
			"similar request body holds the default queries escaped",
			func(v *Values) { v.Gateway.MaxSimilarRequest = 36000 },
			func(v *Values) {},
			[]string{"tuning.gateway.maxSimilarRequest: KRITIKA_TUNING_GATEWAY_MAX_SIMILAR_REQUEST"},
		},
		{
			"similar answer body holds its escaped chunks",
			func(v *Values) {
				v.Runner.MaxSimilarAnswerBody = ByteSize(v.ContextPack.SimilarLimit)*jsonGrowth*(v.Indexer.MaxChunkBytes+indexChunkFields) +
					similarEnvelope - 1
			},
			func(v *Values) {
				v.Runner.MaxSimilarAnswerBody = ByteSize(v.ContextPack.SimilarLimit)*jsonGrowth*(v.Indexer.MaxChunkBytes+indexChunkFields) +
					similarEnvelope
			},
			[]string{"tuning.runner.maxSimilarAnswerBody", "tuning.contextPack.similarLimit", "tuning.indexer.maxChunkBytes"},
		},
		{
			"similar answer body holds more than the raw chunks",
			func(v *Values) { v.Runner.MaxSimilarAnswerBody = 96000 },
			func(*Values) {},
			[]string{"tuning.runner.maxSimilarAnswerBody: KRITIKA_TUNING_RUNNER_MAX_SIMILAR_ANSWER_BODY"},
		},
		{
			"small report holds an escaped failure cause",
			func(v *Values) {
				v.Gateway.MaxSmallReport = jsonGrowth*v.Runner.MaxFailureCause + smallReportEnvelope - 1
			},
			func(v *Values) {
				v.Gateway.MaxSmallReport = jsonGrowth*v.Runner.MaxFailureCause + smallReportEnvelope
			},
			[]string{"tuning.gateway.maxSmallReport: KRITIKA_TUNING_GATEWAY_MAX_SMALL_REPORT", "tuning.runner.maxFailureCause"},
		},
		{
			"small report under a phase report",
			func(v *Values) { v.Gateway.MaxSmallReport = 16 },
			func(*Values) {},
			[]string{"tuning.gateway.maxSmallReport"},
		},
		{
			"heartbeat timeout leaves another beat",
			func(v *Values) { v.Runner.HeartbeatTimeout = 5 * time.Minute },
			func(v *Values) {
				v.Runner.HeartbeatTimeout = (v.RunSpec.ReportRetryWindow + v.RunSpec.ReportTimeout) / 2
			},
			[]string{"tuning.runner.heartbeatTimeout: KRITIKA_TUNING_RUNNER_HEARTBEAT_TIMEOUT", "tuning.runSpec.reportRetryWindow",
				"tuning.runSpec.reportTimeout"},
		},
		{
			"similar limit covers the default answer",
			func(v *Values) { v.Gateway.SimilarMax = v.ContextPack.SimilarLimit + 1 },
			func(v *Values) { v.Gateway.SimilarMax = v.ContextPack.SimilarLimit },
			[]string{"tuning.contextPack.similarLimit: KRITIKA_TUNING_CONTEXT_PACK_SIMILAR_LIMIT", "tuning.gateway.similarMax: KRITIKA_TUNING_GATEWAY_SIMILAR_MAX"},
		},
		{
			"spec within what a version 1 runner reads",
			func(v *Values) { v.RunSpec.MaxBytes = 900<<10 + 1 },
			func(v *Values) { v.RunSpec.MaxBytes = 900 << 10 },
			[]string{"tuning.runSpec.maxBytes: KRITIKA_TUNING_RUN_SPEC_MAX_BYTES", "spec version 1", "Secret"},
		},
		{
			"spec with room for its untrimmed fields",
			func(v *Values) { v.RunSpec.MaxBytes, v.RunSpec.MaxPromptBytes = 2, 1 },
			func(v *Values) { v.RunSpec.MaxBytes, v.RunSpec.MaxPromptBytes = 64<<10, 32<<10 },
			[]string{"tuning.runSpec.maxBytes: KRITIKA_TUNING_RUN_SPEC_MAX_BYTES", "must be at least 64Ki"},
		},
		{
			"parts report within its former limit",
			func(v *Values) {
				v.Runner.MaxFailureCause, v.Gateway.MaxSmallReport, v.Gateway.MaxPartsReport = 64, 1408, 1408
			},
			func(v *Values) {
				v.Runner.MaxFailureCause, v.Gateway.MaxSmallReport, v.Gateway.MaxPartsReport = 64, 1408, 64<<10
			},
			[]string{"tuning.gateway.maxPartsReport: KRITIKA_TUNING_GATEWAY_MAX_PARTS_REPORT", "must be at least 64Ki"},
		},
		{
			"window overlap less than a window",
			func(v *Values) { v.Indexer.WindowOverlap = v.Indexer.WindowLines },
			func(v *Values) { v.Indexer.WindowOverlap = v.Indexer.WindowLines - 1 },
			[]string{"tuning.indexer.windowOverlap: KRITIKA_TUNING_INDEXER_WINDOW_OVERLAP", "tuning.indexer.windowLines: KRITIKA_TUNING_INDEXER_WINDOW_LINES"},
		},
		{
			"window lines lowered to the default overlap",
			func(v *Values) { v.Indexer.WindowLines = 10 },
			func(v *Values) { v.Indexer.WindowLines, v.Indexer.WindowOverlap = 10, 0 },
			[]string{"tuning.indexer.windowOverlap", "tuning.indexer.windowLines"},
		},
		{
			"lexeme within PostgreSQL's limit",
			func(v *Values) { v.Lexical.MaxLexemeBytes = 2048 },
			func(v *Values) { v.Lexical.MaxLexemeBytes = 2047 },
			[]string{"tuning.lexical.maxLexemeBytes: KRITIKA_TUNING_LEXICAL_MAX_LEXEME_BYTES", "PostgreSQL"},
		},
		{
			"event instance within Kubernetes' limit",
			func(v *Values) { v.Executor.EventInstanceBytes = 129 },
			func(v *Values) { v.Executor.EventInstanceBytes = 128 },
			[]string{"tuning.executor.eventInstanceBytes: KRITIKA_TUNING_EXECUTOR_EVENT_INSTANCE_BYTES", "Kubernetes"},
		},
		{
			"event note within Kubernetes' limit",
			func(v *Values) { v.Executor.EventNoteBytes = 1025 },
			func(v *Values) { v.Executor.EventNoteBytes = 1024 },
			[]string{"tuning.executor.eventNoteBytes: KRITIKA_TUNING_EXECUTOR_EVENT_NOTE_BYTES", "Kubernetes"},
		},
		{
			"job ttl at least a second",
			func(v *Values) { v.Executor.JobTTL = 500 * time.Millisecond },
			func(v *Values) { v.Executor.JobTTL = time.Second },
			[]string{"tuning.executor.jobTtl: KRITIKA_TUNING_EXECUTOR_JOB_TTL"},
		},
		{
			"job ttl in whole seconds",
			func(v *Values) { v.Executor.JobTTL = 1500 * time.Millisecond },
			func(v *Values) { v.Executor.JobTTL = 2 * time.Second },
			[]string{"tuning.executor.jobTtl: KRITIKA_TUNING_EXECUTOR_JOB_TTL"},
		},
		{
			"fetch directory name leaves room for .diff",
			func(v *Values) { v.Runner.MaxDirNameBytes = 251 },
			func(v *Values) { v.Runner.MaxDirNameBytes = 250 },
			[]string{"tuning.runner.maxDirNameBytes: KRITIKA_TUNING_RUNNER_MAX_DIR_NAME_BYTES", ".diff"},
		},
		{
			"prompt within the spec",
			func(v *Values) { v.RunSpec.MaxPromptBytes = v.RunSpec.MaxBytes },
			func(v *Values) { v.RunSpec.MaxPromptBytes = v.RunSpec.MaxBytes - 1 },
			[]string{"tuning.runSpec.maxPromptBytes: KRITIKA_TUNING_RUN_SPEC_MAX_PROMPT_BYTES", "tuning.runSpec.maxBytes: KRITIKA_TUNING_RUN_SPEC_MAX_BYTES"},
		},
		{
			"report backoff within its ceiling",
			func(v *Values) { v.Runner.ReportBackoff = v.Runner.ReportMaxBackoff + time.Millisecond },
			func(v *Values) { v.Runner.ReportBackoff = v.Runner.ReportMaxBackoff },
			[]string{"tuning.runner.reportBackoff: KRITIKA_TUNING_RUNNER_REPORT_BACKOFF", "tuning.runner.reportMaxBackoff: KRITIKA_TUNING_RUNNER_REPORT_MAX_BACKOFF"},
		},
		{
			"log read covers the tail",
			func(v *Values) { v.Executor.LogReadBytes = v.Executor.LogTailBytes - 1 },
			func(v *Values) { v.Executor.LogReadBytes = v.Executor.LogTailBytes },
			[]string{"tuning.executor.logReadBytes: KRITIKA_TUNING_EXECUTOR_LOG_READ_BYTES", "tuning.executor.logTailBytes: KRITIKA_TUNING_EXECUTOR_LOG_TAIL_BYTES"},
		},
		{
			"catalog retry within its ttl",
			func(v *Values) { v.Model.CatalogRetry = v.Model.CatalogTTL + time.Millisecond },
			func(v *Values) { v.Model.CatalogRetry = v.Model.CatalogTTL },
			[]string{"tuning.model.catalogRetry: KRITIKA_TUNING_MODEL_CATALOG_RETRY", "tuning.model.catalogTtl: KRITIKA_TUNING_MODEL_CATALOG_TTL"},
		},
		{
			"reservation pad within the gateway's settling",
			func(v *Values) { v.Adapter.ReservationPad = v.Model.GatewaySettle + time.Millisecond },
			func(v *Values) { v.Adapter.ReservationPad = v.Model.GatewaySettle },
			[]string{"tuning.adapter.reservationPad: KRITIKA_TUNING_ADAPTER_RESERVATION_PAD", "tuning.model.gatewaySettle: KRITIKA_TUNING_MODEL_GATEWAY_SETTLE"},
		},
		{
			"release, charge and record within the gateway's settling",
			func(v *Values) {
				v.Adapter.ChargeTimeout = v.Model.GatewaySettle - v.Adapter.RecordTimeout - v.Adapter.LeaseReleaseTimeout + time.Millisecond
			},
			func(v *Values) {
				v.Adapter.ChargeTimeout = v.Model.GatewaySettle - v.Adapter.RecordTimeout - v.Adapter.LeaseReleaseTimeout
			},
			[]string{"tuning.adapter.leaseReleaseTimeout: KRITIKA_TUNING_ADAPTER_LEASE_RELEASE_TIMEOUT",
				"tuning.adapter.chargeTimeout: KRITIKA_TUNING_ADAPTER_CHARGE_TIMEOUT", "tuning.adapter.recordTimeout: KRITIKA_TUNING_ADAPTER_RECORD_TIMEOUT",
				"tuning.model.gatewaySettle"},
		},
		{
			"step retry backoff within its ceiling",
			func(v *Values) { v.Adapter.RetryMin = v.Adapter.RetryMax + time.Millisecond },
			func(v *Values) { v.Adapter.RetryMin = v.Adapter.RetryMax },
			[]string{"tuning.adapter.retryMin: KRITIKA_TUNING_ADAPTER_RETRY_MIN", "tuning.adapter.retryMax: KRITIKA_TUNING_ADAPTER_RETRY_MAX"},
		},
		{
			"grep's default within its cap",
			func(v *Values) { v.Agent.DefaultGrepMaxResults = v.Agent.GrepMaxResultsCap + 1 },
			func(v *Values) { v.Agent.DefaultGrepMaxResults = v.Agent.GrepMaxResultsCap },
			[]string{"tuning.agent.defaultGrepMaxResults: KRITIKA_TUNING_AGENT_DEFAULT_GREP_MAX_RESULTS", "tuning.agent.grepMaxResultsCap"},
		},
		{
			"appended stderr holds the kept stderr",
			func(v *Values) { v.Agent.StderrAppendShare = v.Agent.StderrShare + 1 },
			func(v *Values) { v.Agent.StderrAppendShare = v.Agent.StderrShare },
			[]string{"tuning.agent.stderrAppendShare: KRITIKA_TUNING_AGENT_STDERR_APPEND_SHARE", "tuning.agent.stderrShare"},
		},
		{
			"app JWT within GitHub's cap",
			func(v *Values) { v.GitHub.AppJWTLifetime = GitHubMaxJWTLifetime + time.Second },
			func(v *Values) { v.GitHub.AppJWTLifetime = GitHubMaxJWTLifetime },
			[]string{"tuning.gitHub.appJwtLifetime: KRITIKA_TUNING_GITHUB_APP_JWT_LIFETIME", "GitHub's cap"},
		},
		{
			"app JWT outlives its refresh lead",
			func(v *Values) { v.GitHub.TokenRefreshLead = v.GitHub.AppJWTLifetime },
			func(v *Values) { v.GitHub.TokenRefreshLead = v.GitHub.AppJWTLifetime - time.Millisecond },
			[]string{"tuning.gitHub.appJwtLifetime", "tuning.gitHub.tokenRefreshLead: KRITIKA_TUNING_GITHUB_TOKEN_REFRESH_LEAD"},
		},
		{
			"forgejo token outlives its reuse margin",
			func(v *Values) { v.Forgejo.TokenReuseMargin = v.Forgejo.TokenTTL },
			func(v *Values) { v.Forgejo.TokenReuseMargin = v.Forgejo.TokenTTL - time.Millisecond },
			[]string{"tuning.forgejo.tokenTtl: KRITIKA_TUNING_FORGEJO_TOKEN_TTL", "tuning.forgejo.tokenReuseMargin"},
		},
		{
			"page size within its cap",
			func(v *Values) { v.WebAPI.DefaultLimit = v.WebAPI.MaxLimit + 1 },
			func(v *Values) { v.WebAPI.DefaultLimit = v.WebAPI.MaxLimit },
			[]string{"tuning.webApi.defaultLimit: KRITIKA_TUNING_WEB_API_DEFAULT_LIMIT", "tuning.webApi.maxLimit: KRITIKA_TUNING_WEB_API_MAX_LIMIT"},
		},
		{
			"usage window within its cap",
			func(v *Values) { v.WebAPI.DefaultUsageWindow = v.WebAPI.MaxUsageWindow + time.Hour },
			func(v *Values) { v.WebAPI.DefaultUsageWindow = v.WebAPI.MaxUsageWindow },
			[]string{"tuning.webApi.defaultUsageWindow", "tuning.webApi.maxUsageWindow"},
		},
		{
			"default step timeout within the providers' bound",
			func(v *Values) { v.Model.DefaultStepTimeout = v.ConfigFile.MaxProviderTimeout + time.Second },
			func(v *Values) { v.Model.DefaultStepTimeout = v.ConfigFile.MaxProviderTimeout },
			[]string{"tuning.model.defaultStepTimeout", "tuning.configFile.maxProviderTimeout: KRITIKA_TUNING_CONFIG_FILE_MAX_PROVIDER_TIMEOUT"},
		},
		{
			"rerank candidates within their bound",
			func(v *Values) { v.ConfigFile.MaxRerankCandidates = v.ConfigFile.DefaultRerankCandidates - 1 },
			func(v *Values) { v.ConfigFile.MaxRerankCandidates = v.ConfigFile.DefaultRerankCandidates },
			[]string{"tuning.configFile.defaultRerankCandidates", "tuning.configFile.maxRerankCandidates"},
		},
		{
			"tei rerank candidates within their bound",
			func(v *Values) { v.ConfigFile.DefaultTEIRerankCandidates = v.ConfigFile.MaxRerankCandidates + 1 },
			func(v *Values) { v.ConfigFile.DefaultTEIRerankCandidates = v.ConfigFile.MaxRerankCandidates },
			[]string{"tuning.configFile.defaultTeiRerankCandidates: KRITIKA_TUNING_CONFIG_FILE_DEFAULT_TEI_RERANK_CANDIDATES", "tuning.configFile.maxRerankCandidates"},
		},
		{
			"rerank timeout within its bound",
			func(v *Values) { v.ConfigFile.DefaultRerankTimeout = v.ConfigFile.MaxRerankTimeout + time.Second },
			func(v *Values) { v.ConfigFile.DefaultRerankTimeout = v.ConfigFile.MaxRerankTimeout },
			[]string{"tuning.configFile.defaultRerankTimeout", "tuning.configFile.maxRerankTimeout"},
		},
		{
			"session ttl above its floor",
			func(v *Values) { v.ConfigFile.MinSessionTTL = v.ConfigFile.DefaultSessionTTL + time.Second },
			func(v *Values) { v.ConfigFile.MinSessionTTL = v.ConfigFile.DefaultSessionTTL },
			[]string{"tuning.configFile.minSessionTtl: KRITIKA_TUNING_CONFIG_FILE_MIN_SESSION_TTL", "tuning.configFile.defaultSessionTtl"},
		},
		{
			"session ttl within its ceiling",
			func(v *Values) { v.ConfigFile.MaxSessionTTL = v.ConfigFile.DefaultSessionTTL - time.Second },
			func(v *Values) { v.ConfigFile.MaxSessionTTL = v.ConfigFile.DefaultSessionTTL },
			[]string{"tuning.configFile.defaultSessionTtl", "tuning.configFile.maxSessionTtl: KRITIKA_TUNING_CONFIG_FILE_MAX_SESSION_TTL"},
		},
		{
			"the least prompt within the default budget",
			func(v *Values) { v.ConfigFile.MinPromptTokens = v.Review.DefaultBudgetTokens + 1 },
			func(v *Values) { v.ConfigFile.MinPromptTokens = v.Review.DefaultBudgetTokens },
			[]string{"tuning.configFile.minPromptTokens", "tuning.review.defaultBudgetTokens: KRITIKA_TUNING_REVIEW_DEFAULT_BUDGET_TOKENS"},
		},
		{
			"default agent timeout within what jobs leave",
			func(v *Values) { v.ConfigFile.DefaultAgentTimeout = v.JobTimeout.MaxAgentTimeout() + time.Second },
			func(v *Values) { v.ConfigFile.DefaultAgentTimeout = v.JobTimeout.MaxAgentTimeout() },
			[]string{"tuning.configFile.defaultAgentTimeout: KRITIKA_TUNING_CONFIG_FILE_DEFAULT_AGENT_TIMEOUT", "jobTimeout"},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			v := Defaults()
			tt.breaks(&v)
			err := v.Validate()
			for _, want := range tt.says {
				if err == nil || !strings.Contains(err.Error(), want) {
					t.Errorf("Validate = %v, want an error naming %q", err, want)
				}
			}
			v = Defaults()
			tt.edge(&v)
			if err := v.Validate(); err != nil {
				t.Errorf("at the edge, Validate = %v, want nil", err)
			}
		})
	}
}

// TestCrossFieldMessages: each invariant between fields refuses the values
// that break it with the error it names, and takes those at its edge.
func TestCrossFieldMessages(t *testing.T) {
	tests := []struct {
		name   string
		change func(*Values)
		want   []string // empty: valid
	}{
		{name: "the defaults", change: func(*Values) {}},
		{
			name:   "headroom past the job timeout leaves no runner deadline",
			change: func(v *Values) { v.JobTimeout.IndexWriteHeadroom = v.JobTimeout.MaxJobTimeout },
			want: []string{
				"tuning.jobTimeout.maxJobTimeout: KRITIKA_TUNING_JOB_TIMEOUT_MAX_JOB_TIMEOUT leaves no runner deadline",
				"tuning.jobTimeout.indexWriteHeadroom: KRITIKA_TUNING_JOB_TIMEOUT_INDEX_WRITE_HEADROOM",
			},
		},
		{
			name: "headroom past the job timeout leaves no agent timeout",
			change: func(v *Values) {
				v.JobTimeout.AgentFetchHeadroom = v.JobTimeout.MaxJobTimeout - v.JobTimeout.LeaseWaitHeadroom - v.JobTimeout.PublishHeadroom
			},
			want: []string{
				"tuning.jobTimeout.maxJobTimeout: KRITIKA_TUNING_JOB_TIMEOUT_MAX_JOB_TIMEOUT leaves no agent timeout",
				"tuning.jobTimeout.agentFetchHeadroom: KRITIKA_TUNING_JOB_TIMEOUT_AGENT_FETCH_HEADROOM",
			},
		},
		{
			name:   "a default runner deadline at the most",
			change: func(v *Values) { v.JobTimeout.RunnerDeadline = v.JobTimeout.MaxRunnerDeadline() },
		},
		{
			name:   "a default runner deadline past the most",
			change: func(v *Values) { v.JobTimeout.RunnerDeadline = v.JobTimeout.MaxRunnerDeadline() + time.Second },
			want: []string{
				"tuning.jobTimeout.runnerDeadline: KRITIKA_TUNING_JOB_TIMEOUT_RUNNER_DEADLINE must not exceed",
				"tuning.jobTimeout.maxJobTimeout: KRITIKA_TUNING_JOB_TIMEOUT_MAX_JOB_TIMEOUT",
			},
		},
		{
			name:   "abandoned at the second missed beat",
			change: func(v *Values) { v.JobTimeout.AbandonedBeats = 2 },
			want: []string{
				"tuning.jobTimeout.abandonedBeats: KRITIKA_TUNING_JOB_TIMEOUT_ABANDONED_BEATS must be at least 3",
				"tuning.jobTimeout.heartbeatInterval: KRITIKA_TUNING_JOB_TIMEOUT_HEARTBEAT_INTERVAL",
			},
		},
		{name: "abandoned at the third", change: func(v *Values) { v.JobTimeout.AbandonedBeats = 3 }},
		{
			name:   "a default runner deadline under a second",
			change: func(v *Values) { v.JobTimeout.RunnerDeadline = 500 * time.Millisecond },
			want:   []string{"tuning.jobTimeout.runnerDeadline: KRITIKA_TUNING_JOB_TIMEOUT_RUNNER_DEADLINE must be a whole number of seconds"},
		},
		{
			name:   "a default runner deadline of fractional seconds",
			change: func(v *Values) { v.JobTimeout.RunnerDeadline = 1500 * time.Millisecond },
			want:   []string{"tuning.jobTimeout.runnerDeadline: KRITIKA_TUNING_JOB_TIMEOUT_RUNNER_DEADLINE must be a whole number of seconds"},
		},
		{name: "a default runner deadline of a second", change: func(v *Values) { v.JobTimeout.RunnerDeadline = time.Second }},
		{
			name: "a ChatGPT look and refresh that fill the lead",
			change: func(v *Values) {
				v.Worker.ChatGPTRefreshInterval = v.ChatGPT.RefreshLead - v.Worker.ChatGPTRefreshTimeout
			},
			want: []string{
				"tuning.worker.chatGptRefreshInterval: KRITIKA_TUNING_WORKER_CHAT_GPT_REFRESH_INTERVAL and " +
					"tuning.worker.chatGptRefreshTimeout: KRITIKA_TUNING_WORKER_CHAT_GPT_REFRESH_TIMEOUT together must be less than " +
					"tuning.chatGpt.refreshLead: KRITIKA_TUNING_CHATGPT_REFRESH_LEAD",
			},
		},
		{
			name: "a ChatGPT refresh as long as the lead",
			change: func(v *Values) {
				v.Worker.ChatGPTRefreshInterval, v.Worker.ChatGPTRefreshTimeout = time.Second, v.ChatGPT.RefreshLead
			},
			want: []string{"tuning.worker.chatGptRefreshInterval: KRITIKA_TUNING_WORKER_CHAT_GPT_REFRESH_INTERVAL and "},
		},
		{
			name: "a ChatGPT look and refresh just within the lead",
			change: func(v *Values) {
				v.Worker.ChatGPTRefreshInterval = v.ChatGPT.RefreshLead - v.Worker.ChatGPTRefreshTimeout - time.Nanosecond
			},
		},
		{
			name:   "a ChatGPT exchange that takes the whole refresh",
			change: func(v *Values) { v.Worker.ChatGPTHTTPTimeout = v.Worker.ChatGPTRefreshTimeout },
			want: []string{
				"tuning.worker.chatGptRefreshTimeout: KRITIKA_TUNING_WORKER_CHAT_GPT_REFRESH_TIMEOUT must exceed " +
					"tuning.worker.chatGptHttpTimeout: KRITIKA_TUNING_WORKER_CHAT_GPT_HTTP_TIMEOUT",
			},
		},
		{
			name:   "a ChatGPT exchange that leaves the save a moment",
			change: func(v *Values) { v.Worker.ChatGPTHTTPTimeout = v.Worker.ChatGPTRefreshTimeout - time.Nanosecond },
		},
		{
			name:   "a token lifetime of part of a day",
			change: func(v *Values) { v.WebAPI.TokenLifetimes = Durations{0, 36 * time.Hour} },
			want:   []string{"tuning.webApi.tokenLifetimes: KRITIKA_TUNING_WEB_API_TOKEN_LIFETIMES must be whole days, or 0 for never", "entry 2"},
		},
		{name: "a token lifetime of a day", change: func(v *Values) { v.WebAPI.TokenLifetimes = Durations{24 * time.Hour} }},
		{name: "no token lifetimes", change: func(v *Values) { v.WebAPI.TokenLifetimes = Durations{} }},
		{
			name:   "an application statement timeout under a millisecond",
			change: func(v *Values) { v.Store.AppStatementTimeout = 999 * time.Microsecond },
			want:   []string{"tuning.store.appStatementTimeout: KRITIKA_TUNING_STORE_APP_STATEMENT_TIMEOUT must be at least 1ms"},
		},
		{
			name:   "an owner statement timeout under a millisecond",
			change: func(v *Values) { v.Store.OwnerStatementTimeout = 999 * time.Microsecond },
			want:   []string{"tuning.store.ownerStatementTimeout: KRITIKA_TUNING_STORE_OWNER_STATEMENT_TIMEOUT must be at least 1ms"},
		},
		{
			name: "statement timeouts of a millisecond",
			change: func(v *Values) {
				v.Store.AppStatementTimeout, v.Store.OwnerStatementTimeout = time.Millisecond, time.Millisecond
			},
		},
		{
			name: "statement timeouts at the most Postgres takes",
			change: func(v *Values) {
				v.Store.AppStatementTimeout, v.Store.OwnerStatementTimeout = MaxStatementTimeout, MaxStatementTimeout
			},
		},
		{
			name: "statement timeouts past the most Postgres takes",
			change: func(v *Values) {
				v.Store.AppStatementTimeout, v.Store.OwnerStatementTimeout = MaxStatementTimeout+1, MaxStatementTimeout+1
			},
			want: []string{
				"tuning.store.appStatementTimeout: KRITIKA_TUNING_STORE_APP_STATEMENT_TIMEOUT must not exceed",
				"tuning.store.ownerStatementTimeout: KRITIKA_TUNING_STORE_OWNER_STATEMENT_TIMEOUT must not exceed",
			},
		},
		{
			name:   "an embedding item cap at the batch cap",
			change: func(v *Values) { v.Model.DefaultEmbedMaxItemChars = v.Model.DefaultEmbedMaxBatchChars },
		},
		{
			name:   "an embedding item cap past the batch cap",
			change: func(v *Values) { v.Model.DefaultEmbedMaxItemChars = v.Model.DefaultEmbedMaxBatchChars + 1 },
			want: []string{
				"tuning.model.defaultEmbedMaxItemChars: KRITIKA_TUNING_MODEL_DEFAULT_EMBED_MAX_ITEM_CHARS must not exceed " +
					"tuning.model.defaultEmbedMaxBatchChars: KRITIKA_TUNING_MODEL_DEFAULT_EMBED_MAX_BATCH_CHARS",
			},
		},
		{
			name:   "a default agent timeout under a second",
			change: func(v *Values) { v.ConfigFile.DefaultAgentTimeout = 500 * time.Millisecond },
			want: []string{
				"tuning.configFile.defaultAgentTimeout: KRITIKA_TUNING_CONFIG_FILE_DEFAULT_AGENT_TIMEOUT must be a whole number of seconds",
			},
		},
		{
			name:   "a default agent timeout of fractional seconds",
			change: func(v *Values) { v.ConfigFile.DefaultAgentTimeout = 1500 * time.Millisecond },
			want: []string{
				"tuning.configFile.defaultAgentTimeout: KRITIKA_TUNING_CONFIG_FILE_DEFAULT_AGENT_TIMEOUT must be a whole number of seconds",
			},
		},
		{name: "a default agent timeout of a second", change: func(v *Values) { v.ConfigFile.DefaultAgentTimeout = time.Second }},
		{
			name:   "a default command timeout of fractional seconds",
			change: func(v *Values) { v.ConfigFile.DefaultCommandTimeout = 1500 * time.Millisecond },
			want: []string{
				"tuning.configFile.defaultCommandTimeout: KRITIKA_TUNING_CONFIG_FILE_DEFAULT_COMMAND_TIMEOUT must be a whole number of seconds",
			},
		},
		{
			name:   "a default command timeout under a second",
			change: func(v *Values) { v.ConfigFile.DefaultCommandTimeout = 500 * time.Millisecond },
			want: []string{
				"tuning.configFile.defaultCommandTimeout: KRITIKA_TUNING_CONFIG_FILE_DEFAULT_COMMAND_TIMEOUT must be a whole number of seconds",
			},
		},
		{name: "a default command timeout of a second", change: func(v *Values) { v.ConfigFile.DefaultCommandTimeout = time.Second }},
		{
			name: "a heartbeat window past the longest duration",
			change: func(v *Values) {
				v.JobTimeout.AbandonedBeats = int(math.MaxInt64/int64(v.JobTimeout.HeartbeatInterval)) + 1
			},
			want: []string{
				"tuning.jobTimeout.abandonedBeats: KRITIKA_TUNING_JOB_TIMEOUT_ABANDONED_BEATS times " +
					"tuning.jobTimeout.heartbeatInterval: KRITIKA_TUNING_JOB_TIMEOUT_HEARTBEAT_INTERVAL must not exceed",
			},
		},
		{
			name: "a heartbeat window at the longest duration",
			change: func(v *Values) {
				v.JobTimeout.AbandonedBeats = int(math.MaxInt64 / int64(v.JobTimeout.HeartbeatInterval))
			},
		},
		{
			name:   "an owner pool of one connection",
			change: func(v *Values) { v.Store.OwnerPoolMaxConns = 1 },
			want:   []string{"tuning.store.ownerPoolMaxConns: KRITIKA_TUNING_STORE_OWNER_POOL_MAX_CONNS must be at least 2"},
		},
		{name: "an owner pool of two connections", change: func(v *Values) { v.Store.OwnerPoolMaxConns = 2 }},
		{
			name:   "a GitLab page past what GitLab serves",
			change: func(v *Values) { v.GitLab.PageSize = GitLabMaxPageSize + 1 },
			want:   []string{"tuning.gitLab.pageSize: KRITIKA_TUNING_GITLAB_PAGE_SIZE must not exceed GitLab's largest page, 100"},
		},
		{name: "a GitLab page of what GitLab serves", change: func(v *Values) { v.GitLab.PageSize = GitLabMaxPageSize }},
		{
			name:   "a thread timeout past the job cap",
			change: func(v *Values) { v.Worker.ThreadTimeout = v.JobTimeout.MaxJobTimeout + time.Second },
			want: []string{
				"tuning.worker.threadTimeout: KRITIKA_TUNING_WORKER_THREAD_TIMEOUT must not exceed " +
					"tuning.jobTimeout.maxJobTimeout: KRITIKA_TUNING_JOB_TIMEOUT_MAX_JOB_TIMEOUT",
			},
		},
		{name: "a thread timeout at the job cap", change: func(v *Values) { v.Worker.ThreadTimeout = v.JobTimeout.MaxJobTimeout }},
		{
			name:   "an embedding item cap under a rune",
			change: func(v *Values) { v.Model.DefaultEmbedMaxItemChars = 3 },
			want:   []string{"tuning.model.defaultEmbedMaxItemChars: KRITIKA_TUNING_MODEL_DEFAULT_EMBED_MAX_ITEM_CHARS must be at least 4"},
		},
		{name: "an embedding item cap of a rune", change: func(v *Values) { v.Model.DefaultEmbedMaxItemChars = 4 }},
		{
			name: "merging and scoring past the Prepare job",
			change: func(v *Values) {
				v.JobTimeout.LeaseWaitHeadroom, v.JobTimeout.PublishHeadroom = time.Second, 2*time.Minute
			},
			want: []string{
				"tuning.worker.mergeTimeout: KRITIKA_TUNING_WORKER_MERGE_TIMEOUT and " +
					"tuning.worker.confidenceTimeout: KRITIKA_TUNING_WORKER_CONFIDENCE_TIMEOUT together must be less than",
				"tuning.jobTimeout.leaseWaitHeadroom: KRITIKA_TUNING_JOB_TIMEOUT_LEASE_WAIT_HEADROOM",
			},
		},
		{
			name: "merging and scoring just within the Prepare job",
			change: func(v *Values) {
				v.JobTimeout.PublishHeadroom = 2 * time.Minute
				v.JobTimeout.LeaseWaitHeadroom = v.Worker.MergeTimeout + v.Worker.ConfidenceTimeout - v.JobTimeout.PublishHeadroom + time.Nanosecond
			},
		},
		{
			name:   "an onboarding retry past River's retention",
			change: func(v *Values) { v.Jobs.OnboardRetryAfter = RiverFinishedJobRetention + time.Second },
			want:   []string{"tuning.jobs.onboardRetryAfter: KRITIKA_TUNING_JOBS_ONBOARD_RETRY_AFTER must not exceed 24h0m0s"},
		},
		{name: "an onboarding retry at River's retention", change: func(v *Values) { v.Jobs.OnboardRetryAfter = RiverFinishedJobRetention }},
		{
			name:   "a usage window under the dashboard's",
			change: func(v *Values) { v.WebAPI.MaxUsageWindow, v.WebAPI.DefaultUsageWindow = 336*time.Hour, 168*time.Hour },
			want:   []string{"tuning.webApi.maxUsageWindow: KRITIKA_TUNING_WEB_API_MAX_USAGE_WINDOW must be at least 2184h0m0s"},
		},
		{name: "a usage window of the dashboard's", change: func(v *Values) { v.WebAPI.MaxUsageWindow = DashboardUsageWindow }},
		{
			name: "a step wait under a second",
			change: func(v *Values) {
				v.Model.StepBudgetWaits, v.Model.GatewaySettle = 400*time.Millisecond, 500*time.Millisecond
				v.Adapter.ReservationPad, v.Adapter.ChargeTimeout, v.Adapter.RecordTimeout, v.Adapter.LeaseReleaseTimeout = 1, 1, 1, 1
			},
			want: []string{
				"tuning.model.stepBudgetWaits: KRITIKA_TUNING_MODEL_STEP_BUDGET_WAITS and " +
					"tuning.model.gatewaySettle: KRITIKA_TUNING_MODEL_GATEWAY_SETTLE together must be at least 1s",
			},
		},
		{
			name: "token lifetimes under a second",
			change: func(v *Values) {
				v.GitHub.AppJWTLifetime, v.GitHub.TokenRefreshLead = 900*time.Millisecond, 100*time.Millisecond
				v.Forgejo.TokenTTL, v.Forgejo.TokenReuseMargin, v.Forgejo.RunnerTokenTTL = 900*time.Millisecond, 100*time.Millisecond, 900*time.Millisecond
			},
			want: []string{
				"tuning.gitHub.appJwtLifetime: KRITIKA_TUNING_GITHUB_APP_JWT_LIFETIME must be at least 1s",
				"tuning.forgejo.tokenTtl: KRITIKA_TUNING_FORGEJO_TOKEN_TTL must be at least 1s",
				"tuning.forgejo.runnerTokenTtl: KRITIKA_TUNING_FORGEJO_RUNNER_TOKEN_TTL must be at least 1s",
			},
		},
		{
			name:   "a similar-code floor past 1",
			change: func(v *Values) { v.ConfigFile.DefaultSimilarFloor = 1.1 },
			want:   []string{"tuning.configFile.defaultSimilarFloor: KRITIKA_TUNING_CONFIG_FILE_DEFAULT_SIMILAR_FLOOR must be from 0 to 1"},
		},
		{
			name:   "a similar-code margin past 1",
			change: func(v *Values) { v.ConfigFile.DefaultSimilarMargin = 1.1 },
			want:   []string{"tuning.configFile.defaultSimilarMargin: KRITIKA_TUNING_CONFIG_FILE_DEFAULT_SIMILAR_MARGIN must be from 0 to 1"},
		},
		{
			name:   "a similar-code floor that is not a number",
			change: func(v *Values) { v.ConfigFile.DefaultSimilarFloor = math.NaN() },
			want:   []string{"tuning.configFile.defaultSimilarFloor: KRITIKA_TUNING_CONFIG_FILE_DEFAULT_SIMILAR_FLOOR must be from 0 to 1"},
		},
		{
			name:   "a similar-code floor and margin of 0",
			change: func(v *Values) { v.ConfigFile.DefaultSimilarFloor, v.ConfigFile.DefaultSimilarMargin = 0, 0 },
		},
		{
			name:   "a similar-code floor and margin of 1",
			change: func(v *Values) { v.ConfigFile.DefaultSimilarFloor, v.ConfigFile.DefaultSimilarMargin = 1, 1 },
		},
		{
			name:   "scoring at the publish headroom",
			change: func(v *Values) { v.Worker.ConfidenceTimeout = v.JobTimeout.PublishHeadroom },
		},
		{
			name:   "scoring past the publish headroom",
			change: func(v *Values) { v.Worker.ConfidenceTimeout = v.JobTimeout.PublishHeadroom + time.Second },
			want: []string{
				"tuning.worker.confidenceTimeout: KRITIKA_TUNING_WORKER_CONFIDENCE_TIMEOUT must not exceed " +
					"tuning.jobTimeout.publishHeadroom: KRITIKA_TUNING_JOB_TIMEOUT_PUBLISH_HEADROOM",
			},
		},
		{
			name:   "merging past the publish headroom",
			change: func(v *Values) { v.JobTimeout.PublishHeadroom = v.Worker.MergeTimeout - time.Second },
			want: []string{
				"tuning.worker.mergeTimeout: KRITIKA_TUNING_WORKER_MERGE_TIMEOUT must not exceed " +
					"tuning.jobTimeout.publishHeadroom: KRITIKA_TUNING_JOB_TIMEOUT_PUBLISH_HEADROOM",
			},
		},
		{
			name:   "a lease that expires at its heartbeat",
			change: func(v *Values) { v.Store.LeaseExpiry = v.Store.LeaseHeartbeat },
			want: []string{
				"tuning.store.leaseExpiry: KRITIKA_TUNING_STORE_LEASE_EXPIRY must exceed " +
					"tuning.store.leaseHeartbeat: KRITIKA_TUNING_STORE_LEASE_HEARTBEAT",
			},
		},
		{
			name:   "a snooze floor over its cap",
			change: func(v *Values) { v.Worker.SnoozeMin = v.Worker.SnoozeMax + 1 },
			want: []string{
				"tuning.worker.snoozeMin: KRITIKA_TUNING_WORKER_SNOOZE_MIN must not exceed tuning.worker.snoozeMax: KRITIKA_TUNING_WORKER_SNOOZE_MAX",
			},
		},
		{name: "a snooze floor at its cap", change: func(v *Values) { v.Worker.SnoozeMin = v.Worker.SnoozeMax }},
		{
			name:   "a forge wait floor over its cap",
			change: func(v *Values) { v.Worker.ForgeWaitMax = v.Worker.ForgeWaitMin - 1 },
			want: []string{
				"tuning.worker.forgeWaitMin: KRITIKA_TUNING_WORKER_FORGE_WAIT_MIN must not exceed tuning.worker.forgeWaitMax: KRITIKA_TUNING_WORKER_FORGE_WAIT_MAX",
			},
		},
		{
			name:   "a lease poll floor over its cap",
			change: func(v *Values) { v.Store.LeasePollMin = v.Store.LeasePollMax + 1 },
			want: []string{
				"tuning.store.leasePollMin: KRITIKA_TUNING_STORE_LEASE_POLL_MIN must not exceed tuning.store.leasePollMax: KRITIKA_TUNING_STORE_LEASE_POLL_MAX",
			},
		},
		{
			name:   "a listen backoff floor over its cap",
			change: func(v *Values) { v.Store.ListenBackoffMin = v.Store.ListenBackoffMax + 1 },
			want: []string{
				"tuning.store.listenBackoffMin: KRITIKA_TUNING_STORE_LISTEN_BACKOFF_MIN must not exceed " +
					"tuning.store.listenBackoffMax: KRITIKA_TUNING_STORE_LISTEN_BACKOFF_MAX",
			},
		},
		{
			name:   "a keepalive under a second",
			change: func(v *Values) { v.Store.KeepaliveInterval = 500 * time.Millisecond },
			want:   []string{"tuning.store.keepaliveInterval: KRITIKA_TUNING_STORE_KEEPALIVE_INTERVAL must be at least 1s"},
		},
		{name: "a comment at GitHub's cap", change: func(v *Values) { v.Review.MaxRenderBytes = 65536 }},
		{
			name:   "a comment past GitHub's cap",
			change: func(v *Values) { v.Review.MaxRenderBytes = 65537 },
			want:   []string{"tuning.review.maxRenderBytes: KRITIKA_TUNING_REVIEW_MAX_RENDER_BYTES must not exceed GitHub's comment cap"},
		},
		{name: "a comment at the render floor", change: func(v *Values) { v.Review.MaxRenderBytes = MinRenderBytes }},
		{
			name:   "a comment under the render floor",
			change: func(v *Values) { v.Review.MaxRenderBytes = MinRenderBytes - 1 },
			want:   []string{"tuning.review.maxRenderBytes: KRITIKA_TUNING_REVIEW_MAX_RENDER_BYTES must be at least 1024 bytes"},
		},
		{name: "a body share at its floor", change: func(v *Values) { v.Review.BodyShare = MinBodyShare }},
		{
			name:   "a body share under its floor",
			change: func(v *Values) { v.Review.BodyShare = MinBodyShare - 1 },
			want:   []string{"tuning.review.bodyShare: KRITIKA_TUNING_REVIEW_BODY_SHARE must be at least 3"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := Defaults()
			tt.change(&v)
			err := crossFieldErrors(v)
			if len(tt.want) == 0 {
				if err != "" {
					t.Fatalf("cross-field errors = %s, want none", err)
				}
				return
			}
			for _, want := range tt.want {
				if !strings.Contains(err, want) {
					t.Errorf("cross-field errors = %q, want %q", err, want)
				}
			}
		})
	}
}

// crossFieldErrors is what the cross-field rules say of v, "" when none
// is broken.
func crossFieldErrors(v Values) string {
	var msgs []string
	for _, rule := range crossField {
		if err := rule(v); err != nil {
			msgs = append(msgs, err.Error())
		}
	}
	return strings.Join(msgs, "\n")
}

// TestJobTimeoutChain: the bounds derived from JobTimeout's fields follow
// them, at the defaults and overridden.
func TestJobTimeoutChain(t *testing.T) {
	d := Defaults().JobTimeout
	if got, want := d.MaxRunnerDeadline(), 2*time.Hour; got != want {
		t.Errorf("MaxRunnerDeadline = %s, want %s", got, want)
	}
	if got, want := d.MaxAgentTimeout(), 3*time.Hour-25*time.Minute; got != want {
		t.Errorf("MaxAgentTimeout = %s, want %s", got, want)
	}
	if got, want := d.RescueStuckJobsAfter(), 4*time.Hour; got != want {
		t.Errorf("RescueStuckJobsAfter = %s, want %s", got, want)
	}
	if got, want := d.HeartbeatAbandonedAfter(), 3*time.Minute; got != want {
		t.Errorf("HeartbeatAbandonedAfter = %s, want %s", got, want)
	}
	if got, want := d.JobFenceAfter(), 150*time.Second; got != want {
		t.Errorf("JobFenceAfter = %s, want %s", got, want)
	}
	d.MaxJobTimeout = 5 * time.Hour
	if got, want := d.MaxRunnerDeadline(), 4*time.Hour; got != want {
		t.Errorf("MaxRunnerDeadline at a 5h job timeout = %s, want %s", got, want)
	}
	if got, want := d.RescueStuckJobsAfter(), 6*time.Hour; got != want {
		t.Errorf("RescueStuckJobsAfter at a 5h job timeout = %s, want %s", got, want)
	}
}

// TestHeartbeatStaleSaturates: a sum past the longest Duration is the
// longest, as the heartbeat rule's own sums are.
func TestHeartbeatStaleSaturates(t *testing.T) {
	v := Defaults()
	if got, want := v.HeartbeatStale(), v.RunSpec.ReportRetryWindow+v.RunSpec.ReportTimeout+2*v.RunSpec.HeartbeatInterval; got != want {
		t.Errorf("HeartbeatStale = %s, want %s", got, want)
	}
	v.RunSpec.ReportRetryWindow = math.MaxInt64 - v.RunSpec.ReportTimeout
	if got := v.HeartbeatStale(); got != math.MaxInt64 {
		t.Errorf("HeartbeatStale past the longest Duration = %s, want %s", got, time.Duration(math.MaxInt64))
	}
}
