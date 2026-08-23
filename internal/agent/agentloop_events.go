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
	subscribe := func(name sdkevents.Name, kind EventKind) {
		// Subscribe errors are impossible here: the names are
		// non-blank package constants and the bus is fresh, so the
		// only failure modes (blank name, duplicate registration)
		// cannot occur. Ignoring the error keeps the handler body
		// focused on the translation.
		_ = bus.Subscribe(name, func(_ context.Context, e sdkevents.Event) error {
			emit(opts, Event{Kind: kind, Detail: e.Data})
			return nil
		})
	}
	subscribe(sdkagentloop.EventIterationStart, EventStep)
	subscribe(sdkagentloop.EventToolCallStart, EventToolStart)
	subscribe(sdkagentloop.EventToolCallEnd, EventToolEnd)
	return bus
}
