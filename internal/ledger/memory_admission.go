package ledger

import "context"

func (m *MemoryLedgerRepository) AdmitSingleTask(_ context.Context, a SingleTaskAdmission) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return ErrClosed
	}
	if err := validateSingleTaskAdmission(a); err != nil {
		return err
	}
	if _, ok := m.runs[a.Run.RunID]; ok {
		return ErrDuplicate
	}
	if _, ok := m.idemLookup[a.IdempotencyKey]; ok {
		return ErrDuplicate
	}
	run := a.Run.Clone()
	run.IdempotencyKey = a.IdempotencyKey
	if run.CreatedAt.IsZero() {
		run.CreatedAt = m.now()
	}
	rec := &runRecord{snapshot: run, tasks: map[string]*taskRecord{a.Task.TaskID: {snapshot: a.Task.Clone()}}, events: make([]LifecycleEvent, 0, 16), eventIDs: map[string]struct{}{}, sequences: map[string]uint64{}, idemKeys: map[string]string{a.IdempotencyKey: run.RunID}}
	m.idemLookup[a.IdempotencyKey] = run.RunID
	m.runs[run.RunID] = rec
	return nil
}
