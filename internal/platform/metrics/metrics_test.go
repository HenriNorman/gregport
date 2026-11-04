package metrics

import (
	"sync"
	"testing"
)

func TestCountersSnapshot(t *testing.T) {
	c := New()

	c.CacheHit()
	c.CacheMiss()
	c.CacheError()
	c.RateLimitRejected()
	c.RateLimitError()
	c.IdempotencyReplay()
	c.IdempotencyConflict()
	c.IdempotencyPending()

	got := c.Snapshot()
	want := Snapshot{
		CacheHits:            1,
		CacheMisses:          1,
		CacheErrors:          1,
		RateLimited:          1,
		RateLimitErrors:      1,
		IdempotencyReplays:   1,
		IdempotencyConflicts: 1,
		IdempotencyPending:   1,
	}
	if got != want {
		t.Fatalf("snapshot: got %+v, want %+v", got, want)
	}
}

func TestCountersConcurrentIncrements(t *testing.T) {
	c := New()
	const goroutines = 50
	const each = 20

	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < each; j++ {
				c.CacheHit()
				c.CacheMiss()
				c.RateLimitRejected()
				c.IdempotencyReplay()
			}
		}()
	}
	wg.Wait()

	got := c.Snapshot()
	want := int64(goroutines * each)
	if got.CacheHits != want || got.CacheMisses != want ||
		got.RateLimited != want || got.IdempotencyReplays != want {
		t.Fatalf("concurrent snapshot: %+v, want %d each", got, want)
	}
}

func TestLogDoesNotPanic(t *testing.T) {
	New().Log(nil)
}
