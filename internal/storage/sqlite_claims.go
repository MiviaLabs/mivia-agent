package storage

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// GetClaim reads a run's current execution claim as a read-only liveness
// probe. It never acquires, refreshes, or releases the claim. It is split
// into its own file to keep sqlite.go under the line budget.
func (s *SQLite) GetClaim(ctx context.Context, id string) (Claim, error) {
	var claim Claim
	err := s.db.QueryRowContext(ctx, `SELECT run_id, holder, acquired_at, fence FROM run_claims WHERE run_id = ?`, id).Scan(&claim.RunID, &claim.Holder, &claim.AcquiredAt, &claim.Fence)
	if err == sql.ErrNoRows {
		return Claim{}, ErrClaimNotHeld
	}
	if err != nil {
		return Claim{}, fmt.Errorf("read claim %q: %w", id, err)
	}
	return claim, nil
}

func (s *SQLite) ClaimRun(ctx context.Context, id, h string) error {
	_, err := s.ClaimRunFenced(ctx, id, h)
	return err
}
func (s *SQLite) ClaimRunFenced(ctx context.Context, id, h string) (Claim, error) {
	if h == "" {
		return Claim{}, ErrClaimNotHeld
	}
	var claim Claim
	err := s.db.QueryRowContext(ctx, `INSERT INTO run_claims(run_id, holder, acquired_at, fence) VALUES(?, ?, strftime('%Y-%m-%dT%H:%M:%fZ','now'), 1) ON CONFLICT(run_id) DO UPDATE SET acquired_at=excluded.acquired_at WHERE run_claims.holder = excluded.holder RETURNING run_id, holder, acquired_at, fence`, id, h).Scan(&claim.RunID, &claim.Holder, &claim.AcquiredAt, &claim.Fence)
	if err == sql.ErrNoRows {
		return Claim{}, ErrClaimHeld
	}
	if err != nil {
		return Claim{}, fmt.Errorf("claim run %q: %w", id, err)
	}
	return claim, nil
}
func (s *SQLite) TakeoverClaim(ctx context.Context, id, h string) error {
	if h == "" {
		return ErrClaimNotHeld
	}
	// The takeover bumps the fence on an existing row (and seeds it on a fresh
	// insert) so a previous holder's captured fence never survives a takeover:
	// without the bump, a stale holder with a matching fence value kept write
	// access after the owner changed (F2). It also bumps fence_generation and
	// records the previous holder in fenced_tokens atomically inside the same
	// transaction so a later state query (IsRunTokenFenced) sees the prior
	// owner as fenced exactly once.
	//
	// The Go writeMu serialises concurrent takeovers in-process; the SQL
	// retry handles cross-process or connection-pool contention: a separate
	// AppendClaimedFenced goroutine saturating the connection pool can raise
	// SQLITE_BUSY (snapshot-level, not lock-wait-level) before busy_timeout
	// clears, which the in-Go mutex cannot help with.
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return retrySQLiteBusy(ctx, func() error {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("take over claim %q: %w", id, err)
		}
		committed := false
		defer func() {
			if !committed {
				_ = tx.Rollback()
			}
		}()
		if _, err := takeoverClaimTx(ctx, tx, id, h); err != nil {
			return err
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("take over claim %q: %w", id, err)
		}
		committed = true
		return nil
	})
}

func (s *SQLite) TakeoverClaimFenced(ctx context.Context, id, h string) (Claim, error) {
	if h == "" {
		return Claim{}, ErrClaimNotHeld
	}
	// Same retry rationale as TakeoverClaim: the writeMu serialises in-process
	// takeovers, the SQL retry clears a SQLITE_BUSY collision raised by
	// concurrent AppendClaimedFenced goroutines saturating the connection
	// pool. A retry re-reads the prior holder, so a second takeover that
	// landed between attempts fences the more-recent prior holder; the SDK
	// check cares that "any previously-issued token for this run" is fenced,
	// so this is correct.
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	var claim Claim
	err := retrySQLiteBusy(ctx, func() error {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("take over claim %q: %w", id, err)
		}
		committed := false
		defer func() {
			if !committed {
				_ = tx.Rollback()
			}
		}()
		c, err := takeoverClaimTx(ctx, tx, id, h)
		if err != nil {
			return err
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("take over claim %q: %w", id, err)
		}
		committed = true
		claim = c
		return nil
	})
	if err != nil {
		return Claim{}, err
	}
	return claim, nil
}

