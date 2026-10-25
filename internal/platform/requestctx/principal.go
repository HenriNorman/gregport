package requestctx

import (
	"context"

	"github.com/google/uuid"
)

type Principal struct {
	UserID uuid.UUID
	OrgID  uuid.UUID
	Role   string
}

type ctxKey struct{}

func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, ctxKey{}, p)
}

func FromContext(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(ctxKey{}).(Principal)
	return p, ok
}
