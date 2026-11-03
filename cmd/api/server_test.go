package main

import (
	"net/http"
	"testing"
	"time"

	"github.com/HenriNorman/gregport/internal/platform/config"
)

func TestNewServer_AppliesConfigAndHandler(t *testing.T) {
	handler := http.NewServeMux()
	cfg := config.Config{
		AppPort:                 "9090",
		ServerReadHeaderTimeout: time.Second,
		ServerReadTimeout:       2 * time.Second,
		ServerWriteTimeout:      3 * time.Second,
		ServerIdleTimeout:       4 * time.Second,
	}

	srv := newServer(cfg, handler)

	if srv.Addr != ":9090" {
		t.Errorf("addr: got %q", srv.Addr)
	}
	if srv.Handler != handler {
		t.Error("handler was not attached")
	}
	if srv.ReadHeaderTimeout != time.Second {
		t.Errorf("read header timeout: %s", srv.ReadHeaderTimeout)
	}
	if srv.ReadTimeout != 2*time.Second {
		t.Errorf("read timeout: %s", srv.ReadTimeout)
	}
	if srv.WriteTimeout != 3*time.Second {
		t.Errorf("write timeout: %s", srv.WriteTimeout)
	}
	if srv.IdleTimeout != 4*time.Second {
		t.Errorf("idle timeout: %s", srv.IdleTimeout)
	}
}