// takeoverClaimTx performs the takeover's read-and-update logic on an open
// transaction. It captures the prior holder (when an existing row is being
// replaced) so the caller can record it in fenced_tokens within the same
// transaction. A fresh insert (no prior row) leaves prevHolder empty.
func takeoverClaimTx(ctx context.Context, tx *sql.Tx, id, h string) (Claim, error) {
	var prevHolder string
	if err := tx.QueryRowContext(ctx, `SELECT holder FROM run_claims WHERE run_id = ?`, id).Scan(&prevHolder); err != nil && err != sql.ErrNoRows {
		return Claim{}, fmt.Errorf("read prior claim %q: %w", id, err)
	}
	var claim Claim
	err := tx.QueryRowContext(ctx, `INSERT INTO run_claims(run_id, holder, acquired_at, fence, fence_generation) VALUES(?, ?, strftime('%Y-%m-%dT%H:%M:%fZ','now'), 1, 1) ON CONFLICT(run_id) DO UPDATE SET holder=excluded.holder, acquired_at=excluded.acquired_at, fence = fence + 1, fence_generation = fence_generation + 1 RETURNING run_id, holder, acquired_at, fence`, id, h).Scan(&claim.RunID, &claim.Holder, &claim.AcquiredAt, &claim.Fence)
	if err != nil {
		return Claim{}, fmt.Errorf("take over claim %q: %w", id, err)
	}
	if prevHolder != "" && prevHolder != claim.Holder {
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO fenced_tokens(run_id, token) VALUES(?, ?)`, id, prevHolder); err != nil {
			return Claim{}, fmt.Errorf("record fenced token for %q: %w", id, err)
		}
	}
	return claim, nil
}

// RefreshClaimFenced refreshes the claim's acquired_at ONLY when holder already
// owns the claim row. A missing row (or a row owned by another holder) refreshes
// nothing and returns ErrClaimNotHeld, so a heartbeat can never insert itself
// back into a claim it lost (F2).
func (s *SQLite) RefreshClaimFenced(ctx context.Context, id, h string) (Claim, error) {
	if h == "" {
		return Claim{}, ErrClaimNotHeld
	}
	var claim Claim
	err := s.db.QueryRowContext(ctx, `UPDATE run_claims SET acquired_at = strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE run_id = ? AND holder = ? RETURNING run_id, holder, acquired_at, fence`, id, h).Scan(&claim.RunID, &claim.Holder, &claim.AcquiredAt, &claim.Fence)
	if err == sql.ErrNoRows {
		return Claim{}, ErrClaimNotHeld
	}
	if err != nil {
		return Claim{}, fmt.Errorf("refresh claim %q: %w", id, err)
	}
	return claim, nil
}

func (s *SQLite) TakeoverExpiredClaim(ctx context.Context, id, h string, maxAge time.Duration) error {
	_, err := s.TakeoverExpiredClaimFenced(ctx, id, h, maxAge)
	return err
}
func (s *SQLite) TakeoverExpiredClaimFenced(ctx context.Context, id, h string, maxAge time.Duration) (Claim, error) {
	if h == "" {
		return Claim{}, ErrClaimNotHeld
	}
	millis := maxAge.Milliseconds()
	// Same retry rationale as TakeoverClaimFenced: the writeMu serialises
	// in-process takeovers; the SQL retry clears SQLITE_BUSY raised by a
	// saturating AppendClaimedFenced goroutine. ErrClaimNotHeld and
	// ErrClaimHeld are NOT busy errors, so they exit on the first attempt.
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	var claim Claim
	err := retrySQLiteBusy(ctx, func() error {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("take over expired claim %q: %w", id, err)
		}
		committed := false
		defer func() {
			if !committed {
				_ = tx.Rollback()
			}
		}()
		// Capture the prior holder ONLY when the row matches the age predicate:
		// a row that exists but is too fresh is a held claim, not a fenced one.
		var prevHolder string
		err = tx.QueryRowContext(ctx, `SELECT holder FROM run_claims WHERE run_id = ? AND julianday(acquired_at) <= julianday('now') - (? / 86400000.0)`, id, millis).Scan(&prevHolder)
		if err == sql.ErrNoRows {
			// Distinguish a missing row (ErrClaimNotHeld) from a held-but-not-
			// expired row (ErrClaimHeld), matching the single-statement semantics
			// the caller contract relies on.
			var found int
			if rerr := tx.QueryRowContext(ctx, `SELECT 1 FROM run_claims WHERE run_id = ?`, id).Scan(&found); rerr == sql.ErrNoRows {
				return ErrClaimNotHeld
			} else if rerr != nil {
				return fmt.Errorf("read expired claim %q: %w", id, rerr)
			}
			return ErrClaimHeld
		}
		if err != nil {
			return fmt.Errorf("read expired claim %q: %w", id, err)
		}
		var c Claim
		err = tx.QueryRowContext(ctx, `UPDATE run_claims SET holder = ?, acquired_at = strftime('%Y-%m-%dT%H:%M:%fZ','now'), fence = fence + 1, fence_generation = fence_generation + 1 WHERE run_id = ? RETURNING run_id, holder, acquired_at, fence`, h, id).Scan(&c.RunID, &c.Holder, &c.AcquiredAt, &c.Fence)
		if err != nil {
			return fmt.Errorf("take over expired claim %q: %w", id, err)
		}
		if prevHolder != c.Holder {
			if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO fenced_tokens(run_id, token) VALUES(?, ?)`, id, prevHolder); err != nil {
				return fmt.Errorf("record fenced token for %q: %w", id, err)
			}
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("take over expired claim %q: %w", id, err)
		}
		committed = true
		claim = c
		return nil
	})
	if err != nil {
		return Claim{}, err
	}
	return claim, nil
}

