package config

import (
	"net/netip"
	"net/url"
	"strings"
	"testing"
	"time"
)

var optionalKeys = []string{
	"DB_HOST",
	"DB_PORT",
	"DB_USER",
	"DB_NAME",
	"APP_PORT",
	"LOG_LEVEL",
	"ACCESS_TOKEN_TTL",
	"REFRESH_TOKEN_TTL",
	"SERVER_READ_HEADER_TIMEOUT",
	"SERVER_READ_TIMEOUT",
	"SERVER_WRITE_TIMEOUT",
	"SERVER_IDLE_TIMEOUT",
	"REQUEST_TIMEOUT",
	"SHUTDOWN_TIMEOUT",
	"REDIS_ADDR",
	"REDIS_PASSWORD",
	"REDIS_DB",
	"REDIS_DIAL_TIMEOUT",
	"REDIS_READ_TIMEOUT",
	"REDIS_WRITE_TIMEOUT",
	"CACHE_TIMEOUT",
	"CACHE_TTL_SETTINGS",
	"CACHE_TTL_SLA_POLICIES",
	"CACHE_TTL_DASHBOARD_COUNTS",
	"RATE_LIMIT_REQUESTS",
	"RATE_LIMIT_WINDOW",
	"PUBLIC_RATE_LIMIT_REQUESTS",
	"PUBLIC_RATE_LIMIT_WINDOW",
	"LOGIN_RATE_LIMIT_REQUESTS",
	"LOGIN_RATE_LIMIT_WINDOW",
	"RATING_RATE_LIMIT_REQUESTS",
	"RATING_RATE_LIMIT_WINDOW",
	"RATING_TOKEN_TTL",
	"PUBLIC_APP_URL",
	"TRUST_PROXY_HEADERS",
	"TRUSTED_PROXY_CIDRS",
	"IDEMPOTENCY_TTL",
	"ELASTICSEARCH_URL",
	"ELASTICSEARCH_INDEX",
	"ELASTICSEARCH_USERNAME",
	"ELASTICSEARCH_PASSWORD",
	"ELASTICSEARCH_API_KEY",
	"ELASTICSEARCH_TIMEOUT",
}

func setRequired(t *testing.T) {
	t.Helper()
	t.Setenv("DB_PASSWORD", "test-password")
	t.Setenv("JWT_SECRET", strings.Repeat("x", 32))
	t.Setenv("RATING_TOKEN_SECRET", strings.Repeat("r", 32))
}

func setDefaults(t *testing.T) {
	t.Helper()
	setRequired(t)
	for _, key := range optionalKeys {
		t.Setenv(key, "")
	}
}

