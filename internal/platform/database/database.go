package database

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PoolOption customises a pool before it is created.
type PoolOption func(*pgxpool.Config)

// WithPoolLimits bounds the pool. MaxConns must stay below PostgreSQL's
// max_connections divided by the number of API/worker instances.
func WithPoolLimits(maxConns, minConns int) PoolOption {
	return func(cfg *pgxpool.Config) {
		if maxConns > 0 {
			cfg.MaxConns = clampInt32(maxConns)
		}
		if minConns >= 0 {
			cfg.MinConns = clampInt32(minConns)
		}
	}
}

func clampInt32(n int) int32 {
	if n > int(^uint32(0)>>1) {
		return int32(^uint32(0) >> 1)
	}
	// #nosec G115 -- the conversion is guarded by the clamp above.
	return int32(n)
}

// WithConnLifetime recycles connections. A shorter lifetime makes failover and
// credential rotation visible sooner.
func WithConnLifetime(lifetime, idle time.Duration) PoolOption {
	return func(cfg *pgxpool.Config) {
		if lifetime > 0 {
			cfg.MaxConnLifetime = lifetime
		}
		if idle > 0 {
			cfg.MaxConnIdleTime = idle
		}
	}
}

// WithTracer installs a pgx query tracer, typically the OpenTelemetry tracer.
func WithTracer(tracer pgx.QueryTracer) PoolOption {
	return func(cfg *pgxpool.Config) {
		if tracer != nil {
			cfg.ConnConfig.Tracer = tracer
		}
	}
}

func NewPool(ctx context.Context, dsn string, opts ...PoolOption) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse pool config: %w", err)
	}
	for _, opt := range opts {
		opt(cfg)
	}

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("create pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping db: %w", err)
	}
	return pool, nil
}

func WithTx(ctx context.Context, pool *pgxpool.Pool, fn func(pgx.Tx) error) (err error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}

	defer func() {
		if p := recover(); p != nil {
			_ = tx.Rollback(ctx)
			panic(p)
		}
	}()

	if err := fn(tx); err != nil {
		if rbErr := tx.Rollback(ctx); rbErr != nil {
			return fmt.Errorf("tx error: %w, rollback error: %v", err, rbErr)
		}
		return err
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit tx: %w", err)
	}

	return nil
}
