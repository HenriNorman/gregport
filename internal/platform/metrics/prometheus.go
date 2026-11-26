package metrics

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Prometheus holds the process collectors and every application metric. HTTP
// labels are limited to method, chi route pattern and status: raw paths and
// user ids would create unbounded cardinality.
type Prometheus struct {
	Registry *prometheus.Registry

	HTTPRequests      *prometheus.CounterVec
	HTTPDuration      *prometheus.HistogramVec
	HTTPInFlight      prometheus.Gauge
	HTTPResponseBytes *prometheus.CounterVec

	CacheHits   prometheus.Counter
	CacheMisses prometheus.Counter
	CacheErrors prometheus.Counter

	RateLimitRejected prometheus.Counter
	RateLimitErrors   prometheus.Counter

	QueueDepth    *prometheus.GaugeVec
	QueueArchived *prometheus.GaugeVec

	JobsProcessed *prometheus.CounterVec
	WebhookResult *prometheus.CounterVec

	SSEConnections prometheus.Gauge
}

func NewPrometheus() *Prometheus {
	reg := prometheus.NewRegistry()
	reg.MustRegister(collectors.NewGoCollector())
	reg.MustRegister(collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))

	p := &Prometheus{Registry: reg}

	p.HTTPRequests = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "gregport_http_requests_total",
		Help: "HTTP requests by method, route pattern and status.",
	}, []string{"method", "route", "status"})
	p.HTTPDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "gregport_http_request_duration_seconds",
		Help:    "HTTP request latency by method and route pattern.",
		Buckets: []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10},
	}, []string{"method", "route"})
	p.HTTPInFlight = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "gregport_http_requests_in_flight",
		Help: "HTTP requests currently being served, including SSE streams.",
	})
	p.HTTPResponseBytes = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "gregport_http_response_bytes_total",
		Help: "Bytes written to HTTP clients by method and route pattern.",
	}, []string{"method", "route"})

	p.CacheHits = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "gregport_cache_hits_total",
		Help: "Read-through cache hits.",
	})
	p.CacheMisses = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "gregport_cache_misses_total",
		Help: "Read-through cache misses.",
	})
	p.CacheErrors = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "gregport_cache_errors_total",
		Help: "Read-through cache errors.",
	})

	p.RateLimitRejected = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "gregport_rate_limit_rejected_total",
		Help: "Requests rejected by a rate limiter.",
	})
	p.RateLimitErrors = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "gregport_rate_limit_errors_total",
		Help: "Rate limiter backend failures.",
	})

	p.QueueDepth = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "gregport_queue_depth",
		Help: "Pending or scheduled tasks per queue.",
	}, []string{"queue", "state"})
	p.QueueArchived = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "gregport_queue_archived",
		Help: "Dead-lettered tasks per queue.",
	}, []string{"queue"})

	p.JobsProcessed = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "gregport_jobs_processed_total",
		Help: "Queue jobs by task type and outcome.",
	}, []string{"type", "outcome"})
	p.WebhookResult = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "gregport_webhook_deliveries_total",
		Help: "Outgoing webhook delivery outcomes.",
	}, []string{"result"})

	p.SSEConnections = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "gregport_sse_connections",
		Help: "Active Server-Sent Events connections on this instance.",
	})

	reg.MustRegister(
		p.HTTPRequests, p.HTTPDuration, p.HTTPInFlight, p.HTTPResponseBytes,
		p.CacheHits, p.CacheMisses, p.CacheErrors,
		p.RateLimitRejected, p.RateLimitErrors,
		p.QueueDepth, p.QueueArchived,
		p.JobsProcessed, p.WebhookResult,
		p.SSEConnections,
	)
	return p
}

func (p *Prometheus) Handler() http.Handler {
	return promhttp.HandlerFor(p.Registry, promhttp.HandlerOpts{})
}
