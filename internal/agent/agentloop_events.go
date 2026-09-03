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
// precedent: iteration-end (the CLI has no per-iteration-end kind),
// both heartbeat kinds (progress ticks have no CLI representation,
// and the SDK path leaves HeartbeatInterval at zero so they never
// fire anyway), the thinking bracket (the adapter's onUsage already
// publishes EventThinking from the same response, agentloop_adapter.go),
// and cache usage, calibration deltas and tool_parallel, which nothing
// on the CLI surface consumes from this bridge. EventAssistant is
// bridged as a content-free completion flag; see the subscription below.
package agent

import (
	"context"

	sdkagentloop "github.com/MiviaLabs/mivia-ai-sdk/agentloop"
	sdkevents "github.com/MiviaLabs/mivia-ai-sdk/events"
	"github.com/MiviaLabs/mivia-ai-sdk/toolcallctx"

	"github.com/MiviaLabs/mivia-agent/internal/events"
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
		turn.resetIterationShaping()
		emit(opts, Event{Kind: EventStep, Detail: e.Data})
		return nil
	})
	// The loop recorded one complete assistant message: afterChat emits this
	// after the completer returned - every delta of that message has already
	// been written, synchronously, in the calling goroutine - and BEFORE any
	// tool of the same iteration runs. It is the one same-stream, per-message
	// proof that the model stopped talking, which is what the chat-sync
	// projector needs to release its held tail without waiting for the next
	// block close. The content is deliberately NOT carried: a non-delta
	// KindAssistant with text is a settled aggregate to every other consumer,
	// and the turn keeps exactly one of those (finalizeSDKTurn).
	//
	// The SDK event is ABSENT, not empty, for a message with no content: the
	// bus's Event.Validate rejects empty Data, so a tool-call-only response
	// emits nothing here. Nothing is pending for such a response, so nothing
	// is lost.
	_ = bus.Subscribe(sdkagentloop.EventAssistant, func(_ context.Context, _ sdkevents.Event) error {
		emit(opts, Event{Kind: EventAssistant, Detail: events.DetailAssistantComplete})
		return nil
	})
	_ = bus.Subscribe(sdkagentloop.EventToolCallEnd, func(ctx context.Context, _ sdkevents.Event) error {
		if turn == nil {
			return nil
		}
		// The SDK fires EventToolCallEnd with callCtx on EVERY return path
		// of runOneToolCall, including PointPreTool vetoes and hook errors.
		var outcome *toolCallOutcome
		var callKey, callName string
		if tc, ok := toolcallctx.ToolCallFromContext(ctx); ok {
			callKey = tc.ID
			if callKey == "" {
				callKey = tc.Name
			}
			callName = tc.Name
		}
		if callKey != "" {
			outcome = turn.takeToolCallOutcome(callKey)
		}
		switch {
		case outcome != nil:
			emit(opts, toolEndEventFor(*outcome))
		case callKey != "":
			// No recorded outcome means the SDK's dedup short-circuited
			// the call BEFORE the dispatcher shim could record one
			// (sdkagentloop.runToolCalls.planCalls returns the cached
			// DuplicateCallNotice without invoking runOneToolCall). The
			// model still saw a successful tool message - the dedup
			// cache served it - so the operator-facing detail must
			// not read "failed". Emit "completed (duplicate)" to match
			// the legacy toolEndDetail vocabulary, with an empty body
			// because the suppression notice is model-side, not
			// operator-side.
			emit(opts, Event{
				Kind:       EventToolEnd,
				ToolCallID: callKey,
				Name:       callName,
				Detail:     "completed (duplicate)",
				Output:     "",
			})
		}
		return nil
	})
	return bus
}
