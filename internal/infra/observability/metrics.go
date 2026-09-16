package observability

import (
	"net/http"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Metrics holds Prometheus instruments required by the challenge.
type Metrics struct {
	TransactionsTotal         *prometheus.CounterVec
	IdempotentReplays         prometheus.Counter
	Retries                   prometheus.Counter
	DLQMessages               prometheus.Counter
	ConcurrencyConflicts      prometheus.Counter
	OutboxLagSeconds          prometheus.Gauge
	ProcessingDurationSeconds *prometheus.HistogramVec
	ReconciliationDivergences prometheus.Counter
	ReconciliationChecksTotal *prometheus.CounterVec
}

var (
	metricsOnce sync.Once
	metricsInst *Metrics
)

// NewMetrics returns process-wide Prometheus instruments (safe across multiple Fx apps in tests).
func NewMetrics() *Metrics {
	metricsOnce.Do(func() {
		metricsInst = &Metrics{
			TransactionsTotal: mustCounterVec(prometheus.NewCounterVec(prometheus.CounterOpts{
				Name: "wallet_transactions_total",
				Help: "Wagering transactions by status and kind",
			}, []string{"status", "kind", "channel"})),
			IdempotentReplays: mustCounter(prometheus.NewCounter(prometheus.CounterOpts{
				Name: "wallet_idempotent_replays_total",
				Help: "Idempotent replays of an existing payload hash",
			})),
			Retries: mustCounter(prometheus.NewCounter(prometheus.CounterOpts{
				Name: "wallet_retries_total",
				Help: "Transient retries (SQS visibility, reference backoff, outbox republish)",
			})),
			DLQMessages: mustCounter(prometheus.NewCounter(prometheus.CounterOpts{
				Name: "wallet_dlq_messages_total",
				Help: "Messages sent toward DLQ (poison / maxReceiveCount path)",
			})),
			ConcurrencyConflicts: mustCounter(prometheus.NewCounter(prometheus.CounterOpts{
				Name: "wallet_concurrency_conflicts_total",
				Help: "Optimistic concurrency / payload conflicts",
			})),
			OutboxLagSeconds: mustGauge(prometheus.NewGauge(prometheus.GaugeOpts{
				Name: "wallet_outbox_lag_seconds",
				Help: "Age in seconds of the oldest unpublished outbox event",
			})),
			ProcessingDurationSeconds: mustHistogramVec(prometheus.NewHistogramVec(prometheus.HistogramOpts{
				Name:    "wallet_processing_duration_seconds",
				Help:    "End-to-end processing latency",
				Buckets: prometheus.DefBuckets,
			}, []string{"channel", "kind"})),
			ReconciliationDivergences: mustCounter(prometheus.NewCounter(prometheus.CounterOpts{
				Name: "wallet_reconciliation_divergences_total",
				Help: "Reconciliation runs where stored balance differs from ledger reconstruction",
			})),
			ReconciliationChecksTotal: mustCounterVec(prometheus.NewCounterVec(prometheus.CounterOpts{
				Name: "wallet_reconciliation_checks_total",
				Help: "Reconciliation checks by consistency result",
			}, []string{"consistent"})),
		}
	})
	return metricsInst
}

func mustCounter(c prometheus.Counter) prometheus.Counter {
	if err := prometheus.Register(c); err != nil {
		if are, ok := err.(prometheus.AlreadyRegisteredError); ok {
			return are.ExistingCollector.(prometheus.Counter)
		}
		panic(err)
	}
	return c
}

func mustCounterVec(c *prometheus.CounterVec) *prometheus.CounterVec {
	if err := prometheus.Register(c); err != nil {
		if are, ok := err.(prometheus.AlreadyRegisteredError); ok {
			return are.ExistingCollector.(*prometheus.CounterVec)
		}
		panic(err)
	}
	return c
}

func mustGauge(g prometheus.Gauge) prometheus.Gauge {
	if err := prometheus.Register(g); err != nil {
		if are, ok := err.(prometheus.AlreadyRegisteredError); ok {
			return are.ExistingCollector.(prometheus.Gauge)
		}
		panic(err)
	}
	return g
}

func mustHistogramVec(h *prometheus.HistogramVec) *prometheus.HistogramVec {
	if err := prometheus.Register(h); err != nil {
		if are, ok := err.(prometheus.AlreadyRegisteredError); ok {
			return are.ExistingCollector.(*prometheus.HistogramVec)
		}
		panic(err)
	}
	return h
}

// ObserveProcessing records duration for a channel/kind pair.
func (m *Metrics) ObserveProcessing(channel, kind string, d time.Duration) {
	if m == nil {
		return
	}
	m.ProcessingDurationSeconds.WithLabelValues(channel, kind).Observe(d.Seconds())
}

// MetricsHandler exposes the default Prometheus registry.
func MetricsHandler() http.Handler {
	return promhttp.Handler()
}
