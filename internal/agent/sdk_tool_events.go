package agent

// SDK-path synthesis of the legacy tool_start/tool_end wire shape.
//
// The legacy loop emits two tool_start rows per call - "queued" with
// the redacted input preview when the batch is admitted
// (loop_tools.go runToolBatch) and "running" right before dispatch
// (loop_tool_exec.go executeToolTask) - and one tool_end whose Detail
// carries the legacy completed/failed vocabulary with the redacted
// body (emitToolEnd). The SDK loop has none of that: its bus carries
// opaque label payloads only. The carriers here rebuild the legacy
// shape on the SDK path:
//
//   - PointPreTool (sdkToolEventHooks) owns the "queued" start: it
//     fires before the SDK decodes the call, mirrors the legacy
//     revoke-on-first-call-of-iteration stream gate, and stashes the
//     call on the turn state.
//   - the dispatcher shim (sdk_dispatcher_shim.go Run) owns the
//     "running" start and records the post-cap outcome.
//   - the EventToolCallEnd bus handler (agentloop_events.go) renders
//     the tool_end from the recorded outcome, reusing the legacy
//     toolEndDetail vocabulary and redaction helpers.
//
// The decisive consumer pin is internal/cli/characterization_test.go
// (TestCharacterization_ToolCallRoundTrip,
// TestCharacterization_HookBlockedToolCall), which must stay green
// unedited once internal/chat's backend flips to "sdk".

import (
	"context"
	"errors"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
	sdkhooks "github.com/MiviaLabs/mivia-ai-sdk/hooks"
	sdkshape "github.com/MiviaLabs/mivia-ai-sdk/provider"
)

// toolCallOutcome is one call's recorded execution result: the
// model-visible body (post cap, hook context, and any ref-only or
// turn-shaping rewrite) and whether the dispatcher reported it failed.
// duplicate + originalBody mirror the legacy toolExecResult pair
// (loop_scheduler.go:38-44): duplicate marks a dedup-cache-served
// call, and originalBody preserves the OWNER's pre-rewrite body so
// toolEndDetail can scan it for a run_command exit=N header (the
// suppression notice that replaced the body carries no status of its
// own).
type toolCallOutcome struct {
	id     string
	name   string
	body   string
	failed bool
	// duplicate marks a result served from the dedup cache rather
	// than executed. Its model-visible body is the suppression notice,
	// never the recorded body, so the operator row needs its own
	// failure signal.
	duplicate bool
	// originalBody is the recorded body a duplicate was served from,
	// retained ONLY for the operator row: toolEndDetail judges a
	// duplicate's failure signal against this original output (a
	// run_command duplicate reports its child exit in the recorded
	// header with err=nil), because the notice that replaced it
	// carries no status of its own.
	originalBody string
	// previewOverride replaces the redacted body in the tool_end
	// Output field only (the model still sees body): the legacy
	// emitToolEnd substitutes an ephemeral tool's marker here so the
	// resource body never reaches the operator surface.
	previewOverride string
}

// sdkToolEventState fields live on sdkTurnState (sdk_dispatcher_shim.go);
// the accessors below are their single synchronization point. The SDK
// runs tool calls sequentially from the run goroutine, so the mutex
// only has to hold off the bus handler's take, not sibling calls.

// beginToolCall stashes the call PointPreTool admitted and clears any
// stale outcome, mirroring the legacy queued-start position.
func (s *sdkTurnState) beginToolCall(call *sdkshape.ToolCall) {
	s.toolMu.Lock()
	defer s.toolMu.Unlock()
	s.pendingTool = call
	s.toolOutcome = nil
}

// endToolCall clears the pending call (PointPostTool). The recorded
// outcome, if any, survives for the EventToolCallEnd render.
func (s *sdkTurnState) endToolCall() {
	s.toolMu.Lock()
	defer s.toolMu.Unlock()
	s.pendingTool = nil
}

// currentPendingToolCall returns the call PointPreTool stashed, or nil.
func (s *sdkTurnState) currentPendingToolCall() *sdkshape.ToolCall {
	s.toolMu.Lock()
	defer s.toolMu.Unlock()
	return s.pendingTool
}

// recordToolOutcome records one executed call's outcome: the final
// model-visible body and the dispatcher's failure flag. Callers that
// are not the dispatcher shim pass zero values for duplicate and
// originalBody; only a dedup-cache-served dispatch carries both.
func (s *sdkTurnState) recordToolOutcome(id, name, body string, failed bool) {
	s.recordToolOutcomeWithPreview(id, name, body, failed, "", false, "")
}

