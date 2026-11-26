package middleware

import (
	"net/http"
	"strconv"
	"strings"
	"time"
)

// SecurityHeaders sets the browser-enforced protections that apply to every
// response. HSTS is only sent over TLS; sending it on plain HTTP would be
// ignored at best and misleading at worst.
func SecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Permissions-Policy", "geolocation=(), microphone=(), camera=()")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Cross-Origin-Resource-Policy", "same-origin")
		h.Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'; base-uri 'none'")
		if r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
			h.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}
		next.ServeHTTP(w, r)
	})
}

// CORSConfig is an explicit allow-list. An empty list disables cross-origin
// access entirely, which is the safe default for an API used by a same-origin
// web client.
type CORSConfig struct {
	AllowedOrigins []string
	AllowedMethods []string
	AllowedHeaders []string
	MaxAge         time.Duration
}

func CORS(cfg CORSConfig) func(http.Handler) http.Handler {
	allowed := make(map[string]struct{}, len(cfg.AllowedOrigins))
	allowAny := false
	for _, origin := range cfg.AllowedOrigins {
		origin = strings.TrimSpace(origin)
		if origin == "*" {
			allowAny = true
			continue
		}
		if origin != "" {
			allowed[origin] = struct{}{}
		}
	}

	methods := cfg.AllowedMethods
	if len(methods) == 0 {
		methods = []string{"GET", "POST", "PATCH", "PUT", "DELETE", "OPTIONS"}
	}
	headers := cfg.AllowedHeaders
	if len(headers) == 0 {
		headers = []string{"Authorization", "Content-Type", "Idempotency-Key", "If-Match", "Last-Event-ID"}
	}
	maxAge := cfg.MaxAge
	if maxAge <= 0 {
		maxAge = 10 * time.Minute
	}

	permitted := func(origin string) bool {
		if origin == "" {
			return false
		}
		if allowAny {
			return true
		}
		_, ok := allowed[origin]
		return ok
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			if origin != "" && permitted(origin) {
				h := w.Header()
				h.Set("Access-Control-Allow-Origin", origin)
				h.Add("Vary", "Origin")
				h.Set("Access-Control-Allow-Methods", strings.Join(methods, ", "))
				h.Set("Access-Control-Allow-Headers", strings.Join(headers, ", "))
				h.Set("Access-Control-Max-Age", strconv.Itoa(int(maxAge.Seconds())))
			}

			if r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
				if origin == "" || !permitted(origin) {
					w.WriteHeader(http.StatusForbidden)
					return
				}
				w.WriteHeader(http.StatusNoContent)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
