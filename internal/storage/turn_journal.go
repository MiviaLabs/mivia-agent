package storage

import (
	"context"
	"fmt"
	"math"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
)

// turnJournalRunID composes the shared events table's run_id for one turn's
// crash-forensic journal. Session ids are minted once and never reused
// (context_catalog.go), so this key cannot collide across sessions without
// needing a separate workspace/subject column on the shared events table -
// the same table internal/ledger already appends run-scoped events to.
func turnJournalRunID(sessionID, turnID string) string {
	return "turnjournal:" + sessionID + ":" + turnID
}

// AppendTurnJournalEntry implements contextstate.TurnJournal. It assigns the
// next sequence number for (sessionID, turnID) inside the same write
// transaction as the insert, reusing the store's existing events table and
// writeMu-guarded write pool rather than a second table or connection.
//
// Ordering across DIFFERENT event kinds published concurrently is not
// guaranteed to match publish order (see internal/events.Bus's per-kind
// delivery goroutines) - the sequence here reflects write order, not
// causal order. That is acceptable: each row is self-contained (its own
// kind/payload), so the journal's forensic value does not depend on strict
// cross-kind ordering.
func (s *SQLite) AppendTurnJournalEntry(ctx context.Context, principal contextstate.Principal, sessionID, turnID string, entry contextstate.TurnJournalEntry) error {
	if err := principal.Validate(); err != nil {
		return err
	}
	if sessionID == "" || turnID == "" {
		return fmt.Errorf("%w: turn journal requires session and turn id", contextstate.ErrInvalidDTO)
	}
	id, err := newContextID("tj-")
	if err != nil {
		return err
	}
	runID := turnJournalRunID(sessionID, turnID)
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.beginWrite(ctx)
	if err != nil {
		return err
	}
	var sequence int
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(sequence),0)+1 FROM events WHERE run_id=?`, runID).Scan(&sequence); err != nil {
		_ = tx.Rollback()
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO events(id,run_id,sequence,kind,payload) VALUES(?,?,?,?,?)`, id, runID, sequence, entry.Kind, entry.Payload); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// LoadTurnJournal implements contextstate.TurnJournal.
func (s *SQLite) LoadTurnJournal(ctx context.Context, principal contextstate.Principal, sessionID, turnID string) ([]contextstate.TurnJournalEntry, error) {
	if err := principal.Validate(); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT kind,payload,created_at FROM events WHERE run_id=? ORDER BY sequence`, turnJournalRunID(sessionID, turnID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []contextstate.TurnJournalEntry
	for rows.Next() {
		var e contextstate.TurnJournalEntry
		var createdAt string
		if err := rows.Scan(&e.Kind, &e.Payload, &createdAt); err != nil {
			return nil, err
		}
		e.CreatedAt = contextstate.ParseCatalogTimestamp(createdAt)
		out = append(out, e)
	}
	return out, rows.Err()
}

// ClearTurnJournal implements contextstate.TurnJournal. Callers must call it
// only once a turn's fate is settled - see the interface doc.
func (s *SQLite) ClearTurnJournal(ctx context.Context, principal contextstate.Principal, sessionID, turnID string) error {
	if err := principal.Validate(); err != nil {
		return err
	}
	return s.DeleteRun(ctx, turnJournalRunID(sessionID, turnID), math.MaxInt)
}

// ListJournaledTurns implements contextstate.TurnJournal.
func (s *SQLite) ListJournaledTurns(ctx context.Context, principal contextstate.Principal, sessionID string) ([]string, error) {
	if err := principal.Validate(); err != nil {
		return nil, err
	}
	prefix := turnJournalRunID(sessionID, "")
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT run_id FROM events WHERE run_id LIKE ?`, prefix+"%")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var turns []string
	for rows.Next() {
		var runID string
		if err := rows.Scan(&runID); err != nil {
			return nil, err
		}
		turns = append(turns, runID[len(prefix):])
	}
	return turns, rows.Err()
}
