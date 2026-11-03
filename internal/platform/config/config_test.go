package config

import (
	"strings"
	"testing"
	"time"
)

func TestLoadTimeoutDefaults(t *testing.T) {
	t.Setenv("JWT_SECRET", strings.Repeat("x", 32))
	for _, key := range []string{
		"SERVER_READ_HEADER_TIMEOUT",
		"SERVER_READ_TIMEOUT",
		"SERVER_WRITE_TIMEOUT",
		"SERVER_IDLE_TIMEOUT",
		"REQUEST_TIMEOUT",
	} {
		t.Setenv(key, "")
	}

	cfg := Load()

	if cfg.ServerReadHeaderTimeout != 5*time.Second {
		t.Errorf("read header timeout: %s", cfg.ServerReadHeaderTimeout)
	}
	if cfg.ServerReadTimeout != 15*time.Second {
		t.Errorf("read timeout: %s", cfg.ServerReadTimeout)
	}
	if cfg.ServerWriteTimeout != 30*time.Second {
		t.Errorf("write timeout: %s", cfg.ServerWriteTimeout)
	}
	if cfg.ServerIdleTimeout != 60*time.Second {
		t.Errorf("idle timeout: %s", cfg.ServerIdleTimeout)
	}
	if cfg.RequestTimeout != 10*time.Second {
		t.Errorf("request timeout: %s", cfg.RequestTimeout)
	}
}

func TestLoadTimeoutOverrides(t *testing.T) {
	t.Setenv("JWT_SECRET", strings.Repeat("x", 32))
	t.Setenv("SERVER_READ_HEADER_TIMEOUT", "1s")
	t.Setenv("SERVER_READ_TIMEOUT", "2s")
	t.Setenv("SERVER_WRITE_TIMEOUT", "3s")
	t.Setenv("SERVER_IDLE_TIMEOUT", "4s")
	t.Setenv("REQUEST_TIMEOUT", "500ms")

	cfg := Load()

	if cfg.ServerReadHeaderTimeout != time.Second {
		t.Errorf("read header timeout: %s", cfg.ServerReadHeaderTimeout)
	}
	if cfg.ServerReadTimeout != 2*time.Second {
		t.Errorf("read timeout: %s", cfg.ServerReadTimeout)
	}
	if cfg.ServerWriteTimeout != 3*time.Second {
		t.Errorf("write timeout: %s", cfg.ServerWriteTimeout)
	}
	if cfg.ServerIdleTimeout != 4*time.Second {
		t.Errorf("idle timeout: %s", cfg.ServerIdleTimeout)
	}
	if cfg.RequestTimeout != 500*time.Millisecond {
		t.Errorf("request timeout: %s", cfg.RequestTimeout)
	}
}
