package health

import (
	"context"
	"net/http"
	"time"

	"github.com/HenriNorman/gregport/internal/platform/httpx"
	"github.com/HenriNorman/gregport/internal/platform/logging"
)

const readyTimeout = 2 * time.Second

type Pinger interface {
	Ping(ctx context.Context) error
}

// StatusResponse is the JSON body returned by health probes.
type StatusResponse struct {
	Status string `json:"status" example:"ok"`
}

// Live reports that the process can serve HTTP. It does not touch the database.
//
//	@Summary		Liveness probe
//	@Description	Returns 200 while the process can serve HTTP. It does not touch PostgreSQL.
//	@Tags			health
//	@Produce		json
//	@Success		200	{object}	StatusResponse
//	@Router			/healthz [get]
func Live(w http.ResponseWriter, _ *http.Request) {
	writeStatus(w, http.StatusOK, "ok")
}

// Ready reports whether request-serving dependencies are reachable.
//
//	@Summary		Readiness probe
//	@Description	Pings PostgreSQL with a two-second deadline. Returns 503 when the database is unreachable.
//	@Tags			health
//	@Produce		json
//	@Success		200	{object}	StatusResponse
//	@Failure		503	{object}	StatusResponse	"Database is unreachable"
//	@Router			/readyz [get]
func Ready(db Pinger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), readyTimeout)
		defer cancel()

		if err := db.Ping(ctx); err != nil {
			logging.FromContext(r.Context()).Warn("readiness check failed", "error", err)
			writeStatus(w, http.StatusServiceUnavailable, "unready")
			return
		}

		writeStatus(w, http.StatusOK, "ok")
	}
}

func writeStatus(w http.ResponseWriter, status int, value string) {
	_ = httpx.WriteJSON(w, status, StatusResponse{Status: value})
}