func TestLoadDefaults(t *testing.T) {
	setDefaults(t)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	checks := []struct {
		name string
		got  any
		want any
	}{
		{"DBHost", cfg.DBHost, "localhost"},
		{"DBPort", cfg.DBPort, "5432"},
		{"DBUser", cfg.DBUser, "postgres"},
		{"DBName", cfg.DBName, "gregport"},
		{"AppPort", cfg.AppPort, "8080"},
		{"LogLevel", cfg.LogLevel, "info"},
		{"AccessTokenTTL", cfg.AccessTokenTTL, 15 * time.Minute},
		{"RefreshTokenTTL", cfg.RefreshTokenTTL, 30 * 24 * time.Hour},
		{"ServerReadHeaderTimeout", cfg.ServerReadHeaderTimeout, 5 * time.Second},
		{"ServerReadTimeout", cfg.ServerReadTimeout, 15 * time.Second},
		{"ServerWriteTimeout", cfg.ServerWriteTimeout, 30 * time.Second},
		{"ServerIdleTimeout", cfg.ServerIdleTimeout, 60 * time.Second},
		{"RequestTimeout", cfg.RequestTimeout, 10 * time.Second},
		{"ShutdownTimeout", cfg.ShutdownTimeout, 15 * time.Second},
		{"RedisAddr", cfg.RedisAddr, "localhost:6379"},
		{"RedisPassword", cfg.RedisPassword, ""},
		{"RedisDB", cfg.RedisDB, 0},
		{"RedisDialTimeout", cfg.RedisDialTimeout, time.Second},
		{"RedisReadTimeout", cfg.RedisReadTimeout, 500 * time.Millisecond},
		{"RedisWriteTimeout", cfg.RedisWriteTimeout, 500 * time.Millisecond},
		{"CacheTimeout", cfg.CacheTimeout, 200 * time.Millisecond},
		{"CacheTTLSettings", cfg.CacheTTLSettings, 5 * time.Minute},
		{"CacheTTLSLAPolicies", cfg.CacheTTLSLAPolicies, 5 * time.Minute},
		{"CacheTTLDashboardCounts", cfg.CacheTTLDashboardCounts, 45 * time.Second},
		{"RateLimitRequests", cfg.RateLimitRequests, 100},
		{"RateLimitWindow", cfg.RateLimitWindow, time.Minute},
		{"PublicRateLimitRequests", cfg.PublicRateLimitRequests, 30},
		{"PublicRateLimitWindow", cfg.PublicRateLimitWindow, time.Minute},
		{"LoginRateLimitRequests", cfg.LoginRateLimitRequests, 5},
		{"LoginRateLimitWindow", cfg.LoginRateLimitWindow, 15 * time.Minute},
		{"RatingRateLimitRequests", cfg.RatingRateLimitRequests, 5},
		{"RatingRateLimitWindow", cfg.RatingRateLimitWindow, 15 * time.Minute},
		{"RatingTokenTTL", cfg.RatingTokenTTL, 30 * 24 * time.Hour},
		{"PublicAppURL", cfg.PublicAppURL, "http://localhost:8080"},
		{"TrustProxyHeaders", cfg.TrustProxyHeaders, false},
		{"TrustedProxyCIDRs", len(cfg.TrustedProxyCIDRs), 0},
		{"IdempotencyTTL", cfg.IdempotencyTTL, 24 * time.Hour},
		{"ElasticsearchURL", cfg.ElasticsearchURL, "http://localhost:9200"},
		{"ElasticsearchIndex", cfg.ElasticsearchIndex, "tickets"},
		{"ElasticsearchTimeout", cfg.ElasticsearchTimeout, 3 * time.Second},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, c.got, c.want)
		}
	}
}

