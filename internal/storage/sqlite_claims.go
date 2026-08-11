package storage

import (
	"context"
	"database/sql"
	"fmt"
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
