// Package metrics is every Prometheus series kritika exports beyond the Go
// runtime. One Metrics value is registered per process and shared by its
// parts. A nil *Metrics records nothing, so tests need not register one.
package metrics

import (
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Metrics holds the collectors. Labels are bounded by configuration
// (accounts, connections, models) or by fixed vocabularies (status, stage,
// outcome). Nothing per PR or per commit is ever a label.
type Metrics struct {
	cache          cacheMetrics
	webhooks       *prometheus.CounterVec
	polls          *prometheus.CounterVec
	polled         *prometheus.CounterVec
	reviews        *prometheus.CounterVec
	reviewDuration *prometheus.HistogramVec
	reviewStage    *prometheus.HistogramVec
	reviewSlotWait *prometheus.HistogramVec
	followups      *prometheus.CounterVec
	threads        *prometheus.CounterVec
	findings       *prometheus.CounterVec
	confidence     *prometheus.CounterVec
	confidenceHeld *prometheus.CounterVec
	indexRuns      *prometheus.CounterVec
	indexChunks    *prometheus.CounterVec
	contextChunks  *prometheus.CounterVec
	runnerRuns     *prometheus.CounterVec
	runnerDuration *prometheus.HistogramVec
	leaseWait      *prometheus.HistogramVec
	queueWait      *prometheus.HistogramVec
	vectorSearch   *prometheus.HistogramVec
	reviewSnoozes  *prometheus.CounterVec
	jobsRescued    *prometheus.CounterVec
	modelCalls     *prometheus.CounterVec
	modelTokens    *prometheus.CounterVec
	modelCost      *prometheus.CounterVec
	modelUnpriced  *prometheus.CounterVec
	egress         *prometheus.CounterVec
	forgeLimits    *prometheus.CounterVec
	transcripts    *prometheus.CounterVec
	leader         prometheus.Gauge
}

// Label names shared across series.
const (
	lblAccount    = "account"
	lblModel      = "model"
	lblRole       = "role"
	lblOutcome    = "outcome"
	lblConnection = "connection"
	lblKind       = "kind"
	lblQueue      = "queue"
)

// New builds the collectors, each registered on reg as it is made.
func New(reg prometheus.Registerer) *Metrics {
	f := promauto.With(reg)
	m := &Metrics{
		cache: newCacheMetrics(reg),
		webhooks: f.NewCounterVec(prometheus.CounterOpts{
			Name: "kritika_webhooks_total", Help: "Webhook deliveries by connection and what became of them.",
		}, []string{lblConnection, lblOutcome}),
		polls: f.NewCounterVec(prometheus.CounterOpts{
			Name: "kritika_polls_total", Help: "Backstop polls per connection, by outcome (ok, error).",
		}, []string{lblConnection, lblOutcome}),
		polled: f.NewCounterVec(prometheus.CounterOpts{
			Name: "kritika_polled_pull_requests_total", Help: "Open pull requests the backstop poll handed to ingest.",
		}, []string{lblConnection}),
		reviews: f.NewCounterVec(prometheus.CounterOpts{
			Name: "kritika_reviews_total", Help: "Reviews finished, by terminal status.",
		}, []string{lblAccount, "status"}),
		forgeLimits: f.NewCounterVec(prometheus.CounterOpts{
			Name: "kritika_forge_rate_limits_total",
			Help: "Responses the forge refused for a rate limit, by connection and whether the wait was taken.",
		}, []string{lblConnection, lblOutcome}),
		egress: f.NewCounterVec(prometheus.CounterOpts{
			Name: "kritika_egress_requests_total",
			Help: "Requests runner pods made through the gateway, by kind (connect, http) and outcome (allowed, refused, error).",
		}, []string{lblKind, lblOutcome}),
		leader: f.NewGauge(prometheus.GaugeOpts{
			Name: "kritika_leader", Help: "1 while this replica holds the leader lock, else 0.",
		}),
		transcripts: f.NewCounterVec(prometheus.CounterOpts{
			Name: "kritika_transcript_writes_total",
			Help: "Model calls recorded for the transcript view, by kind (agent_step, confidence, merge) and outcome (ok, error).",
		}, []string{lblKind, lblOutcome}),
		reviewStage: f.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "kritika_review_stage_duration_seconds",
			Help:    "Wall time of review preparation and publication operations, including failed attempts. Stages can overlap.",
			Buckets: []float64{.01, .05, .1, .25, .5, 1, 2, 5, 10, 20, 30, 60, 120, 300, 600, 1800},
		}, []string{lblAccount, "stage"}),
		reviewSlotWait: f.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "kritika_review_slot_wait_seconds",
			Help:    "Elapsed model-slot snooze intervals observed when a review is picked up again, including queue delay.",
			Buckets: []float64{1, 2, 5, 10, 20, 30, 60, 120, 300, 600, 1800, 3600},
		}, []string{lblAccount, lblModel}),
		reviewDuration: f.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "kritika_review_duration_seconds",
			Help:    "Wall time of a review from its first job's pickup to its terminal status, across its stages.",
			Buckets: []float64{5, 10, 20, 30, 60, 120, 300, 600, 900, 1800, 3600, 7200, 10800},
		}, []string{lblAccount}),
		followups: f.NewCounterVec(prometheus.CounterOpts{
			Name: "kritika_followups_total", Help: "Follow-up mentions handled, by outcome: answered, limited, ignored, failed.",
		}, []string{lblAccount, lblOutcome}),
		threads: f.NewCounterVec(prometheus.CounterOpts{
			Name: "kritika_threads_total",
			Help: "Finding threads a person resolved or unresolved, by outcome: dismissed, addressed, restored, ignored, failed.",
		}, []string{lblAccount, lblOutcome}),
		findings: f.NewCounterVec(prometheus.CounterOpts{
			Name: "kritika_findings_total", Help: "Findings posted, by severity and category.",
		}, []string{lblAccount, "severity", "category"}),
		confidence: f.NewCounterVec(prometheus.CounterOpts{
			Name: "kritika_confidence_scores_total",
			Help: "Reviews the confidence model scored, by score (0 to 5) and risk (low, medium, high, critical).",
		}, []string{lblAccount, "score", "risk"}),
		confidenceHeld: f.NewCounterVec(prometheus.CounterOpts{
			Name: "kritika_confidence_held_total",
			Help: "Confidence answers kritika held to its own rule, by what: score (under the findings' ceiling), " +
				"risk (given as no level, taken as critical), floor (raised to a path's floor).",
		}, []string{lblAccount, "what"}),
		indexRuns: f.NewCounterVec(prometheus.CounterOpts{
			Name: "kritika_index_runs_total", Help: "Index runs finished, by mode and status.",
		}, []string{lblAccount, "mode", "status"}),
		indexChunks: f.NewCounterVec(prometheus.CounterOpts{
			Name: "kritika_index_chunks_total", Help: "Chunks embedded into the index.",
		}, []string{lblAccount}),
		contextChunks: f.NewCounterVec(prometheus.CounterOpts{
			Name: "kritika_context_chunks_total",
			Help: "Context chunks a review's prompt was given, by stage (overlay, definition, caller, similar).",
		}, []string{lblAccount, "stage"}),
		runnerRuns: f.NewCounterVec(prometheus.CounterOpts{
			Name: "kritika_runner_runs_total", Help: "Runner Jobs finished, by kind and outcome.",
		}, []string{lblAccount, "kind", lblOutcome}),
		runnerDuration: f.NewHistogramVec(prometheus.HistogramOpts{
			Name: "kritika_runner_duration_seconds", Help: "Runner Job time from start to finish.",
			Buckets: []float64{2, 5, 10, 20, 30, 60, 120, 300, 600, 900, 1800, 3600, 7200, 10800},
		}, []string{"kind"}),
		queueWait: f.NewHistogramVec(prometheus.HistogramOpts{
			Name: "kritika_queue_wait_seconds", Help: "Time from scheduled eligibility to worker start, per pickup including retries and snoozes.",
			Buckets: []float64{0.01, 0.1, 1, 5, 15, 30, 60, 120, 300, 600, 1800},
		}, []string{lblQueue}),
		vectorSearch: f.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "kritika_vector_search_seconds",
			Help:    "Database vector-search transaction time per request, excluding embedding and model-slot waits.",
			Buckets: []float64{0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2, 5, 15, 60},
		}, []string{lblAccount, lblOutcome}),
		leaseWait: f.NewHistogramVec(prometheus.HistogramOpts{
			Name: "kritika_lease_wait_seconds", Help: "Time spent waiting for a model concurrency slot.",
			Buckets: []float64{0.01, 0.1, 1, 5, 15, 30, 60, 120, 300, 600, 900, 1800, 3600},
		}, []string{lblAccount, lblModel}),
		reviewSnoozes: f.NewCounterVec(prometheus.CounterOpts{
			Name: "kritika_review_snoozes_total", Help: "Reviews put back on the queue because every model slot was held.",
		}, []string{lblAccount, lblModel}),
		jobsRescued: f.NewCounterVec(prometheus.CounterOpts{
			Name: "kritika_jobs_rescued_total", Help: "Jobs of a dead replica handed back to the queue by the leader, by kind and state.",
		}, []string{lblKind, "state"}),
		modelCalls: f.NewCounterVec(prometheus.CounterOpts{
			Name: "kritika_model_calls_total", Help: "Model calls, by the model that answered, role and outcome.",
		}, []string{lblAccount, lblModel, lblRole, lblOutcome}),
		modelTokens: f.NewCounterVec(prometheus.CounterOpts{
			Name: "kritika_model_tokens_total",
			Help: "Tokens spent, by role and direction (input, cached, output); " +
				"cached is the part of input the provider served from its prompt cache.",
		}, []string{lblAccount, lblModel, lblRole, "direction"}),
		modelCost: f.NewCounterVec(prometheus.CounterOpts{
			Name: "kritika_model_cost_usd_total", Help: "Provider-reported cost in US dollars, by role.",
		}, []string{lblAccount, lblModel, lblRole}),
		modelUnpriced: f.NewCounterVec(prometheus.CounterOpts{
			Name: "kritika_model_unpriced_calls_total",
			Help: "Model calls with no reported cost and no configured price, by role; kritika_model_cost_usd_total leaves them out.",
		}, []string{lblAccount, lblModel, lblRole}),
	}
	return m
}

