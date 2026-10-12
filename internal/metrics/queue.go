package metrics

import (
	"context"
	"log/slog"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// QueueStats is a queue snapshot supplied by the application's storage layer.
type QueueStats struct {
	Queue         string
	Runnable      int64
	OldestSeconds float64
}

// QueueCollector reads the shared queue on each scrape. Across replicas,
// aggregate its gauges with max, never sum. Failed reads omit queue values
// and export stats_up=0 instead of reporting an empty or stale queue.
type QueueCollector struct {
	ctx   context.Context
	stats func(context.Context) ([]QueueStats, error)
	log   *slog.Logger
}

// NewQueueCollector bounds each database read independently of the scrape.
func NewQueueCollector(ctx context.Context, stats func(context.Context) ([]QueueStats, error), logger *slog.Logger) *QueueCollector {
	return &QueueCollector{ctx: ctx, stats: stats, log: logger}
}

var (
	queueRunnable = prometheus.NewDesc("kritika_queue_runnable_jobs",
		"Due available, retryable and scheduled jobs in the shared database. Aggregate replicas with max, not sum.", []string{lblQueue}, nil)
	queueOldest = prometheus.NewDesc("kritika_queue_oldest_runnable_seconds",
		"Seconds since the oldest runnable job was due, or zero when empty. Aggregate replicas with max, not sum.", []string{lblQueue}, nil)
	queueStatsUp = prometheus.NewDesc("kritika_queue_stats_up",
		"1 when the shared queue snapshot was read successfully, else 0.", nil, nil)
)

// Describe implements prometheus.Collector.
func (c *QueueCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- queueRunnable
	ch <- queueOldest
	ch <- queueStatsUp
}

// Collect implements prometheus.Collector.
func (c *QueueCollector) Collect(ch chan<- prometheus.Metric) {
	ctx, cancel := context.WithTimeout(c.ctx, 2*time.Second)
	defer cancel()
	stats, err := c.stats(ctx)
	if err != nil {
		c.log.Warn("queue metrics unavailable", "error", err)
		ch <- prometheus.MustNewConstMetric(queueStatsUp, prometheus.GaugeValue, 0)
		return
	}
	ch <- prometheus.MustNewConstMetric(queueStatsUp, prometheus.GaugeValue, 1)
	for _, s := range stats {
		ch <- prometheus.MustNewConstMetric(queueRunnable, prometheus.GaugeValue, float64(s.Runnable), s.Queue)
		ch <- prometheus.MustNewConstMetric(queueOldest, prometheus.GaugeValue, s.OldestSeconds, s.Queue)
	}
}
