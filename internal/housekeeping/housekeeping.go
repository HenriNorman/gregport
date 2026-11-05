// Package housekeeping runs retention-based pruning across the system.
//
// Housekeeping is executed as a recurring `housekeeping:run` task on the
// `low` queue. It purges rows that have outlived their retention window:
// expired idempotency keys, dispatched ticket events, old audit logs, and
// sent or dead email deliveries.
//
// Every purge is bounded by a batch size and run inside its own statement, so
// a long retention window never blocks the worker. Purges are independent: a
// failure in one is logged and does not abort the others.
package housekeeping

import (
	"context"
	"fmt"
	"time"
)

// PurgeFunc deletes rows older than before, limited to batch rows, and returns
// the number of rows deleted.
type PurgeFunc func(ctx context.Context, before time.Time, batch int) (int64, error)

// Config holds the retention windows and batch size for housekeeping.
type Config struct {
	// IDempotencyTTL is how long an idempotency key is retained before purge.
	IDempotencyTTL time.Duration

	// TicketEventRetention is how long a dispatched ticket_event is retained.
	TicketEventRetention time.Duration

	// AuditLogRetention is how long an audit_log row is retained.
	AuditLogRetention time.Duration

	// EmailDeliveryRetention is how long a sent or dead email_delivery is
	// retained. Pending and failed deliveries are never purged.
	EmailDeliveryRetention time.Duration

	// Batch is the maximum number of rows deleted per purge statement.
	Batch int
}

// Summary is the result of a housekeeping run.
type Summary struct {
	IDempotencyKeysPurged int64
	TicketEventsPurged    int64
	AuditLogsPurged       int64
	EmailDeliveriesPurged int64
}

// Checker runs the retention purges.
type Checker struct {
	purgeIDempotencyKeys PurgeFunc
	purgeTicketEvents    PurgeFunc
	purgeAuditLogs       PurgeFunc
	purgeEmailDeliveries PurgeFunc
}

// NewChecker builds a checker. Any purge func may be nil, in which case that
// purge is skipped.
func NewChecker(purgeIDempotencyKeys, purgeTicketEvents, purgeAuditLogs, purgeEmailDeliveries PurgeFunc) *Checker {
	return &Checker{
		purgeIDempotencyKeys: purgeIDempotencyKeys,
		purgeTicketEvents:    purgeTicketEvents,
		purgeAuditLogs:       purgeAuditLogs,
		purgeEmailDeliveries: purgeEmailDeliveries,
	}
}

// Run executes every configured purge and returns a summary. A nil config uses
// zero values, which disables every purge.
func (c *Checker) Run(ctx context.Context, cfg Config) (Summary, error) {
	if cfg.Batch <= 0 {
		cfg.Batch = 100
	}

	var summary Summary
	var firstErr error

	now := time.Now()

	if c.purgeIDempotencyKeys != nil && cfg.IDempotencyTTL > 0 {
		n, err := c.purgeIDempotencyKeys(ctx, now.Add(-cfg.IDempotencyTTL), cfg.Batch)
		if err != nil && firstErr == nil {
			firstErr = err
		}
		summary.IDempotencyKeysPurged = n
	}

	if c.purgeTicketEvents != nil && cfg.TicketEventRetention > 0 {
		n, err := c.purgeTicketEvents(ctx, now.Add(-cfg.TicketEventRetention), cfg.Batch)
		if err != nil && firstErr == nil {
			firstErr = err
		}
		summary.TicketEventsPurged = n
	}

	if c.purgeAuditLogs != nil && cfg.AuditLogRetention > 0 {
		n, err := c.purgeAuditLogs(ctx, now.Add(-cfg.AuditLogRetention), cfg.Batch)
		if err != nil && firstErr == nil {
			firstErr = err
		}
		summary.AuditLogsPurged = n
	}

	if c.purgeEmailDeliveries != nil && cfg.EmailDeliveryRetention > 0 {
		n, err := c.purgeEmailDeliveries(ctx, now.Add(-cfg.EmailDeliveryRetention), cfg.Batch)
		if err != nil && firstErr == nil {
			firstErr = err
		}
		summary.EmailDeliveriesPurged = n
	}

	if firstErr != nil {
		return summary, fmt.Errorf("housekeeping: %w", firstErr)
	}
	return summary, nil
}
