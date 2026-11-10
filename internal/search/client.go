package search

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/elastic/go-elasticsearch/v8"
	"github.com/elastic/go-elasticsearch/v8/esapi"
)

// DefaultTimeout bounds a single Elasticsearch request. Search degrades to the
// Postgres fallback when this is exceeded, so it must stay well below the
// API's request timeout.
const DefaultTimeout = 3 * time.Second

// Sentinel errors let callers decide whether to fall back to Postgres. They
// are wrapped, so errors.Is works on returned errors.
var (
	// ErrSearchUnavailable means the cluster could not be reached, timed out,
	// or returned a 5xx. Search should fall back to Postgres.
	ErrSearchUnavailable = errors.New("search: elasticsearch unavailable")
	// ErrIndexNotFound means the alias or index does not exist yet. This is
	// expected before the first reindex and also falls back to Postgres.
	ErrIndexNotFound = errors.New("search: index not found")
	// ErrVersionConflict means an index-sync write lost an external-version
	// race. The job can be safely retried or dropped: a newer document exists.
	ErrVersionConflict = errors.New("search: document version conflict")
)

// ClientConfig holds the Elasticsearch connection settings. Index is the
// stable alias, not a concrete versioned index.
type ClientConfig struct {
	URL      string
	Index    string
	Username string
	Password string
	APIKey   string
	Timeout  time.Duration
}

// Client wraps the official Elasticsearch client. It owns the stable alias
// name and the naming convention for concrete indexes, so the rest of the
// package never hardcodes "tickets".
type Client struct {
	es      *elasticsearch.Client
	alias   string
	timeout time.Duration
}

// NewClient builds the Elasticsearch client. It does not contact the cluster:
// startup must succeed while Elasticsearch is down.
func NewClient(cfg ClientConfig) (*Client, error) {
	if strings.TrimSpace(cfg.URL) == "" {
		return nil, errors.New("search: elasticsearch url is required")
	}
	if cfg.Index == "" {
		return nil, errors.New("search: elasticsearch index is required")
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = DefaultTimeout
	}

	addresses := splitAddresses(cfg.URL)
	if len(addresses) == 0 {
		return nil, errors.New("search: elasticsearch url is required")
	}

	es, err := elasticsearch.NewClient(elasticsearch.Config{
		Addresses: addresses,
		Username:  cfg.Username,
		Password:  cfg.Password,
		APIKey:    cfg.APIKey,
		Transport: newTransport(cfg.Timeout),
		// Retry transient failures once. Retrying a search is safe because
		// reads are idempotent; index writes carry an external version, so a
		// retry cannot overwrite newer data.
		MaxRetries:    2,
		RetryOnStatus: []int{http.StatusTooManyRequests, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout},
		RetryBackoff: func(attempt int) time.Duration {
			return time.Duration(attempt) * 100 * time.Millisecond
		},
	})
	if err != nil {
		return nil, fmt.Errorf("search: create elasticsearch client: %w", err)
	}

	return &Client{es: es, alias: cfg.Index, timeout: cfg.Timeout}, nil
}

// splitAddresses accepts one URL or a comma-separated list of nodes.
func splitAddresses(raw string) []string {
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func newTransport(timeout time.Duration) *http.Transport {
	return &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   timeout,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   10,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   timeout,
		ResponseHeaderTimeout: timeout,
		ExpectContinueTimeout: time.Second,
		TLSClientConfig: &tls.Config{
			MinVersion: tls.VersionTLS12,
		},
	}
}

// ES exposes the underlying client for index lifecycle and bulk operations.
// Callers must still scope every query by org id.
func (c *Client) ES() *elasticsearch.Client { return c.es }

// Alias is the stable name searches target ("tickets"). Writes to a versioned
// index are promoted by atomically swapping this alias.
func (c *Client) Alias() string { return c.alias }

// Timeout is the per-request budget configured for this client.
func (c *Client) Timeout() time.Duration { return c.timeout }

// IndexName returns the concrete versioned index name for a version, for
// example "tickets-v1". Reindex builds a new version and then swaps Alias.
func (c *Client) IndexName(version string) string {
	if version == "" {
		return c.alias
	}
	return c.alias + "-" + version
}

// Ping verifies the cluster is reachable and speaks the Elasticsearch API.
func (c *Client) Ping(ctx context.Context) error {
	res, err := c.es.Ping(c.es.Ping.WithContext(ctx))
	if err != nil {
		return fmt.Errorf("%w: %v", ErrSearchUnavailable, err)
	}
	defer res.Body.Close()

	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return fmt.Errorf("%w: ping returned %s", ErrSearchUnavailable, res.Status())
	}
	return nil
}

// ResponseError maps a non-2xx Elasticsearch response to a sentinel. It reads
// a bounded amount of the body to classify the failure; callers that need the
// body should only call it after they are done with it.
func ResponseError(res *esapi.Response) error {
	if res == nil {
		return ErrSearchUnavailable
	}
	if res.StatusCode >= 200 && res.StatusCode < 300 {
		return nil
	}

	var payload struct {
		Error struct {
			Type   string `json:"type"`
			Reason string `json:"reason"`
		} `json:"error"`
	}
	if res.Body != nil {
		raw, _ := io.ReadAll(io.LimitReader(res.Body, 8<<10))
		_ = json.Unmarshal(raw, &payload)
	}

	switch {
	case payload.Error.Type == "version_conflict_engine_exception":
		return fmt.Errorf("%w: %s", ErrVersionConflict, payload.Error.Reason)
	case payload.Error.Type == "index_not_found_exception" || res.StatusCode == http.StatusNotFound:
		return fmt.Errorf("%w: %s", ErrIndexNotFound, payload.Error.Reason)
	case res.StatusCode >= 500:
		return fmt.Errorf("%w: %s: %s", ErrSearchUnavailable, res.Status(), payload.Error.Reason)
	default:
		return fmt.Errorf("search: elasticsearch returned %s: %s: %s", res.Status(), payload.Error.Type, payload.Error.Reason)
	}
}
