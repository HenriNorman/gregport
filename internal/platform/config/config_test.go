package config

import (
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
}

func setRequired(t *testing.T) {
	t.Helper()
	t.Setenv("DB_PASSWORD", "test-password")
	t.Setenv("JWT_SECRET", strings.Repeat("x", 32))
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
}

func TestLoadRejectsMissingRequiredValues(t *testing.T) {
	setDefaults(t)
	t.Setenv("DB_PASSWORD", "")
	t.Setenv("JWT_SECRET", "")

	_, err := Load()
	if err == nil {
		t.Fatal("expected error")
	}
	assertContains(t, err, "DB_PASSWORD is required")
	assertContains(t, err, "JWT_SECRET must be at least 32 bytes")
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

func assertContains(t *testing.T, err error, want string) {
	t.Helper()
	if !strings.Contains(err.Error(), want) {
		t.Errorf("error %q does not contain %q", err, want)
	}
}
