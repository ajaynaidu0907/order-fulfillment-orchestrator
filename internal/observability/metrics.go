package observability

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Metrics holds the application's Prometheus instruments on a private registry.
type Metrics struct {
	registry *prometheus.Registry

	HTTPRequests     *prometheus.CounterVec   // by method, route, status
	HTTPDuration     *prometheus.HistogramVec // by method, route
	OrdersCreated    prometheus.Counter
	OrderTransitions *prometheus.CounterVec // by from, to
	JobsProcessed    *prometheus.CounterVec   // by type, result
	JobDuration      *prometheus.HistogramVec // by type
	JobsInflight     prometheus.Gauge
	Compensations    *prometheus.CounterVec // by action, result
}

// NewMetrics builds all instruments on a fresh registry (avoiding the global
// default registry) and registers Go runtime and process collectors.
func NewMetrics() *Metrics {
	reg := prometheus.NewRegistry()
	m := &Metrics{
		registry: reg,
		HTTPRequests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "ofo_http_requests_total",
			Help: "Total HTTP requests handled by the API.",
		}, []string{"method", "route", "status"}),
		HTTPDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "ofo_http_request_duration_seconds",
			Help:    "HTTP request duration in seconds.",
			Buckets: prometheus.DefBuckets,
		}, []string{"method", "route"}),
		OrdersCreated: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "ofo_orders_created_total",
			Help: "Total orders created.",
		}),
		OrderTransitions: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "ofo_order_transitions_total",
			Help: "Total order state transitions.",
		}, []string{"from", "to"}),
		JobsProcessed: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "ofo_jobs_processed_total",
			Help: "Total jobs processed by the worker.",
		}, []string{"type", "result"}),
		JobDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "ofo_job_duration_seconds",
			Help:    "Job processing duration in seconds.",
			Buckets: prometheus.DefBuckets,
		}, []string{"type"}),
		JobsInflight: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "ofo_jobs_inflight",
			Help: "Jobs currently being processed.",
		}),
		Compensations: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "ofo_compensations_total",
			Help: "Total compensation actions executed (saga rollbacks).",
		}, []string{"action", "result"}),
	}

	reg.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		m.HTTPRequests, m.HTTPDuration,
		m.OrdersCreated, m.OrderTransitions,
		m.JobsProcessed, m.JobDuration, m.JobsInflight,
		m.Compensations,
	)
	return m
}

// Handler returns the Prometheus scrape handler for this registry.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{})
}

// Registry exposes the underlying registry (e.g. for tests).
func (m *Metrics) Registry() *prometheus.Registry { return m.registry }
