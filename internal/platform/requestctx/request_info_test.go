package requestctx

import (
	"context"
	"sync"
	"testing"

	"github.com/google/uuid"
)

func TestRequestInfo_PopulatedByWithPrincipal(t *testing.T) {
	ctx, info := WithRequestInfo(context.Background())

	if _, ok := info.Principal(); ok {
		t.Fatal("expected no principal before authentication")
	}

	aid := uuid.New()
	oid := uuid.New()
	p := Principal{Kind: KindUser, ActorID: aid, OrgID: oid, Role: "agent"}

	// Authentication derives a new context; the holder installed earlier must
	// still observe the principal.
	_ = WithPrincipal(ctx, p)

	got, ok := info.Principal()
	if !ok {
		t.Fatal("expected principal in holder")
	}
	if got != p {
		t.Fatalf("got %+v, want %+v", got, p)
	}
}

func TestWithPrincipal_WithoutHolder(t *testing.T) {
	p := Principal{Kind: KindUser, ActorID: uuid.New(), OrgID: uuid.New(), Role: "owner"}
	ctx := WithPrincipal(context.Background(), p)

	got, ok := FromContext(ctx)
	if !ok || got != p {
		t.Fatalf("principal round trip failed: %+v %v", got, ok)
	}
}

func TestRequestInfo_ConcurrentAccess(t *testing.T) {
	_, info := WithRequestInfo(context.Background())
	p := Principal{Kind: KindUser, ActorID: uuid.New(), OrgID: uuid.New(), Role: "agent"}

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			info.Set(p)
		}()
		go func() {
			defer wg.Done()
			info.Principal()
		}()
	}
	wg.Wait()
}