func (s *SQLite) AppendClaimedFenced(ctx context.Context, e Event, claim Claim) error {
	if len(e.Payload) == 0 {
		return fmt.Errorf("empty payload")
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO events(id,run_id,sequence,kind,payload) SELECT ?,?,?,?,? WHERE EXISTS (SELECT 1 FROM run_claims WHERE run_id=? AND holder=? AND fence=?)`, e.ID, e.RunID, e.Sequence, e.Kind, e.Payload, e.RunID, claim.Holder, claim.Fence)
	if err != nil {
		if isConstraint(err) {
			return ErrDuplicate
		}
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrClaimHeld
	}
	return nil
}

func (s *SQLite) ReleaseClaimFenced(ctx context.Context, claim Claim) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM run_claims WHERE run_id=? AND holder=? AND fence=?`, claim.RunID, claim.Holder, claim.Fence)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrClaimNotHeld
	}
	return nil
}
func (s *SQLite) ReleaseClaim(ctx context.Context, id, h string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM run_claims WHERE run_id = ? AND holder = ?`, id, h)
	if err != nil {
		return fmt.Errorf("release claim %q: %w", id, err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrClaimNotHeld
	}
	return nil
}
func (s *SQLite) ClearClaim(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM run_claims WHERE run_id = ?`, id)
	return err
}

// IsRunHeld reports whether runID currently has an active claim row. It is a
// pure liveness probe; it never acquires, refreshes, or releases a claim, so
// observing a run can never disturb its holder. Returns (false, nil) when no
// claim row exists.
func (s *SQLite) IsRunHeld(ctx context.Context, runID string) (bool, error) {
	var one int
	err := s.db.QueryRowContext(ctx, `SELECT 1 FROM run_claims WHERE run_id = ?`, runID).Scan(&one)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("is run held %q: %w", runID, err)
	}
	return true, nil
}

// IsRunTokenFenced reports whether token has been fenced out of runID by a
// subsequent takeover. The history is durable: a fenced token stays fenced
// across releases, so a re-issued claim by the same token reads false UNLESS
// the token has been fenced by an intervening takeover. A token that is the
// current holder of runID always reads false.
func (s *SQLite) IsRunTokenFenced(ctx context.Context, runID, token string) (bool, error) {
	if token == "" {
		return false, nil
	}
	// Current owner is, by definition, not fenced.
	var currentHolder string
	err := s.db.QueryRowContext(ctx, `SELECT holder FROM run_claims WHERE run_id = ?`, runID).Scan(&currentHolder)
	if err != nil && err != sql.ErrNoRows {
		return false, fmt.Errorf("is run token fenced %q: %w", runID, err)
	}
	if err == nil && currentHolder == token {
		return false, nil
	}
	var one int
	err = s.db.QueryRowContext(ctx, `SELECT 1 FROM fenced_tokens WHERE run_id = ? AND token = ?`, runID, token).Scan(&one)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("is run token fenced %q: %w", runID, err)
	}
	return true, nil
}

func isConstraint(err error) bool {
	return err != nil && (contains(err.Error(), "constraint") || contains(err.Error(), "UNIQUE"))
}
func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
