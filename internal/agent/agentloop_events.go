// Package agent - event translation for the SDK-backed loop.
//
// The SDK loop publishes lifecycle events onto an events.Bus (name +
// string data); the CLI loop fans agent.Event values out to the
// caller's OnEvent callback and EventBus. The bridge subscribes one
// handler per mapped SDK event name and re-emits through the CLI's
// own emit helper, so session stamping, typed-bus publication, and
// agent attribution behave exactly as the legacy path's do.
//
// Dropped by design, mirroring the legacy surface's droppedKinds
// precedent: iteration-end (the CLI has no per-iteration-end kind)
// and both heartbeat kinds (progress ticks have no CLI
// representation, and the SDK path leaves HeartbeatInterval at zero
// so they never fire anyway).
package agent

import (
	"context"

	sdkagentloop "github.com/MiviaLabs/mivia-ai-sdk/agentloop"
	sdkevents "github.com/MiviaLabs/mivia-ai-sdk/events"
)

// bridgeAgentLoopEvents builds the SDK-side bus whose emissions are
// translated onto the CLI event surface carried by opts. The caller
// installs the returned bus as agentloop.Options.Bus. turn is the
// run's sdkTurnState: the tool-event synthesis carriers
// (sdk_tool_events.go) stash the pending call and recorded outcome
// there, and the iteration-start handler resets the stream-revoke
// gate the PointPreTool hook arms.
func bridgeAgentLoopEvents(opts Options, turn *sdkTurnState) *sdkevents.Bus {
	bus := sdkevents.New()
	// Subscribe errors are impossible here: the names are
	// non-blank package constants and the bus is fresh, so the
	// only failure modes (blank name, duplicate registration)
	// cannot occur. Ignoring the error keeps the handler bodies
	// focused on the translation.
	_ = bus.Subscribe(sdkagentloop.EventIterationStart, func(_ context.Context, e sdkevents.Event) error {
		// Iteration boundary: re-arm the once-per-iteration
		// stream-revoke gate the first PointPreTool of the next
		// iteration arms (the revoke moved off the bus handler;
		// PointPreTool fires before the queued tool_start, the
		// legacy ordering).
		turn.resetStreamRevoke()
		emit(opts, Event{Kind: EventStep, Detail: e.Data})
		return nil
	})
	_ = bus.Subscribe(sdkagentloop.EventToolCallEnd, func(_ context.Context, _ sdkevents.Event) error {
		if turn == nil {
			return nil
		}
		// The SDK fires EventToolCallEnd from a deferred closure on
		// EVERY return path of runOneToolCall, including PointPreTool
		// vetoes and hook errors where no outcome was recorded.
		// Render order: a recorded outcome wins (the legacy
		// completed/failed vocabulary over the redacted body); a
		// pending call with no outcome is a veto - emit the failed
		// fallback with an empty output; neither means the call never
		// reached the host (e.g. an ordinary decode failure the
		// error reporter owns), so nothing is emitted.
		outcome, pending := turn.takeToolCallOutcome()
		switch {
		case outcome != nil:
			// Legacy emitToolEnd preview rule: the redacted body,
			// unless an ephemeral tool supplied a marker override.
			output := redactToolOutputForTool(outcome.name, outcome.body)
			if outcome.previewOverride != "" {
				output = outcome.previewOverride
			}
			emit(opts, Event{
				Kind:       EventToolEnd,
				ToolCallID: outcome.id,
				Name:       outcome.name,
				Detail:     sdkToolEndDetail(*outcome),
				Output:     output,
			})
		case pending != nil:
			emit(opts, Event{
				Kind:       EventToolEnd,
				ToolCallID: pending.ID,
				Name:       pending.Name,
				Detail:     "failed",
				Output:     "",
			})
		}
		return nil
	})
	return bus
}