func TestLoadOverrides(t *testing.T) {
	setDefaults(t)
	t.Setenv("DB_HOST", "db.internal")
	t.Setenv("DB_PORT", "5433")
	t.Setenv("DB_USER", "app")
	t.Setenv("DB_PASSWORD", "secret")
	t.Setenv("DB_NAME", "tickets")
	t.Setenv("APP_PORT", "9090")
	t.Setenv("LOG_LEVEL", "DEBUG")
	t.Setenv("ACCESS_TOKEN_TTL", "5m")
	t.Setenv("REFRESH_TOKEN_TTL", "1h")
	t.Setenv("SERVER_READ_HEADER_TIMEOUT", "1s")
	t.Setenv("SERVER_READ_TIMEOUT", "2s")
	t.Setenv("SERVER_WRITE_TIMEOUT", "3s")
	t.Setenv("SERVER_IDLE_TIMEOUT", "4s")
	t.Setenv("REQUEST_TIMEOUT", "500ms")
	t.Setenv("SHUTDOWN_TIMEOUT", "5s")
	t.Setenv("REDIS_ADDR", "redis.internal:6380")
	t.Setenv("REDIS_PASSWORD", "redis-secret")
	t.Setenv("REDIS_DB", "3")
	t.Setenv("REDIS_DIAL_TIMEOUT", "150ms")
	t.Setenv("REDIS_READ_TIMEOUT", "250ms")
	t.Setenv("REDIS_WRITE_TIMEOUT", "350ms")
	t.Setenv("CACHE_TIMEOUT", "100ms")
	t.Setenv("CACHE_TTL_SETTINGS", "2m")
	t.Setenv("CACHE_TTL_SLA_POLICIES", "3m")
	t.Setenv("CACHE_TTL_DASHBOARD_COUNTS", "30s")
	t.Setenv("RATE_LIMIT_REQUESTS", "200")
	t.Setenv("RATE_LIMIT_WINDOW", "2m")
	t.Setenv("PUBLIC_RATE_LIMIT_REQUESTS", "40")
	t.Setenv("PUBLIC_RATE_LIMIT_WINDOW", "90s")
	t.Setenv("LOGIN_RATE_LIMIT_REQUESTS", "7")
	t.Setenv("LOGIN_RATE_LIMIT_WINDOW", "10m")
	t.Setenv("RATING_RATE_LIMIT_REQUESTS", "3")
	t.Setenv("RATING_RATE_LIMIT_WINDOW", "1m")
	t.Setenv("RATING_TOKEN_TTL", "168h")
	t.Setenv("PUBLIC_APP_URL", "https://support.example.com")
	t.Setenv("TRUST_PROXY_HEADERS", "true")
	t.Setenv("TRUSTED_PROXY_CIDRS", "10.0.0.0/8, 192.168.0.0/16")
	t.Setenv("IDEMPOTENCY_TTL", "12h")
	t.Setenv("ELASTICSEARCH_URL", "https://es.internal:9200")
	t.Setenv("ELASTICSEARCH_INDEX", "support-tickets")
	t.Setenv("ELASTICSEARCH_USERNAME", "elastic")
	t.Setenv("ELASTICSEARCH_PASSWORD", "es-secret")
	t.Setenv("ELASTICSEARCH_TIMEOUT", "1500ms")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.DBHost != "db.internal" || cfg.DBPort != "5433" || cfg.DBUser != "app" {
		t.Errorf("db connection: %+v", cfg)
	}
	if cfg.DBPassword != "secret" || cfg.DBName != "tickets" {
		t.Errorf("db credentials: %+v", cfg)
	}
	if cfg.AppPort != "9090" || cfg.LogLevel != "DEBUG" {
		t.Errorf("app settings: %+v", cfg)
	}
	if cfg.AccessTokenTTL != 5*time.Minute || cfg.RefreshTokenTTL != time.Hour {
		t.Errorf("token ttl: %s %s", cfg.AccessTokenTTL, cfg.RefreshTokenTTL)
	}
	if cfg.ServerReadHeaderTimeout != time.Second ||
		cfg.ServerReadTimeout != 2*time.Second ||
		cfg.ServerWriteTimeout != 3*time.Second ||
		cfg.ServerIdleTimeout != 4*time.Second ||
		cfg.RequestTimeout != 500*time.Millisecond ||
		cfg.ShutdownTimeout != 5*time.Second {
		t.Errorf("timeouts: %+v", cfg)
	}
	if cfg.RedisAddr != "redis.internal:6380" || cfg.RedisPassword != "redis-secret" || cfg.RedisDB != 3 {
		t.Errorf("redis connection: %+v", cfg)
	}
	if cfg.RedisDialTimeout != 150*time.Millisecond ||
		cfg.RedisReadTimeout != 250*time.Millisecond ||
		cfg.RedisWriteTimeout != 350*time.Millisecond ||
		cfg.CacheTimeout != 100*time.Millisecond {
		t.Errorf("redis timeouts: %+v", cfg)
	}
	if cfg.CacheTTLSettings != 2*time.Minute ||
		cfg.CacheTTLSLAPolicies != 3*time.Minute ||
		cfg.CacheTTLDashboardCounts != 30*time.Second {
		t.Errorf("cache ttls: %+v", cfg)
	}
	if cfg.RateLimitRequests != 200 || cfg.RateLimitWindow != 2*time.Minute ||
		cfg.PublicRateLimitRequests != 40 || cfg.PublicRateLimitWindow != 90*time.Second ||
		cfg.LoginRateLimitRequests != 7 || cfg.LoginRateLimitWindow != 10*time.Minute ||
		cfg.RatingRateLimitRequests != 3 || cfg.RatingRateLimitWindow != time.Minute {
		t.Errorf("rate limits: %+v", cfg)
	}
	if cfg.RatingTokenTTL != 168*time.Hour || cfg.PublicAppURL != "https://support.example.com" {
		t.Errorf("rating settings: %+v", cfg)
	}
	if !cfg.TrustProxyHeaders {
		t.Error("trust proxy headers was not enabled")
	}
	wantPrefixes := []netip.Prefix{
		netip.MustParsePrefix("10.0.0.0/8"),
		netip.MustParsePrefix("192.168.0.0/16"),
	}
	if len(cfg.TrustedProxyCIDRs) != len(wantPrefixes) {
		t.Fatalf("trusted proxies: %+v", cfg.TrustedProxyCIDRs)
	}
	for i, want := range wantPrefixes {
		if cfg.TrustedProxyCIDRs[i] != want {
			t.Errorf("trusted proxy %d: got %s, want %s", i, cfg.TrustedProxyCIDRs[i], want)
		}
	}
	if cfg.IdempotencyTTL != 12*time.Hour {
		t.Errorf("idempotency ttl: %s", cfg.IdempotencyTTL)
	}
	if cfg.ElasticsearchURL != "https://es.internal:9200" ||
		cfg.ElasticsearchIndex != "support-tickets" ||
		cfg.ElasticsearchUsername != "elastic" ||
		cfg.ElasticsearchPassword != "es-secret" ||
		cfg.ElasticsearchTimeout != 1500*time.Millisecond {
		t.Errorf("elasticsearch settings: %+v", cfg)
	}
}

