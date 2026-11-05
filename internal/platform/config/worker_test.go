package config

import (
	"runtime"
	"strings"
	"testing"
	"time"
)

func clearWorkerEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"DB_HOST", "DB_PORT", "DB_USER", "DB_PASSWORD", "DB_NAME", "LOG_LEVEL",
		"REDIS_ADDR", "REDIS_PASSWORD", "REDIS_DB",
		"REDIS_DIAL_TIMEOUT", "REDIS_READ_TIMEOUT", "REDIS_WRITE_TIMEOUT",
		"WORKER_PORT", "WORKER_CONCURRENCY", "WORKER_CPU_CONCURRENCY", "WORKER_SHUTDOWN_TIMEOUT",
		"WORKER_READ_HEADER_TIMEOUT", "WORKER_READ_TIMEOUT",
		"WORKER_WRITE_TIMEOUT", "WORKER_IDLE_TIMEOUT",
		"JWT_SECRET",
	} {
		t.Setenv(key, "")
	}
}

func TestLoadWorkerDefaults(t *testing.T) {
	clearWorkerEnv(t)
	t.Setenv("DB_PASSWORD", "secret")

	cfg, err := LoadWorker()
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	if cfg.DBHost != "localhost" || cfg.DBPort != "5432" || cfg.DBUser != "postgres" || cfg.DBName != "gregport" {
		t.Fatalf("db defaults: %+v", cfg)
	}
	if cfg.WorkerPort != "8081" {
		t.Fatalf("worker port: %q", cfg.WorkerPort)
	}
	if cfg.WorkerConcurrency != 20 {
		t.Fatalf("concurrency: %d", cfg.WorkerConcurrency)
	}
	if cfg.WorkerCPUConcurrency != runtime.NumCPU() {
		t.Fatalf("cpu concurrency: %d, want %d", cfg.WorkerCPUConcurrency, runtime.NumCPU())
	}
	if cfg.WorkerShutdownTimeout != 30*time.Second {
		t.Fatalf("shutdown: %s", cfg.WorkerShutdownTimeout)
	}
	if cfg.RedisAddr != "localhost:6379" || cfg.RedisDB != 0 {
		t.Fatalf("redis defaults: %+v", cfg)
	}
}

func TestLoadWorkerDoesNotRequireJWTSecret(t *testing.T) {
	clearWorkerEnv(t)
	t.Setenv("DB_PASSWORD", "secret")

	if _, err := LoadWorker(); err != nil {
		t.Fatalf("worker must not require JWT_SECRET: %v", err)
	}
}

func TestLoadWorkerOverrides(t *testing.T) {
	clearWorkerEnv(t)
	t.Setenv("DB_PASSWORD", "secret")
	t.Setenv("DB_HOST", "db.internal")
	t.Setenv("DB_PORT", "6543")
	t.Setenv("DB_NAME", "jobs")
	t.Setenv("LOG_LEVEL", "debug")
	t.Setenv("REDIS_ADDR", "redis.internal:6380")
	t.Setenv("REDIS_DB", "3")
	t.Setenv("WORKER_PORT", "9091")
	t.Setenv("WORKER_CONCURRENCY", "7")
	t.Setenv("WORKER_CPU_CONCURRENCY", "3")
	t.Setenv("WORKER_SHUTDOWN_TIMEOUT", "45s")
	t.Setenv("WORKER_READ_HEADER_TIMEOUT", "2s")
	t.Setenv("WORKER_READ_TIMEOUT", "3s")
	t.Setenv("WORKER_WRITE_TIMEOUT", "4s")
	t.Setenv("WORKER_IDLE_TIMEOUT", "5s")

	cfg, err := LoadWorker()
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	if cfg.DBHost != "db.internal" || cfg.DBPort != "6543" || cfg.DBName != "jobs" {
		t.Fatalf("db overrides: %+v", cfg)
	}
	if cfg.RedisAddr != "redis.internal:6380" || cfg.RedisDB != 3 {
		t.Fatalf("redis overrides: %+v", cfg)
	}
	if cfg.WorkerPort != "9091" || cfg.WorkerConcurrency != 7 {
		t.Fatalf("worker overrides: %+v", cfg)
	}
	if cfg.WorkerCPUConcurrency != 3 {
		t.Fatalf("cpu concurrency override: %d", cfg.WorkerCPUConcurrency)
	}
	if cfg.WorkerShutdownTimeout != 45*time.Second ||
		cfg.WorkerReadHeaderTimeout != 2*time.Second ||
		cfg.WorkerReadTimeout != 3*time.Second ||
		cfg.WorkerWriteTimeout != 4*time.Second ||
		cfg.WorkerIdleTimeout != 5*time.Second {
		t.Fatalf("duration overrides: %+v", cfg)
	}
}

func TestLoadWorkerValidation(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want string
	}{
		{
			name: "missing password",
			env:  map[string]string{},
			want: "DB_PASSWORD",
		},
		{
			name: "invalid port",
			env:  map[string]string{"DB_PASSWORD": "secret", "WORKER_PORT": "70000"},
			want: "WORKER_PORT",
		},
		{
			name: "invalid concurrency",
			env:  map[string]string{"DB_PASSWORD": "secret", "WORKER_CONCURRENCY": "0"},
			want: "WORKER_CONCURRENCY",
		},
		{
			name: "invalid duration",
			env:  map[string]string{"DB_PASSWORD": "secret", "WORKER_SHUTDOWN_TIMEOUT": "soon"},
			want: "WORKER_SHUTDOWN_TIMEOUT",
		},
		{
			name: "invalid log level",
			env:  map[string]string{"DB_PASSWORD": "secret", "LOG_LEVEL": "loud"},
			want: "log level",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearWorkerEnv(t)
			for k, v := range tt.env {
				t.Setenv(k, v)
			}

			_, err := LoadWorker()
			if err == nil {
				t.Fatal("expected an error")
			}
			if !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(tt.want)) {
				t.Fatalf("error %q does not mention %q", err, tt.want)
			}
		})
	}
}

func TestWorkerConfigDSN(t *testing.T) {
	cfg := WorkerConfig{
		DBHost: "localhost", DBPort: "5432", DBUser: "postgres",
		DBPassword: "p@ss word", DBName: "gregport",
	}
	got := cfg.DSN()
	if !strings.HasPrefix(got, "postgres://postgres:") || !strings.Contains(got, "@localhost:5432/gregport") {
		t.Fatalf("dsn: %s", got)
	}
	if !strings.Contains(got, "sslmode=disable") {
		t.Fatalf("dsn missing sslmode: %s", got)
	}
}
