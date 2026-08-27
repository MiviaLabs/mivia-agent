package remainder

import (
	"bytes"
	"context"
	"sync"
)

// MemoryStore is an in-process ContentStore for tests and host wiring that
// does not share the ledger repository. It is keyed by content reference:
// the same body always mints the same ref (sdkadapter.Mint is
// deterministic), so re-storing a duplicate lands on the same key and never
// grows the store.
type MemoryStore struct {
	mu   sync.RWMutex
	data map[string][]byte
}

// NewMemoryStore returns an empty memory-backed content store. Storing the
// same content twice yields the same ref and does not duplicate storage.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{data: make(map[string][]byte)}
}

// StoreContent persists raw bytes under ref. It is idempotent: re-storing the
// same content under the same ref (dedupe by ref) is a no-op, so repeated
// prepares do not grow the store (see the ContentStore contract in spool.go).
func (m *MemoryStore) StoreContent(_ context.Context, ref string, data []byte) error {
	if len(data) == 0 {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.data == nil {
		m.data = make(map[string][]byte)
	}
	if existing, ok := m.data[ref]; ok && bytes.Equal(existing, data) {
		// Duplicate content under the same ref: nothing new to store.
		return nil
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

// Len reports how many bodies are stored. Tests that assert nothing was
// spooled need to distinguish "stored nothing" from "stored something under a
// ref I did not predict"; counting is the only way to say the former.
func (m *MemoryStore) Len() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.data)
}

// Delete removes a ref so subsequent loads report not-found / expired.
func (m *MemoryStore) Delete(ref string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.data, ref)
}
