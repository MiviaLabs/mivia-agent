package ledger

import (
	"context"
	"time"
)

// RecoveredRun describes a run that was recovered from durable storage.
type RecoveredRun struct {
	RunID          string
	DisplayName    string
	Status         RunStatus
	WasInterrupted bool
	// CreatedAt is when the run was created, carried through so callers can tell
	// a run interrupted moments ago from one abandoned days back. Both classify
	// identically, and only the first is worth telling the user about.
	CreatedAt time.Time
}

// Recover brings the projection up to date and classifies every run, reporting
// which ones were left non-terminal by a previous process. It deliberately
// mutates no run status: there is no non-terminal "interrupted" status to write,
// and all three terminal statuses make ResumeInterruptedRun refuse the run, so
// marking anything here would destroy the recoverability this report exists to
// advertise. Classification is therefore recomputed on every call, and a run
// stays reported as interrupted until it is resumed, canceled or deleted.
//
// It also clears stale execution claims on terminal runs (the holder crashed
// before releasing the claim). Non-terminal runs with stale claims are NOT
// cleared because a live concurrent process may be executing without
// re-acquiring its claim on every write. Stale non-terminal claims require
// explicit user intervention (CLI force-release).
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
			CreatedAt:      r.CreatedAt,
		})
		// Clear stale claims on terminal runs: a run that completed but still
		// has a claim means the holder crashed before releasing it.
		// Non-terminal runs are NOT cleared here - a live concurrent holder
		// does not re-acquire its claim on every store write, so clearing
		// would let a third process steal the run mid-execution.
		if isRunTerminal(r.Status) {
			_ = s.store.ClearClaim(ctx, r.RunID)
		}
	}
	return recovered, nil
}
