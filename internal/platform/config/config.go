package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
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
	}

	var errs []error

	if cfg.DBPassword == "" {
		errs = append(errs, errors.New("DB_PASSWORD is required"))
	}
	if len(cfg.JWTSecret) < 32 {
		errs = append(errs, errors.New("JWT_SECRET must be at least 32 bytes"))
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
