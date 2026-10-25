package auth

import (
	"net/http"
	"strings"
	
	"github.com/HenriNorman/gregport/internal/platform/httpx"
	"github.com/HenriNorman/gregport/internal/platform/requestctx"
)

func Authenticate(tm *TokenManager) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			header := r.Header.Get("Authorization")
			if header == "" {
				httpx.WriteError(w, unauthorized())
				return
			}

			parts := strings.SplitN(header, " ", 2)
			if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || parts[1] == "" {
				httpx.WriteError(w, unauthorized())
				return
			}

			claims, err := tm.VerifyAccess(parts[1])
			if err != nil {
				httpx.WriteError(w, unauthorized())
				return
			}

			p := requestctx.Principal{
				UserID: claims.UserID,
				OrgID:  claims.OrgID,
				Role:   claims.Role,
			}
			ctx := requestctx.WithPrincipal(r.Context(), p)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func RequireRole(roles ...string) func(http.Handler) http.Handler {
	allowed := make(map[string]struct{}, len(roles))
	for _, r := range roles {
		allowed[r] = struct{}{}
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p, ok := requestctx.FromContext(r.Context())
			if !ok {
				httpx.WriteError(w, unauthorized())
				return
			}
			if _, ok := allowed[p.Role]; !ok {
				httpx.WriteError(w, &httpx.APIError{
					Status:  http.StatusForbidden,
					Code:    "forbidden",
					Message: "insufficient permissions",
				})
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func unauthorized() *httpx.APIError {
	return &httpx.APIError{
		Status:  http.StatusUnauthorized,
		Code:    "unauthorized",
		Message: "authentication required",
	}
}
