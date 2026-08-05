package ledger

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"sort"
)

// newHolderID generates a random per-process identifier for run execution
// claims. It is never a principal, session ID or role.
//
// crypto/rand.Read never returns an error and always fills its buffer,
// crashing the program itself if the operating system's source fails, so
// there is no error to handle.
func newHolderID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return "wfh-" + base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b[:])
}

// Recover brings the projection up to date, classifies every run, and
// clears stale claims on terminal runs only. It mutates no run status.
func (s *StorageRepository) Recover(ctx context.Context) ([]RecoveredRun, error) {
	if err := s.checkOpen(); err != nil {
		return nil, err
	}
	if err := s.catchUp(ctx); err != nil {
		return nil, err
	}

	type classification struct {
		run        RecoveredRun
		clearClaim bool
	}
	s.mu.RLock()
	ids := make([]string, 0, len(s.proj))
	for id := range s.proj {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	classified := make([]classification, 0, len(ids))
	for _, id := range ids {
		p := s.proj[id]
		if !p.HasRun || p.Run == nil {
			continue
		}
		r := p.Run
		terminal := IsTerminalRunStatus(r.Status) || IsTerminalStepID(p.ActiveStepID)
		classified = append(classified, classification{
			run: RecoveredRun{
				RunID:          id,
				WorkflowName:   r.WorkflowName,
				Status:         r.Status,
				WasInterrupted: IsResumableRunStatus(r.Status) && !IsTerminalStepID(p.ActiveStepID),
				CreatedAt:      r.StartedAt,
			},
			clearClaim: terminal,
		})
	}
	s.mu.RUnlock()

	out := make([]RecoveredRun, 0, len(classified))
	for _, c := range classified {
		out = append(out, c.run)
		if c.clearClaim {
			if err := s.store.ClearClaim(ctx, c.run.RunID); err != nil {
				return out, err
			}
			s.mu.Lock()
			delete(s.claimedRuns, c.run.RunID)
			s.mu.Unlock()
		}
	}
	return out, nil
}
