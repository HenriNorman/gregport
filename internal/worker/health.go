package worker

import (
	"net/http"

	"github.com/HenriNorman/gregport/internal/platform/health"
)

// HealthHandler exposes the worker's liveness and readiness probes. Readiness
// checks both PostgreSQL and Redis; the worker cannot process jobs without
// either.
func HealthHandler(db, redis health.Pinger) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", health.Live)
	mux.HandleFunc("/readyz", health.Ready(db, redis))
	return mux
}
