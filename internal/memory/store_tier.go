package memory

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// CoreTierCap bounds the number of "core" entries per (scope, org) bucket
// (decision 1: 24 rows, sized to keep the auto-injected block small).
const CoreTierCap = 24

// ErrEntryNotFound is returned by PromoteToCore when no entry with the given
// id exists in any store this Store has access to.
var ErrEntryNotFound = errors.New("memory: entry not found")

// ErrCoreTierFull is returned by PromoteToCore when the target (scope, org)
// bucket already holds CoreTierCap core entries.
var ErrCoreTierFull = fmt.Errorf("memory: core tier is full (max %d entries); merge or archive an existing core entry first", CoreTierCap)

// migrateTierColumn adds the tier column to a database created before this
// column existed. CREATE TABLE IF NOT EXISTS is a no-op against an
// already-existing table, so a pre-existing file needs this explicit
// ALTER TABLE step; PRAGMA table_info is checked first because ALTER TABLE
// ADD COLUMN has no IF NOT EXISTS form and would error on a DB that already
// has the column (already-migrated, or freshly created by memorySchema).
func migrateTierColumn(db *sql.DB) error {
	rows, err := db.Query("PRAGMA table_info(memories)")
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, ctype string
		var notNull int
		var dflt sql.NullString
		var pk int
		if err := rows.Scan(&cid, &name, &ctype, &notNull, &dflt, &pk); err != nil {
			return err
		}
		if name == "tier" {
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	_, err = db.Exec("ALTER TABLE memories ADD COLUMN tier TEXT NOT NULL DEFAULT 'archive' CHECK(tier IN ('core','archive'))")
	return err
}

// ensureCapacity is Save's cap check, extracted to keep Save itself under
// the repo's function-length gate: at consolidateThresholdRatio of the cap,
// try to make room by merging near-duplicates and evicting the oldest
// archive row (D2) before falling back to refusing the write - inside tx,
// the same transaction Save already holds, never a second one (see
// consolidate's doc comment).
func (s *sqliteStore) ensureCapacity(ctx context.Context, tx *sql.Tx, scope Scope, org string) error {
	where := "scope = ? AND org = ?"
	args := []any{string(scope), org}
	var count int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM memories WHERE "+where, args...).Scan(&count); err != nil {
		return err
	}
	if count >= int(float64(s.cfg.MaxEntries)*consolidateThresholdRatio) {
		if err := s.consolidate(ctx, tx, scope, org, s.cfg.MaxEntries); err != nil {
			return err
		}
		if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM memories WHERE "+where, args...).Scan(&count); err != nil {
			return err
		}
	}
	if count >= s.cfg.MaxEntries {
		return fmt.Errorf("memory store is full (max_entries=%d); consolidate or raise [memory] max_entries", s.cfg.MaxEntries)
	}
	return nil
}

// errNotFoundHere signals promoteInDB found no row with the id in that one
// database, so PromoteToCore should try the next one (project, then org)
// rather than fail immediately.
var errNotFoundHere = errors.New("memory: not found in this database")

func (s *sqliteStore) PromoteToCore(ctx context.Context, id string) error {
	if s.cfg.ReadOnly {
		return errors.New("memory store is read-only")
	}
	for _, db := range []*sql.DB{s.projectDB, s.orgDB} {
		if db == nil {
			continue
		}
		err := s.promoteInDB(ctx, db, id)
		if err == nil {
			return nil
		}
		if !errors.Is(err, errNotFoundHere) {
			return err
		}
	}
	return ErrEntryNotFound
}

// promoteInDB runs the promotion inside its own transaction: it must not
// reuse Save's transaction (Save is not on the call stack here), and holding
// s.mu for its duration keeps it serialized against concurrent Saves the
// same way Save serializes against itself.
func (s *sqliteStore) promoteInDB(ctx context.Context, db *sql.DB, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var scope, org, tier string
	err = tx.QueryRowContext(ctx, "SELECT scope, org, tier FROM memories WHERE id = ?", id).Scan(&scope, &org, &tier)
	if errors.Is(err, sql.ErrNoRows) {
		return errNotFoundHere
	}
	if err != nil {
		return err
	}
	if tier == "core" {
		return tx.Commit()
	}
	var count int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM memories WHERE scope = ? AND org = ? AND tier = 'core'", scope, org).Scan(&count); err != nil {
		return err
	}
	if count >= CoreTierCap {
		return ErrCoreTierFull
	}
	if _, err := tx.ExecContext(ctx, "UPDATE memories SET tier = 'core' WHERE id = ?", id); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.checkpoint(ctx, db)
	return nil
}

// Delete removes one entry (by id) from whichever database holds it. The
// project and org stores are separate SQLite files, so a transaction cannot
// span them: each is tried in its own transaction (mirroring PromoteToCore's
// per-DB iteration), and ErrEntryNotFound is returned only when neither holds
// the id. deleteRow removes the FTS5 index entry and the row atomically.
func (s *sqliteStore) Delete(ctx context.Context, id string) error {
	if s.cfg.ReadOnly {
		return errors.New("memory store is read-only")
	}
	for _, db := range []*sql.DB{s.projectDB, s.orgDB} {
		if db == nil {
			continue
		}
		err := s.deleteInDB(ctx, db, id)
		if err == nil {
			return nil
		}
		if !errors.Is(err, errNotFoundHere) {
			return err
		}
	}
	return ErrEntryNotFound
}

// deleteInDB runs the delete inside its own transaction, serialized against
// concurrent Saves the same way promoteInDB serializes against them.
func (s *sqliteStore) deleteInDB(ctx context.Context, db *sql.DB, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var exists int
	if err := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM memories WHERE id = ?)", id).Scan(&exists); err != nil {
		return err
	}
	if exists == 0 {
		return errNotFoundHere
	}
	if err := s.deleteRow(ctx, tx, id); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.checkpoint(ctx, db)
	return nil
}

func (s *sqliteStore) CoreEntries(ctx context.Context, scope Scope) ([]Result, error) {
	// ScopeAll is invalid here (plan 77, E4/AR-4-adjacent finding): dbFor
	// silently routes anything but ScopeOrg to the project DB, and no entry
	// can ever have scope "all" (Entry.Validate rejects it), so an
	// unguarded call would return an empty result instead of the loud,
	// catchable error a caller-side mistake deserves. Matches memStore's
	// existing (already correct) validation for backend parity.
	if scope != ScopeProject && scope != ScopeOrg {
		return nil, fmt.Errorf("scope must be project or org, got %q", scope)
	}
	db, org := s.dbFor(scope)
	if db == nil {
		return nil, nil
	}
	query := "SELECT id, scope, org, title, verdict, tags, created, summary FROM memories\n" +
		"WHERE scope = ? AND org = ? AND tier = 'core'\n" +
		"ORDER BY created DESC, title ASC, id ASC\nLIMIT ?"
	return s.querySearch(ctx, db, query, []any{string(scope), org, CoreTierCap})
}
