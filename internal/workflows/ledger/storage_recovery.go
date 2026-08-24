package ledger

import (
	"context"
	"sort"

	"github.com/MiviaLabs/mivia-agent/internal/ledgercore"
)

// newHolderID generates a random per-process identifier for run execution
// claims. It is never a principal, session ID or role.
//
// crypto/rand.Read never returns an error and always fills its buffer,
// crashing the program itself if the operating system's source fails, so
// there is no error to handle.
func newHolderID() string {
	return "wf" + ledgercore.NewHolderID()
}

// Recover brings the projection up to date, classifies every run, and
// clears stale claims on terminal runs only. It mutates no run status.
//
// Carve-out: a delivery_pending run parked at a reserved terminal step
// ("success"/"failure") keeps its claim. The delivery phase runs after the
// success terminal, outside the step graph, and the live publisher holds the
// claim for the whole publish; unconditionally clearing it here would let a
// second host claim the run and double-deliver. Stale delivery claims are
// reclaimed by the delivery path's lease takeover (TakeoverExpiredRunClaim
// with DefaultClaimLease), never by Recover.
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
		// Delivery-pending carve-out: a run parked at a reserved terminal step
		// while waiting for publication is settled, but its claim is LIVE — the
		// publisher holds and heartbeats it for the whole publish. Only clear
		// the claim on the terminal-status branch or on a terminal step that is
		// not a delivery_pending pause.
		clearClaim := IsTerminalRunStatus(r.Status) || (IsTerminalStepID(p.ActiveStepID) && r.Status != RunStatusDeliveryPending)
		classified = append(classified, classification{
			run: RecoveredRun{
				RunID:          id,
				WorkflowName:   r.WorkflowName,
				Status:         r.Status,
				WasInterrupted: IsResumableRunStatus(r.Status) && !IsTerminalStepID(p.ActiveStepID),
				CreatedAt:      r.StartedAt,
			},
			clearClaim: clearClaim,
		})
	}
	s.mu.RUnlock()

	out := make([]RecoveredRun, 0, len(classified))
	for _, c := range classified {
		out = append(out, c.run)
		if c.clearClaim {
			if err := s.engine.Store().ClearClaim(ctx, c.run.RunID); err != nil {
				return out, err
			}
			s.engine.Claims().DropClaim(c.run.RunID)
		}
	}
	return out, nil
}
