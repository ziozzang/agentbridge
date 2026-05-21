package responsecache

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sync"
	"time"
)

type Stats struct {
	Hits      uint64 `json:"hits"`
	Misses    uint64 `json:"misses"`
	Sets      uint64 `json:"sets"`
	Evictions uint64 `json:"evictions"`
	Items     int    `json:"items"`
}

type entry struct {
	value     []byte
	expiresAt time.Time
}

type Memory struct {
	mu        sync.Mutex
	items     map[string]entry
	maxItems  int
	hits      uint64
	misses    uint64
	sets      uint64
	evictions uint64
}

func NewMemory(maxItems int) *Memory {
	return &Memory{items: map[string]entry{}, maxItems: maxItems}
}

func (m *Memory) Get(_ context.Context, key string) ([]byte, bool) {
	if m == nil || key == "" {
		return nil, false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.items[key]
	if !ok {
		m.misses++
		return nil, false
	}
	if !e.expiresAt.IsZero() && time.Now().After(e.expiresAt) {
		delete(m.items, key)
		m.misses++
		m.evictions++
		return nil, false
	}
	m.hits++
	out := make([]byte, len(e.value))
	copy(out, e.value)
	return out, true
}

func (m *Memory) Set(_ context.Context, key string, value []byte, ttl time.Duration) {
	if m == nil || key == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	v := make([]byte, len(value))
	copy(v, value)
	e := entry{value: v}
	if ttl > 0 {
		e.expiresAt = time.Now().Add(ttl)
	}
	m.items[key] = e
	m.sets++
	if m.maxItems > 0 && len(m.items) > m.maxItems {
		m.evictOldestLocked(len(m.items) - m.maxItems)
	}
}

func (m *Memory) Stats() Stats {
	if m == nil {
		return Stats{}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return Stats{Hits: m.hits, Misses: m.misses, Sets: m.sets, Evictions: m.evictions, Items: len(m.items)}
}

func Key(namespace string, payload any) (string, error) {
	b, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return namespace + ":" + hex.EncodeToString(sum[:]), nil
}

func (m *Memory) evictOldestLocked(n int) {
	for n > 0 && len(m.items) > 0 {
		var key string
		var exp time.Time
		first := true
		for k, v := range m.items {
			if first || lessExpiry(v.expiresAt, exp) {
				key, exp, first = k, v.expiresAt, false
			}
		}
		delete(m.items, key)
		m.evictions++
		n--
	}
}

func lessExpiry(a, b time.Time) bool {
	if a.IsZero() && !b.IsZero() {
		return false
	}
	if !a.IsZero() && b.IsZero() {
		return true
	}
	return a.Before(b)
}
