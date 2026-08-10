package ledger

import (
	"context"
)

func (m *MemoryLedgerRepository) ClaimRun(_ context.Context, runID, holder string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	existing, ok := m.claims[runID]
	if ok && existing.holder != holder {
		return ErrClaimHeld
	}
	m.claims[runID] = memoryClaim{holder: holder}
	return nil
}

func (m *MemoryLedgerRepository) ReleaseRun(_ context.Context, runID, holder string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	existing, ok := m.claims[runID]
	if !ok || existing.holder != holder {
		return ErrClaimNotHeld
	}
	delete(m.claims, runID)
	return nil
}

func (m *MemoryLedgerRepository) ClearRunClaim(_ context.Context, runID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.claims, runID)
	return nil
}

func (m *MemoryLedgerRepository) StoreContent(_ context.Context, ref string, data []byte) error {
	if len(data) == 0 {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.content[ref] = append([]byte(nil), data...)
	return nil
}

func (m *MemoryLedgerRepository) LoadContent(_ context.Context, ref string) ([]byte, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	data, ok := m.content[ref]
	if !ok {
		return nil, ErrContentNotFound
	}
	out := make([]byte, len(data))
	copy(out, data)
	return out, nil
}
