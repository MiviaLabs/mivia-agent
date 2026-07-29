package ledger

import "context"

// RecoveredRun describes a run that was recovered from durable storage.
type RecoveredRun struct {
	RunID          string
	DisplayName    string
	Status         RunStatus
	WasInterrupted bool
}

// Recover scans the store for all runs, brings the projection up to date, and
// marks any run with a non-terminal status as interrupted. It also clears
// stale execution claims on terminal runs (the holder crashed before
// releasing the claim).
func (s *StorageLedgerRepository) Recover(ctx context.Context) ([]RecoveredRun, error) {
	if err := s.checkOpen(); err != nil {
		return nil, err
	}
	if err := s.catchUp(ctx); err != nil {
		return nil, err
	}
	runs, err := s.mem.ListRuns(ctx)
	if err != nil {
		return nil, err
	}
	recovered := make([]RecoveredRun, 0, len(runs))
	for _, r := range runs {
		recovered = append(recovered, RecoveredRun{
			RunID: r.RunID, DisplayName: r.DisplayName, Status: r.Status,
			WasInterrupted: r.Status == RunStatusRunning || r.Status == RunStatusQueued || r.Status == RunStatusCreated,
		})
		// Clear stale claims on terminal runs: a run that completed but still
		// has a claim means the holder crashed before releasing it.
		if isRunTerminal(r.Status) {
			_ = s.store.ClearClaim(ctx, r.RunID)
		}
	}
	return recovered, nil
}
