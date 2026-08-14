package ledger

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/storage"
)

// deliveryKey identifies one delivery idempotency key within a run.
type deliveryKey struct {
	runID string
	key   string
}

// UpsertDelivery records a delivery attempt keyed by idempotency key.
func (s *StorageRepository) UpsertDelivery(ctx context.Context, d DeliveryRecord) error {
	if err := s.ensureBuilt(ctx); err != nil {
		return err
	}
	lock := s.runLock(d.RunID)
	lock.Lock()
	defer lock.Unlock()

	// The retry loop absorbs a cross-instance ordinal collision: two
	// instances over the same store can mint the same ordinal for a key
	// before either sees the other's event. appendEvent rebuilds the
	// projection and the ordinal bookkeeping from durable state on
	// ErrConflict, so the second pass mints the next free ordinal and
	// advances instead of returning a permanent conflict.
	for attempt := 0; attempt < 2; attempt++ {
		retry, err := s.upsertDeliveryOnce(ctx, d)
		if retry {
			continue
		}
		return err
	}
	return fmt.Errorf("marshal %s payload: %w", eventKindDeliveryUpserted, ErrConflict)
}

// upsertDeliveryOnce performs one delivery upsert pass under the run's
// per-run lock. It returns retry=true when a concurrent writer on another
// instance minted the same event ordinal and appendEvent already rebuilt the
// projection and ordinal bookkeeping, so the caller re-runs with the next
// free ordinal.
func (s *StorageRepository) upsertDeliveryOnce(ctx context.Context, d DeliveryRecord) (retry bool, err error) {
	s.mu.Lock()
	p, ok := s.proj[d.RunID]
	if !ok || !p.HasRun {
		s.mu.Unlock()
		return false, ErrNotFound
	}
	// Cross-instance idempotency: a retry of the same upsert (same caller
	// fields under the same idempotency key) must be absorbed without
	// minting a duplicate wf_delivery_upserted event, even when the retry
	// arrives through a different repository instance over the same store.
	// Only CALLER-OWNED fields are compared: UpdatedAt is repo-stamped and
	// may legitimately differ across instances/retries. A changed field
	// (e.g. Status pending->pushed) still proceeds to a new event; the
	// projection's latest-wins merge keeps GetDeliveryByIdempotencyKey
	// correct.
	for i := range p.Deliveries {
		if p.Deliveries[i].IdempotencyKey != d.IdempotencyKey {
			continue
		}
		existing := p.Deliveries[i]
		if existing.Mode == d.Mode &&
			existing.BaseRef == d.BaseRef &&
			existing.HeadRef == d.HeadRef &&
			existing.CommitSHA == d.CommitSHA &&
			existing.TreeSHA == d.TreeSHA &&
			existing.Provider == d.Provider &&
			existing.RemoteID == d.RemoteID &&
			existing.URL == d.URL &&
			existing.Status == d.Status &&
			existing.ErrorRef == d.ErrorRef &&
			existing.DiffRef == d.DiffRef &&
			existing.DeferredFiles == d.DeferredFiles {
			s.mu.Unlock()
			return false, nil
		}
		break
	}
	now := s.now()
	payload, rollback, ordinal, err := s.applyDeliveryUpsertLocked(&p, d, now)
	key := deliveryKey{runID: d.RunID, key: d.IdempotencyKey}
	s.proj[d.RunID] = p
	s.mu.Unlock()
	if err != nil {
		s.rollbackAndRebuild(ctx, d.RunID, rollback)
		return false, fmt.Errorf("marshal %s payload: %w", eventKindDeliveryUpserted, err)
	}

	evt := storage.Event{
		ID:       EventID(d.RunID, eventKindDeliveryUpserted, d.IdempotencyKey, strconv.Itoa(ordinal)),
		RunID:    d.RunID,
		Sequence: int(s.nextSequence(d.RunID)),
		Kind:     eventKindDeliveryUpserted,
		Payload:  payload,
	}
	if err := s.appendEvent(ctx, evt, rollback); err != nil {
		if errors.Is(err, ErrConflict) {
			return true, nil // appendEvent already rebuilt projection + ordinals
		}
		return false, err
	}
	// Record the ordinal only after the append succeeded, so a retry of the
	// same upsert (same payload) mints the same event ID and is absorbed as
	// an idempotent duplicate.
	s.mu.Lock()
	if ordinal > s.deliverySeqs[key] {
		s.deliverySeqs[key] = ordinal
	}
	s.mu.Unlock()
	return false, nil
}

// applyDeliveryUpsertLocked applies one delivery upsert to the cached
// projection, computes the deterministic event ordinal and builds the
// wf_delivery_upserted payload. It must be called with the run's per-run
// mutex held (the caller holds s.mu while mutating); the returned rollback
// restores the projection on marshal/append failure.
func (s *StorageRepository) applyDeliveryUpsertLocked(p *Projection, d DeliveryRecord, now time.Time) ([]byte, func(), int, error) {
	rec := d.Clone()
	rec.UpdatedAt = now
	key := deliveryKey{runID: d.RunID, key: d.IdempotencyKey}
	ordinal := s.deliverySeqs[key] + 1
	prevDeliveries := append([]DeliveryRecord(nil), p.Deliveries...)
	found := false
	for i := range p.Deliveries {
		if p.Deliveries[i].IdempotencyKey == d.IdempotencyKey {
			p.Deliveries[i] = rec
			found = true
			break
		}
	}
	if !found {
		p.Deliveries = append(p.Deliveries, rec)
	}
	rollback := func() {
		q := s.proj[d.RunID]
		q.Deliveries = prevDeliveries
		s.proj[d.RunID] = q
	}
	payload, err := marshalDeliveryUpserted(deliveryUpsertedPayload{Delivery: rec, CreatedAt: now})
	if err != nil {
		return nil, rollback, ordinal, err
	}
	return payload, rollback, ordinal, nil
}

// GetDeliveryByIdempotencyKey returns the delivery record for a key.
// Returns ErrNotFound if absent.
func (s *StorageRepository) GetDeliveryByIdempotencyKey(ctx context.Context, key string) (DeliveryRecord, error) {
	if err := s.ensureBuilt(ctx); err != nil {
		return DeliveryRecord{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	ids := make([]string, 0, len(s.proj))
	for id := range s.proj {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		p := s.proj[id]
		if !p.HasRun {
			continue
		}
		for i := range p.Deliveries {
			if p.Deliveries[i].IdempotencyKey == key {
				return p.Deliveries[i].Clone(), nil
			}
		}
	}
	return DeliveryRecord{}, ErrNotFound
}

// ListDeliveries returns the run's delivery records.
func (s *StorageRepository) ListDeliveries(ctx context.Context, runID string) ([]DeliveryRecord, error) {
	if err := s.ensureBuilt(ctx); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.proj[runID]
	if !ok || !p.HasRun {
		return nil, ErrNotFound
	}
	out := make([]DeliveryRecord, 0, len(p.Deliveries))
	for i := range p.Deliveries {
		out = append(out, p.Deliveries[i].Clone())
	}
	return out, nil
}
