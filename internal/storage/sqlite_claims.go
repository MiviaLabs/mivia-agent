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
	_, err := s.db.ExecContext(ctx, `INSERT INTO run_claims(run_id, holder, acquired_at) VALUES(?, ?, datetime('now')) ON CONFLICT(run_id) DO UPDATE SET holder=excluded.holder, acquired_at=excluded.acquired_at`, id, h)
	if err != nil {
		return fmt.Errorf("take over claim %q: %w", id, err)
	}
	return nil
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
	var claim Claim
	err := s.db.QueryRowContext(ctx, `UPDATE run_claims SET holder = ?, acquired_at = strftime('%Y-%m-%dT%H:%M:%fZ','now'), fence = fence + 1 WHERE run_id = ? AND julianday(acquired_at) <= julianday('now') - (? / 86400000.0) RETURNING run_id, holder, acquired_at, fence`, h, id, millis).Scan(&claim.RunID, &claim.Holder, &claim.AcquiredAt, &claim.Fence)
	if err == sql.ErrNoRows {
		var found int
		err := s.db.QueryRowContext(ctx, `SELECT 1 FROM run_claims WHERE run_id = ?`, id).Scan(&found)
		if err == sql.ErrNoRows {
			return Claim{}, ErrClaimNotHeld
		}
		if err != nil {
			return Claim{}, fmt.Errorf("read expired claim %q: %w", id, err)
		}
		return Claim{}, ErrClaimHeld
	}
	if err != nil {
		return Claim{}, fmt.Errorf("take over expired claim %q: %w", id, err)
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
