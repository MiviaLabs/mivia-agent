package coordinator

import (
	"context"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
)

// registerSubagentToolCanceler installs the ToolCanceler for one task,
// overwriting any prior registration for the same taskID (a task never
// re-enters this path more than once per invocation - MultiStepHandler's
// OnToolCancelReady hook fires exactly once per SDK-backed run - so an
// overwrite here only ever happens across genuinely distinct invocations,
// never mid-flight for the one this call belongs to). Safe for concurrent
// registration across sibling tasks; a nil handle, blank taskID, or nil
// canceler is a no-op.
func (h *RunHandle) registerSubagentToolCanceler(taskID string, canceler agent.ToolCanceler) {
	if h == nil || taskID == "" || canceler == nil {
		return
	}
	h.subagentToolCancelMu.Lock()
	if h.subagentToolCancelers == nil {
		h.subagentToolCancelers = map[string]agent.ToolCanceler{}
	}
	h.subagentToolCancelers[taskID] = canceler
	h.subagentToolCancelMu.Unlock()
}

// subagentToolCanceler returns the registered ToolCanceler for a task, if
// any. ok is false when the task never registered one: it has not started,
// its loop uses the legacy (non-SDK) backend, or - for a recovered handle -
// this process never ran the task at all and so never saw its hook fire.
func (h *RunHandle) subagentToolCanceler(taskID string) (agent.ToolCanceler, bool) {
	if h == nil {
		return nil, false
	}
	h.subagentToolCancelMu.RLock()
	c, ok := h.subagentToolCancelers[taskID]
	h.subagentToolCancelMu.RUnlock()
	return c, ok
}

// RegisterSubagentToolCanceler implements Coordinator. See the interface
// doc comment (types.go) for the contract; HandleForRun already returns nil
// for an unknown runID, so a not-yet-visible or already-evicted run is a
// clean no-op rather than a panic.
func (c *coordinator) RegisterSubagentToolCanceler(runID, taskID string, canceler agent.ToolCanceler) {
	if c == nil || runID == "" {
		return
	}
	h := c.HandleForRun(runID)
	if h == nil {
		return
	}
	h.registerSubagentToolCanceler(taskID, canceler)
}

// CancelSubagentToolCall implements Coordinator. See the interface doc
// comment (types.go) for the contract.
func (c *coordinator) CancelSubagentToolCall(ctx context.Context, h *RunHandle, taskID, callID string) (bool, error) {
	if err := c.validateHandle(h); err != nil {
		return false, err
	}
	canceler, ok := h.subagentToolCanceler(taskID)
	if !ok || canceler == nil {
		return false, nil
	}
	return canceler(callID), nil
}
