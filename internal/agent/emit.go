package agent

import (
	"context"
	"fmt"

	"github.com/MiviaLabs/mivia-agent/internal/contextmgr"
	"github.com/MiviaLabs/mivia-agent/internal/events"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	usagepkg "github.com/MiviaLabs/mivia-agent/internal/usage"
)

// recordUsage writes record to opts.UsageWriter when one is set. Best-effort:
// a write failure is dropped, never returned to or allowed to fail the turn
// it describes - matches the same "logged and dropped" contract as every
// other emit-path failure in this file (e.g. a typed-event construction
// error above), just with nothing yet to log to in this package.
func recordUsage(ctx context.Context, opts Options, record usagepkg.UsageRecord) {
	if opts.UsageWriter == nil {
		return
	}
	record.SessionID, record.TurnID = opts.SessionID, opts.TurnID
	_ = opts.UsageWriter.Record(ctx, record)
}

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
		ev.InputBody = e.InputBody
		ev.OutputBody = e.OutputBody
		if e.Kind == EventHook {
			// The generic conversion carries only strings, so the hook's
			// verdict would stop here - and a consumer past the bus could not
			// tell a hook that reported from one that refused a tool call.
			ev.Hook = &events.HookEvent{
				Phase:   e.Name,
				Program: e.Program,
				Tool:    e.Tool,
				Denied:  e.Denied,
				Output:  e.HookStdout,
			}
		}
		ev.SessionID = opts.SessionID
		ev.TurnID = opts.TurnID
		if opts.EventIdentity != nil {
			copy := *opts.EventIdentity
			ev.Identity = &copy
		}
		if !e.Origin.IsZero() {
			ev = ev.WithAgentAttribution(e.Origin.TaskID, e.Origin.Agent, e.Origin.Depth).
				WithAgentParent(e.Origin.ParentTaskID)
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
func EmitCacheUsage(ctx context.Context, opts Options, providerName, model string, usage provider.CacheUsage) {
	if !usage.Reported {
		return
	}
	typed, err := events.NewCacheUsageEvent(providerName, model, string(usage.Style), usage.InputTokens, usage.CachedInputTokens, usage.CacheWriteTokens)
	if err != nil {
		return
	}
	recordUsage(ctx, opts, usagepkg.UsageRecord{
		Kind: "cache_usage", Provider: providerName, Model: model,
		CachedInputTokens: typed.CachedInputTokens, CacheWriteTokens: typed.CacheWriteTokens,
		InputTokens: typed.InputTokens,
	})
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
func EmitTokenUsage(ctx context.Context, opts Options, providerName, model string, usage provider.TokenUsage, estimatedTokens int, calibrationRatio float64) {
	if !usage.Reported {
		return
	}
	typed, err := events.NewTokenUsageEvent(providerName, model, usage.InputTokens, usage.OutputTokens, estimatedTokens, calibrationRatio)
	if err != nil {
		return
	}
	recordUsage(ctx, opts, usagepkg.UsageRecord{
		Kind: "token_usage", Provider: providerName, Model: model,
		InputTokens: typed.InputTokens, OutputTokens: typed.OutputTokens,
		EstimatedTokens: typed.EstimatedTokens, CalibrationRatio: typed.CalibrationRatio,
	})
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
// reason is only meaningful when summarized is false: the classified,
// content-free cause (see events.CompactionEvent.Reason). Callers pass "" when
// they have none to report.
//
// This is the one Emit* reached while the caller still holds internal/chat's
// contextPublishMu, a session-wide lock also taken by /compact, session
// reset, and model switch - recordUsage itself stays synchronous here
// (matching EmitTokenUsage/EmitCacheUsage), but the concrete UsageWriter this
// repo wires in production (storage.usageWriter) dispatches its own write off
// this call's goroutine and tracks it against the store's own WaitGroup, so
// Record returns immediately without this function needing to know that.
func EmitCompaction(ctx context.Context, opts Options, preparation contextmgr.Preparation, summarized bool, reason string) {
	if !preparation.Compacted {
		return
	}
	typed, err := events.NewCompactionEvent(events.CompactionEventParams{
		Trigger: "threshold", BeforeTokens: preparation.BeforeTokens, AfterTokens: preparation.AfterTokens,
		ElidedMessages: preparation.ElidedMessages, ElidedBytes: preparation.ElidedBytes,
		SourceRange: preparation.Token.Range, SummaryVersion: 1, Summarized: summarized, Reason: reason,
	})
	if err != nil {
		return
	}
	summarizedCopy := typed.Summarized
	recordUsage(ctx, opts, usagepkg.UsageRecord{
		Kind: "compaction", BeforeTokens: typed.BeforeTokens, AfterTokens: typed.AfterTokens,
		ElidedMessages: typed.ElidedMessages, ElidedBytes: typed.ElidedBytes,
		Summarized: &summarizedCopy, Reason: typed.Reason,
	})
	detail := fmt.Sprintf("context compacted: %d -> %d tokens", typed.BeforeTokens, typed.AfterTokens)
	if typed.ElidedMessages > 0 {
		detail = fmt.Sprintf("%s (%d tool results elided, %d bytes)", detail, typed.ElidedMessages, typed.ElidedBytes)
	}
	// Say it in Detail too, not only in the typed field: Detail is what a
	// plain renderer shows, and a banner that reads the same whether or not
	// the dropped messages were summarized is the difference between
	// "compaction worked" and "compaction silently threw context away".
	if !typed.Summarized {
		detail += " (structural only, no summary"
		if typed.Reason != "" {
			detail += ": " + typed.Reason
		}
		detail += ")"
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
