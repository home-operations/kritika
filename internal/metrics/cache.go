package metrics

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

const (
	outcomeCanceled = "canceled"
	outcomeError    = "error"
)

type cacheMetrics struct {
	requests, bypasses, transfers, responses *prometheus.CounterVec
	uploadSkips                              *prometheus.CounterVec
	operations                               *prometheus.HistogramVec
	active, localBytes, localLimit           prometheus.Gauge
	evictions                                prometheus.Counter
}

func newCacheMetrics(reg prometheus.Registerer) cacheMetrics {
	c := cacheMetrics{
		uploadSkips: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "kritika_cache_upload_skips_total", Help: "S3 publications skipped before an upload, by bounded reason.",
		}, []string{"reason"}),
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "kritika_cache_requests_total", Help: "Cache decisions for admitted HTTP requests, by outcome (local_hit, s3_hit, miss, bypass).",
		}, []string{lblOutcome}),
		bypasses: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "kritika_cache_bypasses_total", Help: "Requests forwarded without the cache, by bounded reason.",
		}, []string{"reason"}),
		transfers: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "kritika_cache_transfer_bytes_total", Help: "Actual body bytes read or written by cache operations; excludes uncached forwarding.",
		}, []string{"operation"}),
		responses: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "kritika_cache_responses_total", Help: "Cache responses after serving, by source (local, s3) and result (ok, error, canceled).",
		}, []string{"source", lblOutcome}),
		operations: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "kritika_cache_operation_seconds", Help: "Cache operation wall time, including failed operations and canceled reads.",
			Buckets: []float64{0.005, 0.025, 0.1, 0.5, 1, 5, 15, 30, 60, 120, 300, 600},
		}, []string{"operation", lblOutcome}),
		active: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "kritika_cache_active_fills", Help: "Origin fills currently in progress on this replica, excluding background S3 uploads.",
		}),
		localBytes: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "kritika_cache_local_bytes",
			Help: "Retained local entry bytes at the last directory scan; excludes fills and unlinked open files.",
		}),
		localLimit: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "kritika_cache_local_limit_bytes", Help: "Local retained-entry budget on this replica; not a hard disk usage limit.",
		}),
		evictions: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "kritika_cache_evictions_total", Help: "Local entries removed to meet the retained-entry budget.",
		}),
	}
	reg.MustRegister(c.requests, c.bypasses, c.transfers, c.responses, c.operations,
		c.active, c.localBytes, c.localLimit, c.evictions, c.uploadSkips)
	for _, reason := range []string{"queue_full", "circuit_open", "entry_unavailable"} {
		c.uploadSkips.WithLabelValues(reason)
	}
	for _, outcome := range []string{"local_hit", "s3_hit", "miss", "bypass"} {
		c.requests.WithLabelValues(outcome)
	}
	for _, op := range []string{"origin_fetch", "s3_lookup", "s3_read", "s3_upload"} {
		for _, outcome := range []string{"ok", outcomeError, outcomeCanceled} {
			c.operations.WithLabelValues(op, outcome)
		}
		c.transfers.WithLabelValues(op)
	}
	for _, reason := range []string{"ineligible", "retry", "fill_error", "entry_unavailable",
		outcomeCanceled, "fill_busy", "fill_capacity", "too_large", "private", "encoded", "unknown_length"} {
		c.bypasses.WithLabelValues(reason)
	}
	for _, source := range []string{"local", "s3"} {
		for _, outcome := range []string{"ok", outcomeError, outcomeCanceled} {
			c.responses.WithLabelValues(source, outcome)
		}
		c.transfers.WithLabelValues("response_" + source)
	}
	c.operations.WithLabelValues("s3_lookup", "miss")
	c.operations.WithLabelValues("origin_fetch", "skipped")
	return c
}

// CacheUploadSkipped counts dropped publications without diluting S3 operation error rates.
func (m *Metrics) CacheUploadSkipped(reason string) {
	if m != nil {
		m.cache.uploadSkips.WithLabelValues(reason).Inc()
	}
}

// CacheRequest records exactly one routing decision, with a reason only for bypasses.
func (m *Metrics) CacheRequest(outcome, reason string) {
	if m != nil {
		m.cache.requests.WithLabelValues(outcome).Inc()
		if outcome == "bypass" {
			m.cache.bypasses.WithLabelValues(reason).Inc()
		}
	}
}

// CacheOperation records a completed operation, including partial transferred bytes.
func (m *Metrics) CacheOperation(operation, outcome string, took time.Duration, bytes int64) {
	if m != nil {
		m.cache.operations.WithLabelValues(operation, outcome).Observe(took.Seconds())
		m.cache.transfers.WithLabelValues(operation).Add(float64(bytes))
	}
}

// CacheResponse records bytes accepted by the response writer, not the entry's full size.
func (m *Metrics) CacheResponse(source, outcome string, bytes int64) {
	if m != nil {
		m.cache.responses.WithLabelValues(source, outcome).Inc()
		m.cache.transfers.WithLabelValues("response_" + source).Add(float64(bytes))
	}
}

// CacheFill records a fill starting (+1) or finishing (-1).
func (m *Metrics) CacheFill(delta float64) {
	if m != nil {
		m.cache.active.Add(delta)
	}
}

// CacheLocal records a scan of retained files and the entries evicted by it.
func (m *Metrics) CacheLocal(bytes, limit int64, evictions int) {
	if m != nil {
		m.cache.localBytes.Set(float64(bytes))
		m.cache.localLimit.Set(float64(limit))
		m.cache.evictions.Add(float64(evictions))
	}
}

// CacheTransfer counts bytes independently of an operation's final outcome.
func (m *Metrics) CacheTransfer(operation string, bytes int64) {
	if m != nil {
		m.cache.transfers.WithLabelValues(operation).Add(float64(bytes))
	}
}
