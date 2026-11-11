package realtime

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"github.com/HenriNorman/gregport/internal/platform/requestctx"
)

func tokenTestStore(t *testing.T) (*TokenStore, *miniredis.Miniredis) {
	t.Helper()

	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	return NewTokenStore(client, 30*time.Second), mr
}

func TestTokenIssueConsumeOnce(t *testing.T) {
	store, _ := tokenTestStore(t)
	ctx := context.Background()
	want := requestctx.Principal{
		Kind:    requestctx.KindUser,
		ActorID: uuid.New(),
		OrgID:   uuid.New(),
		Role:    "agent",
	}

	token, expiresAt, err := store.Issue(ctx, want)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if token == "" {
		t.Fatal("empty token")
	}
	if time.Until(expiresAt) > 30*time.Second {
		t.Fatalf("expiry too far in the future: %s", expiresAt)
	}

	got, err := store.Consume(ctx, token)
	if err != nil {
		t.Fatalf("consume: %v", err)
	}
	if got != want {
		t.Fatalf("principal: got %+v, want %+v", got, want)
	}

	if _, err := store.Consume(ctx, token); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("second consume: got %v, want ErrInvalidToken", err)
	}
}

func TestTokenExpires(t *testing.T) {
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	store := NewTokenStore(client, 30*time.Second)
	token, _, err := store.Issue(context.Background(), requestctx.Principal{OrgID: uuid.New()})
	if err != nil {
		t.Fatalf("issue: %v", err)
	}

	mr.FastForward(31 * time.Second)

	if _, err := store.Consume(context.Background(), token); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("expired token: got %v, want ErrInvalidToken", err)
	}
}

func TestTokenEmptyOrUnknownIsInvalid(t *testing.T) {
	store, _ := tokenTestStore(t)
	for _, token := range []string{"", "not-a-real-token"} {
		if _, err := store.Consume(context.Background(), token); !errors.Is(err, ErrInvalidToken) {
			t.Fatalf("token %q: got %v, want ErrInvalidToken", token, err)
		}
	}
}

func TestSafeDataOnlyAllowsAgentFields(t *testing.T) {
	raw := []byte(`{"number":42,"subject":"Printer","priority":"high","internal_note":"secret","org_id":"other"}`)
	got := string(SafeData("ticket.created", raw))
	want := `{"number":42,"priority":"high","subject":"Printer"}`
	if got != want {
		t.Fatalf("filtered data: got %s, want %s", got, want)
	}

	if got := string(SafeData("unknown.event", []byte(`{"a":1}`))); got != `{}` {
		t.Fatalf("unknown event: got %s", got)
	}
	if got := string(SafeData("ticket.created", []byte(`not json`))); got != `{}` {
		t.Fatalf("invalid json: got %s", got)
	}
}