func TestLoadRejectsInvalidElasticsearchSettings(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
	}{
		{
			name: "unsupported scheme",
			env:  map[string]string{"ELASTICSEARCH_URL": "ftp://localhost:9200"},
		},
		{
			name: "missing host",
			env:  map[string]string{"ELASTICSEARCH_URL": "http://"},
		},
		{
			name: "invalid index",
			env:  map[string]string{"ELASTICSEARCH_INDEX": "Tickets"},
		},
		{
			name: "half credentials",
			env:  map[string]string{"ELASTICSEARCH_USERNAME": "elastic"},
		},
		{
			name: "api key and basic auth",
			env: map[string]string{
				"ELASTICSEARCH_USERNAME": "elastic",
				"ELASTICSEARCH_PASSWORD": "secret",
				"ELASTICSEARCH_API_KEY":  "key",
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setDefaults(t)
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			if _, err := Load(); err == nil {
				t.Fatal("Load = nil error, want validation error")
			}
		})
	}
}

func TestLoadRejectsMissingRequiredValues(t *testing.T) {
	setDefaults(t)
	t.Setenv("DB_PASSWORD", "")
	t.Setenv("JWT_SECRET", "")
	t.Setenv("RATING_TOKEN_SECRET", "")

	_, err := Load()
	if err == nil {
		t.Fatal("expected error")
	}
	assertContains(t, err, "DB_PASSWORD is required")
	assertContains(t, err, "JWT_SECRET must be at least 32 bytes")
	assertContains(t, err, "RATING_TOKEN_SECRET must be at least 32 bytes")
}

func TestLoadRejectsShortRatingTokenSecret(t *testing.T) {
	setDefaults(t)
	t.Setenv("RATING_TOKEN_SECRET", "too-short")

	_, err := Load()
	if err == nil {
		t.Fatal("expected error")
	}
	assertContains(t, err, "RATING_TOKEN_SECRET must be at least 32 bytes")
}

