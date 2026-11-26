package middleware

import (
	"net/http"
	"strconv"
	"time"

	"github.com/HenriNorman/gregport/internal/platform/httpx"
	"github.com/HenriNorman/gregport/internal/platform/metrics"
)

// HTTPMetrics records request rate, latency and in-flight count. The route
// label is the chi pattern, never the raw path, so cardinality stays bounded.
func HTTPMetrics(p *metrics.Prometheus) func(http.Handler) http.Handler {
	if p == nil {
		return func(next http.Handler) http.Handler { return next }
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p.HTTPInFlight.Inc()
			defer p.HTTPInFlight.Dec()

			start := time.Now()
			rec := &responseRecorder{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(rec, r)

			route := httpx.RoutePattern(r.Context())
			status := strconv.Itoa(rec.status)
			p.HTTPRequests.WithLabelValues(r.Method, route, status).Inc()
			p.HTTPDuration.WithLabelValues(r.Method, route).Observe(time.Since(start).Seconds())
			p.HTTPResponseBytes.WithLabelValues(r.Method, route).Add(float64(rec.bytes))
		})
	}
}

// BodyLimit rejects oversized request bodies before a handler decodes them.
func BodyLimit(maxBytes int64) func(http.Handler) http.Handler {
	if maxBytes <= 0 {
		return func(next http.Handler) http.Handler { return next }
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Body != nil {
				r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
			}
			next.ServeHTTP(w, r)
		})
	}
}
