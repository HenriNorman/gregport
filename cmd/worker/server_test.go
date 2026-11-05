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

func TestNewWorkerServerAppliesConfig(t *testing.T) {
	handler := http.NewServeMux()
	cfg := config.WorkerConfig{
		WorkerPort:              "9091",
		WorkerReadHeaderTimeout: time.Second,
		WorkerReadTimeout:       2 * time.Second,
		WorkerWriteTimeout:      3 * time.Second,
		WorkerIdleTimeout:       4 * time.Second,
	}

	srv := newServer(cfg, handler)
	if srv.Addr != ":9091" {
		t.Fatalf("addr: %q", srv.Addr)
	}
	if srv.Handler != handler {
		t.Fatal("handler not attached")
	}
	if srv.ReadHeaderTimeout != time.Second || srv.ReadTimeout != 2*time.Second ||
		srv.WriteTimeout != 3*time.Second || srv.IdleTimeout != 4*time.Second {
		t.Fatalf("timeouts: %+v", srv)
	}
}

func TestWorkerRunServerGracefulShutdown(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})

	mux := http.NewServeMux()
	mux.HandleFunc("/slow", func(w http.ResponseWriter, _ *http.Request) {
		close(started)
		<-release
		_, _ = w.Write([]byte("done"))
	})

	ln := newWorkerListener(t)
	errCh := make(chan error, 1)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		errCh <- runServer(ctx, &http.Server{Handler: mux}, ln, 2*time.Second, workerDiscardLogger())
	}()

	type result struct {
		body string
		err  error
	}
	respCh := make(chan result, 1)
	go func() {
		resp, err := http.Get("http://" + ln.Addr().String() + "/slow")
		if err != nil {
			respCh <- result{err: err}
			return
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		respCh <- result{body: string(body)}
	}()

	waitForWorkerSignal(t, started, "handler did not start")
	cancel()

	select {
	case err := <-errCh:
		t.Fatalf("server returned before in-flight request finished: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	close(release)

	select {
	case got := <-respCh:
		if got.err != nil || got.body != "done" {
			t.Fatalf("in-flight response: %+v", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("in-flight request did not finish")
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

func TestWorkerRunServerTimeout(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})

	mux := http.NewServeMux()
	mux.HandleFunc("/stuck", func(_ http.ResponseWriter, _ *http.Request) {
		close(started)
		<-release
	})

	ln := newWorkerListener(t)
	errCh := make(chan error, 1)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		errCh <- runServer(ctx, &http.Server{Handler: mux}, ln, 50*time.Millisecond, workerDiscardLogger())
	}()

	go func() {
		resp, err := http.Get("http://" + ln.Addr().String() + "/stuck")
		if err == nil {
			resp.Body.Close()
		}
	}()

	waitForWorkerSignal(t, started, "handler did not start")
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

func TestWorkerRunServerPropagatesServeError(t *testing.T) {
	ln := newWorkerListener(t)
	ln.Close()

	err := runServer(context.Background(), &http.Server{Handler: http.NewServeMux()}, ln, time.Second, workerDiscardLogger())
	if err == nil {
		t.Fatal("expected serve error")
	}
}

func newWorkerListener(t *testing.T) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	return ln
}

func workerDiscardLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(io.Discard, nil))
}

func waitForWorkerSignal(t *testing.T, ch <-chan struct{}, msg string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatal(msg)
	}
}
