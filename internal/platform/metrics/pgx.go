package metrics

import (
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
)

// RegisterPoolMetrics exports pgxpool saturation. Every collector reads the
// live Stat, so no background polling is needed.
func RegisterPoolMetrics(reg prometheus.Registerer, pool *pgxpool.Pool, name string) {
	if reg == nil || pool == nil {
		return
	}
	labels := prometheus.Labels{"pool": name}

	gauge := func(metric, help string, fn func(*pgxpool.Stat) float64) {
		reg.MustRegister(prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Name:        metric,
			Help:        help,
			ConstLabels: labels,
		}, func() float64 { return fn(pool.Stat()) }))
	}
	counter := func(metric, help string, fn func(*pgxpool.Stat) int64) {
		reg.MustRegister(prometheus.NewCounterFunc(prometheus.CounterOpts{
			Name:        metric,
			Help:        help,
			ConstLabels: labels,
		}, func() float64 { return float64(fn(pool.Stat())) }))
	}

	gauge("gregport_db_pool_acquired_conns", "Connections currently acquired from the pool.",
		func(s *pgxpool.Stat) float64 { return float64(s.AcquiredConns()) })
	gauge("gregport_db_pool_idle_conns", "Idle connections in the pool.",
		func(s *pgxpool.Stat) float64 { return float64(s.IdleConns()) })
	gauge("gregport_db_pool_total_conns", "Total open connections.",
		func(s *pgxpool.Stat) float64 { return float64(s.TotalConns()) })
	gauge("gregport_db_pool_max_conns", "Configured maximum connections.",
		func(s *pgxpool.Stat) float64 { return float64(s.MaxConns()) })
	gauge("gregport_db_pool_constructing_conns", "Connections being established.",
		func(s *pgxpool.Stat) float64 { return float64(s.ConstructingConns()) })

	counter("gregport_db_pool_acquire_total", "Successful pool acquisitions.",
		func(s *pgxpool.Stat) int64 { return s.AcquireCount() })
	counter("gregport_db_pool_empty_acquire_total", "Acquisitions that had to wait for a connection.",
		func(s *pgxpool.Stat) int64 { return s.EmptyAcquireCount() })
	counter("gregport_db_pool_canceled_acquire_total", "Acquisitions canceled while waiting.",
		func(s *pgxpool.Stat) int64 { return s.CanceledAcquireCount() })
	counter("gregport_db_pool_new_conns_total", "New connections opened.",
		func(s *pgxpool.Stat) int64 { return s.NewConnsCount() })
	counter("gregport_db_pool_max_lifetime_destroy_total", "Connections closed after reaching max lifetime.",
		func(s *pgxpool.Stat) int64 { return s.MaxLifetimeDestroyCount() })
	counter("gregport_db_pool_max_idle_destroy_total", "Connections closed after being idle.",
		func(s *pgxpool.Stat) int64 { return s.MaxIdleDestroyCount() })
}
