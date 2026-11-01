package requestctx

import (
	"context"

	"github.com/google/uuid"
)

type Kind string
const (
	KindUser Kind	 = "user"
	KindAPIKey Kind	 = "api_key" 
)

type Principal struct {
	Kind 	Kind
	ActorID uuid.UUID
	OrgID  	uuid.UUID
	Role   	string
}

type ctxKey struct{}

func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, ctxKey{}, p)
}

func FromContext(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(ctxKey{}).(Principal)
	return p, ok
}
