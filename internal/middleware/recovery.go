package middleware

import (
	"log"
	"net/http"
	"runtime/debug"

	"github.com/HenriNorman/gregport/internal/platform/httpx"
)

func Recovery(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rvr := recover(); rvr != nil {
				if rvr == http.ErrAbortHandler {
					panic(rvr)
				}

				reqID := GetRequestID(r.Context())
				log.Printf("PANIC [request_id=%s]: %v\n%s", reqID, rvr, debug.Stack())

				httpx.WriteError(w, httpx.InternalError(nil))
			}
		}()

		next.ServeHTTP(w, r)
	})
}
