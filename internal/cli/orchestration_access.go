package cli

import (
	"context"

	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
)

// Run-handle error envelopes returned by the orchestration read tools.
// INV-AG-9: an unknown run and an inaccessible (foreign-principal) run MUST
// return the identical errJSONUnknownRunID string. Do not split them.
const (
	errJSONUnknownRunID  = `{"error":"unknown run_id"}`
	errJSONRunIDRequired = `{"error":"run_id is required"}`
)

// accessibleOrchestrationHandle performs the run-handle lookup and accessibility
// gate shared by the read/control tools (inspect_agents, join_run, cancel_run,
// list_run_events). On success it returns the resolved handle record and an
// empty error string. On any failure (empty id, unregistered id, wrong type,
// or an inaccessible foreign-principal run) it returns a nil record and the
// matching error JSON string, which the caller returns verbatim.
//
// INV-AG-9: the "not registered" and "registered but inaccessible" cases both
// return errJSONUnknownRunID so a caller cannot tell them apart. This
// indistinguishability is load-bearing - do not add a distinguishing branch.
func accessibleOrchestrationHandle(
	ctx context.Context,
	runID string,
	dispatcher *runtime.Dispatcher,
	repo ledger.LedgerRepository,
) (*orchestrationHandle, string) {
	if runID == "" {
		return nil, errJSONRunIDRequired
	}
	rawHandle, ok := runHandles.Load(runID)
	if !ok {
		return nil, errJSONUnknownRunID
	}
	record, ok := rawHandle.(*orchestrationHandle)
	if !ok || !orchestrationHandleAccessible(ctx, record, dispatcher, repo) {
		return nil, errJSONUnknownRunID
	}
	return record, ""
}
