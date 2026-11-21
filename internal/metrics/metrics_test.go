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
	none.InitAccount("account")
	none.InitSimilarRequests("account")
	none.VectorSearch("account", "ok", time.Second)
	none.LexicalSearch("account", "ok", time.Second)
	none.CacheUploadSkipped("queue_full")
	none.IndexSnoozed("t", "forge")
	none.SimilarRequest("account", "diff", "ok")
	none.SimilarBest("account", "diff", 0.8)
	none.SimilarHits("account", "kept", "vector", 3)

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
	m.LexicalSearch("onedr0p", "ok", time.Second)
	m.ReviewSnoozed("onedr0p", "openai/gpt-6-sol")
	m.JobRescued("review", "running")
	m.Review("onedr0p", "completed", 12*time.Second)
	m.Findings("onedr0p", "p1", "correctness", 2)
	m.IndexRun("onedr0p", "full", "completed", 565)
	m.IndexSnoozed("onedr0p", "forge")
	m.IndexSnoozed("onedr0p", "forge")
	if v := testutil.ToFloat64(m.indexSnoozes.WithLabelValues("onedr0p", "forge")); v != 2 {
		t.Fatalf("index snoozes = %v", v)
	}
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
	m.IndexVectors("onedr0p", 500, 65)
	m.IndexVectors("onedr0p", 0, 3)
	if r, e := testutil.ToFloat64(m.indexVectors.WithLabelValues("onedr0p", "reused")),
		testutil.ToFloat64(m.indexVectors.WithLabelValues("onedr0p", "embedded")); r != 500 || e != 68 {
		t.Fatalf("index vectors reused = %v, embedded = %v", r, e)
	}
	m.IndexTruncated("onedr0p")
	if v := testutil.ToFloat64(m.indexTruncated.WithLabelValues("onedr0p")); v != 1 {
		t.Fatalf("truncated index builds = %v", v)
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
	m.SimilarRequest("onedr0p", "search", "empty")
	m.SimilarRequest("onedr0p", "search", "empty")
	if v := testutil.ToFloat64(m.similarRequests.WithLabelValues("onedr0p", "search", "empty")); v != 2 {
		t.Fatalf("similar requests = %v", v)
	}
	m.SimilarBest("onedr0p", "diff", 0.83)
	m.SimilarHits("onedr0p", "margin", "lexical", 4)
	m.SimilarHits("onedr0p", "floor", "vector", 0)
	if v := testutil.ToFloat64(m.similarHits.WithLabelValues("onedr0p", "margin", "lexical")); v != 4 {
		t.Fatalf("similar hits = %v", v)
	}
	if n := testutil.CollectAndCount(m.similarHits); n != 1 {
		t.Fatalf("similar hit series = %d, want none for a count of 0", n)
	}
	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	if len(families) != 47 {
		t.Fatalf("metric families = %d, want all 47", len(families))
	}
	if problems, err := promlint.NewWithMetricFamilies(families).Lint(); err != nil || len(problems) != 0 {
		t.Fatalf("metric lint = %v, %v", problems, err)
	}
}

// Startup initialization must expose zero counts of the vector search and of
// the search by name without recording observations, and must not reset a
// count when called again.
func TestInitVectorSearch(t *testing.T) {
	reg := prometheus.NewPedanticRegistry()
	m := New(reg)
	m.InitVectorSearch("account")
	for _, family := range []struct {
		name   string
		vec    *prometheus.HistogramVec
		record func(account, outcome string, took time.Duration)
	}{
		{"kritika_vector_search_seconds", m.vectorSearch, m.VectorSearch},
		{"kritika_lexical_search_seconds", m.lexicalSearch, m.LexicalSearch},
	} {
		if n, err := testutil.GatherAndCount(reg, family.name); err != nil || n != 3 {
			t.Fatalf("%s outcome series = %d, %v; want 3", family.name, n, err)
		}
		for _, outcome := range []string{"ok", "error", "canceled"} {
			t.Run(family.name+"/"+outcome, func(t *testing.T) {
				histogram := family.vec.WithLabelValues("account", outcome).(prometheus.Metric)
				for _, count := range []uint64{0, 1} {
					var metric dto.Metric
					if err := histogram.Write(&metric); err != nil {
						t.Fatal(err)
					}
					if got := metric.GetHistogram().GetSampleCount(); got != count {
						t.Fatalf("sample count = %d, want %d", got, count)
					}
					if count == 0 {
						family.record("account", outcome, time.Second)
						m.InitVectorSearch("account")
					}
				}
			})
		}
	}
}

