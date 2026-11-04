package cache

import (
	"context"
	"io"
	"log/slog"
	"time"
)

// Counters receives cache outcomes. metrics.Counters implements it.
type Counters interface {
	CacheHit()
	CacheMiss()
	CacheError()
}

// Observed wraps a Cache and records hits, misses, and errors without
// changing miss semantics: a Redis failure is still returned to the caller,
// which decides whether to fall back to Postgres.
type Observed struct {
	Inner    Cache
	Counters Counters
	Logger   *slog.Logger
}

// NewObserved creates an instrumented cache.
func NewObserved(inner Cache, counters Counters, logger *slog.Logger) *Observed {
	return &Observed{Inner: inner, Counters: counters, Logger: logger}
}

func (o *Observed) Get(ctx context.Context, key string) ([]byte, bool, error) {
	value, ok, err := o.Inner.Get(ctx, key)
	switch {
	case err != nil:
		o.count(func(c Counters) { c.CacheError() })
		o.log().Warn("cache get failed", "error", err)
	case ok:
		o.count(func(c Counters) { c.CacheHit() })
	default:
		o.count(func(c Counters) { c.CacheMiss() })
	}
	return value, ok, err
}

func (o *Observed) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	err := o.Inner.Set(ctx, key, value, ttl)
	if err != nil {
		o.count(func(c Counters) { c.CacheError() })
		o.log().Warn("cache set failed", "error", err)
	}
	return err
}

func (o *Observed) Delete(ctx context.Context, key string) error {
	err := o.Inner.Delete(ctx, key)
	if err != nil {
		o.count(func(c Counters) { c.CacheError() })
		o.log().Warn("cache delete failed", "error", err)
	}
	return err
}

// Ping delegates to the underlying cache when it supports health checks.
func (o *Observed) Ping(ctx context.Context) error {
	if pinger, ok := o.Inner.(Pinger); ok {
		return pinger.Ping(ctx)
	}
	return nil
}

// Close delegates to the underlying cache when it owns resources.
func (o *Observed) Close() error {
	if closer, ok := o.Inner.(io.Closer); ok {
		return closer.Close()
	}
	return nil
}

func (o *Observed) count(fn func(Counters)) {
	if o.Counters != nil {
		fn(o.Counters)
	}
}

func (o *Observed) log() *slog.Logger {
	if o.Logger != nil {
		return o.Logger
	}
	return slog.Default()
}
