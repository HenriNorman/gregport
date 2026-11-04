// Package metrics holds process-local counters for infrastructure behaviour
// that does not yet have a metrics endpoint. Slice 13 can export the same
// snapshot to Prometheus without changing call sites.
package metrics

import (
	"log/slog"
	"sync/atomic"
)

// Counters is safe for concurrent use.
type Counters struct {
	cacheHits     atomic.Int64
	cacheMisses   atomic.Int64
	cacheErrors   atomic.Int64
	rateLimited   atomic.Int64
	rateLimitErrs atomic.Int64
	idemReplays   atomic.Int64
	idemConflicts atomic.Int64
	idemPending   atomic.Int64
	idemErrors    atomic.Int64
}

// Snapshot is a point-in-time copy of all counters.
type Snapshot struct {
	CacheHits            int64 `json:"cache_hits"`
	CacheMisses          int64 `json:"cache_misses"`
	CacheErrors          int64 `json:"cache_errors"`
	RateLimited          int64 `json:"rate_limited"`
	RateLimitErrors      int64 `json:"rate_limit_errors"`
	IdempotencyReplays   int64 `json:"idempotency_replays"`
	IdempotencyConflicts int64 `json:"idempotency_conflicts"`
	IdempotencyPending   int64 `json:"idempotency_pending"`
	IdempotencyErrors    int64 `json:"idempotency_errors"`
}

// New returns a zeroed counter set.
func New() *Counters {
	return &Counters{}
}

func (c *Counters) CacheHit()          { c.cacheHits.Add(1) }
func (c *Counters) CacheMiss()         { c.cacheMisses.Add(1) }
func (c *Counters) CacheError()        { c.cacheErrors.Add(1) }
func (c *Counters) RateLimitRejected() { c.rateLimited.Add(1) }
func (c *Counters) RateLimitError()    { c.rateLimitErrs.Add(1) }
func (c *Counters) IdempotencyReplay()   { c.idemReplays.Add(1) }
func (c *Counters) IdempotencyConflict() {
	c.idemConflicts.Add(1)
}
func (c *Counters) IdempotencyPending() { c.idemPending.Add(1) }
func (c *Counters) IdempotencyError()   { c.idemErrors.Add(1) }

// Snapshot returns the current values.
func (c *Counters) Snapshot() Snapshot {
	return Snapshot{
		CacheHits:            c.cacheHits.Load(),
		CacheMisses:          c.cacheMisses.Load(),
		CacheErrors:          c.cacheErrors.Load(),
		RateLimited:          c.rateLimited.Load(),
		RateLimitErrors:      c.rateLimitErrs.Load(),
		IdempotencyReplays:   c.idemReplays.Load(),
		IdempotencyConflicts: c.idemConflicts.Load(),
		IdempotencyPending:   c.idemPending.Load(),
		IdempotencyErrors:    c.idemErrors.Load(),
	}
}

// Log writes the snapshot as one structured line at shutdown.
func (c *Counters) Log(logger *slog.Logger) {
	if logger == nil {
		logger = slog.Default()
	}
	s := c.Snapshot()
	logger.Info("counter snapshot",
		"cache_hits", s.CacheHits,
		"cache_misses", s.CacheMisses,
		"cache_errors", s.CacheErrors,
		"rate_limited", s.RateLimited,
		"rate_limit_errors", s.RateLimitErrors,
		"idempotency_replays", s.IdempotencyReplays,
		"idempotency_conflicts", s.IdempotencyConflicts,
		"idempotency_pending", s.IdempotencyPending,
		"idempotency_errors", s.IdempotencyErrors,
	)
}
