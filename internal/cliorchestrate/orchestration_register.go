package cliorchestrate

import (
	"errors"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/coordinator"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
)

// RegisterChildRunHandle registers one child run (workflow or panel step run)
// in the orchestration handle registry so control tools (inspect_agents,
// join_run, cancel_run) can resolve it by run ID rather than returning
// "unknown run_id".
//
// The record carries two identities:
//   - repo: owning session orchestration repo checked by repositoriesMatch gate.
//   - c, h, d: child execution side (coordinator, run handle, dispatcher that
//     evicts on close).
//
// Seam characteristics (mirrors StoreTestRunHandle parameter order):
//   - sessionID is the explicit non-empty owner (never derived from ctx).
//   - Uses storeOrchestrationHandle (LoadOrStore preserves original owner;
//     configures retention timer and OnClose eviction).
//   - Dispatcher must be non-nil to prevent panic on close.
//   - Additive; cfg supplies handle retention window (orchestrationHandleRetention).
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
