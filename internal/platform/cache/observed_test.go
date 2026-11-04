package cache

import (
	"context"
	"errors"
	"testing"
	"time"
)

type testCounters struct {
	hits, misses, errors int
}

func (c *testCounters) CacheHit()   { c.hits++ }
func (c *testCounters) CacheMiss()  { c.misses++ }
func (c *testCounters) CacheError() { c.errors++ }

type failingCache struct {
	getErr  error
	setErr  error
	delErr  error
	pingErr error
}

func (f *failingCache) Get(context.Context, string) ([]byte, bool, error) {
	return nil, false, f.getErr
}
func (f *failingCache) Set(context.Context, string, []byte, time.Duration) error {
	return f.setErr
}
func (f *failingCache) Delete(context.Context, string) error { return f.delErr }
func (f *failingCache) Ping(context.Context) error           { return f.pingErr }

func TestObservedCountsHitsAndMisses(t *testing.T) {
	inner := NewMemory()
	counters := &testCounters{}
	c := NewObserved(inner, counters, nil)
	ctx := context.Background()

	if _, ok, err := c.Get(ctx, "key"); err != nil || ok {
		t.Fatalf("expected miss: ok=%v err=%v", ok, err)
	}
	if counters.misses != 1 || counters.hits != 0 || counters.errors != 0 {
		t.Fatalf("after miss: %+v", counters)
	}

	if err := c.Set(ctx, "key", []byte("value"), time.Minute); err != nil {
		t.Fatalf("set: %v", err)
	}
	if _, ok, err := c.Get(ctx, "key"); err != nil || !ok {
		t.Fatalf("expected hit: ok=%v err=%v", ok, err)
	}
	if counters.hits != 1 || counters.misses != 1 || counters.errors != 0 {
		t.Fatalf("after hit: %+v", counters)
	}
}

func TestObservedCountsErrors(t *testing.T) {
	wantErr := errors.New("redis unavailable")
	inner := &failingCache{getErr: wantErr, setErr: wantErr, delErr: wantErr}
	counters := &testCounters{}
	c := NewObserved(inner, counters, nil)
	ctx := context.Background()

	if _, _, err := c.Get(ctx, "key"); !errors.Is(err, wantErr) {
		t.Fatalf("get error: %v", err)
	}
	if err := c.Set(ctx, "key", []byte("value"), time.Minute); !errors.Is(err, wantErr) {
		t.Fatalf("set error: %v", err)
	}
	if err := c.Delete(ctx, "key"); !errors.Is(err, wantErr) {
		t.Fatalf("delete error: %v", err)
	}
	if counters.errors != 3 || counters.hits != 0 || counters.misses != 0 {
		t.Fatalf("counters: %+v", counters)
	}
}

func TestObservedPingAndCloseDelegate(t *testing.T) {
	mr := newMemoryWithPing(t)
	c := NewObserved(mr, &testCounters{}, nil)

	if err := c.Ping(context.Background()); err != nil {
		t.Fatalf("ping: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

type memoryWithPing struct {
	*Memory
	closed bool
}

func newMemoryWithPing(t *testing.T) *memoryWithPing {
	t.Helper()
	return &memoryWithPing{Memory: NewMemory()}
}

func (m *memoryWithPing) Close() error {
	m.closed = true
	return nil
}
