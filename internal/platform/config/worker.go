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

	OutboxInterval     time.Duration
	OutboxBatch        int
	QueueStatsInterval time.Duration

	SLABreachCheckInterval time.Duration
	SLABreachCheckBatch    int

	AutoCloseWindow        time.Duration
	AutoCloseCheckInterval time.Duration
	AutoCloseCheckBatch    int

	HousekeepingInterval time.Duration
	HousekeepingBatch    int

	TicketEventRetention       time.Duration
	AuditLogRetention          time.Duration
	EmailDeliveryRetention     time.Duration
	PendingAttachmentRetention time.Duration
	DeletedTicketRetention     time.Duration

	IDempotencyTTL time.Duration

	SMTPHost     string
	SMTPPort     int
	SMTPUser     string
	SMTPPassword string
	SMTPFrom     string
	SMTPFromName string

	// RatingTokenSecret signs the satisfaction link in solved-ticket email.
	RatingTokenSecret string
	RatingTokenTTL    time.Duration
	PublicAppURL      string

	// Object storage for attachments, thumbnails and exports.
	S3Endpoint            string
	S3AccessKey           string
	S3SecretKey           string
	S3Bucket              string
	S3Region              string
	S3UseSSL              bool
	PresignDownloadTTL    time.Duration
	ExportLinkTTL         time.Duration
	AttachmentPendingTTL  time.Duration
	MaxAttachmentBytes    int
	ExportBatchSize       int
	ThumbnailMaxDimension int

	// Elasticsearch index-sync settings. The worker writes derived documents
	// and never blocks the API's ticket writes on them.
	ElasticsearchURL      string
	ElasticsearchIndex    string
	ElasticsearchUsername string
	ElasticsearchPassword string
	ElasticsearchAPIKey   string
	ElasticsearchTimeout  time.Duration
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

		QueueStatsInterval: time.Minute,

		SLABreachCheckInterval: time.Minute,
		SLABreachCheckBatch:    100,

		AutoCloseWindow:        7 * 24 * time.Hour,
		AutoCloseCheckInterval: time.Hour,
		AutoCloseCheckBatch:    100,

		HousekeepingInterval: time.Hour,
		HousekeepingBatch:    100,

		TicketEventRetention:       720 * time.Hour,
		AuditLogRetention:          2160 * time.Hour,
		EmailDeliveryRetention:     720 * time.Hour,
		PendingAttachmentRetention: 24 * time.Hour,
		DeletedTicketRetention:     720 * time.Hour,

		IDempotencyTTL: 24 * time.Hour,

		SMTPHost:     getEnv("SMTP_HOST", "localhost"),
		SMTPPort:     2525,
		SMTPUser:     os.Getenv("SMTP_USER"),
		SMTPPassword: os.Getenv("SMTP_PASSWORD"),
		SMTPFrom:     getEnv("SMTP_FROM", "no-reply@localhost"),
		SMTPFromName: getEnv("SMTP_FROM_NAME", "Gregport"),

		RatingTokenSecret: os.Getenv("RATING_TOKEN_SECRET"),
		RatingTokenTTL:    30 * 24 * time.Hour,
		PublicAppURL:      getEnv("PUBLIC_APP_URL", "http://localhost:8080"),

		S3Endpoint:            getEnv("S3_ENDPOINT", "127.0.0.1:9000"),
		S3AccessKey:           getEnv("S3_ACCESS_KEY", "minioadmin"),
		S3SecretKey:           getEnv("S3_SECRET_KEY", "minioadmin"),
		S3Bucket:              getEnv("S3_BUCKET", "gregport-attachments"),
		S3Region:              getEnv("S3_REGION", "us-east-1"),
		S3UseSSL:              false,
		PresignDownloadTTL:    2 * time.Minute,
		ExportLinkTTL:         24 * time.Hour,
		AttachmentPendingTTL:  time.Hour,
		MaxAttachmentBytes:    10 << 20,
		ExportBatchSize:       1000,
		ThumbnailMaxDimension: 512,

		ElasticsearchURL:      getEnv("ELASTICSEARCH_URL", "http://localhost:9200"),
		ElasticsearchIndex:    getEnv("ELASTICSEARCH_INDEX", "tickets"),
		ElasticsearchUsername: os.Getenv("ELASTICSEARCH_USERNAME"),
		ElasticsearchPassword: os.Getenv("ELASTICSEARCH_PASSWORD"),
		ElasticsearchAPIKey:   os.Getenv("ELASTICSEARCH_API_KEY"),
		ElasticsearchTimeout:  3 * time.Second,
	}

	var errs []error

	if cfg.DBPassword == "" {
		errs = append(errs, errors.New("DB_PASSWORD is required"))
	}
	if len(cfg.RatingTokenSecret) < 32 {
		errs = append(errs, errors.New("RATING_TOKEN_SECRET must be at least 32 bytes"))
	}
	if value, err := envBool("S3_USE_SSL", cfg.S3UseSSL); err != nil {
		errs = append(errs, err)
	} else {
		cfg.S3UseSSL = value
	}
	if cfg.S3Endpoint == "" {
		errs = append(errs, errors.New("S3_ENDPOINT is required"))
	}
	if cfg.S3Bucket == "" {
		errs = append(errs, errors.New("S3_BUCKET is required"))
	}
	if cfg.S3AccessKey == "" || cfg.S3SecretKey == "" {
		errs = append(errs, errors.New("S3_ACCESS_KEY and S3_SECRET_KEY are required"))
	}
	if err := validateElasticsearch(
		cfg.ElasticsearchURL,
		cfg.ElasticsearchIndex,
		cfg.ElasticsearchUsername,
		cfg.ElasticsearchPassword,
		cfg.ElasticsearchAPIKey,
	); err != nil {
		errs = append(errs, err)
	}
	if err := validateAppURL(cfg.PublicAppURL); err != nil {
		errs = append(errs, err)
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
		name      string
		dst       *time.Duration
		allowZero bool
	}{
		{"REDIS_DIAL_TIMEOUT", &cfg.RedisDialTimeout, false},
		{"REDIS_READ_TIMEOUT", &cfg.RedisReadTimeout, false},
		{"REDIS_WRITE_TIMEOUT", &cfg.RedisWriteTimeout, false},
		{"WORKER_SHUTDOWN_TIMEOUT", &cfg.WorkerShutdownTimeout, false},
		{"WORKER_READ_HEADER_TIMEOUT", &cfg.WorkerReadHeaderTimeout, false},
		{"WORKER_READ_TIMEOUT", &cfg.WorkerReadTimeout, false},
		{"WORKER_WRITE_TIMEOUT", &cfg.WorkerWriteTimeout, false},
		{"WORKER_IDLE_TIMEOUT", &cfg.WorkerIdleTimeout, false},
		{"WORKER_OUTBOX_INTERVAL", &cfg.OutboxInterval, false},
		{"WORKER_QUEUE_STATS_INTERVAL", &cfg.QueueStatsInterval, true},
		{"SLA_BREACH_CHECK_INTERVAL", &cfg.SLABreachCheckInterval, true},
		{"AUTO_CLOSE_CHECK_INTERVAL", &cfg.AutoCloseCheckInterval, true},
		{"AUTO_CLOSE_WINDOW", &cfg.AutoCloseWindow, true},
		{"HOUSEKEEPING_INTERVAL", &cfg.HousekeepingInterval, true},
		{"TICKET_EVENT_RETENTION", &cfg.TicketEventRetention, true},
		{"AUDIT_LOG_RETENTION", &cfg.AuditLogRetention, true},
		{"EMAIL_DELIVERY_RETENTION", &cfg.EmailDeliveryRetention, true},
		{"PENDING_ATTACHMENT_RETENTION", &cfg.PendingAttachmentRetention, true},
		{"DELETED_TICKET_RETENTION", &cfg.DeletedTicketRetention, true},
		{"RATING_TOKEN_TTL", &cfg.RatingTokenTTL, false},
		{"IDEMPOTENCY_TTL", &cfg.IDempotencyTTL, true},
		{"PRESIGN_DOWNLOAD_TTL", &cfg.PresignDownloadTTL, false},
		{"EXPORT_LINK_TTL", &cfg.ExportLinkTTL, false},
		{"ATTACHMENT_PENDING_TTL", &cfg.AttachmentPendingTTL, false},
		{"ELASTICSEARCH_TIMEOUT", &cfg.ElasticsearchTimeout, false},
	}
	for _, d := range durations {
		value, err := envDuration(d.name, *d.dst, d.allowZero)
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
		{"SLA_BREACH_CHECK_BATCH", &cfg.SLABreachCheckBatch, 1},
		{"AUTO_CLOSE_CHECK_BATCH", &cfg.AutoCloseCheckBatch, 1},
		{"HOUSEKEEPING_BATCH", &cfg.HousekeepingBatch, 1},
		{"SMTP_PORT", &cfg.SMTPPort, 1},
		{"MAX_ATTACHMENT_BYTES", &cfg.MaxAttachmentBytes, 1},
		{"EXPORT_BATCH_SIZE", &cfg.ExportBatchSize, 1},
		{"THUMBNAIL_MAX_DIMENSION", &cfg.ThumbnailMaxDimension, 16},
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
