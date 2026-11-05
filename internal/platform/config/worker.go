package config

import (
	"errors"
	"net"
	"net/url"
	"os"
	"runtime"
	"time"

	"github.com/HenriNorman/gregport/internal/platform/logging"
)

// WorkerConfig is the worker process's own configuration. It shares the
// database and Redis settings with the API but does not require JWT or HTTP
// API settings, because the worker never authenticates users.
type WorkerConfig struct {
	DBHost     string
	DBPort     string
	DBUser     string
	DBPassword string
	DBName     string
	LogLevel   string

	RedisAddr         string
	RedisPassword     string
	RedisDB           int
	RedisDialTimeout  time.Duration
	RedisReadTimeout  time.Duration
	RedisWriteTimeout time.Duration

	WorkerPort              string
	WorkerConcurrency       int
	WorkerCPUConcurrency    int
	WorkerShutdownTimeout   time.Duration
	WorkerReadHeaderTimeout time.Duration
	WorkerReadTimeout       time.Duration
	WorkerWriteTimeout      time.Duration
	WorkerIdleTimeout       time.Duration

	OutboxInterval time.Duration
	OutboxBatch    int
}

// LoadWorker reads the worker configuration from the environment. Defaults
// mirror the API's connection settings, while worker-only knobs use the
// WORKER_ prefix.
func LoadWorker() (WorkerConfig, error) {
	cfg := WorkerConfig{
		DBHost:     getEnv("DB_HOST", "localhost"),
		DBPort:     getEnv("DB_PORT", "5432"),
		DBUser:     getEnv("DB_USER", "postgres"),
		DBPassword: os.Getenv("DB_PASSWORD"),
		DBName:     getEnv("DB_NAME", "gregport"),
		LogLevel:   getEnv("LOG_LEVEL", "info"),

		RedisAddr:         getEnv("REDIS_ADDR", "localhost:6379"),
		RedisPassword:     os.Getenv("REDIS_PASSWORD"),
		RedisDB:           0,
		RedisDialTimeout:  time.Second,
		RedisReadTimeout:  500 * time.Millisecond,
		RedisWriteTimeout: 500 * time.Millisecond,

		WorkerPort:              getEnv("WORKER_PORT", "8081"),
		WorkerConcurrency:       20,
		WorkerCPUConcurrency:    runtime.NumCPU(),
		WorkerShutdownTimeout:   30 * time.Second,
		WorkerReadHeaderTimeout: 5 * time.Second,
		WorkerReadTimeout:       10 * time.Second,
		WorkerWriteTimeout:      10 * time.Second,
		WorkerIdleTimeout:       60 * time.Second,

		OutboxInterval: time.Second,
		OutboxBatch:    100,
	}

	var errs []error

	if cfg.DBPassword == "" {
		errs = append(errs, errors.New("DB_PASSWORD is required"))
	}
	if _, err := logging.ParseLevel(cfg.LogLevel); err != nil {
		errs = append(errs, err)
	}

	if port, err := envPort("DB_PORT", cfg.DBPort); err != nil {
		errs = append(errs, err)
	} else {
		cfg.DBPort = port
	}
	if port, err := envPort("WORKER_PORT", cfg.WorkerPort); err != nil {
		errs = append(errs, err)
	} else {
		cfg.WorkerPort = port
	}

	durations := []struct {
		name string
		dst  *time.Duration
	}{
		{"REDIS_DIAL_TIMEOUT", &cfg.RedisDialTimeout},
		{"REDIS_READ_TIMEOUT", &cfg.RedisReadTimeout},
		{"REDIS_WRITE_TIMEOUT", &cfg.RedisWriteTimeout},
		{"WORKER_SHUTDOWN_TIMEOUT", &cfg.WorkerShutdownTimeout},
		{"WORKER_READ_HEADER_TIMEOUT", &cfg.WorkerReadHeaderTimeout},
		{"WORKER_READ_TIMEOUT", &cfg.WorkerReadTimeout},
		{"WORKER_WRITE_TIMEOUT", &cfg.WorkerWriteTimeout},
		{"WORKER_IDLE_TIMEOUT", &cfg.WorkerIdleTimeout},
		{"WORKER_OUTBOX_INTERVAL", &cfg.OutboxInterval},
	}
	for _, d := range durations {
		value, err := envDuration(d.name, *d.dst, false)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		*d.dst = value
	}

	ints := []struct {
		name string
		dst  *int
		min  int
	}{
		{"REDIS_DB", &cfg.RedisDB, 0},
		{"WORKER_CONCURRENCY", &cfg.WorkerConcurrency, 1},
		{"WORKER_CPU_CONCURRENCY", &cfg.WorkerCPUConcurrency, 1},
		{"WORKER_OUTBOX_BATCH", &cfg.OutboxBatch, 1},
	}
	for _, n := range ints {
		value, err := envInt(n.name, *n.dst, n.min)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		*n.dst = value
	}

	if len(errs) > 0 {
		return WorkerConfig{}, errors.Join(errs...)
	}
	return cfg, nil
}

// DSN builds the PostgreSQL connection string.
func (c WorkerConfig) DSN() string {
	u := url.URL{
		Scheme:   "postgres",
		User:     url.UserPassword(c.DBUser, c.DBPassword),
		Host:     net.JoinHostPort(c.DBHost, c.DBPort),
		Path:     "/" + c.DBName,
		RawQuery: "sslmode=disable",
	}
	return u.String()
}
