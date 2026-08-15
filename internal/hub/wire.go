package hub

import (
	"errors"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/events"
)

// WireEvent is the hub socket's newline-delimited JSON framing: a bounded,
// explicit projection of events.Event, not the raw struct - that carries an
// `error` field encoding/json cannot round-trip, and every internal Kind,
// including process-local ones (UI resize, config change) this package
// never wants to commit to a cross-process wire contract.
type WireEvent struct {
	Kind       string    `json:"kind"`
	Timestamp  time.Time `json:"timestamp"`
	SessionID  string    `json:"session_id"`
	TurnID     string    `json:"turn_id"`
	ToolCallID string    `json:"tool_call_id,omitempty"`
	Name       string    `json:"name,omitempty"`
	// Detail carries KindTurnStart's user-submitted text (events.Event.Detail
	// - see relayedKinds' doc comment); other relayed kinds don't use it.
	Detail     string `json:"detail,omitempty"`
	Content    string `json:"content,omitempty"`
	Input      string `json:"input,omitempty"`
	Output     string `json:"output,omitempty"`
	ErrorText  string `json:"error,omitempty"`
	AgentTask  string `json:"agent_task,omitempty"`
	AgentName  string `json:"agent_name,omitempty"`
	AgentDepth int    `json:"agent_depth,omitempty"`
	// Compaction carries the typed payload for KindCompaction only. Nested and
	// pointer so every other kind's wire form stays byte-identical, and
	// content-free by construction so it is safe to commit to the wire
	// contract (INV-AG-32).
	Compaction *WireCompaction `json:"compaction,omitempty"`
}

// WireCompaction is the cross-process projection of events.CompactionEvent,
// including SourceRange so the reconstructed event stays valid (a zero
// SourceRange fails the payload's own Validate, and a reconstructed event
// that cannot pass its own validation is a latent trap for later consumers).
type WireCompaction struct {
	Trigger        string                   `json:"trigger"`
	BeforeTokens   int                      `json:"before_tokens"`
	AfterTokens    int                      `json:"after_tokens"`
	ElidedMessages int                      `json:"elided_messages"`
	ElidedBytes    int                      `json:"elided_bytes"`
	SourceRange    contextstate.SourceRange `json:"source_range"`
	SummaryVersion uint32                   `json:"summary_version"`
	// Summarized must cross the wire. It defaults to false, so omitting it
	// does not lose information - it asserts "structural only, no summary"
	// for every relayed compaction, including summarized ones, and a second
	// surface renders the opposite of what happened.
	Summarized bool `json:"summarized"`
	// Reason mirrors events.CompactionEvent.Reason: the classified,
	// content-free explanation for Summarized=false, so a second live
	// surface (the relaying process) can render the same real cause the
	// originating process saw instead of a generic fallback.
	Reason string `json:"reason,omitempty"`
}

// toWire projects an internal events.Event onto the wire format. Metadata
// and Identity are dropped: neither is meaningful to a second live surface
// today, and both are free-form/typed internal detail this package doesn't
// want to commit to a public wire contract.
func toWire(ev events.Event) WireEvent {
	w := WireEvent{
		Kind: string(ev.Kind), Timestamp: ev.Timestamp,
		SessionID: ev.SessionID, TurnID: ev.TurnID, ToolCallID: ev.ToolCallID,
		Name: ev.Name, Detail: ev.Detail, Content: ev.Content, Input: ev.Input, Output: ev.Output,
		AgentTask: ev.AgentTask, AgentName: ev.AgentName, AgentDepth: ev.AgentDepth,
	}
	if ev.Compaction != nil {
		w.Compaction = &WireCompaction{
			Trigger:        ev.Compaction.Trigger,
			BeforeTokens:   ev.Compaction.BeforeTokens,
			AfterTokens:    ev.Compaction.AfterTokens,
			ElidedMessages: ev.Compaction.ElidedMessages,
			ElidedBytes:    ev.Compaction.ElidedBytes,
			SourceRange:    ev.Compaction.SourceRange,
			SummaryVersion: ev.Compaction.SummaryVersion,
			Summarized:     ev.Compaction.Summarized,
			Reason:         ev.Compaction.Reason,
		}
	}
	if ev.Err != nil {
		w.ErrorText = ev.Err.Error()
	}
	return w
}

// fromWire reconstructs the subset of events.Event a received WireEvent can
// populate - the inverse of toWire, used by a hub member rendering another
// process's events onto this process's own live surface.
func fromWire(w WireEvent) events.Event {
	ev := events.Event{
		Kind: events.Kind(w.Kind), Timestamp: w.Timestamp,
		SessionID: w.SessionID, TurnID: w.TurnID, ToolCallID: w.ToolCallID,
		Name: w.Name, Detail: w.Detail, Content: w.Content, Input: w.Input, Output: w.Output,
		AgentTask: w.AgentTask, AgentName: w.AgentName, AgentDepth: w.AgentDepth,
	}
	if w.Compaction != nil {
		ev.Compaction = events.RehydrateCompactionEvent(events.CompactionEvent{
			Trigger:        w.Compaction.Trigger,
			BeforeTokens:   w.Compaction.BeforeTokens,
			AfterTokens:    w.Compaction.AfterTokens,
			ElidedMessages: w.Compaction.ElidedMessages,
			ElidedBytes:    w.Compaction.ElidedBytes,
			SourceRange:    w.Compaction.SourceRange,
			SummaryVersion: w.Compaction.SummaryVersion,
			Summarized:     w.Compaction.Summarized,
			Reason:         w.Compaction.Reason,
		})
	}
	if w.ErrorText != "" {
		ev.Err = errors.New(w.ErrorText)
	}
	return ev
}
