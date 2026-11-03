package middleware

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/HenriNorman/gregport/internal/platform/httpx"
)

// Timeout cancels the request context at the deadline and returns the
// standard 503 envelope when the handler finishes without writing a response.
// Server WriteTimeout is the hard stop for handlers that ignore cancellation.
func Timeout(d time.Duration) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if d <= 0 {
			return next
		}

		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx, cancel := context.WithTimeout(r.Context(), d)
			defer cancel()

			tw := &writeTracker{ResponseWriter: w}
			next.ServeHTTP(tw, r.WithContext(ctx))

			if errors.Is(ctx.Err(), context.DeadlineExceeded) && !tw.wrote {
				httpx.WriteError(w, r, httpx.ServiceUnavailable("request_timeout", "request timed out"))
			}
		})
	}
}

type writeTracker struct {
	http.ResponseWriter
	wrote bool
}

func (tw *writeTracker) WriteHeader(code int) {
	tw.wrote = true
	tw.ResponseWriter.WriteHeader(code)
}

func (tw *writeTracker) Write(p []byte) (int, error) {
	tw.wrote = true
	return tw.ResponseWriter.Write(p)
}

func (tw *writeTracker) Unwrap() http.ResponseWriter {
	return tw.ResponseWriter
}