// TestDurationBucketsReachTheirLongest: a review job, a runner, and an index
// job's wait for a model slot each have a longest time that they can take.
// That time lands in a bucket, not only in +Inf.
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

// TestSimilarBestNegative: a query whose best match points away from it has
// a negative cosine similarity, which lands in a bucket at or below zero, so
// that a quantile of the histogram reads it as negative and not as a small
// positive similarity.
func TestSimilarBestNegative(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := New(reg)
	m.SimilarBest("account", "diff", -0.3)
	var metric dto.Metric
	if err := m.similarBest.WithLabelValues("account", "diff").(prometheus.Metric).Write(&metric); err != nil {
		t.Fatal(err)
	}
	for _, b := range metric.GetHistogram().GetBucket() {
		if b.GetCumulativeCount() == 1 {
			if b.GetUpperBound() > 0 {
				t.Fatalf("-0.3 first lands in the bucket up to %g, want one at or below 0", b.GetUpperBound())
			}
			return
		}
	}
	t.Fatal("-0.3 lands in no bucket")
}

// TestInitAccountIndexSnoozes: startup exposes an account's index snoozes of
// each reason at zero, beside its searches, so the first snooze after a
// scrape shows in rate/increase. Initializing again keeps a snooze recorded
// since.
func TestInitAccountIndexSnoozes(t *testing.T) {
	reg := prometheus.NewPedanticRegistry()
	m := New(reg)
	m.InitAccount("account")
	if n, err := testutil.GatherAndCount(reg, "kritika_index_snoozes_total"); err != nil || n != 2 {
		t.Fatalf("index snooze series = %d, %v; want 2", n, err)
	}
	if n, err := testutil.GatherAndCount(reg, "kritika_vector_search_seconds"); err != nil || n != 3 {
		t.Fatalf("vector search series = %d, %v; want 3", n, err)
	}
	for _, reason := range []string{"forge", "build"} {
		t.Run(reason, func(t *testing.T) {
			counter := m.indexSnoozes.WithLabelValues("account", reason)
			if got := testutil.ToFloat64(counter); got != 0 {
				t.Fatalf("initialized snoozes = %v, want 0", got)
			}
			m.IndexSnoozed("account", reason)
			m.InitAccount("account")
			if got := testutil.ToFloat64(counter); got != 1 {
				t.Fatalf("snoozes after one and initializing again = %v, want 1", got)
			}
		})
	}
}

// TestInitAccountSimilarRequests: startup exposes an account's similar-code
// requests of every kind and outcome that the gateway counts at zero, so the
// first error after a scrape shows in rate/increase. Initializing again
// keeps a request recorded since.
func TestInitAccountSimilarRequests(t *testing.T) {
	reg := prometheus.NewPedanticRegistry()
	m := New(reg)
	m.InitAccount("account")
	// diff and search of each of eight outcomes, and unknown of too_large
	// and invalid.
	if n, err := testutil.GatherAndCount(reg, "kritika_similar_requests_total"); err != nil || n != 18 {
		t.Fatalf("similar request series = %d, %v; want 18", n, err)
	}
	for _, tt := range []struct{ kind, outcome string }{
		{"diff", "error"}, {"search", "refused"}, {"unknown", "too_large"}, {"unknown", "invalid"},
	} {
		t.Run(tt.kind+"/"+tt.outcome, func(t *testing.T) {
			counter := m.similarRequests.WithLabelValues("account", tt.kind, tt.outcome)
			if got := testutil.ToFloat64(counter); got != 0 {
				t.Fatalf("initialized requests = %v, want 0", got)
			}
			m.SimilarRequest("account", tt.kind, tt.outcome)
			m.InitAccount("account")
			if got := testutil.ToFloat64(counter); got != 1 {
				t.Fatalf("requests after one and initializing again = %v, want 1", got)
			}
		})
	}
}
