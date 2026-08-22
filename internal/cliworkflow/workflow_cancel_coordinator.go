package cliworkflow

import (
	"github.com/MiviaLabs/mivia-agent/internal/coordinator"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/controller"
)

// cliPanelCancelCoordinator returns the coordinator that can inspect and
// cancel exact panel children for an operator-driven cancel (D15). It
// mirrors localengine.Engine.panelCancelCoordinator: when live is non-nil,
// it reuses the exact instance the in-flight run dispatched panel children
// with, so a live local-actor member is genuinely canceled instead of merely
// refusing a held claim. Otherwise it builds a fresh, cancel-only
// coordinator over store's own ledger.
//
// The fresh coordinator carries a nil subagent pool. This is deliberately
// minimal, not a placeholder: cancel-only admission (JoinAsRecovered,
// EnsureTerminalSingleTaskRun, and Cancel's recovered path) never dispatches
// a handler, so it never touches the pool. Building the full provider-backed
// coordinator here would force auth, provider routing, and a model catalog
// onto an operator who only wants to cancel a stuck or misconfigured run,
// contradicting D15's stated intent that terminal admission needs no
// provider credentials or tool execution authority.
func cliPanelCancelCoordinator(live *controller.CoordinatorRunner, store *storage.SQLite) coordinator.Coordinator {
	if live != nil {
		return live.Coordinator
	}
	if store == nil {
		return nil
	}
	return coordinator.New(ledger.NewStorageLedgerRepository(store), nil)
}