// recordToolOutcomeWithPreview is recordToolOutcome with the tool_end
// Output override (the ephemeral marker) and the duplicate/originalBody
// pair the legacy toolEndDetail uses to emit the "(duplicate)" suffix
// on a dedup-cache-served call.
func (s *sdkTurnState) recordToolOutcomeWithPreview(id, name, body string, failed bool, previewOverride string, duplicate bool, originalBody string) {
	s.toolMu.Lock()
	defer s.toolMu.Unlock()
	s.toolOutcome = &toolCallOutcome{id: id, name: name, body: body, failed: failed, duplicate: duplicate, originalBody: originalBody, previewOverride: previewOverride}
}

// overwriteToolOutcomeBody replaces the recorded outcome's body after
// a later shim (ref-only notice, turn-shaping re-cut) rewrites it, so
// tool_end matches the legacy post-shaping body the model sees.
func (s *sdkTurnState) overwriteToolOutcomeBody(body string) {
	s.toolMu.Lock()
	defer s.toolMu.Unlock()
	if s.toolOutcome != nil {
		s.toolOutcome.body = body
	}
}

// takeToolCallOutcome consumes the recorded outcome and the pending
// call. The pending call outlives the outcome only on a veto or hook
// error (EventToolCallEnd fires from a deferred closure on every
// return path; no outcome is recorded there), which the caller renders
// as the amendment-5 failed fallback.
func (s *sdkTurnState) takeToolCallOutcome() (*toolCallOutcome, *sdkshape.ToolCall) {
	s.toolMu.Lock()
	defer s.toolMu.Unlock()
	o, p := s.toolOutcome, s.pendingTool
	s.toolOutcome, s.pendingTool = nil, nil
	return o, p
}

// armStreamRevoke is the once-per-iteration stream-revoke gate the
// EventIterationStart bus subscription resets. It reports whether the
// caller won the arm (first tool call of the iteration).
func (s *sdkTurnState) armStreamRevoke() bool {
	return s.streamRevoked.CompareAndSwap(false, true)
}

// resetStreamRevoke re-arms the gate at an iteration boundary.
func (s *sdkTurnState) resetStreamRevoke() {
	s.streamRevoked.Store(false)
}

// sdkToolEventHooks builds the hooks.Registry the SDK options carry:
// a PointPreTool observer that arms the stream revoke, stashes the
// call, and emits the legacy "queued" tool_start, and a PointPostTool
// observer that clears the stash. Both always allow (observers never
// veto - the approval and admission gates live elsewhere).
func sdkToolEventHooks(opts Options, turn *sdkTurnState) *sdkhooks.Registry {
	reg := sdkhooks.New()
	_ = reg.Add(sdkhooks.PointPreTool, "agent.tool-events", func(_ context.Context, payload any) (bool, error) {
		call, ok := payload.(sdkshape.ToolCall)
		if !ok {
			return true, nil
		}
		stash := call
		turn.beginToolCall(&stash)
		// Content-then-tools: the first tool call of an iteration
		// clears the optimistic final-stream tokens, the same
		// revokeStreamWriter call the legacy loop makes ahead of
		// processToolCalls, once per iteration.
		if turn.armStreamRevoke() {
			revokeStreamWriter(opts.FinalWriter)
		}
		emit(opts, Event{
			Kind:       EventToolStart,
			ToolCallID: call.ID,
			Name:       call.Name,
			Detail:     "queued",
			Input:      redactToolInput(string(call.Arguments)),
		})
		return true, nil
	})
	_ = reg.Add(sdkhooks.PointPostTool, "agent.tool-events", func(_ context.Context, _ any) (bool, error) {
		turn.endToolCall()
		return true, nil
	})
	return reg
}

// sdkToolEndDetail reuses the legacy toolEndDetail vocabulary
// (loop_tools.go) for one recorded SDK-path outcome: a dedup-cache
// served call emits the "(duplicate)" suffix (failed or completed),
// failed takes precedence, the run_command body scan applies to the
// ORIGINAL body the dedup cache served, and completed is the healthy
// word the uiadapter derives status ok from.
func sdkToolEndDetail(o toolCallOutcome) string {
	var call provider.ToolCall
	call.ID = o.id
	call.Function.Name = o.name
	var err error
	if o.failed {
		err = errors.New("tool call failed")
	}
	return toolEndDetail(toolExecResult{
		toolCall:     call,
		result:       o.body,
		err:          err,
		duplicate:    o.duplicate,
		originalBody: o.originalBody,
	})
}
