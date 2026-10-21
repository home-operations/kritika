package metrics

import (
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/prometheus/client_golang/prometheus/testutil/promlint"

	"github.com/home-operations/kritika/internal/jobtimeout"
	dto "github.com/prometheus/client_model/go"
)

func TestMetricsRecordAndNilIsSafe(t *testing.T) {
	var none *Metrics
	none.Webhook("a", "b")
	none.Review("t", "completed", time.Second)
	none.ReviewStage("t", "merge", time.Second)
	none.ReviewSlotWait("t", "m", time.Second)
	none.ModelCall("t", "m", "review", "ok", 1, 0, 1, 0.1, false)
	none.TranscriptWrite("review", "ok")
	none.ConfidenceScored("t", 5, "low")
	none.ConfidenceHeld("t", "score")
	none.Leading(true)
	none.QueueWait("review", time.Second)
	none.InitVectorSearch("account")
	none.VectorSearch("account", "ok", time.Second)
	none.CacheUploadSkipped("queue_full")

	reg := prometheus.NewPedanticRegistry()
	m := New(reg)
	m.Webhook("onedr0p-github", "enqueued")
	m.ReviewStage("onedr0p", "merge", time.Second)
	m.ReviewSlotWait("onedr0p", "model", time.Second)
	m.Poll("onedr0p-github", "ok", 1)
	m.ForgeRateLimited("onedr0p-github", "waited")
	m.Egress("http", "allowed")
	m.CacheRequest("bypass", "retry")
	m.CacheResponse("local", "ok", 3)
	m.CacheOperation("origin_fetch", "ok", time.Second, 3)
	m.CacheFill(1)
	m.CacheFill(-1)
	m.CacheLocal(3, 1024, 1)
	m.CacheTransfer("s3_upload", 3)
	m.CacheUploadSkipped("queue_full")
	if got := testutil.ToFloat64(m.cache.uploadSkips.WithLabelValues("queue_full")); got != 1 {
		t.Fatalf("skipped uploads = %v", got)
	}
	m.FollowUp("onedr0p", "answered")
	m.Thread("onedr0p", "addressed")
	m.ContextChunks("onedr0p", "similar", 1)
	m.QueueWait("review", time.Second)
	m.VectorSearch("onedr0p", "ok", time.Second)
	m.ReviewSnoozed("onedr0p", "openai/gpt-6-sol")
	m.JobRescued("review", "running")
	m.Review("onedr0p", "completed", 12*time.Second)
	m.Findings("onedr0p", "p1", "correctness", 2)
	m.IndexRun("onedr0p", "full", "completed", 565)
	m.RunnerRun("onedr0p", "index", "success", 4*time.Second)
	m.LeaseWait("onedr0p", "openai/gpt-6-sol", 5*time.Millisecond)
	m.ModelCall("onedr0p", "openai/gpt-6-sol", "review", "ok", 1706, 1574, 83, 0.005029, false)
	m.ModelCall("onedr0p", "openai/gpt-6-sol", "review", "error", 0, 0, 0, 0, false)

	want := `# HELP kritika_model_tokens_total Tokens spent, by role and direction (input, cached, output); cached is the part of input the provider served from its prompt cache.
# TYPE kritika_model_tokens_total counter
kritika_model_tokens_total{account="onedr0p",direction="cached",model="openai/gpt-6-sol",role="review"} 1574
kritika_model_tokens_total{account="onedr0p",direction="input",model="openai/gpt-6-sol",role="review"} 1706
kritika_model_tokens_total{account="onedr0p",direction="output",model="openai/gpt-6-sol",role="review"} 83
`
	if err := testutil.GatherAndCompare(reg, strings.NewReader(want), "kritika_model_tokens_total"); err != nil {
		t.Fatal(err)
	}
	m.ModelCall("onedr0p", "claude-sonnet-5-5", "confidence", "ok", 6000, 0, 200, 0, true)
	m.ModelCall("onedr0p", "claude-sonnet-5-5", "confidence", "error", 0, 0, 0, 0, true)
	if v := testutil.ToFloat64(m.modelUnpriced.WithLabelValues("onedr0p", "claude-sonnet-5-5", "confidence")); v != 1 {
		t.Fatalf("unpriced calls = %v, want only the one that answered", v)
	}
	if n := testutil.CollectAndCount(m.modelUnpriced); n != 1 {
		t.Fatalf("unpriced call series = %d", n)
	}
	if n := testutil.CollectAndCount(m.modelCalls); n != 4 {
		t.Fatalf("model call series = %d", n)
	}
	if v := testutil.ToFloat64(m.indexChunks.WithLabelValues("onedr0p")); v != 565 {
		t.Fatalf("index chunks = %v", v)
	}
	if v := testutil.ToFloat64(m.modelCost.WithLabelValues("onedr0p", "openai/gpt-6-sol", "review")); v != 0.005029 {
		t.Fatalf("cost = %v", v)
	}
	m.TranscriptWrite("agent_step", "error")
	if v := testutil.ToFloat64(m.transcripts.WithLabelValues("agent_step", "error")); v != 1 {
		t.Fatalf("transcript writes = %v", v)
	}
	m.ConfidenceScored("onedr0p", 4, "high")
	m.ConfidenceScored("onedr0p", 4, "high")
	if v := testutil.ToFloat64(m.confidence.WithLabelValues("onedr0p", "4", "high")); v != 2 {
		t.Fatalf("confidence scores = %v", v)
	}
	m.ConfidenceHeld("onedr0p", "score")
	m.ConfidenceHeld("onedr0p", "score")
	m.ConfidenceHeld("onedr0p", "risk")
	if s, r := testutil.ToFloat64(m.confidenceHeld.WithLabelValues("onedr0p", "score")), testutil.ToFloat64(m.confidenceHeld.WithLabelValues("onedr0p", "risk")); s != 2 || r != 1 {
		t.Fatalf("confidence held = %v score, %v risk", s, r)
	}
	if v := testutil.ToFloat64(m.leader); v != 0 {
		t.Fatalf("leader before leading = %v", v)
	}
	m.Leading(true)
	if v := testutil.ToFloat64(m.leader); v != 1 {
		t.Fatalf("leader while leading = %v", v)
	}
	m.Leading(false)
	if v := testutil.ToFloat64(m.leader); v != 0 {
		t.Fatalf("leader after stepping down = %v", v)
	}
	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	if len(families) != 40 {
		t.Fatalf("metric families = %d, want all 40", len(families))
	}
	if problems, err := promlint.NewWithMetricFamilies(families).Lint(); err != nil || len(problems) != 0 {
		t.Fatalf("metric lint = %v, %v", problems, err)
	}
}

