package agent

import (
	"github.com/MiviaLabs/mivia-agent/internal/events"
)

// emit delivers an event to both OnEvent callback and EventBus (if set).
// Always call emit instead of opts.OnEvent directly to ensure EventBus
// delivery in the agent loop and supporting functions.
func emit(opts Options, e Event) {
	if opts.OnEvent != nil {
		opts.OnEvent(e)
	}
	if opts.EventBus != nil {
		ev := events.NewEventFromAgentParts(
			events.Kind(e.Kind),
			e.ToolCallID,
			e.Name,
			e.Detail,
			e.Content,
			e.Input,
			e.Output,
		)
		ev.SessionID = opts.SessionID
		ev.TurnID = opts.TurnID
		if opts.EventIdentity != nil {
			copy := *opts.EventIdentity
			ev.Identity = &copy
		}
		if !e.Origin.IsZero() {
			ev = ev.WithAgentAttribution(e.Origin.TaskID, e.Origin.Agent, e.Origin.Depth)
		}
		opts.EventBus.Publish(ev)
	}
}
