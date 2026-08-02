package remainder

import (
	"context"
	"sync"
)

// MemoryStore is an in-process ContentStore for tests and host wiring that
// does not share the ledger repository.
type MemoryStore struct {
	mu   sync.RWMutex
	data map[string][]byte
}

// NewMemoryStore returns an empty memory-backed content store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{data: make(map[string][]byte)}
}

// StoreContent persists raw bytes under ref.
func (m *MemoryStore) StoreContent(_ context.Context, ref string, data []byte) error {
	if len(data) == 0 {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.data == nil {
		m.data = make(map[string][]byte)
	}
	cp := make([]byte, len(data))
	copy(cp, data)
	m.data[ref] = cp
	return nil
}

// LoadContent retrieves previously stored bytes.
func (m *MemoryStore) LoadContent(_ context.Context, ref string) ([]byte, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	data, ok := m.data[ref]
	if !ok {
		return nil, ErrNotFound
	}
	out := make([]byte, len(data))
	copy(out, data)
	return out, nil
}

// IsContentNotFound reports whether err is this store's absence sentinel.
func (m *MemoryStore) IsContentNotFound(err error) bool {
	return err != nil && err == ErrNotFound
}

// Delete removes a ref so subsequent loads report not-found / expired.
func (m *MemoryStore) Delete(ref string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.data, ref)
}
