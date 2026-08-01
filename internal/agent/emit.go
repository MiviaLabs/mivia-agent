package agent

import (
	"fmt"

	"github.com/MiviaLabs/mivia-agent/internal/contextmgr"
	"github.com/MiviaLabs/mivia-agent/internal/events"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
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

// EmitCacheUsage publishes provider-reported prompt-cache accounting for one
// completion turn. It only publishes when the provider actually reported
// cache usage (usage.Reported) - a silent no-op otherwise, since most turns
// against a provider with capture disabled or with nothing to report carry
// no signal worth an event.
//
// This only fires for turns that reach the agent loop: tool-enabled sessions
// and all subagent turns. A plain --no-tools chat session calls the provider
// directly via Completer.ChatStream and never reaches here; extending
// coverage there would require breaking ChatStream's public signature to
// carry structured usage back, which is out of scope for this feature.
func EmitCacheUsage(opts Options, providerName, model string, usage provider.CacheUsage) {
	if !usage.Reported {
		return
	}
	typed, err := events.NewCacheUsageEvent(providerName, model, string(usage.Style), usage.InputTokens, usage.CachedInputTokens, usage.CacheWriteTokens)
	if err != nil {
		return
	}
	detail := fmt.Sprintf("prompt cache: %d/%d tokens cached", typed.CachedInputTokens, typed.InputTokens)
	e := Event{Kind: EventCacheUsage, Detail: detail, CacheUsage: &typed}
	if opts.OnEvent != nil {
		opts.OnEvent(e)
	}
	if opts.EventBus != nil {
		ev := events.NewEvent(events.KindCacheUsage)
		ev.SessionID, ev.TurnID, ev.Detail = opts.SessionID, opts.TurnID, detail
		if opts.EventIdentity != nil {
			copy := *opts.EventIdentity
			ev.Identity = &copy
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
