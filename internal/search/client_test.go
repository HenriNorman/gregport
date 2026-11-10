package search

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/elastic/go-elasticsearch/v8/esapi"
)

func TestNewClientDefaultsTimeoutAndDerivesIndexNames(t *testing.T) {
	c, err := NewClient(ClientConfig{URL: "http://localhost:9200", Index: "tickets"})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if c.Timeout() != DefaultTimeout {
		t.Fatalf("timeout = %s, want %s", c.Timeout(), DefaultTimeout)
	}
	if c.Alias() != "tickets" {
		t.Fatalf("alias = %q", c.Alias())
	}
	if got := c.IndexName("v1"); got != "tickets-v1" {
		t.Fatalf("IndexName(v1) = %q", got)
	}
	if got := c.IndexName(""); got != "tickets" {
		t.Fatalf("IndexName(empty) = %q", got)
	}
}

func TestNewClientRejectsMissingSettings(t *testing.T) {
	if _, err := NewClient(ClientConfig{Index: "tickets"}); err == nil {
		t.Fatal("NewClient without url = nil error")
	}
	if _, err := NewClient(ClientConfig{URL: "http://localhost:9200"}); err == nil {
		t.Fatal("NewClient without index = nil error")
	}
}

func TestSplitAddresses(t *testing.T) {
	got := splitAddresses(" http://a:9200, ,http://b:9200 ")
	if len(got) != 2 || got[0] != "http://a:9200" || got[1] != "http://b:9200" {
		t.Fatalf("splitAddresses = %#v", got)
	}
}

func TestPingSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Elastic-Product", "Elasticsearch")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"version":{"number":"8.15.3"}}`)
	}))
	defer srv.Close()

	c, err := NewClient(ClientConfig{URL: srv.URL, Index: "tickets", Timeout: time.Second})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if err := c.Ping(context.Background()); err != nil {
		t.Fatalf("Ping: %v", err)
	}
}

func TestPingUnavailable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Elastic-Product", "Elasticsearch")
		http.Error(w, `{"error":{"type":"cluster_block_exception","reason":"blocked"}}`, http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	c, err := NewClient(ClientConfig{URL: srv.URL, Index: "tickets", Timeout: time.Second})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	err = c.Ping(context.Background())
	if !errors.Is(err, ErrSearchUnavailable) {
		t.Fatalf("Ping error = %v, want ErrSearchUnavailable", err)
	}
}

func TestPingUnreachable(t *testing.T) {
	c, err := NewClient(ClientConfig{URL: "http://127.0.0.1:1", Index: "tickets", Timeout: 100 * time.Millisecond})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	err = c.Ping(context.Background())
	if !errors.Is(err, ErrSearchUnavailable) {
		t.Fatalf("Ping error = %v, want ErrSearchUnavailable", err)
	}
}

func TestResponseErrorClassifiesFailures(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
		want   error
	}{
		{
			name:   "ok",
			status: http.StatusOK,
			body:   `{}`,
		},
		{
			name:   "version conflict",
			status: http.StatusConflict,
			body:   `{"error":{"type":"version_conflict_engine_exception","reason":"version conflict"}}`,
			want:   ErrVersionConflict,
		},
		{
			name:   "index missing",
			status: http.StatusNotFound,
			body:   `{"error":{"type":"index_not_found_exception","reason":"no such index"}}`,
			want:   ErrIndexNotFound,
		},
		{
			name:   "server error",
			status: http.StatusBadGateway,
			body:   `{"error":{"type":"unavailable_shards_exception","reason":"shard unavailable"}}`,
			want:   ErrSearchUnavailable,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res := &esapi.Response{
				StatusCode: tc.status,
				Body:       io.NopCloser(strings.NewReader(tc.body)),
			}
			err := ResponseError(res)
			if tc.want == nil {
				if err != nil {
					t.Fatalf("ResponseError = %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, tc.want) {
				t.Fatalf("ResponseError = %v, want %v", err, tc.want)
			}
		})
	}
}
