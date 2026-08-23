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
// installs the returned bus as agentloop.Options.Bus.
func bridgeAgentLoopEvents(opts Options) *sdkevents.Bus {
	bus := sdkevents.New()
	// revoked gates the stream revoke to once per iteration. The SDK
	// emits every event of one run from that run's goroutine, so no
	// lock guards it; it is a local so concurrent runs on separate
	// bridges never share it.
	revoked := false
	// Subscribe errors are impossible here: the names are
	// non-blank package constants and the bus is fresh, so the
	// only failure modes (blank name, duplicate registration)
	// cannot occur. Ignoring the error keeps the handler bodies
	// focused on the translation.
	_ = bus.Subscribe(sdkagentloop.EventIterationStart, func(_ context.Context, e sdkevents.Event) error {
		revoked = false
		emit(opts, Event{Kind: EventStep, Detail: e.Data})
		return nil
	})
	_ = bus.Subscribe(sdkagentloop.EventToolCallStart, func(_ context.Context, e sdkevents.Event) error {
		// Content-then-tools: the first tool call of an iteration
		// clears the optimistic final-stream tokens the Completer
		// streamed before the tool_calls arrived, the same
		// revokeStreamWriter call the legacy loop makes ahead of
		// processToolCalls. Once per iteration: later calls in the
		// same batch must not revoke again.
		if !revoked {
			revoked = true
			revokeStreamWriter(opts.FinalWriter)
		}
		emit(opts, Event{Kind: EventToolStart, Detail: e.Data})
		return nil
	})
	_ = bus.Subscribe(sdkagentloop.EventToolCallEnd, func(_ context.Context, e sdkevents.Event) error {
		emit(opts, Event{Kind: EventToolEnd, Detail: e.Data})
		return nil
	})
	return bus
}
