package ledger

import (
	"context"
	"fmt"

	"github.com/MiviaLabs/mivia-agent/internal/storage"
)

// CreateApproval records a pending human-gate request (provisional).
func (s *StorageRepository) CreateApproval(ctx context.Context, a ApprovalRecord) error {
	if err := s.ensureBuilt(ctx); err != nil {
		return err
	}
	lock := s.runLock(a.RunID)
	lock.Lock()
	defer lock.Unlock()

	s.mu.Lock()
	p, ok := s.proj[a.RunID]
	if !ok || !p.HasRun {
		s.mu.Unlock()
		return ErrNotFound
	}
	for _, ap := range p.Approvals {
		if ap.ApprovalID == a.ApprovalID {
			s.mu.Unlock()
			return ErrDuplicate
		}
	}
	now := s.now()
	rec := a.Clone()
	if rec.CreatedAt.IsZero() {
		rec.CreatedAt = now
	}
	p.Approvals = append(p.Approvals, rec)
	s.proj[a.RunID] = p
	s.mu.Unlock()

	payload, err := marshalApprovalCreated(approvalCreatedPayload{Approval: rec, CreatedAt: now})
	if err != nil {
		s.rollbackAndRebuild(ctx, a.RunID, s.removeApprovalRollback(a.RunID, a.ApprovalID))
		return fmt.Errorf("marshal %s payload: %w", eventKindApprovalCreated, err)
	}

	evt := storage.Event{
		ID:       EventID(a.RunID, eventKindApprovalCreated, a.ApprovalID),
		RunID:    a.RunID,
		Sequence: int(s.nextSequence(a.RunID)),
		Kind:     eventKindApprovalCreated,
		Payload:  payload,
	}
	return s.appendEvent(ctx, evt, s.removeApprovalRollback(a.RunID, a.ApprovalID))
}

// removeApprovalRollback returns a rollback closure that removes one approval
// from the cached projection by ID (for failed appends).
func (s *StorageRepository) removeApprovalRollback(runID, approvalID string) func() {
	return func() {
		q := s.proj[runID]
		for i := range q.Approvals {
			if q.Approvals[i].ApprovalID == approvalID {
				q.Approvals = append(q.Approvals[:i], q.Approvals[i+1:]...)
				break
			}
		}
		s.proj[runID] = q
	}
}

// ResolveApproval resolves a pending approval to approved or rejected.
func (s *StorageRepository) ResolveApproval(ctx context.Context, runID, approvalID, actor, status, reason string) error {
	if err := s.ensureBuilt(ctx); err != nil {
		return err
	}
	lock := s.runLock(runID)
	lock.Lock()
	defer lock.Unlock()

	s.mu.Lock()
	p, ok := s.proj[runID]
	if !ok || !p.HasRun {
		s.mu.Unlock()
		return ErrNotFound
	}
	idx := -1
	for i := range p.Approvals {
		if p.Approvals[i].ApprovalID == approvalID {
			idx = i
			break
		}
	}
	if idx < 0 {
		s.mu.Unlock()
		return ErrNotFound
	}
	cur := &p.Approvals[idx]
	if cur.Status != "pending" || (status != "approved" && status != "rejected") {
		s.mu.Unlock()
		return ErrInvalidTransition
	}
	now := s.now()
	prev := cur.Clone()
	cur.Status = status
	cur.Actor = actor
	cur.Reason = reason
	cur.ResolvedAt = &now
	s.proj[runID] = p
	s.mu.Unlock()

	payload, err := marshalApprovalResolved(approvalResolvedPayload{
		ApprovalID: approvalID,
		Status:     status,
		Actor:      actor,
		Reason:     reason,
		ResolvedAt: now,
		CreatedAt:  now,
	})
	if err != nil {
		s.rollbackAndRebuild(ctx, runID, func() {
			q := s.proj[runID]
			if idx < len(q.Approvals) {
				q.Approvals[idx] = prev
			}
			s.proj[runID] = q
		})
		return fmt.Errorf("marshal %s payload: %w", eventKindApprovalResolved, err)
	}

	evt := storage.Event{
		ID:       EventID(runID, eventKindApprovalResolved, approvalID),
		RunID:    runID,
		Sequence: int(s.nextSequence(runID)),
		Kind:     eventKindApprovalResolved,
		Payload:  payload,
	}
	return s.appendEvent(ctx, evt, func() {
		q := s.proj[runID]
		if idx < len(q.Approvals) {
			q.Approvals[idx] = prev
		}
		s.proj[runID] = q
	})
}

// ListApprovals returns the run's approval records.
func (s *StorageRepository) ListApprovals(ctx context.Context, runID string) ([]ApprovalRecord, error) {
	if err := s.ensureBuilt(ctx); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.proj[runID]
	if !ok || !p.HasRun {
		return nil, ErrNotFound
	}
	out := make([]ApprovalRecord, 0, len(p.Approvals))
	for i := range p.Approvals {
		out = append(out, p.Approvals[i].Clone())
	}
	return out, nil
}
