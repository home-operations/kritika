package metrics

import (
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestMetricsRecordAndNilIsSafe(t *testing.T) {
	var none *Metrics
	none.Webhook("a", "b")
	none.Review("t", "completed", time.Second)
	none.ModelCall("t", "m", "review", "ok", 1, 0, 1, 0.1, false)
	none.TranscriptWrite("review", "ok")
	none.ConfidenceScored("t", 5, "low")
	none.Leading(true)

	reg := prometheus.NewRegistry()
	m := New(reg)
	m.Webhook("onedr0p-github", "enqueued")
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
}
