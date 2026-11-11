package main

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
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

func TestRunServer_GracefulShutdownWaitsForInflight(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})

	mux := http.NewServeMux()
	mux.HandleFunc("/slow", func(w http.ResponseWriter, _ *http.Request) {
		close(started)
		<-release
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("done"))
	})

	ln := newListener(t)
	errCh := make(chan error, 1)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		errCh <- runServer(ctx, &http.Server{Handler: mux}, ln, time.Second, discardLogger())
	}()

	respCh := make(chan *http.Response, 1)
	reqErrCh := make(chan error, 1)
	go func() {
		resp, err := http.Get("http://" + ln.Addr().String() + "/slow")
		if err != nil {
			reqErrCh <- err
			return
		}
		respCh <- resp
	}()

	waitFor(t, started, "handler did not start")
	cancel()

	waitForListenerClosed(t, ln.Addr().String())

	select {
	case err := <-errCh:
		t.Fatalf("server returned before the in-flight request finished: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	close(release)

	select {
	case err := <-reqErrCh:
		t.Fatalf("in-flight request: %v", err)
	case resp := <-respCh:
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusOK || string(body) != "done" {
			t.Fatalf("in-flight response: %d %q", resp.StatusCode, body)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("in-flight request did not complete")
	}

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("runServer: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("runServer did not return")
	}
}

func TestRunServer_ShutdownHookReleasesLongLivedHandler(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	hookCalled := make(chan struct{})

	mux := http.NewServeMux()
	mux.HandleFunc("/stream", func(_ http.ResponseWriter, _ *http.Request) {
		close(started)
		<-release
	})

	ln := newListener(t)
	errCh := make(chan error, 1)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		errCh <- runServer(ctx, &http.Server{Handler: mux}, ln, time.Second, discardLogger(), func() {
			close(hookCalled)
			close(release)
		})
	}()

	go func() {
		resp, err := http.Get("http://" + ln.Addr().String() + "/stream")
		if err == nil {
			resp.Body.Close()
		}
	}()

	waitFor(t, started, "stream handler did not start")
	cancel()

	select {
	case <-hookCalled:
	case <-time.After(2 * time.Second):
		t.Fatal("shutdown hook did not run")
	}

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("runServer: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("runServer did not return after the stream handler was released")
	}
}

func TestRunServer_ShutdownTimeout(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})

	mux := http.NewServeMux()
	mux.HandleFunc("/stuck", func(_ http.ResponseWriter, _ *http.Request) {
		close(started)
		<-release
	})

	ln := newListener(t)
	errCh := make(chan error, 1)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		errCh <- runServer(ctx, &http.Server{Handler: mux}, ln, 50*time.Millisecond, discardLogger())
	}()

	go func() {
		resp, err := http.Get("http://" + ln.Addr().String() + "/stuck")
		if err == nil {
			resp.Body.Close()
		}
	}()

	waitFor(t, started, "handler did not start")
	cancel()

	select {
	case err := <-errCh:
		if err == nil || !strings.Contains(err.Error(), "shutdown") {
			t.Fatalf("unexpected error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("runServer did not time out")
	}

	close(release)
}

func TestRunServer_PropagatesServeError(t *testing.T) {
	ln := newListener(t)
	ln.Close()

	err := runServer(context.Background(), &http.Server{Handler: http.NewServeMux()}, ln, time.Second, discardLogger())
	if err == nil {
		t.Fatal("expected serve error")
	}
}

func newListener(t *testing.T) net.Listener {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	return ln
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(io.Discard, nil))
}

func waitFor(t *testing.T, ch <-chan struct{}, msg string) {
	t.Helper()

	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatal(msg)
	}
}

func waitForListenerClosed(t *testing.T, addr string) {
	t.Helper()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
		if err != nil {
			return
		}
		_ = conn.Close()
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("listener still accepting connections")
}
