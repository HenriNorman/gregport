package cache

import (
	"context"
	"sync"
	"time"
)

// Memory is an in-process Cache used by tests and by local fallbacks. It is
// safe for concurrent use.
type Memory struct {
	mu     sync.Mutex
	items  map[string]memoryItem
	offset time.Duration
}

type memoryItem struct {
	value     []byte
	expiresAt time.Time
}

// NewMemory creates an empty in-memory cache.
func NewMemory() *Memory {
	return &Memory{items: make(map[string]memoryItem)}
}

func (m *Memory) now() time.Time {
	return time.Now().Add(m.offset)
}

// Get returns a value while it has not expired.
func (m *Memory) Get(_ context.Context, key string) ([]byte, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	item, ok := m.items[key]
	if !ok {
		return nil, false, nil
	}
	if !m.now().Before(item.expiresAt) {
		delete(m.items, key)
		return nil, false, nil
	}
	return append([]byte(nil), item.value...), true, nil
}

// Set stores a value with a mandatory positive TTL.
func (m *Memory) Set(_ context.Context, key string, value []byte, ttl time.Duration) error {
	if ttl <= 0 {
		return ErrInvalidTTL
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	m.items[key] = memoryItem{
		value:     append([]byte(nil), value...),
		expiresAt: m.now().Add(ttl),
	}
	return nil
}

// Delete removes a key.
func (m *Memory) Delete(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	delete(m.items, key)
	return nil
}

// Ping always succeeds for the in-memory cache.
func (m *Memory) Ping(context.Context) error {
	return nil
}

// Advance shifts the cache clock, which lets tests exercise TTL expiry
// without sleeping.
func (m *Memory) Advance(d time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.offset += d
}

// Len reports the number of stored keys, including not-yet-swept expired keys.
func (m *Memory) Len() int {
	m.mu.Lock()
	defer m.mu.Unlock()

	return len(m.items)
}
