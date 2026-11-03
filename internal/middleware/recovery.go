package middleware

import (
	"fmt"
	"net/http"
	"runtime/debug"

	"github.com/HenriNorman/gregport/internal/platform/httpx"
	"github.com/HenriNorman/gregport/internal/platform/logging"
)

// Recovery turns panics into the standard error envelope and logs the panic
// with its stack. It must run inside RequestLogger so the 500 status and
// response size are recorded.
func Recovery(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			rvr := recover()
			if rvr == nil {
				return
			}

			if rvr == http.ErrAbortHandler {
				panic(rvr)
			}

			logging.FromContext(r.Context()).Error("panic recovered",
				"panic", fmt.Sprint(rvr),
				"stack", string(debug.Stack()),
				"method", r.Method,
				"route", routePattern(r.Context()),
			)

			httpx.WriteError(w, httpx.InternalError(nil))
		}()

		next.ServeHTTP(w, r)
	})
}