func TestLoadRejectsInvalidPublicAppURL(t *testing.T) {
	tests := []string{"not a url", "ftp://support.example.com", "/relative"}
	for _, raw := range tests {
		t.Run(raw, func(t *testing.T) {
			setDefaults(t)
			t.Setenv("PUBLIC_APP_URL", raw)

			_, err := Load()
			if err == nil {
				t.Fatal("expected error")
			}
			assertContains(t, err, "PUBLIC_APP_URL")
		})
	}
}

func TestLoadRejectsShortJWTSecret(t *testing.T) {
	setDefaults(t)
	t.Setenv("JWT_SECRET", "too-short")

	_, err := Load()
	if err == nil {
		t.Fatal("expected error")
	}
	assertContains(t, err, "JWT_SECRET must be at least 32 bytes")
}

func TestLoadRejectsInvalidLogLevel(t *testing.T) {
	setDefaults(t)
	t.Setenv("LOG_LEVEL", "verbose")

	_, err := Load()
	if err == nil {
		t.Fatal("expected error")
	}
	assertContains(t, err, `invalid log level "verbose"`)
}

func TestLoadRejectsInvalidPorts(t *testing.T) {
	setDefaults(t)
	t.Setenv("DB_PORT", "not-a-port")
	t.Setenv("APP_PORT", "70000")

	_, err := Load()
	if err == nil {
		t.Fatal("expected error")
	}
	assertContains(t, err, "DB_PORT must be a port between 1 and 65535")
	assertContains(t, err, "APP_PORT must be a port between 1 and 65535")
}

func TestLoadRejectsInvalidDurations(t *testing.T) {
	setDefaults(t)
	t.Setenv("SERVER_READ_TIMEOUT", "0s")
	t.Setenv("SERVER_IDLE_TIMEOUT", "-5s")
	t.Setenv("REQUEST_TIMEOUT", "banana")

	_, err := Load()
	if err == nil {
		t.Fatal("expected error")
	}
	assertContains(t, err, "SERVER_READ_TIMEOUT must be greater than zero")
	assertContains(t, err, "SERVER_IDLE_TIMEOUT must be greater than zero")
	assertContains(t, err, "REQUEST_TIMEOUT")
}

func TestLoadAllowsDisabledRequestTimeout(t *testing.T) {
	setDefaults(t)
	t.Setenv("REQUEST_TIMEOUT", "0s")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.RequestTimeout != 0 {
		t.Errorf("request timeout: %s", cfg.RequestTimeout)
	}
}

func TestLoadRejectsTimeoutOrdering(t *testing.T) {
	setDefaults(t)
	t.Setenv("REQUEST_TIMEOUT", "30s")
	t.Setenv("SERVER_WRITE_TIMEOUT", "10s")
	t.Setenv("SHUTDOWN_TIMEOUT", "5s")

	_, err := Load()
	if err == nil {
		t.Fatal("expected error")
	}
	assertContains(t, err, "REQUEST_TIMEOUT (30s) must be less than SERVER_WRITE_TIMEOUT (10s)")
	assertContains(t, err, "SHUTDOWN_TIMEOUT (5s) must be at least REQUEST_TIMEOUT (30s)")
}

func TestLoadJoinsAllErrors(t *testing.T) {
	setDefaults(t)
	t.Setenv("DB_PASSWORD", "")
	t.Setenv("JWT_SECRET", "short")
	t.Setenv("LOG_LEVEL", "loud")
	t.Setenv("APP_PORT", "0")
	t.Setenv("SERVER_WRITE_TIMEOUT", "never")

	_, err := Load()
	if err == nil {
		t.Fatal("expected error")
	}
	for _, want := range []string{
		"DB_PASSWORD is required",
		"JWT_SECRET must be at least 32 bytes",
		`invalid log level "loud"`,
		"APP_PORT must be a port between 1 and 65535",
		"SERVER_WRITE_TIMEOUT",
	} {
		assertContains(t, err, want)
	}
}