// Webhook counts one delivery.
func (m *Metrics) Webhook(connection, outcome string) {
	if m != nil {
		m.webhooks.WithLabelValues(connection, outcome).Inc()
	}
}

// Poll counts one connection poll and the pull requests it handed on.
func (m *Metrics) Poll(connection, outcome string, pullRequests int) {
	if m != nil {
		m.polls.WithLabelValues(connection, outcome).Inc()
		if pullRequests > 0 {
			m.polled.WithLabelValues(connection).Add(float64(pullRequests))
		}
	}
}

// Review counts a finished review and its duration.
func (m *Metrics) Review(account, status string, took time.Duration) {
	if m != nil {
		m.reviews.WithLabelValues(account, status).Inc()
		m.reviewDuration.WithLabelValues(account).Observe(took.Seconds())
	}
}

// FollowUp counts one handled mention.
func (m *Metrics) FollowUp(account, outcome string) {
	if m != nil {
		m.followups.WithLabelValues(account, outcome).Inc()
	}
}

// Thread counts one resolved or unresolved finding thread handled.
func (m *Metrics) Thread(account, outcome string) {
	if m != nil {
		m.threads.WithLabelValues(account, outcome).Inc()
	}
}

// Findings counts posted findings of one severity.
func (m *Metrics) Findings(account, severity, category string, n int) {
	if m != nil && n > 0 {
		m.findings.WithLabelValues(account, severity, category).Add(float64(n))
	}
}

