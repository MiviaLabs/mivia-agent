package cliorchestrate

import (
	"errors"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/coordinator"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
)

// RegisterChildRunHandle registers one child run (a workflow or panel step
// run) in the orchestration handle registry, so the standard control tools
// (inspect_agents, join_run, cancel_run) can resolve it by run ID. Without
// this registration those tools answer "unknown run_id" for every child run,
// and a session can neither inspect nor cancel work it started.
//
// The record carries two identities on purpose:
//   - repo is the OWNING SESSION's effective orchestration repo - the same
//     instance the session's control tools carry, and the one the access gate
//     compares (repositoriesMatch). It participates only in that gate.
//   - c, h, and d stay the child run's own execution side: the coordinator
//     that runs the child, its live handle, and the dispatcher whose close
//     evicts the record.
//
// The seam mirrors StoreTestRunHandle's parameter order, but it is production
// wiring, not a test fixture:
//   - The owner is the explicit sessionID parameter. The seam never derives
//     the principal from ctx, and it refuses an empty owner: a zero-owner
//     registration would stay invisible to every session forever.
//   - Registration goes through storeOrchestrationHandle, so a repeat call for
//     an existing run ID keeps the ORIGINAL owner (LoadOrStore), and the
//     retention timer plus the OnClose eviction wiring come from that one
//     code path.
//   - The dispatcher must be non-nil. The OnClose closure would panic on a
//     nil receiver, so the seam refuses it here at the boundary.
//
// The seam is additive: it changes no coordinator or pool behavior. cfg
// supplies only the handle retention window (see orchestrationHandleRetention).
func RegisterChildRunHandle(runID string, c coordinator.Coordinator, h *coordinator.RunHandle, repo ledger.LedgerRepository, d *runtime.Dispatcher, sessionID string, cfg config.SubagentConfig) error {
	if d == nil {
		return errors.New("child run registration needs a dispatcher")
	}
	if h == nil {
		return errors.New("child run registration needs a run handle")
	}
	if sessionID == "" {
		return errors.New("child run registration needs an owning session ID")
	}
	storeOrchestrationHandle(runID, &orchestrationHandle{
		coord:      c,
		handle:     h,
		repo:       EffectiveOrchestrationRepo(repo),
		dispatcher: d,
		principal:  orchestrationPrincipal{sessionID: sessionID},
		retention:  orchestrationHandleRetention(cfg),
	})
	return nil
}
