package email

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sony/gobreaker"
)

type failingSender struct {
	calls atomic.Int32
	fail  bool
}

func (f *failingSender) Send(_ context.Context, to, template string, payload DeliveryPayload) error {
	f.calls.Add(1)
	if f.fail {
		return errors.New("smtp: connection refused")
	}
	return nil
}

func TestCircuitBreakerTripsAfterConsecutiveFailures(t *testing.T) {
	sender := &failingSender{fail: true}
	cb := NewCircuitBreaker(sender, 3, 50*time.Millisecond)

	for i := 0; i < 3; i++ {
		err := cb.Send(context.Background(), "a@b.c", "t", DeliveryPayload{})
		if err == nil || !errors.Is(err, gobreaker.ErrOpenState) && !IsCircuitOpen(err) {
			// On the 3rd call the breaker trips; gobreaker returns ErrOpenState
			// for the call that trips and subsequent calls while open.
			t.Logf("call %d: %v", i, err)
		}
	}

	// After tripping, calls are rejected without reaching the sender.
	before := sender.calls.Load()
	for i := 0; i < 10; i++ {
		err := cb.Send(context.Background(), "a@b.c", "t", DeliveryPayload{})
		if !IsCircuitOpen(err) && !errors.Is(err, gobreaker.ErrOpenState) {
			t.Fatalf("expected open circuit, got %v", err)
		}
	}
	if sender.calls.Load() != before {
		t.Fatalf("sender was called while circuit open: %d calls", sender.calls.Load())
	}
}

func TestCircuitBreakerRecoversAfterTimeout(t *testing.T) {
	sender := &failingSender{fail: true}
	cb := NewCircuitBreaker(sender, 2, 50*time.Millisecond)

	// Trip the breaker.
	for i := 0; i < 2; i++ {
		_ = cb.Send(context.Background(), "a@b.c", "t", DeliveryPayload{})
	}
	if cb.State() != gobreaker.StateOpen {
		t.Fatalf("expected open state, got %s", cb.State())
	}

	// Wait for the open timeout to elapse so the breaker goes half-open.
	time.Sleep(100 * time.Millisecond)

	// Half-open admits one probe; it fails, so the breaker goes open again.
	if err := cb.Send(context.Background(), "a@b.c", "t", DeliveryPayload{}); err == nil {
		t.Fatal("expected an error from the failed probe")
	}
	if cb.State() != gobreaker.StateOpen {
		t.Fatalf("expected open state after failed probe, got %s", cb.State())
	}

	// Now make the sender succeed and wait again for half-open.
	sender.fail = false
	time.Sleep(100 * time.Millisecond)
	if err := cb.Send(context.Background(), "a@b.c", "t", DeliveryPayload{}); err != nil {
		t.Fatalf("expected success after recovery, got %v", err)
	}
	if cb.State() != gobreaker.StateClosed {
		t.Fatalf("expected closed state after success, got %s", cb.State())
	}
}

func TestCircuitBreakerDoesNotTripOnSuccess(t *testing.T) {
	sender := &failingSender{}
	cb := NewCircuitBreaker(sender, 3, time.Minute)
	for i := 0; i < 10; i++ {
		if err := cb.Send(context.Background(), "a@b.c", "t", DeliveryPayload{}); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if cb.State() != gobreaker.StateClosed {
		t.Fatalf("expected closed state, got %s", cb.State())
	}
	if sender.calls.Load() != 10 {
		t.Fatalf("calls: %d", sender.calls.Load())
	}
}

func TestIsCircuitOpen(t *testing.T) {
	if !IsCircuitOpen(ErrCircuitOpen) {
		t.Fatal("ErrCircuitOpen not recognized")
	}
	if !IsCircuitOpen(gobreaker.ErrOpenState) {
		t.Fatal("gobreaker.ErrOpenState not recognized")
	}
	if IsCircuitOpen(errors.New("other error")) {
		t.Fatal("other error should not be circuit open")
	}
	if IsCircuitOpen(nil) {
		t.Fatal("nil should not be circuit open")
	}
}
