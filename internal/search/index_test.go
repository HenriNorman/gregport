package search

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestIndexMappingHasRequiredFields(t *testing.T) {
	var mapping struct {
		Mappings struct {
			Dynamic    string `json:"dynamic"`
			Properties map[string]struct {
				Type string `json:"type"`
			} `json:"properties"`
		} `json:"mappings"`
	}
	if err := json.Unmarshal([]byte(IndexMapping), &mapping); err != nil {
		t.Fatalf("mapping is not valid JSON: %v", err)
	}
	if mapping.Mappings.Dynamic != "strict" {
		t.Fatalf("dynamic = %q, want strict", mapping.Mappings.Dynamic)
	}
	want := map[string]string{
		"org_id":          "keyword",
		"number":          "long",
		"subject":         "text",
		"comments_text":   "text",
		"status":          "keyword",
		"priority":        "keyword",
		"tags":            "keyword",
		"assignee_id":     "keyword",
		"requester_email": "keyword",
		"created_at":      "date",
		"updated_at":      "date",
		"version":         "long",
	}
	for field, typ := range want {
		got, ok := mapping.Mappings.Properties[field]
		if !ok {
			t.Errorf("mapping is missing %s", field)
			continue
		}
		if got.Type != typ {
			t.Errorf("%s type = %q, want %q", field, got.Type, typ)
		}
	}
}

func integrationESClient(t *testing.T) *Client {
	t.Helper()
	if testing.Short() {
		t.Skip("elasticsearch integration test")
	}
	url := os.Getenv("ELASTICSEARCH_URL")
	if url == "" {
		url = "http://localhost:9200"
	}
	alias := "tickets-test-" + strings.ReplaceAll(uuid.NewString()[:8], "-", "")
	c, err := NewClient(ClientConfig{URL: url, Index: alias, Timeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.Ping(ctx); err != nil {
		t.Fatalf("elasticsearch is not reachable at %s: %v", url, err)
	}
	return c
}

func TestIndexManagerBootstrapAndSwap(t *testing.T) {
	client := integrationESClient(t)
	manager := NewIndexManager(client)
	ctx := context.Background()

	v1 := client.IndexName("v1")
	v2 := client.IndexName("v2")
	t.Cleanup(func() {
		_ = manager.DeleteIndex(context.Background(), v1)
		_ = manager.DeleteIndex(context.Background(), v2)
	})

	if target, err := manager.AliasTarget(ctx); err != nil {
		t.Fatalf("alias target before bootstrap: %v", err)
	} else if target != "" {
		t.Fatalf("alias target before bootstrap = %q, want none", target)
	}

	if err := manager.Bootstrap(ctx); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	target, err := manager.AliasTarget(ctx)
	if err != nil {
		t.Fatalf("alias target: %v", err)
	}
	if target != v1 {
		t.Fatalf("alias target = %q, want %q", target, v1)
	}

	// Bootstrap is idempotent: a second start must not create v2 or fail.
	if err := manager.Bootstrap(ctx); err != nil {
		t.Fatalf("second bootstrap: %v", err)
	}
	if target, err := manager.AliasTarget(ctx); err != nil || target != v1 {
		t.Fatalf("alias after second bootstrap = %q, %v", target, err)
	}

	mapping, err := indexMapping(ctx, client, v1)
	if err != nil {
		t.Fatalf("mapping: %v", err)
	}
	if !strings.Contains(mapping, `"org_id"`) || !strings.Contains(mapping, `"comments_text"`) {
		t.Fatalf("mapping missing fields: %s", mapping)
	}

	if err := manager.CreateIndex(ctx, v2); err != nil {
		t.Fatalf("create v2: %v", err)
	}
	if err := manager.SwapAlias(ctx, v1, v2); err != nil {
		t.Fatalf("swap alias: %v", err)
	}
	target, err = manager.AliasTarget(ctx)
	if err != nil {
		t.Fatalf("alias target after swap: %v", err)
	}
	if target != v2 {
		t.Fatalf("alias target after swap = %q, want %q", target, v2)
	}
}

// indexMapping returns the raw mapping JSON for an index.
func indexMapping(ctx context.Context, client *Client, index string) (string, error) {
	res, err := client.ES().Indices.GetMapping(
		client.ES().Indices.GetMapping.WithContext(ctx),
		client.ES().Indices.GetMapping.WithIndex(index),
	)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	if err := ResponseError(res); err != nil {
		return "", err
	}
	var raw json.RawMessage
	if err := json.NewDecoder(res.Body).Decode(&raw); err != nil {
		return "", err
	}
	return string(raw), nil
}
