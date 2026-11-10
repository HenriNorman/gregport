package search

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// DefaultIndexVersion is the initial concrete index version. A reindex builds
// the next version and atomically re-points the alias at it.
const DefaultIndexVersion = "v1"

// IndexMapping is the Elasticsearch mapping for one ticket document. Dynamic
// mapping is strict so a sync bug that invents a field (for example an
// unscoped index) fails loudly during indexing instead of silently becoming
// searchable. The org id is a keyword because every query filters on it.
const IndexMapping = `{
  "settings": {
    "number_of_shards": 1,
    "number_of_replicas": 0,
    "refresh_interval": "1s"
  },
  "mappings": {
    "dynamic": "strict",
    "properties": {
      "org_id":          { "type": "keyword" },
      "number":          { "type": "long" },
      "subject":         { "type": "text", "analyzer": "english" },
      "comments_text":   { "type": "text", "analyzer": "english" },
      "status":          { "type": "keyword" },
      "priority":        { "type": "keyword" },
      "tags":            { "type": "keyword" },
      "assignee_id":     { "type": "keyword" },
      "requester_email": { "type": "keyword" },
      "created_at":      { "type": "date" },
      "updated_at":      { "type": "date" },
      "version":         { "type": "long" }
    }
  }
}`

// IndexManager owns the concrete index and its stable alias. All searches use
// the alias; writes target a versioned index so a reindex can build a
// replacement without affecting live traffic.
type IndexManager struct {
	client *Client
}

// NewIndexManager creates an index lifecycle manager.
func NewIndexManager(client *Client) *IndexManager {
	return &IndexManager{client: client}
}

// Bootstrap makes the alias usable: it creates tickets-v1 with the ticket
// mapping if no index is aliased yet, then points the alias at it. It is
// idempotent and safe to run on every process start. It does not fail when
// Elasticsearch is unavailable; callers decide whether that is fatal.
func (m *IndexManager) Bootstrap(ctx context.Context) error {
	target, err := m.AliasTarget(ctx)
	if err != nil {
		return err
	}
	if target != "" {
		return nil
	}

	concrete := m.client.IndexName(DefaultIndexVersion)
	exists, err := m.indexExists(ctx, concrete)
	if err != nil {
		return err
	}
	if !exists {
		if err := m.CreateIndex(ctx, concrete); err != nil && !isResourceAlreadyExists(err) {
			return err
		}
	}
	return m.PutAlias(ctx, concrete)
}

// CreateIndex creates one concrete index with the ticket mapping.
func (m *IndexManager) CreateIndex(ctx context.Context, index string) error {
	res, err := m.client.ES().Indices.Create(
		index,
		m.client.ES().Indices.Create.WithContext(ctx),
		m.client.ES().Indices.Create.WithBody(strings.NewReader(IndexMapping)),
	)
	if err != nil {
		return fmt.Errorf("%w: create index %s: %v", ErrSearchUnavailable, index, err)
	}
	defer res.Body.Close()
	return ResponseError(res)
}

// PutAlias points the stable alias at a concrete index.
func (m *IndexManager) PutAlias(ctx context.Context, index string) error {
	res, err := m.client.ES().Indices.PutAlias(
		[]string{index},
		m.client.Alias(),
		m.client.ES().Indices.PutAlias.WithContext(ctx),
	)
	if err != nil {
		return fmt.Errorf("%w: put alias: %v", ErrSearchUnavailable, err)
	}
	defer res.Body.Close()
	return ResponseError(res)
}

// AliasTarget returns the concrete index currently behind the alias, or an
// empty string when the alias does not exist. A missing alias is normal before
// the first bootstrap or reindex.
func (m *IndexManager) AliasTarget(ctx context.Context) (string, error) {
	res, err := m.client.ES().Indices.GetAlias(
		m.client.ES().Indices.GetAlias.WithContext(ctx),
		m.client.ES().Indices.GetAlias.WithName(m.client.Alias()),
	)
	if err != nil {
		return "", fmt.Errorf("%w: get alias: %v", ErrSearchUnavailable, err)
	}
	defer res.Body.Close()

	if res.StatusCode == http.StatusNotFound {
		return "", nil
	}
	if err := ResponseError(res); err != nil {
		if errors.Is(err, ErrIndexNotFound) {
			return "", nil
		}
		return "", err
	}

	var payload map[string]json.RawMessage
	if err := json.NewDecoder(res.Body).Decode(&payload); err != nil {
		return "", fmt.Errorf("search: decode alias response: %w", err)
	}
	alias := m.client.Alias()
	for index := range payload {
		if index != alias {
			return index, nil
		}
	}
	return "", nil
}

// SwapAlias atomically moves the alias from oldIndex to newIndex. Passing an
// empty oldIndex just adds the alias, which is the first-reindex case. The
// remove and add actions are a single request, so searches never see a moment
// with no index behind the alias.
func (m *IndexManager) SwapAlias(ctx context.Context, oldIndex, newIndex string) error {
	if newIndex == "" {
		return errors.New("search: new index is required")
	}
	if oldIndex == newIndex {
		return m.PutAlias(ctx, newIndex)
	}

	type aliasRef struct {
		Index string `json:"index"`
		Alias string `json:"alias"`
	}
	type action struct {
		Remove *aliasRef `json:"remove,omitempty"`
		Add    *aliasRef `json:"add,omitempty"`
	}

	actions := make([]action, 0, 2)
	if oldIndex != "" {
		actions = append(actions, action{Remove: &aliasRef{Index: oldIndex, Alias: m.client.Alias()}})
	}
	actions = append(actions, action{Add: &aliasRef{Index: newIndex, Alias: m.client.Alias()}})

	body, err := json.Marshal(map[string]any{"actions": actions})
	if err != nil {
		return fmt.Errorf("search: build alias swap: %w", err)
	}

	res, err := m.client.ES().Indices.UpdateAliases(
		bytes.NewReader(body),
		m.client.ES().Indices.UpdateAliases.WithContext(ctx),
	)
	if err != nil {
		return fmt.Errorf("%w: swap alias: %v", ErrSearchUnavailable, err)
	}
	defer res.Body.Close()
	return ResponseError(res)
}

// DeleteIndex removes a concrete index. It is used to clean up a failed
// reindex target and by tests; never call it on a live alias target.
func (m *IndexManager) DeleteIndex(ctx context.Context, index string) error {
	res, err := m.client.ES().Indices.Delete(
		[]string{index},
		m.client.ES().Indices.Delete.WithContext(ctx),
		m.client.ES().Indices.Delete.WithIgnoreUnavailable(true),
	)
	if err != nil {
		return fmt.Errorf("%w: delete index %s: %v", ErrSearchUnavailable, index, err)
	}
	defer res.Body.Close()
	return ResponseError(res)
}

func (m *IndexManager) indexExists(ctx context.Context, index string) (bool, error) {
	res, err := m.client.ES().Indices.Exists(
		[]string{index},
		m.client.ES().Indices.Exists.WithContext(ctx),
	)
	if err != nil {
		return false, fmt.Errorf("%w: index exists %s: %v", ErrSearchUnavailable, index, err)
	}
	defer res.Body.Close()
	switch res.StatusCode {
	case http.StatusOK:
		return true, nil
	case http.StatusNotFound:
		return false, nil
	default:
		return false, ResponseError(res)
	}
}

// isResourceAlreadyExists reports a create race: another process created the
// same concrete index between our existence check and create call.
func isResourceAlreadyExists(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "resource_already_exists_exception")
}
