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
	// Show the hit rate so operators can read cache health from one line.
	// HitPercent guards the division: zero input tokens reads as 0%.
	detail := fmt.Sprintf("prompt cache: %d/%d tokens cached (%d%%)", typed.CachedInputTokens, typed.InputTokens, typed.HitPercent())
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

// EmitTokenUsage publishes provider-reported input/output token counts and
// estimate-vs-actual drift for one completion turn. It only publishes when
// the provider actually reported usage. This enables operators to see when
// the len(s)/4 heuristic diverges from real token accounting.
func EmitTokenUsage(opts Options, providerName, model string, usage provider.TokenUsage, estimatedTokens int, calibrationRatio float64) {
	if !usage.Reported {
		return
	}
	typed, err := events.NewTokenUsageEvent(providerName, model, usage.InputTokens, usage.OutputTokens, estimatedTokens, calibrationRatio)
	if err != nil {
		return
	}
	drift := ""
	if estimatedTokens > 0 {
		drift = fmt.Sprintf("estimate %d vs actual %d (ratio %.2f)", estimatedTokens, usage.InputTokens, calibrationRatio)
	} else {
		drift = fmt.Sprintf("actual %d in / %d out", usage.InputTokens, usage.OutputTokens)
	}
	e := Event{Kind: EventTokenUsage, Detail: drift, TokenUsage: &typed}
	if opts.OnEvent != nil {
		opts.OnEvent(e)
	}
	if opts.EventBus != nil {
		ev := events.NewEvent(events.KindTokenUsage)
		ev.SessionID, ev.TurnID, ev.Detail = opts.SessionID, opts.TurnID, drift
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
func EmitCompaction(opts Options, preparation contextmgr.Preparation, summarized bool) {
	if !preparation.Compacted {
		return
	}
	typed, err := events.NewCompactionEvent(events.CompactionEventParams{
		Trigger: "threshold", BeforeTokens: preparation.BeforeTokens, AfterTokens: preparation.AfterTokens,
		ElidedMessages: preparation.ElidedMessages, ElidedBytes: preparation.ElidedBytes,
		SourceRange: preparation.Token.Range, SummaryVersion: 1, Summarized: summarized,
	})
	if err != nil {
		return
	}
	detail := fmt.Sprintf("context compacted: %d -> %d tokens", typed.BeforeTokens, typed.AfterTokens)
	if typed.ElidedMessages > 0 {
		detail = fmt.Sprintf("%s (%d tool results elided, %d bytes)", detail, typed.ElidedMessages, typed.ElidedBytes)
	}
	// Say it in Detail too, not only in the typed field: Detail is what a
	// plain renderer shows, and a banner that reads the same whether or not
	// the dropped messages were summarized is the difference between
	// "compaction worked" and "compaction silently threw context away".
	if !typed.Summarized {
		detail += " (structural only, no summary)"
	}
	e := Event{Kind: EventCompaction, Detail: detail, Compaction: &typed}
	if opts.OnEvent != nil {
		opts.OnEvent(e)
	}
	if opts.EventBus != nil {
		ev := events.NewEvent(events.KindCompaction)
		ev.SessionID, ev.TurnID, ev.Detail = opts.SessionID, opts.TurnID, detail
		// The typed payload rides the bus event too, so bus consumers (the
		// cross-process hub, a --json sidecar) get the real numbers instead
		// of parsing Detail.
		ev.Compaction = &typed
		if opts.EventIdentity != nil {
			copy := *opts.EventIdentity
			ev.Identity = &copy
		}
		opts.EventBus.Publish(ev)
	}
}
