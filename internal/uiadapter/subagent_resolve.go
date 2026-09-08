package uiadapter

import (
	"context"
	"encoding/json"
	"time"
)

// toolCallStepMirror is a package-local, field-for-field mirror of
// internal/subagents.ToolCallStep. It is duplicated rather than imported
// because internal/uiadapter must never import internal/subagents
// (INV-TUI-29's import-layers allow-list) - the same reason
// toolCallContentResolver mirrors a ledger method set structurally instead
// of importing internal/ledger, and namespacedTaskID in
// subagent_reconstruct.go duplicates internal/cliorchestrate's function of
// the same name.
//
// No json tags: the real producer (internal/coordinator/record_results.go)
// marshals subagents.ToolCallStep with none either, so Go's default
// field-name-based JSON matching is what the wire actually uses, and
// giving this mirror tags it does not need would be a second, unforced
// place the two could drift. subagent_wire_contract_test.go pins the two
// types field-for-field AND pins the exact JSON key set, so a future field
// added to the real type without a matching mirror update fails loudly
// here instead of silently reconstructing incomplete tool-call rows.
type toolCallStepMirror struct {
	ToolCallID string
	Name       string
	Kind       string // "start" | "end"
	Input      string
	Output     string
	At         time.Time
}

// mergeToolCallSteps folds a flat, chronological []toolCallStepMirror (the
// raw per-step trace a coordinator-dispatched task records, one entry per
// tool_start/tool_end) into one toolCallSummary row per tool invocation,
// with LIFO start/end pairing:
//
//   - A "start" step ALWAYS opens a NEW row, even when a CLOSED row for the
//     same ToolCallID already exists. This is what correctly renders a
//     duplicate-ID re-emit (the same ID legitimately reused later in the
//     trace) as two separate rows instead of merging them into one.
//   - An "end" step closes the MOST RECENTLY OPENED still-open row sharing
//     its ToolCallID - a per-ID stack, not a flat map keyed only by ID,
//     because a flat map mis-pairs two interleaved parallel calls that
//     happen to share an ID (start A, start A, end A, end A must close the
//     SECOND open A with the FIRST end, and the FIRST open A with the
//     SECOND end - LIFO order - not both ends racing the same map slot).
//   - A step whose Kind is neither "start" nor "end" is skipped.
//   - An "end" with no open row for its ToolCallID is dropped: there is
//     nothing to close, and fabricating a row from an end alone would
//     invent a call that never started.
//   - A row still open at the end of the step list (a start with no
//     matching end - the trace was truncated, or the call never finished)
//     is still emitted, with Incomplete=true.
//
// Row order in the result is FIRST-OPEN order: rows appear in the order
// their opening "start" was seen, matching how the legacy inline
// toolCallSummary wire shape (matchTaskToolCalls) already reads today, so
// a reader sees calls in the order the subagent actually made them.
func mergeToolCallSteps(steps []toolCallStepMirror) []toolCallSummary {
	var rows []toolCallSummary
	open := make(map[string][]int)
	for _, s := range steps {
		switch s.Kind {
		case "start":
			rows = append(rows, toolCallSummary{
				ToolCallID: s.ToolCallID,
				Name:       s.Name,
				Input:      s.Input,
				Incomplete: true,
			})
			open[s.ToolCallID] = append(open[s.ToolCallID], len(rows)-1)
		case "end":
			stack := open[s.ToolCallID]
			if len(stack) == 0 {
				continue
			}
			top := stack[len(stack)-1]
			open[s.ToolCallID] = stack[:len(stack)-1]
			rows[top].Output = s.Output
			rows[top].Incomplete = false
		default:
			continue
		}
	}
	return rows
}

// resolveToolCallsPending resolves this conversation's sourceToolCallsRef
// into real tool-call rows the first time it is called on a conversation
// carrying one, replacing the static "(tool calls recorded)" placeholder
// History() has rendered since slice 2. It is a no-op on every call after
// the first - successful or not (see the resolveAttempted field's doc
// comment for why a failed attempt is cached rather than retried).
//
// LOCKING: c.mu is deliberately NOT held across the LoadContent call. The
// resolver is an external ledger repository this package does not control
// the timing of (a real implementation may hit disk or a remote store);
// holding c.mu across that call would block every other method on this
// conversation - RecordEvent, ActiveTurn, another History() - for as long
// as that I/O takes, on a conversation that (per the SubagentThreads
// contract) can still be live-registered under other keys. Instead this
// copies the two fields it needs (sourceToolCallsRef, contentResolver)
// under a short lock, releases it, calls LoadContent unlocked, then
// re-acquires c.mu to re-check resolved/resolveAttempted (a concurrent
// caller may have already finished a resolution while this one was
// in flight) before writing the result - the standard double-checked
// pattern. A concurrent duplicate LoadContent call in the race window
// before the first attempt's result lands is accepted: it is wasted work,
// not a correctness hazard, since the final write is still guarded and
// idempotent.
func (c *SubagentTranscriptConversation) resolveToolCallsPending() {
	c.mu.Lock()
	if c.resolved || c.resolveAttempted || c.sourceToolCallsRef == "" || c.contentResolver == nil {
		c.mu.Unlock()
		return
	}
	ref := c.sourceToolCallsRef
	resolver := c.contentResolver
	c.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), subagentTaskTimeout)
	defer cancel()
	raw, err := resolver.LoadContent(ctx, ref)

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.resolved || c.resolveAttempted {
		// A concurrent caller already resolved (or gave up on) this ref
		// while this call's LoadContent was in flight; do not double-apply.
		return
	}
	if err != nil {
		c.resolveAttempted = true
		return
	}
	var steps []toolCallStepMirror
	if jsonErr := json.Unmarshal(raw, &steps); jsonErr != nil {
		c.resolveAttempted = true
		return
	}
	c.applyResolvedToolCalls(mergeToolCallSteps(steps))
	c.resolved = true
	c.resolveAttempted = true
}

// applyResolvedToolCalls replaces the placeholder assistant message's
// Text/ToolCalls IN PLACE with the resolved rows - never appended, since
// the placeholder message already occupies the slot a live/legacy-decoded
// tool-call message would. It targets the LAST assistant message in
// history: registerDispatchedTask appends at most one assistant message
// per reconstructed conversation, and only when it has content (real
// output text, legacy inline tool calls, or - the case this resolves -
// the toolCallsRecordedNotice placeholder), so that message is always the
// one carrying the pending ref.
//
// A placeholder Text of exactly toolCallsRecordedNotice is cleared to ""
// once real tool calls replace what it stood in for; any OTHER text
// (resultText found a real Output/Synopsis/Error already) is left
// untouched, since a task can carry both real output text and a recorded
// tool-call trace.
//
// Caller must hold c.mu.
func (c *SubagentTranscriptConversation) applyResolvedToolCalls(merged []toolCallSummary) {
	for i := len(c.history) - 1; i >= 0; i-- {
		if c.history[i].Role != "assistant" {
			continue
		}
		if c.history[i].Text == toolCallsRecordedNotice {
			c.history[i].Text = ""
		}
		c.history[i].ToolCalls = toolCallSummariesToPortsToolCalls(merged)
		return
	}
}
