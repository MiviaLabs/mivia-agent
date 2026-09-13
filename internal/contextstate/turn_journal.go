package contextstate

import (
	"context"
	"time"
)

// TurnJournalEntry is one durably recorded step of an in-flight turn: a
// completed tool call, a settled assistant message, or settled reasoning. It
// is a forensic record for crash recovery, not a replayable message -
// recovery surfaces it for a human to read, never replays it back into the
// model's own message history (doing so would risk the same tool-pairing /
// message-shape hazards provider.ValidateToolPairing and
// provider.RepairToolPairing already guard the committed history against).
type TurnJournalEntry struct {
	Kind      string
	Payload   []byte
	CreatedAt time.Time
}

// TurnJournal durably records a turn's discrete steps as they happen, so a
// process kill mid-turn does not silently lose work that already completed.
// It is optional on the low-level context Store, matching every other
// catalog surface in this file: a store that does not implement it simply
// has no crash-forensic journal.
//
// AppendTurnJournalEntry and ClearTurnJournal are best-effort from the
// caller's point of view: a write failure must never fail or slow the turn
// the journal exists to protect. ClearTurnJournal must be called only once a
// turn's fate is settled - durably committed, or intentionally superseded by
// a newer turn - never on a bare terminal event, so a turn whose commit
// genuinely failed leaves its journal in place as evidence.
type TurnJournal interface {
	AppendTurnJournalEntry(ctx context.Context, principal Principal, sessionID, turnID string, entry TurnJournalEntry) error
	LoadTurnJournal(ctx context.Context, principal Principal, sessionID, turnID string) ([]TurnJournalEntry, error)
	ClearTurnJournal(ctx context.Context, principal Principal, sessionID, turnID string) error
	// ListJournaledTurns returns the turn ids with a non-empty journal for a
	// session, for a session-open recovery check. Order is unspecified.
	ListJournaledTurns(ctx context.Context, principal Principal, sessionID string) ([]string, error)
}
