package ledger

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/ledgercore"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
)

// StorageRepository is the durable Repository implementation, event-sourced
// over a shared storage.Store (the same instance the coordinator uses — same
// SQLite file, same content-addressed content table, same run_claims table).
// It is a NON-OWNING user of the store: Close() releases only the claims this
// instance holds and never closes the borrowed store.
type StorageRepository struct {
	store        storage.Store
	claims       *ledgercore.ClaimsTracker
	engine       *ledgercore.Engine
	mu           sync.RWMutex
	proj         map[string]Projection
	deliverySeqs map[deliveryKey]int
}

// NewStorageRepository wraps a shared storage.Store (non-owning).
func NewStorageRepository(store storage.Store) *StorageRepository {
	engine := ledgercore.NewEngine(store, false, newHolderID())
	return &StorageRepository{
		store:        store,
		claims:       engine.Claims(),
		engine:       engine,
		proj:         make(map[string]Projection),
		deliverySeqs: make(map[deliveryKey]int),
	}
}

// NewMemoryRepository returns a repository over a fresh in-memory store.
func NewMemoryRepository() *StorageRepository {
	return NewStorageRepository(storage.NewMemory())
}

// SetTimeSource replaces the clock for deterministic tests.
func (s *StorageRepository) SetTimeSource(now func() time.Time) {
	s.engine.SetTimeSource(now)
}

func (s *StorageRepository) now() time.Time {
	return s.engine.Now()
}

// Close releases claims held by this instance and marks the repository closed.
func (s *StorageRepository) Close() error {
	return s.engine.Close(context.Background())
}

// runLock returns the per-run mutex for runID, creating it on first use.
func (s *StorageRepository) runLock(runID string) *sync.Mutex {
	return s.engine.RunLock(runID)
}

// checkOpen returns ErrClosed if the repository has been closed.
func (s *StorageRepository) checkOpen() error {
	return s.engine.CheckOpen()
}

// ensureBuilt brings the in-memory projection up to date with the store.
func (s *StorageRepository) ensureBuilt(ctx context.Context) error {
	if err := s.checkOpen(); err != nil {
		return err
	}
	return s.catchUp(ctx)
}

// catchUp probes the store for runs that moved since this instance's cursor.
func (s *StorageRepository) catchUp(ctx context.Context) error {
	return s.engine.CatchUp(ctx, func(runID string, maxSeq int) ledgercore.FilterDecision {
		if s.engine.Watermarks().Applied(runID) == 0 && !strings.HasPrefix(runID, "wfr-") {
			return ledgercore.FilterAdvanceOnly
		}
		return ledgercore.FilterApply
	}, func(ctx context.Context, runID string, events []storage.Event) error {
		return s.applyRunEventsLocked(ctx, runID, events)
	})
}

// catchUpRunLocked rebuilds one run's cached projection from the store's full event log.
func (s *StorageRepository) catchUpRunLocked(ctx context.Context, runID string) error {
	events, err := s.engine.Store().Events(ctx, runID)
	if err != nil {
		return fmt.Errorf("read events for %s: %w", runID, err)
	}
	return s.applyRunEventsLocked(ctx, runID, events)
}

// applyRunEventsLocked folds a run's full event log into the cached projection.
func (s *StorageRepository) applyRunEventsLocked(ctx context.Context, runID string, events []storage.Event) error {
	var maxSeq uint64
	for _, ev := range events {
		if uint64(ev.Sequence) > maxSeq {
			maxSeq = uint64(ev.Sequence)
		}
	}

	proj, err := RebuildProjection(events)
	if err != nil {
		return fmt.Errorf("rebuild projection for %s: %w", runID, err)
	}

	deliveryCounts := make(map[string]int)
	for _, ev := range events {
		if ev.Kind != eventKindDeliveryUpserted {
			continue
		}
		p, err := unmarshalDeliveryUpserted(ev.Payload)
		if err != nil {
			return fmt.Errorf("decode %s payload: %w", ev.Kind, err)
		}
		deliveryCounts[p.Delivery.IdempotencyKey]++
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if proj.HasRun {
		s.proj[runID] = proj
	} else {
		delete(s.proj, runID)
	}
	s.engine.Watermarks().SetApplied(runID, maxSeq)
	for k := range s.deliverySeqs {
		if k.runID == runID {
			delete(s.deliverySeqs, k)
		}
	}
	for key, n := range deliveryCounts {
		s.deliverySeqs[deliveryKey{runID: runID, key: key}] = n
	}
	return nil
}

// rebaseRunSequence preserves sequence monotonicity when a run ID is recreated.
func (s *StorageRepository) rebaseRunSequence(ctx context.Context, runID string) error {
	return s.engine.RebaseRunSequence(ctx, runID)
}

// nextSequence returns the next sequence number for a run.
func (s *StorageRepository) nextSequence(runID string) uint64 {
	return s.engine.NextSequence(runID)
}

// appendEvent writes the event to the store and resolves the writer's bookkeeping.
func (s *StorageRepository) appendEvent(ctx context.Context, evt storage.Event, rollback func()) error {
	holder, _ := claimHolderFromContext(ctx)
	return s.engine.AppendEvent(ctx, evt, ledgercore.AppendOptions{
		BoundHolder: holder,
		Rollback: func() {
			if rollback != nil {
				s.mu.Lock()
				rollback()
				s.mu.Unlock()
			}
		},
		RebuildRun: func(ctx context.Context, runID string) error {
			return s.catchUpRunLocked(ctx, runID)
		},
		OnDuplicate: func(ctx context.Context, e storage.Event) error {
			if cerr := s.catchUpRunLocked(ctx, e.RunID); cerr != nil {
				return fmt.Errorf("catch up after duplicate: %w", cerr)
			}
			return s.engine.CheckDuplicatePayload(ctx, e)
		},
	})
}

// rollbackAndRebuild rolls back the projection mutation and rebuilds from store.
func (s *StorageRepository) rollbackAndRebuild(ctx context.Context, runID string, rollback func()) {
	if rollback != nil {
		s.mu.Lock()
		rollback()
		s.mu.Unlock()
	}
	_ = s.catchUpRunLocked(ctx, runID)
}
