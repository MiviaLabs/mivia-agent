// Package agent - event translation for the SDK-backed loop.
//
// The SDK loop publishes lifecycle events onto an events.Bus (name +
// string data); the CLI loop fans agent.Event values out to the
// caller's OnEvent callback and EventBus. The bridge subscribes one
// handler per mapped SDK event name and re-emits through the CLI's
// own emit helper, so session stamping, typed-bus publication, and
// agent attribution behave exactly as the legacy path's do.
//
// Heartbeat kinds ARE bridged now that HeartbeatInterval is adopted
// (agentloop_adoption.go): both tick kinds re-emit as the legacy
// EventHeartbeat "working" tick. Still dropped, mirroring the legacy
// surface's droppedKinds precedent: iteration-end (the CLI has no
// per-iteration-end kind), the thinking bracket (the adapter's onUsage already
// publishes EventThinking from the same response, agentloop_adapter.go),
// and cache usage, calibration deltas and tool_parallel, which nothing
// on the CLI surface consumes from this bridge. EventAssistant is
// bridged as a content-free completion flag; see the subscription below.
package agent

import (
	"context"

	sdkagentloop "github.com/MiviaLabs/mivia-ai-sdk/agentloop"
	sdkevents "github.com/MiviaLabs/mivia-ai-sdk/events"

	"github.com/MiviaLabs/mivia-agent/internal/events"
)

// bridgeToolCallEnd translates one SDK ToolCallEnd into the operator
// tool_end event. The SDK fires the event with callCtx on EVERY
// return path of runOneToolCall, including PointPreTool vetoes and
// hook errors.
func bridgeToolCallEnd(opts Options, turn *sdkTurnState, ctx context.Context) {
	var outcome *toolCallOutcome
	var callKey, callName string
	if tc, ok := sdkagentloop.ToolCallFromContext(ctx); ok {
		// The same helper the recorders key on: an outcome stored under one
		// fallback and looked up under another is an outcome nobody sees.
		callKey = toolCallKey(tc)
		callName = tc.Name
	}
	if turn != nil && callKey != "" {
		outcome = turn.takeToolCallOutcome(callKey)
	}
	switch {
	case outcome != nil:
		emit(opts, toolEndEventFor(*outcome))
	case callKey != "":
		// No recorded outcome means the call never reached the
		// dispatcher shim: the SDK rejected it inside decodeAndRun
		// (unknown tool, scope denial, argument-schema violation,
		// undecodable payload) or a PointPreTool hook vetoed it.
		// Every one of those is a call that did NOT run, so the
		// operator row must read "failed". The rejections also feed
		// the SDK's failure-spiral bound; a veto does not (it returns
		// reported=nil), but it is still a call that produced no
		// result, which is what this row reports.
		//
		// It used to read "completed (duplicate)", which reported
		// every rejected call as a success; the SDK dedup that
		// theory rested on is off here and fires no ToolCallEnd
		// anyway. See pre_shim_failure_reporting_test.go.
		emit(opts, Event{
			Kind:       EventToolEnd,
			ToolCallID: callKey,
			Name:       callName,
			Detail:     "failed",
			Output:     "",
		})
	}
}

// bridgeAgentLoopHeartbeats subscribes both SDK heartbeat tick kinds
// onto the same EventHeartbeat surface the legacy thinking-tick loop
// used (loop_stream.go's emitModelThinkingHeartbeatAt). The SDK loop
// now runs with a positive HeartbeatInterval (agentloop_adoption.go),
// so the ticks fire for the first time and reach the operator through
// the same OnEvent/EventBus path as every other bridged event.
func bridgeAgentLoopHeartbeats(bus *sdkevents.Bus, opts Options) {
	tick := func(_ context.Context, _ sdkevents.Event) error {
		emit(opts, Event{Kind: EventHeartbeat, Detail: "working"})
		return nil
	}
	_ = bus.Subscribe(sdkagentloop.EventToolCallHeartbeat, tick)
	_ = bus.Subscribe(sdkagentloop.EventCompletionHeartbeat, tick)
}

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
		// legacy ordering). The per-iteration shaping-gate reset
		// does NOT live here: headless runs install no Bus, so it
		// rides the completer's per-Chat bump instead
		// (newSDKTurnCompleter's onChat).
		turn.resetStreamRevoke()
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
	bridgeAgentLoopHeartbeats(bus, opts)
	_ = bus.Subscribe(sdkagentloop.EventToolCallEnd, func(ctx context.Context, _ sdkevents.Event) error {
		bridgeToolCallEnd(opts, turn, ctx)
		return nil
	})
	return bus
}
