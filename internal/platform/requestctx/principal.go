package requestctx

import (
	"context"

	"github.com/google/uuid"
)

type Kind string

const (
	KindUser   Kind = "user"
	KindAPIKey Kind = "api_key"
)

type Principal struct {
	Kind    Kind
	ActorID uuid.UUID
	OrgID   uuid.UUID
	Role    string
}

type ctxKey struct{}

func WithPrincipal(ctx context.Context, p Principal) context.Context {
	// Keep the early-installed RequestInfo holder in sync so the access log
	// can report the principal even though it was attached downstream.
	if info, ok := RequestInfoFromContext(ctx); ok {
		info.Set(p)
	}
	return context.WithValue(ctx, ctxKey{}, p)
}

func FromContext(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(ctxKey{}).(Principal)
	return p, ok
}
