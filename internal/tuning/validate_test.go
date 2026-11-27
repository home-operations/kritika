package tuning

import (
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
			"index body holds the index",
			func(v *Values) {
				v.Gateway.MaxIndexBody = ByteSize(int64(v.Indexer.MaxChunks)*int64(v.Indexer.MaxChunkBytes) - 1)
			},
			func(v *Values) {
				v.Gateway.MaxIndexBody = ByteSize(int64(v.Indexer.MaxChunks) * int64(v.Indexer.MaxChunkBytes))
			},
			[]string{"KRITIKA_TUNING_GATEWAY_MAX_INDEX_BODY", "KRITIKA_TUNING_INDEXER_MAX_CHUNKS", "KRITIKA_TUNING_INDEXER_MAX_CHUNK_BYTES"},
		},
		{
			"report body holds the pack's diffs and the conversation",
			func(v *Values) { v.Runner.MaxPackDiffBytes = v.Gateway.MaxReportBody / 2 },
			func(v *Values) {
				v.Runner.MaxPackDiffBytes = (v.Gateway.MaxReportBody - v.Gateway.MaxConversationBytes) / 2
			},
			[]string{"tuning.gateway.maxReportBody: KRITIKA_TUNING_GATEWAY_MAX_REPORT_BODY", "tuning.runner.maxPackDiffBytes", "tuning.gateway.maxConversationBytes"},
		},
		{
			"similar request body holds its queries",
			func(v *Values) { v.ContextPack.SimilarQueryChars = int(v.Gateway.MaxSimilarRequest) },
			func(v *Values) {
				v.ContextPack.SimilarQueryChars = int(v.Gateway.MaxSimilarRequest) / v.ContextPack.SimilarQueries
			},
			[]string{"tuning.gateway.maxSimilarRequest", "KRITIKA_TUNING_CONTEXT_PACK_SIMILAR_QUERIES", "KRITIKA_TUNING_CONTEXT_PACK_SIMILAR_QUERY_CHARS"},
		},
		{
			"similar answer body holds its chunks",
			func(v *Values) {
				v.Runner.MaxSimilarAnswerBody = ByteSize(v.ContextPack.SimilarLimit)*v.Indexer.MaxChunkBytes - 1
			},
			func(v *Values) {
				v.Runner.MaxSimilarAnswerBody = ByteSize(v.ContextPack.SimilarLimit) * v.Indexer.MaxChunkBytes
			},
			[]string{"tuning.runner.maxSimilarAnswerBody", "tuning.contextPack.similarLimit", "tuning.indexer.maxChunkBytes"},
		},
		{
			"similar limit covers the default answer",
			func(v *Values) { v.Gateway.SimilarMax = v.ContextPack.SimilarLimit + 1 },
			func(v *Values) { v.Gateway.SimilarMax = v.ContextPack.SimilarLimit },
			[]string{"tuning.contextPack.similarLimit: KRITIKA_TUNING_CONTEXT_PACK_SIMILAR_LIMIT", "tuning.gateway.similarMax: KRITIKA_TUNING_GATEWAY_SIMILAR_MAX"},
		},
		{
			"spec within the Secret",
			func(v *Values) { v.RunSpec.MaxBytes = 1 << 20 },
			func(v *Values) { v.RunSpec.MaxBytes = 1<<20 - 1 },
			[]string{"tuning.runSpec.maxBytes: KRITIKA_TUNING_RUN_SPEC_MAX_BYTES", "Secret"},
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
			"charge and record within the gateway's settling",
			func(v *Values) {
				v.Adapter.ChargeTimeout = v.Model.GatewaySettle - v.Adapter.RecordTimeout + time.Millisecond
			},
			func(v *Values) { v.Adapter.ChargeTimeout = v.Model.GatewaySettle - v.Adapter.RecordTimeout },
			[]string{"tuning.adapter.chargeTimeout: KRITIKA_TUNING_ADAPTER_CHARGE_TIMEOUT", "tuning.adapter.recordTimeout: KRITIKA_TUNING_ADAPTER_RECORD_TIMEOUT",
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

// TestCrossFieldMessages: each of the timeout, lease and comment
// invariants refuses the values that break it with the error it names, by
// both fields' paths and variables, and takes those at its edge.
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
			name:   "abandoned at the first missed beat",
			change: func(v *Values) { v.JobTimeout.AbandonedBeats = 1 },
			want: []string{
				"tuning.jobTimeout.abandonedBeats: KRITIKA_TUNING_JOB_TIMEOUT_ABANDONED_BEATS must be at least 2",
				"tuning.jobTimeout.heartbeatInterval: KRITIKA_TUNING_JOB_TIMEOUT_HEARTBEAT_INTERVAL",
			},
		},
		{name: "abandoned at the second", change: func(v *Values) { v.JobTimeout.AbandonedBeats = 2 }},
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
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := Defaults()
			tt.change(&v)
			err := crossFieldErrors(v)
			if len(tt.want) == 0 {
				if err != "" {
					t.Fatalf("Validate = %s, want no cross-field error", err)
				}
				return
			}
			for _, want := range tt.want {
				if !strings.Contains(err, want) {
					t.Errorf("Validate = %q, want %q", err, want)
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
