package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/events"
)

// journaledKinds are the discrete, low-frequency step boundaries worth a
// durable write: a turn starting, a completed tool call, a settled assistant
// message, and settled reasoning (agent/loop_step.go's emitReasoning fires
// once per completed provider response with the full ReasoningContent, not
// per token). Streaming delta kinds are deliberately excluded: the bus's
// bounded per-subscription queue drops the OLDEST event on overflow with no
// signal to the handler, and a per-token write rate risks exactly the silent
// loss this journal exists to prevent.
var journaledKinds = []events.Kind{
	events.KindTurnStart,
	events.KindToolStart,
	events.KindToolEnd,
	events.KindAssistant,
	events.KindThinking,
}

// journalWriteTimeout bounds one journal write so a stalled or contended
// connection cannot pile up behind the turn it is trying to protect.
const journalWriteTimeout = 2 * time.Second

// ensureTurnJournalSubscribed attaches the durable turn-step journal to the
// session's event bus exactly once. Safe to call on every turn; only the
// first call with a usable bus and a journal-capable store does anything.
// Called lazily from sendAgent rather than at session construction, because
// EventBus and the context store are both wired in by callers after
// NewSession and this way needs no dependency on their exact ordering.
func (s *Session) ensureTurnJournalSubscribed() {
	s.turnJournalOnce.Do(func() {
		if s == nil || s.EventBus == nil {
			return
		}
		if _, _, ok := s.turnJournalState(); !ok {
			return
		}
		s.EventBus.SubscribeMany(journaledKinds, events.HandlerFunc(s.journalBusEvent))
	})
}

// journalEventPayload is the durable copy of one bus event's model-relevant
// fields. It carries nothing rawer than what agent/emit.go already redacts
// before publishing onto the bus.
type journalEventPayload struct {
	ToolCallID string `json:"tool_call_id,omitempty"`
	Name       string `json:"name,omitempty"`
	Detail     string `json:"detail,omitempty"`
	Content    string `json:"content,omitempty"`
	Input      string `json:"input,omitempty"`
	Output     string `json:"output,omitempty"`
}

// journalBusEvent durably records one step-shaped bus event. Best-effort: a
// journal write failure is reported to stderr, never allowed to fail or slow
// the turn it is trying to protect.
func (s *Session) journalBusEvent(ctx context.Context, ev events.Event) {
	journal, principal, ok := s.turnJournalState()
	if !ok || ev.SessionID == "" || ev.TurnID == "" {
		return
	}
	// KindAssistant overloads one Kind with three meanings (internal/events/
	// event.go): a streaming delta (Detail=="delta"), a content-free
	// per-iteration "release held text" signal (Detail==
	// events.DetailAssistantComplete), and the turn's one settled message
	// (Detail=="", full Content). Only the settled message is a discrete,
	// low-frequency step worth a durable write; the other two are exactly
	// the per-token write-amplification this journal is designed to avoid.
	if ev.Kind == events.KindAssistant && ev.Detail != "" {
		return
	}
	payload, err := json.Marshal(journalEventPayload{
		ToolCallID: ev.ToolCallID,
		Name:       ev.Name,
		Detail:     ev.Detail,
		Content:    ev.Content,
		Input:      ev.Input,
		Output:     ev.Output,
	})
	if err != nil {
		return
	}
	writeCtx, cancel := context.WithTimeout(ctx, journalWriteTimeout)
	defer cancel()
	entry := contextstate.TurnJournalEntry{Kind: string(ev.Kind), Payload: payload, CreatedAt: time.Now()}
	if err := journal.AppendTurnJournalEntry(writeCtx, principal, ev.SessionID, ev.TurnID, entry); err != nil {
		fmt.Fprintf(os.Stderr, "\n⚠ turn journal append failed (turn history may be incomplete on crash recovery): %v\n", err)
	}
}

// clearTurnJournal removes one turn's journaled steps once its fate is
// settled: durably committed, or intentionally superseded by a newer turn
// (fencing.go's ErrStaleOperation - a routine drop, not a bug). Callers must
// NOT call this for any other finishAgentTurn outcome, so a turn whose
// commit genuinely failed leaves its journal in place as evidence.
//
// It flushes the event bus first. Bus.Publish is explicitly non-blocking
// (events queue to a per-subscriber delivery goroutine - internal/events/
// bus.go), so without the flush this turn's last steps could still be in
// flight to the journal when the clear runs: either the clear races them and
// loses nothing (they land after, on an already-cleared turn) making the NEXT
// session-open recovery check see a false "interrupted turn" for a turn that
// actually completed, or the clear wins the race and never sees rows that
// were never getting written anyway. Flush eliminates both by guaranteeing
// every event published before this call has already reached the handler.
func (s *Session) clearTurnJournal(sessionID string, turn uint64) {
	journal, principal, ok := s.turnJournalState()
	if !ok {
		return
	}
	if s.EventBus != nil {
		s.EventBus.Flush()
	}
	// context.Background(), not the turn's own ctx: this mirrors
	// commitContextTurn's commitCtx handling for a cancelled turn - the
	// journal cleanup for an already-decided turn must complete even when
	// the caller's context is what ended the turn.
	writeCtx, cancel := context.WithTimeout(context.Background(), journalWriteTimeout)
	defer cancel()
	if err := journal.ClearTurnJournal(writeCtx, principal, sessionID, turnEventID(turn)); err != nil {
		fmt.Fprintf(os.Stderr, "\n⚠ turn journal clear failed: %v\n", err)
	}
}
