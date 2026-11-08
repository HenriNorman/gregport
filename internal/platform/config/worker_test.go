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
		"WORKER_OUTBOX_INTERVAL", "WORKER_OUTBOX_BATCH",
		"WORKER_QUEUE_STATS_INTERVAL",
		"SLA_BREACH_CHECK_INTERVAL", "SLA_BREACH_CHECK_BATCH",
		"AUTO_CLOSE_CHECK_INTERVAL", "AUTO_CLOSE_WINDOW", "AUTO_CLOSE_CHECK_BATCH",
		"HOUSEKEEPING_INTERVAL", "HOUSEKEEPING_BATCH",
		"TICKET_EVENT_RETENTION", "AUDIT_LOG_RETENTION", "EMAIL_DELIVERY_RETENTION",
		"IDEMPOTENCY_TTL",
		"SMTP_HOST", "SMTP_PORT", "SMTP_USER", "SMTP_PASSWORD",
		"SMTP_FROM", "SMTP_FROM_NAME",
		"RATING_TOKEN_SECRET", "RATING_TOKEN_TTL", "PUBLIC_APP_URL",
		"JWT_SECRET",
	} {
		t.Setenv(key, "")
	}
	// A valid baseline secret keeps the many non-rating tests focused on their
	// own knob; tests that exercise the secret set it explicitly.
	t.Setenv("RATING_TOKEN_SECRET", strings.Repeat("r", 32))
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
	if cfg.OutboxInterval != time.Second || cfg.OutboxBatch != 100 {
		t.Fatalf("outbox defaults: interval=%s batch=%d", cfg.OutboxInterval, cfg.OutboxBatch)
	}
	if cfg.QueueStatsInterval != time.Minute {
		t.Fatalf("queue stats interval: %s", cfg.QueueStatsInterval)
	}
	if cfg.SMTPHost != "localhost" || cfg.SMTPPort != 2525 {
		t.Fatalf("smtp defaults: host=%s port=%d", cfg.SMTPHost, cfg.SMTPPort)
	}
	if cfg.SMTPFrom != "no-reply@localhost" || cfg.SMTPFromName != "Gregport" {
		t.Fatalf("smtp from defaults: %s %s", cfg.SMTPFrom, cfg.SMTPFromName)
	}
	if cfg.SLABreachCheckInterval != time.Minute {
		t.Fatalf("sla breach interval: %s", cfg.SLABreachCheckInterval)
	}
	if cfg.SLABreachCheckBatch != 100 {
		t.Fatalf("sla breach batch: %d", cfg.SLABreachCheckBatch)
	}
	if cfg.AutoCloseWindow != 7*24*time.Hour {
		t.Fatalf("auto close window: %s", cfg.AutoCloseWindow)
	}
	if cfg.AutoCloseCheckInterval != time.Hour {
		t.Fatalf("auto close check interval: %s", cfg.AutoCloseCheckInterval)
	}
	if cfg.AutoCloseCheckBatch != 100 {
		t.Fatalf("auto close check batch: %d", cfg.AutoCloseCheckBatch)
	}
	if cfg.HousekeepingInterval != time.Hour {
		t.Fatalf("housekeeping interval: %s", cfg.HousekeepingInterval)
	}
	if cfg.HousekeepingBatch != 100 {
		t.Fatalf("housekeeping batch: %d", cfg.HousekeepingBatch)
	}
	if cfg.TicketEventRetention != 720*time.Hour {
		t.Fatalf("ticket event retention: %s", cfg.TicketEventRetention)
	}
	if cfg.AuditLogRetention != 2160*time.Hour {
		t.Fatalf("audit log retention: %s", cfg.AuditLogRetention)
	}
	if cfg.EmailDeliveryRetention != 720*time.Hour {
		t.Fatalf("email delivery retention: %s", cfg.EmailDeliveryRetention)
	}
	if cfg.IDempotencyTTL != 24*time.Hour {
		t.Fatalf("idempotency TTL: %s", cfg.IDempotencyTTL)
	}
	if cfg.RatingTokenTTL != 30*24*time.Hour || cfg.PublicAppURL != "http://localhost:8080" {
		t.Fatalf("rating defaults: ttl=%s url=%s", cfg.RatingTokenTTL, cfg.PublicAppURL)
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
	t.Setenv("WORKER_OUTBOX_INTERVAL", "250ms")
	t.Setenv("WORKER_OUTBOX_BATCH", "25")
	t.Setenv("WORKER_QUEUE_STATS_INTERVAL", "30s")
	t.Setenv("SMTP_HOST", "smtp.example.com")
	t.Setenv("SMTP_PORT", "587")
	t.Setenv("SMTP_USER", "user")
	t.Setenv("SMTP_PASSWORD", "pass")
	t.Setenv("SMTP_FROM", "noreply@example.com")
	t.Setenv("SMTP_FROM_NAME", "Example")
	t.Setenv("SLA_BREACH_CHECK_INTERVAL", "45s")
	t.Setenv("SLA_BREACH_CHECK_BATCH", "50")
	t.Setenv("AUTO_CLOSE_CHECK_INTERVAL", "30m")
	t.Setenv("AUTO_CLOSE_WINDOW", "72h")
	t.Setenv("AUTO_CLOSE_CHECK_BATCH", "25")
	t.Setenv("HOUSEKEEPING_INTERVAL", "2h")
	t.Setenv("HOUSEKEEPING_BATCH", "75")
	t.Setenv("TICKET_EVENT_RETENTION", "48h")
	t.Setenv("AUDIT_LOG_RETENTION", "100h")
	t.Setenv("EMAIL_DELIVERY_RETENTION", "200h")
	t.Setenv("IDEMPOTENCY_TTL", "6h")
	t.Setenv("RATING_TOKEN_TTL", "48h")
	t.Setenv("PUBLIC_APP_URL", "https://support.example.com")

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
	if cfg.OutboxInterval != 250*time.Millisecond || cfg.OutboxBatch != 25 {
		t.Fatalf("outbox overrides: interval=%s batch=%d", cfg.OutboxInterval, cfg.OutboxBatch)
	}
	if cfg.QueueStatsInterval != 30*time.Second {
		t.Fatalf("queue stats override: %s", cfg.QueueStatsInterval)
	}
	if cfg.SMTPHost != "smtp.example.com" || cfg.SMTPPort != 587 {
		t.Fatalf("smtp host/port override: %s %d", cfg.SMTPHost, cfg.SMTPPort)
	}
	if cfg.SMTPUser != "user" || cfg.SMTPPassword != "pass" {
		t.Fatalf("smtp auth override: %s %s", cfg.SMTPUser, cfg.SMTPPassword)
	}
	if cfg.SMTPFrom != "noreply@example.com" || cfg.SMTPFromName != "Example" {
		t.Fatalf("smtp from override: %s %s", cfg.SMTPFrom, cfg.SMTPFromName)
	}
	if cfg.SLABreachCheckInterval != 45*time.Second {
		t.Fatalf("sla breach interval override: %s", cfg.SLABreachCheckInterval)
	}
	if cfg.SLABreachCheckBatch != 50 {
		t.Fatalf("sla breach batch override: %d", cfg.SLABreachCheckBatch)
	}
	if cfg.AutoCloseCheckInterval != 30*time.Minute {
		t.Fatalf("auto close check interval override: %s", cfg.AutoCloseCheckInterval)
	}
	if cfg.AutoCloseWindow != 3*24*time.Hour {
		t.Fatalf("auto close window override: %s", cfg.AutoCloseWindow)
	}
	if cfg.AutoCloseCheckBatch != 25 {
		t.Fatalf("auto close check batch override: %d", cfg.AutoCloseCheckBatch)
	}
	if cfg.HousekeepingInterval != 2*time.Hour {
		t.Fatalf("housekeeping interval override: %s", cfg.HousekeepingInterval)
	}
	if cfg.HousekeepingBatch != 75 {
		t.Fatalf("housekeeping batch override: %d", cfg.HousekeepingBatch)
	}
	if cfg.TicketEventRetention != 48*time.Hour {
		t.Fatalf("ticket event retention override: %s", cfg.TicketEventRetention)
	}
	if cfg.AuditLogRetention != 100*time.Hour {
		t.Fatalf("audit log retention override: %s", cfg.AuditLogRetention)
	}
	if cfg.EmailDeliveryRetention != 200*time.Hour {
		t.Fatalf("email delivery retention override: %s", cfg.EmailDeliveryRetention)
	}
	if cfg.IDempotencyTTL != 6*time.Hour {
		t.Fatalf("idempotency TTL override: %s", cfg.IDempotencyTTL)
	}
	if cfg.RatingTokenTTL != 48*time.Hour || cfg.PublicAppURL != "https://support.example.com" {
		t.Fatalf("rating overrides: ttl=%s url=%s", cfg.RatingTokenTTL, cfg.PublicAppURL)
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
		{
			name: "invalid outbox interval",
			env:  map[string]string{"DB_PASSWORD": "secret", "WORKER_OUTBOX_INTERVAL": "0s"},
			want: "WORKER_OUTBOX_INTERVAL",
		},
		{
			name: "invalid outbox batch",
			env:  map[string]string{"DB_PASSWORD": "secret", "WORKER_OUTBOX_BATCH": "0"},
			want: "WORKER_OUTBOX_BATCH",
		},
		{
			name: "invalid queue stats interval",
			env:  map[string]string{"DB_PASSWORD": "secret", "WORKER_QUEUE_STATS_INTERVAL": "often"},
			want: "WORKER_QUEUE_STATS_INTERVAL",
		},
		{
			name: "negative queue stats interval",
			env:  map[string]string{"DB_PASSWORD": "secret", "WORKER_QUEUE_STATS_INTERVAL": "-1s"},
			want: "WORKER_QUEUE_STATS_INTERVAL",
		},
		{
			name: "invalid sla breach interval",
			env:  map[string]string{"DB_PASSWORD": "secret", "SLA_BREACH_CHECK_INTERVAL": "soon"},
			want: "SLA_BREACH_CHECK_INTERVAL",
		},
		{
			name: "negative sla breach interval",
			env:  map[string]string{"DB_PASSWORD": "secret", "SLA_BREACH_CHECK_INTERVAL": "-1s"},
			want: "SLA_BREACH_CHECK_INTERVAL",
		},
		{
			name: "invalid sla breach batch",
			env:  map[string]string{"DB_PASSWORD": "secret", "SLA_BREACH_CHECK_BATCH": "0"},
			want: "SLA_BREACH_CHECK_BATCH",
		},
		{
			name: "invalid auto close check interval",
			env:  map[string]string{"DB_PASSWORD": "secret", "AUTO_CLOSE_CHECK_INTERVAL": "soon"},
			want: "AUTO_CLOSE_CHECK_INTERVAL",
		},
		{
			name: "negative auto close check interval",
			env:  map[string]string{"DB_PASSWORD": "secret", "AUTO_CLOSE_CHECK_INTERVAL": "-1s"},
			want: "AUTO_CLOSE_CHECK_INTERVAL",
		},
		{
			name: "invalid auto close window",
			env:  map[string]string{"DB_PASSWORD": "secret", "AUTO_CLOSE_WINDOW": "soon"},
			want: "AUTO_CLOSE_WINDOW",
		},
		{
			name: "negative auto close window",
			env:  map[string]string{"DB_PASSWORD": "secret", "AUTO_CLOSE_WINDOW": "-1s"},
			want: "AUTO_CLOSE_WINDOW",
		},
		{
			name: "invalid auto close batch",
			env:  map[string]string{"DB_PASSWORD": "secret", "AUTO_CLOSE_CHECK_BATCH": "0"},
			want: "AUTO_CLOSE_CHECK_BATCH",
		},
		{
			name: "invalid housekeeping interval",
			env:  map[string]string{"DB_PASSWORD": "secret", "HOUSEKEEPING_INTERVAL": "soon"},
			want: "HOUSEKEEPING_INTERVAL",
		},
		{
			name: "negative housekeeping interval",
			env:  map[string]string{"DB_PASSWORD": "secret", "HOUSEKEEPING_INTERVAL": "-1s"},
			want: "HOUSEKEEPING_INTERVAL",
		},
		{
			name: "invalid housekeeping batch",
			env:  map[string]string{"DB_PASSWORD": "secret", "HOUSEKEEPING_BATCH": "0"},
			want: "HOUSEKEEPING_BATCH",
		},
		{
			name: "invalid ticket event retention",
			env:  map[string]string{"DB_PASSWORD": "secret", "TICKET_EVENT_RETENTION": "soon"},
			want: "TICKET_EVENT_RETENTION",
		},
		{
			name: "invalid idempotency TTL",
			env:  map[string]string{"DB_PASSWORD": "secret", "IDEMPOTENCY_TTL": "soon"},
			want: "IDEMPOTENCY_TTL",
		},
		{
			name: "missing rating token secret",
			env:  map[string]string{"DB_PASSWORD": "secret", "RATING_TOKEN_SECRET": ""},
			want: "RATING_TOKEN_SECRET",
		},
		{
			name: "short rating token secret",
			env:  map[string]string{"DB_PASSWORD": "secret", "RATING_TOKEN_SECRET": "short"},
			want: "RATING_TOKEN_SECRET",
		},
		{
			name: "invalid public app url",
			env:  map[string]string{"DB_PASSWORD": "secret", "PUBLIC_APP_URL": "ftp://example.com"},
			want: "PUBLIC_APP_URL",
		},
		{
			name: "invalid rating token ttl",
			env:  map[string]string{"DB_PASSWORD": "secret", "RATING_TOKEN_TTL": "soon"},
			want: "RATING_TOKEN_TTL",
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

func TestLoadWorkerAllowsZeroQueueStatsInterval(t *testing.T) {
	clearWorkerEnv(t)
	t.Setenv("DB_PASSWORD", "secret")
	t.Setenv("WORKER_QUEUE_STATS_INTERVAL", "0s")

	cfg, err := LoadWorker()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.QueueStatsInterval != 0 {
		t.Fatalf("queue stats interval: %s, want 0", cfg.QueueStatsInterval)
	}
}

func TestLoadWorkerAllowsZeroSlaBreachInterval(t *testing.T) {
	clearWorkerEnv(t)
	t.Setenv("DB_PASSWORD", "secret")
	t.Setenv("SLA_BREACH_CHECK_INTERVAL", "0s")

	cfg, err := LoadWorker()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.SLABreachCheckInterval != 0 {
		t.Fatalf("sla breach interval: %s, want 0", cfg.SLABreachCheckInterval)
	}
}

func TestLoadWorkerAllowsZeroAutoCloseCheckInterval(t *testing.T) {
	clearWorkerEnv(t)
	t.Setenv("DB_PASSWORD", "secret")
	t.Setenv("AUTO_CLOSE_CHECK_INTERVAL", "0s")

	cfg, err := LoadWorker()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.AutoCloseCheckInterval != 0 {
		t.Fatalf("auto close check interval: %s, want 0", cfg.AutoCloseCheckInterval)
	}
}

func TestLoadWorkerAllowsZeroAutoCloseWindow(t *testing.T) {
	clearWorkerEnv(t)
	t.Setenv("DB_PASSWORD", "secret")
	t.Setenv("AUTO_CLOSE_WINDOW", "0s")

	cfg, err := LoadWorker()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.AutoCloseWindow != 0 {
		t.Fatalf("auto close window: %s, want 0", cfg.AutoCloseWindow)
	}
}

func TestLoadWorkerAllowsZeroHousekeepingInterval(t *testing.T) {
	clearWorkerEnv(t)
	t.Setenv("DB_PASSWORD", "secret")
	t.Setenv("HOUSEKEEPING_INTERVAL", "0s")

	cfg, err := LoadWorker()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.HousekeepingInterval != 0 {
		t.Fatalf("housekeeping interval: %s, want 0", cfg.HousekeepingInterval)
	}
}

func TestLoadWorkerAllowsZeroTicketEventRetention(t *testing.T) {
	clearWorkerEnv(t)
	t.Setenv("DB_PASSWORD", "secret")
	t.Setenv("TICKET_EVENT_RETENTION", "0s")

	cfg, err := LoadWorker()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.TicketEventRetention != 0 {
		t.Fatalf("ticket event retention: %s, want 0", cfg.TicketEventRetention)
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
