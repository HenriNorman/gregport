package config

import (
	"log"
	"os"
	"time"
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
}

func Load() Config {
	cfg := Config{
		DBHost:          getEnv("DB_HOST", "localhost"),
		DBPort:          getEnv("DB_PORT", "5432"),
		DBUser:          getEnv("DB_USER", "postgres"),
		DBPassword:      getEnv("DB_PASSWORD", "Henri123"),
		DBName:          getEnv("DB_NAME", "gregport"),
		AppPort:         getEnv("APP_PORT", "8080"),
		LogLevel:        getEnv("LOG_LEVEL", "info"),
		JWTSecret:       os.Getenv("JWT_SECRET"),
		AccessTokenTTL:  getEnvDuration("ACCESS_TOKEN_TTL", 15*time.Minute),
		RefreshTokenTTL: getEnvDuration("REFRESH_TOKEN_TTL", 30*24*time.Hour),

		ServerReadHeaderTimeout: getEnvDuration("SERVER_READ_HEADER_TIMEOUT", 5*time.Second),
		ServerReadTimeout:       getEnvDuration("SERVER_READ_TIMEOUT", 15*time.Second),
		ServerWriteTimeout:      getEnvDuration("SERVER_WRITE_TIMEOUT", 30*time.Second),
		ServerIdleTimeout:       getEnvDuration("SERVER_IDLE_TIMEOUT", 60*time.Second),
		RequestTimeout:          getEnvDuration("REQUEST_TIMEOUT", 10*time.Second),
	}

	if len(cfg.JWTSecret) < 32 {
		log.Fatal("JWT_SECRET must be set and at least 32 bytes")
	}

	return cfg
}

func (c Config) DSN() string {
	return "postgres://" + c.DBUser + ":" + c.DBPassword + "@" + c.DBHost + ":" + c.DBPort + "/" + c.DBName + "?sslmode=disable"
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func getEnvDuration(key string, fallback time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return fallback
}
