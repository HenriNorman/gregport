package middleware

import (
	"net/http"

	"github.com/google/uuid"

	"github.com/HenriNorman/gregport/internal/platform/requestctx"
)

// RequestID reads an inbound X-Request-ID when it is sane, generates one
// otherwise, echoes it to the client and stores it in the context.
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-ID")
		if !validRequestID(id) {
			id = uuid.NewString()
		}

		w.Header().Set("X-Request-ID", id)

		ctx := requestctx.WithRequestID(r.Context(), id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// validRequestID bounds and filters caller-supplied ids so a client cannot
// inject arbitrary text into logs.
func validRequestID(id string) bool {
	if id == "" || len(id) > 128 {
		return false
	}
	for _, c := range id {
		switch {
		case c >= 'a' && c <= 'z':
		case c >= 'A' && c <= 'Z':
		case c >= '0' && c <= '9':
		case c == '-' || c == '_' || c == '.':
		default:
			return false
		}
	}
	return true
}