// ConfidenceScored counts a published review's confidence score and risk.
func (m *Metrics) ConfidenceScored(account string, score int, risk string) {
	if m != nil {
		m.confidence.WithLabelValues(account, strconv.Itoa(score), risk).Inc()
	}
}

// ConfidenceHeld counts a judge's answer held to kritika's rule: what is
// risk, step or floor.
func (m *Metrics) ConfidenceHeld(account, what string) {
	if m != nil {
		m.confidenceHeld.WithLabelValues(account, what).Inc()
	}
}

// IndexRun counts a finished index run and the chunks it embedded.
func (m *Metrics) IndexRun(account, mode, status string, chunks int) {
	if m != nil {
		m.indexRuns.WithLabelValues(account, mode, status).Inc()
		if chunks > 0 {
			m.indexChunks.WithLabelValues(account).Add(float64(chunks))
		}
	}
}

// ContextChunks counts the chunks one stage gave a review's prompt.
func (m *Metrics) ContextChunks(account, stage string, n int) {
	if m != nil && n > 0 {
		m.contextChunks.WithLabelValues(account, stage).Add(float64(n))
	}
}

// RunnerRun counts a finished runner Job. took is zero when unknown.
func (m *Metrics) RunnerRun(account, kind, outcome string, took time.Duration) {
	if m != nil {
		m.runnerRuns.WithLabelValues(account, kind, outcome).Inc()
		if took > 0 {
			m.runnerDuration.WithLabelValues(kind).Observe(took.Seconds())
		}
	}
}

