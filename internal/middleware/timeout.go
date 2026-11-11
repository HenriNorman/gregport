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

// TimeoutExcept applies Timeout to every request except the exact paths
// listed. Long-lived handlers such as Server-Sent Events must outlive the
// normal request deadline.
func TimeoutExcept(d time.Duration, paths ...string) func(http.Handler) http.Handler {
	if len(paths) == 0 {
		return Timeout(d)
	}

	skip := make(map[string]struct{}, len(paths))
	for _, p := range paths {
		skip[p] = struct{}{}
	}

	withTimeout := Timeout(d)
	return func(next http.Handler) http.Handler {
		timed := withTimeout(next)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if _, ok := skip[r.URL.Path]; ok {
				next.ServeHTTP(w, r)
				return
			}
			timed.ServeHTTP(w, r)
		})
	}
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
