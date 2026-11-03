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

// Live reports that the process can serve HTTP. It does not touch the database.
func Live(w http.ResponseWriter, _ *http.Request) {
	writeStatus(w, http.StatusOK, "ok")
}

// Ready reports whether request-serving dependencies are reachable.
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
	_ = httpx.WriteJSON(w, status, map[string]string{"status": value})
}