// LeaseWait records how long a lease took to acquire.
func (m *Metrics) LeaseWait(account, model string, took time.Duration) {
	if m != nil {
		m.leaseWait.WithLabelValues(account, model).Observe(took.Seconds())
	}
}

// ReviewSnoozed counts a review put back on the queue to wait for a model
// slot.
func (m *Metrics) ReviewSnoozed(account, model string) {
	if m != nil {
		m.reviewSnoozes.WithLabelValues(account, model).Inc()
	}
}

// JobRescued counts a job of a dead replica that the leader handed back to
// the queue. It counts by job kind and the state that the job was handed back
// in.
func (m *Metrics) JobRescued(kind, state string) {
	if m != nil {
		m.jobsRescued.WithLabelValues(kind, state).Inc()
	}
}

// ModelCall records one call: outcome is ok or error. Tokens and cost are
// added only for ok. cachedTokens is the part of inputTokens that the
// provider served from its prompt cache. unpriced says that nothing gave the
// call a cost, so costUSD is no measure of it.
func (m *Metrics) ModelCall(
	account, model, role, outcome string, inputTokens, cachedTokens, outputTokens int64, costUSD float64, unpriced bool,
) {
	if m == nil {
		return
	}
	m.modelCalls.WithLabelValues(account, model, role, outcome).Inc()
	if outcome != "ok" {
		return
	}
	if inputTokens > 0 {
		m.modelTokens.WithLabelValues(account, model, role, "input").Add(float64(inputTokens))
	}
	if cachedTokens > 0 {
		m.modelTokens.WithLabelValues(account, model, role, "cached").Add(float64(cachedTokens))
	}
	if outputTokens > 0 {
		m.modelTokens.WithLabelValues(account, model, role, "output").Add(float64(outputTokens))
	}
	if costUSD > 0 {
		m.modelCost.WithLabelValues(account, model, role).Add(costUSD)
	}
	if unpriced {
		m.modelUnpriced.WithLabelValues(account, model, role).Inc()
	}
}

// ForgeRateLimited counts a response that the forge refused for a rate limit.
// outcome is waited when the request waited the limit out and was sent again.
// outcome is refused when the wait was too long or the request cannot be sent
// again.
func (m *Metrics) ForgeRateLimited(connection, outcome string) {
	if m != nil {
		m.forgeLimits.WithLabelValues(connection, outcome).Inc()
	}
}

// Egress counts one gateway request.
func (m *Metrics) Egress(kind, outcome string) {
	if m == nil {
		return
	}
	m.egress.WithLabelValues(kind, outcome).Inc()
}

// Leading records whether this replica holds the leader lock.
func (m *Metrics) Leading(held bool) {
	if m == nil {
		return
	}
	v := 0.0
	if held {
		v = 1
	}
	m.leader.Set(v)
}

// TranscriptWrite counts one model call recorded, or not, for the
// transcript view.
func (m *Metrics) TranscriptWrite(kind, outcome string) {
	if m != nil {
		m.transcripts.WithLabelValues(kind, outcome).Inc()
	}
}

// QueueWait records time spent eligible for pickup. It leaves out intentional
// delays.
func (m *Metrics) QueueWait(queue string, took time.Duration) {
	if m != nil {
		m.queueWait.WithLabelValues(queue).Observe(took.Seconds())
	}
}

// InitVectorSearch exposes zero counters before requests arrive so a first
// error following a scrape is visible to rate/increase.
func (m *Metrics) InitVectorSearch(account string) {
	if m != nil {
		for _, outcome := range []string{"ok", outcomeError, outcomeCanceled} {
			m.vectorSearch.WithLabelValues(account, outcome)
		}
	}
}

// VectorSearch records the database phase. outcome is ok, error or canceled.
func (m *Metrics) VectorSearch(account, outcome string, took time.Duration) {
	if m != nil {
		m.vectorSearch.WithLabelValues(account, outcome).Observe(took.Seconds())
	}
}

// ReviewStage observes one named preparation or publication operation.
func (m *Metrics) ReviewStage(account, stage string, took time.Duration) {
	if m != nil {
		m.reviewStage.WithLabelValues(account, stage).Observe(max(0, took.Seconds()))
	}
}

// ReviewSlotWait observes an actual snooze interval. This includes an early
// wake.
func (m *Metrics) ReviewSlotWait(account, model string, took time.Duration) {
	if m != nil {
		m.reviewSlotWait.WithLabelValues(account, model).Observe(max(0, took.Seconds()))
	}
}
