package agent

import (
	"fmt"

	"github.com/MiviaLabs/mivia-agent/internal/contextmgr"
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

// EmitCompaction publishes the sealed, content-free progress event after the
// owning surface has durably committed the preparation. It is intentionally
// separate from emit so the generic event adapter cannot receive summary data.
func EmitCompaction(opts Options, preparation contextmgr.Preparation) {
	if !preparation.Compacted {
		return
	}
	typed, err := events.NewCompactionEvent("threshold", preparation.BeforeTokens, preparation.AfterTokens, preparation.Token.Range, 1)
	if err != nil {
		return
	}
	detail := fmt.Sprintf("context compacted: %d -> %d tokens", typed.BeforeTokens, typed.AfterTokens)
	e := Event{Kind: EventCompaction, Detail: detail, Compaction: &typed}
	if opts.OnEvent != nil {
		opts.OnEvent(e)
	}
	if opts.EventBus != nil {
		ev := events.NewEvent(events.KindCompaction)
		ev.SessionID, ev.TurnID, ev.Detail = opts.SessionID, opts.TurnID, detail
		if opts.EventIdentity != nil {
			copy := *opts.EventIdentity
			ev.Identity = &copy
		}
		opts.EventBus.Publish(ev)
	}
}