// Startup initialization must expose zero counts without recording observations,
// and must not reset a count when called again.
func TestInitVectorSearch(t *testing.T) {
	reg := prometheus.NewPedanticRegistry()
	m := New(reg)
	m.InitVectorSearch("account")
	if n, err := testutil.GatherAndCount(reg, "kritika_vector_search_seconds"); err != nil || n != 3 {
		t.Fatalf("outcome series = %d, %v; want 3", n, err)
	}
	for _, outcome := range []string{"ok", "error", "canceled"} {
		t.Run(outcome, func(t *testing.T) {
			histogram := m.vectorSearch.WithLabelValues("account", outcome).(prometheus.Metric)
			for _, count := range []uint64{0, 1} {
				var metric dto.Metric
				if err := histogram.Write(&metric); err != nil {
					t.Fatal(err)
				}
				if got := metric.GetHistogram().GetSampleCount(); got != count {
					t.Fatalf("sample count = %d, want %d", got, count)
				}
				if count == 0 {
					m.VectorSearch("account", outcome, time.Second)
					m.InitVectorSearch("account")
				}
			}
		})
	}
}

// TestDurationBucketsReachTheirLongest: the longest a review job, a runner
// and an index job's wait for a model slot may take lands in a bucket, not
// only in +Inf.
func TestDurationBucketsReachTheirLongest(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := New(reg)
	m.Review("acme", "completed", jobtimeout.MaxJobTimeout)
	m.RunnerRun("acme", "review", "ok", jobtimeout.MaxJobTimeout)
	m.LeaseWait("acme", "model", jobtimeout.IndexWriteHeadroom)
	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	seen := 0
	for _, f := range families {
		switch f.GetName() {
		case "kritika_review_duration_seconds", "kritika_runner_duration_seconds", "kritika_lease_wait_seconds":
		default:
			continue
		}
		seen++
		h := f.GetMetric()[0].GetHistogram()
		buckets := h.GetBucket()
		if last := buckets[len(buckets)-1]; last.GetCumulativeCount() != h.GetSampleCount() {
			t.Errorf("%s: the longest observation is past its last bucket, %g", f.GetName(), last.GetUpperBound())
		}
	}
	if seen != 3 {
		t.Fatalf("found %d of the 3 histograms", seen)
	}
}
