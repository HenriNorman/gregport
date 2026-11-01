package requestctx

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

func TestFromContext_RoundTrip(t *testing.T) {
	aid := uuid.New()
	oid := uuid.New()

	ctx := WithPrincipal(context.Background(), Principal{Kind: "user", ActorID: aid, OrgID: oid, Role: "owner"})

	p, ok := FromContext(ctx)
	if !ok {
		t.Fatal("expected principal")
	}
	if p.ActorID != aid || p.OrgID != oid || p.Role != "owner" {
		t.Fatalf("mismatch: %+v", p)
	}
}

func TestFromContext_Missing(t *testing.T) {
	_, ok := FromContext(context.Background())
	if ok {
		t.Fatal("expected no principal")
	}
}
