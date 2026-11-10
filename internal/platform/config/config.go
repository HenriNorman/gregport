package config

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/HenriNorman/gregport/internal/platform/logging"
)

type Config struct {
	DBHost          string
	DBPort          string
	DBUser          string
	DBPassword      string
	DBName          string
	AppPort         string
	LogLevel        string
	JWTSecret       string
	AccessTokenTTL  time.Duration
	RefreshTokenTTL time.Duration

	ServerReadHeaderTimeout time.Duration
	ServerReadTimeout       time.Duration
	ServerWriteTimeout      time.Duration
	ServerIdleTimeout       time.Duration
	RequestTimeout          time.Duration
	ShutdownTimeout         time.Duration

	RedisAddr         string
	RedisPassword     string
	RedisDB           int
	RedisDialTimeout  time.Duration
	RedisReadTimeout  time.Duration
	RedisWriteTimeout time.Duration
	CacheTimeout      time.Duration

	CacheTTLSettings        time.Duration
	CacheTTLSLAPolicies     time.Duration
	CacheTTLDashboardCounts time.Duration

	RateLimitRequests       int
	RateLimitWindow         time.Duration
	PublicRateLimitRequests int
	PublicRateLimitWindow   time.Duration
	LoginRateLimitRequests  int
	LoginRateLimitWindow    time.Duration
	RatingRateLimitRequests int
	RatingRateLimitWindow   time.Duration
	TrustProxyHeaders       bool
	TrustedProxyCIDRs       []netip.Prefix
	IdempotencyTTL          time.Duration

	// RatingTokenSecret signs the public satisfaction-rating links embedded in
	// solved-ticket email. It is separate from JWT_SECRET so rotating one does
	// not invalidate the other.
	RatingTokenSecret string
	RatingTokenTTL    time.Duration
	PublicAppURL      string

	// InboundEmailSecret verifies provider signatures on inbound email
	// webhooks (HMAC-SHA256 over timestamp + "." + body).
	InboundEmailSecret string

	// Object storage for attachments, thumbnails and exports.
	S3Endpoint           string
	S3AccessKey          string
	S3SecretKey          string
	S3Bucket             string
	S3Region             string
	S3UseSSL             bool
	PresignPutTTL        time.Duration
	PresignDownloadTTL   time.Duration
	ExportLinkTTL        time.Duration
	AttachmentPendingTTL time.Duration
	MaxAttachmentBytes   int

	// Elasticsearch is the primary full-text backend. The index is derived
	// from Postgres and can be rebuilt, so an unreachable cluster degrades
	// search without blocking ticket writes.
	ElasticsearchURL      string
	ElasticsearchIndex    string
	ElasticsearchUsername string
	ElasticsearchPassword string
	ElasticsearchAPIKey   string
	ElasticsearchTimeout  time.Duration
}

