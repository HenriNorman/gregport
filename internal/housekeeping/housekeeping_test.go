package housekeeping

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func fakePurge(n int64, err error) PurgeFunc {
	return func(ctx context.Context, before time.Time, batch int) (int64, error) {
		return n, err
	}
}

func countingPurge(calls *int32, n int64) PurgeFunc {
	return func(ctx context.Context, before time.Time, batch int) (int64, error) {
		atomic.AddInt32(calls, 1)
		return n, nil
	}
}

func TestCheckerRunsAllPurges(t *testing.T) {
	c := NewChecker(
		fakePurge(5, nil),
		fakePurge(10, nil),
		fakePurge(3, nil),
		fakePurge(7, nil),
	)

	cfg := Config{
		IDempotencyTTL:         24 * time.Hour,
		TicketEventRetention:   720 * time.Hour,
		AuditLogRetention:      2160 * time.Hour,
		EmailDeliveryRetention: 720 * time.Hour,
		Batch:                  50,
	}

	summary, err := c.Run(context.Background(), cfg)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if summary.IDempotencyKeysPurged != 5 ||
		summary.TicketEventsPurged != 10 ||
		summary.AuditLogsPurged != 3 ||
		summary.EmailDeliveriesPurged != 7 {
		t.Fatalf("summary: %+v", summary)
	}
}

func TestCheckerSkipsNilPurgeFuncs(t *testing.T) {
	c := NewChecker(nil, nil, nil, nil)
	summary, err := c.Run(context.Background(), Config{})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if summary.IDempotencyKeysPurged != 0 || summary.TicketEventsPurged != 0 ||
		summary.AuditLogsPurged != 0 || summary.EmailDeliveriesPurged != 0 {
		t.Fatalf("expected empty summary, got %+v", summary)
	}
}

func TestCheckerSkipsZeroRetention(t *testing.T) {
	idemCalls := int32(0)
	c := NewChecker(
		countingPurge(&idemCalls, 1),
		nil, nil, nil,
	)

	cfg := Config{
		IDempotencyTTL:         0, // disabled
		TicketEventRetention:   720 * time.Hour,
		AuditLogRetention:      2160 * time.Hour,
		EmailDeliveryRetention: 720 * time.Hour,
	}

	_, err := c.Run(context.Background(), cfg)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if atomic.LoadInt32(&idemCalls) != 0 {
		t.Fatalf("idempotency purge ran despite zero TTL: %d calls", atomic.LoadInt32(&idemCalls))
	}
}

func TestCheckerContinuesOnPurgeError(t *testing.T) {
	c := NewChecker(
		fakePurge(0, errors.New("idempotency down")),
		fakePurge(10, nil),
		nil, nil,
	)

	cfg := Config{
		IDempotencyTTL:       24 * time.Hour,
		TicketEventRetention: 720 * time.Hour,
	}

	summary, err := c.Run(context.Background(), cfg)
	if err == nil {
		t.Fatal("expected an error from the failing purge")
	}
	if summary.TicketEventsPurged != 10 {
		t.Fatalf("ticket events purge should still run: %+v", summary)
	}
	if !errors.Is(err, errors.New("idempotency down")) &&
		!contains(err.Error(), "idempotency down") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCheckerBatchDefault(t *testing.T) {
	var batch int
	c := NewChecker(func(ctx context.Context, before time.Time, b int) (int64, error) {
		batch = b
		return 0, nil
	}, nil, nil, nil)

	if _, err := c.Run(context.Background(), Config{IDempotencyTTL: time.Hour}); err != nil {
		t.Fatalf("run: %v", err)
	}
	if batch != 100 {
		t.Fatalf("batch: %d, want 100", batch)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}
