package ledger

import (
	"github.com/MiviaLabs/mivia-agent/internal/ledgercore"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
)

// NewStorageLedgerRepository creates a StorageLedgerRepository backed by the
// given store. The in-memory projection is built lazily on first access and
// refreshed incrementally afterwards.
func NewStorageLedgerRepository(store storage.Store) *StorageLedgerRepository {
	return newStorageLedgerRepository(store, true)
}

// NewBorrowedStorageLedgerRepository creates a ledger projection over a
// caller-owned store. Closing the repository releases its claims but leaves
// the shared store open for the owning lifecycle coordinator.
func NewBorrowedStorageLedgerRepository(store storage.Store) *StorageLedgerRepository {
	return newStorageLedgerRepository(store, false)
}

func newStorageLedgerRepository(store storage.Store, ownsStore bool) *StorageLedgerRepository {
	engine := ledgercore.NewEngine(store, ownsStore, "")
	return &StorageLedgerRepository{
		store:     store,
		mem:       NewMemoryLedgerRepository(),
		engine:    engine,
		claims:    engine.Claims(),
		ownsStore: ownsStore,
		inflight:  make(map[inflightKey]struct{}),
	}
}

// UnderlyingStore returns the dependency injected at construction. It is a
// read-only identity seam for lifecycle wiring and ownership tests.
func (s *StorageLedgerRepository) UnderlyingStore() storage.Store { return s.store }