func TestDSNEscapesCredentials(t *testing.T) {
	setDefaults(t)
	t.Setenv("DB_HOST", "db.internal")
	t.Setenv("DB_PORT", "5433")
	t.Setenv("DB_USER", "user@example.com")
	t.Setenv("DB_PASSWORD", "p@ss:w/rd?#%")
	t.Setenv("DB_NAME", "gregport")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	dsn := cfg.DSN()
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("parse DSN %q: %v", dsn, err)
	}

	if u.Scheme != "postgres" {
		t.Errorf("scheme: %q", u.Scheme)
	}
	if u.User.Username() != "user@example.com" {
		t.Errorf("user: %q", u.User.Username())
	}
	if password, _ := u.User.Password(); password != "p@ss:w/rd?#%" {
		t.Errorf("password: %q", password)
	}
	if u.Host != "db.internal:5433" {
		t.Errorf("host: %q", u.Host)
	}
	if u.Path != "/gregport" {
		t.Errorf("path: %q", u.Path)
	}
	if u.Query().Get("sslmode") != "disable" {
		t.Errorf("sslmode: %q", u.Query().Get("sslmode"))
	}
	if strings.Contains(dsn, "p@ss:w/rd?#%") {
		t.Errorf("password was not escaped: %s", dsn)
	}
}

func TestDSNIPv6Host(t *testing.T) {
	setDefaults(t)
	t.Setenv("DB_HOST", "::1")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	u, err := url.Parse(cfg.DSN())
	if err != nil {
		t.Fatalf("parse DSN: %v", err)
	}
	if u.Host != "[::1]:5432" {
		t.Errorf("host: %q", u.Host)
	}
}

func TestLoadRejectsInvalidRedisAndLimiterValues(t *testing.T) {
	setDefaults(t)
	t.Setenv("REDIS_DB", "-1")
	t.Setenv("RATE_LIMIT_REQUESTS", "0")
	t.Setenv("PUBLIC_RATE_LIMIT_REQUESTS", "nope")
	t.Setenv("LOGIN_RATE_LIMIT_REQUESTS", "-5")
	t.Setenv("CACHE_TIMEOUT", "0s")
	t.Setenv("IDEMPOTENCY_TTL", "-1h")
	t.Setenv("TRUST_PROXY_HEADERS", "maybe")

	_, err := Load()
	if err == nil {
		t.Fatal("expected error")
	}
	for _, want := range []string{
		"REDIS_DB must be at least 0",
		"RATE_LIMIT_REQUESTS must be at least 1",
		"PUBLIC_RATE_LIMIT_REQUESTS must be an integer",
		"LOGIN_RATE_LIMIT_REQUESTS must be at least 1",
		"CACHE_TIMEOUT must be greater than zero",
		"IDEMPOTENCY_TTL must be greater than zero",
		"TRUST_PROXY_HEADERS must be true or false",
	} {
		assertContains(t, err, want)
	}
}

func TestLoadRejectsInvalidTrustedProxyCIDR(t *testing.T) {
	setDefaults(t)
	t.Setenv("TRUSTED_PROXY_CIDRS", "10.0.0.0/8,not-a-cidr")

	_, err := Load()
	if err == nil {
		t.Fatal("expected error")
	}
	assertContains(t, err, `TRUSTED_PROXY_CIDRS contains invalid CIDR "not-a-cidr"`)
}

func TestLoadRequiresCIDRsWhenProxyHeadersEnabled(t *testing.T) {
	setDefaults(t)
	t.Setenv("TRUST_PROXY_HEADERS", "true")

	_, err := Load()
	if err == nil {
		t.Fatal("expected error")
	}
	assertContains(t, err, "TRUSTED_PROXY_CIDRS is required when TRUST_PROXY_HEADERS is true")
}

func assertContains(t *testing.T, err error, want string) {
	t.Helper()
	if !strings.Contains(err.Error(), want) {
		t.Errorf("error %q does not contain %q", err, want)
	}
}
