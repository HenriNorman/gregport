package auth

import (
	"context"
	"github.com/HenriNorman/gregport/internal/platform/httpx"
	"github.com/HenriNorman/gregport/internal/platform/requestctx"
	"net/http"
	"strings"
)

func Authenticate(
	tm *TokenManager,
	apiKeyAuth func(ctx context.Context, raw string) (requestctx.Principal, error),
) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			header := r.Header.Get("Authorization")
			if header == "" {
				httpx.WriteError(w, r, unauthorized())
				return
			}

			parts := strings.SplitN(header, " ", 2)
			if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || parts[1] == "" {
				httpx.WriteError(w, r, unauthorized())
				return
			}
			token := parts[1]

			var p requestctx.Principal

			if strings.HasPrefix(token, "gp_live_") {
				kp, err := apiKeyAuth(r.Context(), token)
				if err != nil {
					httpx.WriteError(w, r, unauthorized())
					return
				}
				p = kp
			} else {
				claims, err := tm.VerifyAccess(token)
				if err != nil {
					httpx.WriteError(w, r, unauthorized())
					return
				}
				p = requestctx.Principal{
					Kind:    requestctx.KindUser,
					ActorID: claims.UserID,
					OrgID:   claims.OrgID,
					Role:    claims.Role,
				}
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
				httpx.WriteError(w, r, unauthorized())
				return
			}
			if _, ok := allowed[p.Role]; !ok {
				httpx.WriteError(w, r, &httpx.APIError{
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