func Load() (Config, error) {
	cfg := Config{
		DBHost:          getEnv("DB_HOST", "localhost"),
		DBPort:          getEnv("DB_PORT", "5432"),
		DBUser:          getEnv("DB_USER", "postgres"),
		DBPassword:      os.Getenv("DB_PASSWORD"),
		DBName:          getEnv("DB_NAME", "gregport"),
		AppPort:         getEnv("APP_PORT", "8080"),
		LogLevel:        getEnv("LOG_LEVEL", "info"),
		JWTSecret:       os.Getenv("JWT_SECRET"),
		AccessTokenTTL:  15 * time.Minute,
		RefreshTokenTTL: 30 * 24 * time.Hour,

		ServerReadHeaderTimeout: 5 * time.Second,
		ServerReadTimeout:       15 * time.Second,
		ServerWriteTimeout:      30 * time.Second,
		ServerIdleTimeout:       60 * time.Second,
		RequestTimeout:          10 * time.Second,
		ShutdownTimeout:         15 * time.Second,

		RedisAddr:         getEnv("REDIS_ADDR", "localhost:6379"),
		RedisPassword:     os.Getenv("REDIS_PASSWORD"),
		RedisDB:           0,
		RedisDialTimeout:  time.Second,
		RedisReadTimeout:  500 * time.Millisecond,
		RedisWriteTimeout: 500 * time.Millisecond,
		CacheTimeout:      200 * time.Millisecond,

		CacheTTLSettings:        5 * time.Minute,
		CacheTTLSLAPolicies:     5 * time.Minute,
		CacheTTLDashboardCounts: 45 * time.Second,

		RateLimitRequests:       100,
		RateLimitWindow:         time.Minute,
		PublicRateLimitRequests: 30,
		PublicRateLimitWindow:   time.Minute,
		LoginRateLimitRequests:  5,
		LoginRateLimitWindow:    15 * time.Minute,
		RatingRateLimitRequests: 5,
		RatingRateLimitWindow:   15 * time.Minute,
		TrustProxyHeaders:       false,
		IdempotencyTTL:          24 * time.Hour,

		RatingTokenSecret:  os.Getenv("RATING_TOKEN_SECRET"),
		RatingTokenTTL:     30 * 24 * time.Hour,
		PublicAppURL:       getEnv("PUBLIC_APP_URL", "http://localhost:8080"),
		InboundEmailSecret: os.Getenv("INBOUND_EMAIL_SECRET"),

		S3Endpoint:           getEnv("S3_ENDPOINT", "127.0.0.1:9000"),
		S3AccessKey:          getEnv("S3_ACCESS_KEY", "minioadmin"),
		S3SecretKey:          getEnv("S3_SECRET_KEY", "minioadmin"),
		S3Bucket:             getEnv("S3_BUCKET", "gregport-attachments"),
		S3Region:             getEnv("S3_REGION", "us-east-1"),
		S3UseSSL:             false,
		PresignPutTTL:        5 * time.Minute,
		PresignDownloadTTL:   2 * time.Minute,
		ExportLinkTTL:        24 * time.Hour,
		AttachmentPendingTTL: time.Hour,
		MaxAttachmentBytes:   10 << 20,

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
	if len(cfg.JWTSecret) < 32 {
		errs = append(errs, errors.New("JWT_SECRET must be at least 32 bytes"))
	}
	if len(cfg.RatingTokenSecret) < 32 {
		errs = append(errs, errors.New("RATING_TOKEN_SECRET must be at least 32 bytes"))
	}
	if cfg.InboundEmailSecret != "" && len(cfg.InboundEmailSecret) < 32 {
		errs = append(errs, errors.New("INBOUND_EMAIL_SECRET must be at least 32 bytes"))
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
	if port, err := envPort("APP_PORT", cfg.AppPort); err != nil {
		errs = append(errs, err)
	} else {
		cfg.AppPort = port
	}

	durations := []struct {
		name      string
		dst       *time.Duration
		allowZero bool
	}{
		{"ACCESS_TOKEN_TTL", &cfg.AccessTokenTTL, false},
		{"REFRESH_TOKEN_TTL", &cfg.RefreshTokenTTL, false},
		{"SERVER_READ_HEADER_TIMEOUT", &cfg.ServerReadHeaderTimeout, false},
		{"SERVER_READ_TIMEOUT", &cfg.ServerReadTimeout, false},
		{"SERVER_WRITE_TIMEOUT", &cfg.ServerWriteTimeout, false},
		{"SERVER_IDLE_TIMEOUT", &cfg.ServerIdleTimeout, false},
		{"REQUEST_TIMEOUT", &cfg.RequestTimeout, true},
		{"SHUTDOWN_TIMEOUT", &cfg.ShutdownTimeout, false},
	}
	for _, d := range durations {
		value, err := envDuration(d.name, *d.dst, d.allowZero)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		*d.dst = value
	}

	redisDurations := []struct {
		name string
		dst  *time.Duration
	}{
		{"REDIS_DIAL_TIMEOUT", &cfg.RedisDialTimeout},
		{"REDIS_READ_TIMEOUT", &cfg.RedisReadTimeout},
		{"REDIS_WRITE_TIMEOUT", &cfg.RedisWriteTimeout},
		{"CACHE_TIMEOUT", &cfg.CacheTimeout},
		{"CACHE_TTL_SETTINGS", &cfg.CacheTTLSettings},
		{"CACHE_TTL_SLA_POLICIES", &cfg.CacheTTLSLAPolicies},
		{"CACHE_TTL_DASHBOARD_COUNTS", &cfg.CacheTTLDashboardCounts},
		{"RATE_LIMIT_WINDOW", &cfg.RateLimitWindow},
		{"PUBLIC_RATE_LIMIT_WINDOW", &cfg.PublicRateLimitWindow},
		{"LOGIN_RATE_LIMIT_WINDOW", &cfg.LoginRateLimitWindow},
		{"RATING_RATE_LIMIT_WINDOW", &cfg.RatingRateLimitWindow},
		{"RATING_TOKEN_TTL", &cfg.RatingTokenTTL},
		{"IDEMPOTENCY_TTL", &cfg.IdempotencyTTL},
		{"PRESIGN_PUT_TTL", &cfg.PresignPutTTL},
		{"PRESIGN_DOWNLOAD_TTL", &cfg.PresignDownloadTTL},
		{"EXPORT_LINK_TTL", &cfg.ExportLinkTTL},
		{"ATTACHMENT_PENDING_TTL", &cfg.AttachmentPendingTTL},
		{"ELASTICSEARCH_TIMEOUT", &cfg.ElasticsearchTimeout},
	}
	for _, d := range redisDurations {
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
		{"RATE_LIMIT_REQUESTS", &cfg.RateLimitRequests, 1},
		{"PUBLIC_RATE_LIMIT_REQUESTS", &cfg.PublicRateLimitRequests, 1},
		{"LOGIN_RATE_LIMIT_REQUESTS", &cfg.LoginRateLimitRequests, 1},
		{"RATING_RATE_LIMIT_REQUESTS", &cfg.RatingRateLimitRequests, 1},
		{"MAX_ATTACHMENT_BYTES", &cfg.MaxAttachmentBytes, 1},
	}
	for _, n := range ints {
		value, err := envInt(n.name, *n.dst, n.min)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		*n.dst = value
	}

	if value, err := envBool("TRUST_PROXY_HEADERS", cfg.TrustProxyHeaders); err != nil {
		errs = append(errs, err)
	} else {
		cfg.TrustProxyHeaders = value
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

	if cidrs, err := envCIDRs("TRUSTED_PROXY_CIDRS"); err != nil {
		errs = append(errs, err)
	} else {
		cfg.TrustedProxyCIDRs = cidrs
	}
	if cfg.TrustProxyHeaders && len(cfg.TrustedProxyCIDRs) == 0 {
		errs = append(errs, errors.New("TRUSTED_PROXY_CIDRS is required when TRUST_PROXY_HEADERS is true"))
	}

	if cfg.ServerWriteTimeout > 0 && cfg.RequestTimeout >= cfg.ServerWriteTimeout {
		errs = append(errs, fmt.Errorf(
			"REQUEST_TIMEOUT (%s) must be less than SERVER_WRITE_TIMEOUT (%s)",
			cfg.RequestTimeout, cfg.ServerWriteTimeout,
		))
	}
	if cfg.RequestTimeout > 0 && cfg.ShutdownTimeout < cfg.RequestTimeout {
		errs = append(errs, fmt.Errorf(
			"SHUTDOWN_TIMEOUT (%s) must be at least REQUEST_TIMEOUT (%s)",
			cfg.ShutdownTimeout, cfg.RequestTimeout,
		))
	}

	if len(errs) > 0 {
		return Config{}, errors.Join(errs...)
	}
	return cfg, nil
}

func (c Config) DSN() string {
	u := url.URL{
		Scheme:   "postgres",
		User:     url.UserPassword(c.DBUser, c.DBPassword),
		Host:     net.JoinHostPort(c.DBHost, c.DBPort),
		Path:     "/" + c.DBName,
		RawQuery: "sslmode=disable",
	}
	return u.String()
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// elasticsearchIndexRE matches an index or alias name. Elasticsearch requires
// lowercase names and forbids a leading -, _, +, so both the concrete
// versioned index (tickets-v1) and the stable alias (tickets) match.
var elasticsearchIndexRE = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)

// validateElasticsearch checks settings that would otherwise fail only at the
// first search: a URL with the wrong scheme, an invalid index name, or half of
// a credential pair. The cluster itself is allowed to be down at startup.
func validateElasticsearch(rawURL, index, username, password, apiKey string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("ELASTICSEARCH_URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return errors.New("ELASTICSEARCH_URL must use http or https")
	}
	if u.Host == "" {
		return errors.New("ELASTICSEARCH_URL must include a host")
	}
	if !elasticsearchIndexRE.MatchString(index) {
		return errors.New("ELASTICSEARCH_INDEX must be a lowercase index or alias name")
	}
	if (username == "") != (password == "") {
		return errors.New("ELASTICSEARCH_USERNAME and ELASTICSEARCH_PASSWORD must be set together")
	}
	if apiKey != "" && username != "" {
		return errors.New("set either ELASTICSEARCH_API_KEY or ELASTICSEARCH_USERNAME, not both")
	}
	return nil
}

func envDuration(name string, fallback time.Duration, allowZero bool) (time.Duration, error) {
	raw := os.Getenv(name)
	if raw == "" {
		return fallback, nil
	}

	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", name, err)
	}
	if d < 0 || (d == 0 && !allowZero) {
		return 0, fmt.Errorf("%s must be greater than zero", name)
	}
	return d, nil
}

func envPort(name, fallback string) (string, error) {
	raw := getEnv(name, fallback)

	port, err := strconv.Atoi(raw)
	if err != nil || port < 1 || port > 65535 {
		return "", fmt.Errorf("%s must be a port between 1 and 65535, got %q", name, raw)
	}
	return strconv.Itoa(port), nil
}

func envInt(name string, fallback, min int) (int, error) {
	raw := os.Getenv(name)
	if raw == "" {
		return fallback, nil
	}

	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer, got %q", name, raw)
	}
	if value < min {
		return 0, fmt.Errorf("%s must be at least %d, got %d", name, min, value)
	}
	return value, nil
}

func envBool(name string, fallback bool) (bool, error) {
	raw := os.Getenv(name)
	if raw == "" {
		return fallback, nil
	}

	value, err := strconv.ParseBool(raw)
	if err != nil {
		return false, fmt.Errorf("%s must be true or false, got %q", name, raw)
	}
	return value, nil
}

func envCIDRs(name string) ([]netip.Prefix, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return nil, nil
	}

	parts := strings.Split(raw, ",")
	prefixes := make([]netip.Prefix, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		prefix, err := netip.ParsePrefix(part)
		if err != nil {
			return nil, fmt.Errorf("%s contains invalid CIDR %q", name, part)
		}
		prefixes = append(prefixes, prefix)
	}
	return prefixes, nil
}

// validateAppURL requires an absolute http(s) URL so a malformed PUBLIC_APP_URL
// cannot produce broken rating links in outgoing email.
func validateAppURL(raw string) error {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return fmt.Errorf("PUBLIC_APP_URL: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("PUBLIC_APP_URL must use http or https, got %q", raw)
	}
	if parsed.Host == "" {
		return fmt.Errorf("PUBLIC_APP_URL must include a host, got %q", raw)
	}
	return nil
}
