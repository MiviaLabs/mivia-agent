package storage

import (
	"database/sql"
	"fmt"
)

const currentContextSchemaVersion = 14

func migrateContextSchema(db *sql.DB) error {
	if err := rejectNewerContextSchema(db); err != nil {
		return err
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS context_schema_migrations(version INTEGER PRIMARY KEY, dirty INTEGER NOT NULL CHECK(dirty IN (0,1)))`); err != nil {
		return fmt.Errorf("create context migration table: %w", err)
	}
	if err := repairContextSchema(db); err != nil {
		return err
	}
	version, dirty, err := contextSchemaState(db)
	if err != nil {
		return err
	}
	if dirty {
		return fmt.Errorf("context schema is dirty at version %d", version)
	}
	if version > currentContextSchemaVersion {
		return fmt.Errorf("context schema version %d is newer than supported version %d", version, currentContextSchemaVersion)
	}
	if version == currentContextSchemaVersion {
		return ensureContextSchemaV14(db)
	}
	return migrateContextSchemaLadder(db, version)
}

// migrateContextSchemaLadder advances an old store one migration at a time.
// Every step is a self-contained apply (create/alter plus dirty mark and a
// finalize that atomically publishes user_version and clears the flag), so the
// ladder is safe to re-run from any point after a crash. version is the schema
// version the caller already read; the final step returns once the store is
// current.
// contextMigrationLadder enumerates every schema-version transition this
// opener knows how to apply, keyed by the version migrating FROM. version 8
// jumps straight to 10 (applyContextSchemaV9AndV10 covers both in one step),
// so 9 is also a valid FROM (a store that already sits at 9 still needs
// V10). A version with no entry here (including the current version itself,
// deliberately - see migrateContextSchemaLadder) is unsupported.
var contextMigrationLadder = []struct {
	from  int
	to    int
	apply func(*sql.DB) error
}{
	{0, 1, applyContextSchemaV1},
	{1, 2, applyContextSchemaV2},
	{2, 3, applyContextSchemaV3},
	{3, 4, applyContextSchemaV4},
	{4, 5, applyContextSchemaV5},
	{5, 6, applyContextSchemaV6},
	{6, 7, applyContextSchemaV7},
	{7, 8, applyContextSchemaV8},
	{8, 10, applyContextSchemaV9AndV10},
	{9, 10, applyContextSchemaV10},
	{10, 11, applyContextSchemaV11},
	{11, 12, applyContextSchemaV12},
	{12, 13, applyContextSchemaV13},
	{13, 14, applyContextSchemaV14},
}

// migrateContextSchemaLadder walks version forward one ladder step at a time
// until it applies the step landing on currentContextSchemaVersion, matching
// the prior cascading-if implementation's exact behavior: an unrecognized
// starting version (one with no matching "from" anywhere in the ladder) is
// unsupported, but successfully reaching the current version always returns
// nil rather than looking for one more step past the top.
func migrateContextSchemaLadder(db *sql.DB, version int) error {
	for {
		var step *struct {
			from  int
			to    int
			apply func(*sql.DB) error
		}
		for i := range contextMigrationLadder {
			if contextMigrationLadder[i].from == version {
				step = &contextMigrationLadder[i]
				break
			}
		}
		if step == nil {
			return fmt.Errorf("unsupported context schema version %d", version)
		}
		if err := step.apply(db); err != nil {
			return err
		}
		version = step.to
		if version == currentContextSchemaVersion {
			return nil
		}
	}
}

func rejectNewerContextSchema(db *sql.DB) error {
	var version int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		return fmt.Errorf("read context schema version: %w", err)
	}
	if version > currentContextSchemaVersion {
		return fmt.Errorf("context schema version %d is newer than supported version %d", version, currentContextSchemaVersion)
	}
	return nil
}

func contextSchemaState(db *sql.DB) (int, bool, error) {
	var version int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		return 0, false, fmt.Errorf("read context schema version: %w", err)
	}
	var dirty int
	err := db.QueryRow(`SELECT COALESCE(MAX(dirty), 0) FROM context_schema_migrations`).Scan(&dirty)
	if err != nil {
		return 0, false, fmt.Errorf("read context schema state: %w", err)
	}
	return version, dirty != 0, nil
}

// repairContextSchema recovers a committed apply phase before finalization.
func repairContextSchema(db *sql.DB) error {
	var version int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		return fmt.Errorf("read context schema version during repair: %w", err)
	}
	for v := 1; v <= currentContextSchemaVersion; v++ {
		var dirty int
		err := db.QueryRow(`SELECT COALESCE(dirty, 0) FROM context_schema_migrations WHERE version = ?`, v).Scan(&dirty)
		if err != nil {
			continue // row doesn't exist yet, nothing to repair
		}
		if dirty == 0 {
			continue
		}
		repaired, err := finalizeContextVersion(db, v, version)
		if err != nil {
			return err
		}
		if !repaired {
			// The apply phase never committed. Leaving the dirty row untouched
			// makes migrateContextSchema report the dirty schema, and no later
			// version can be repairable once an earlier one is missing.
			return nil
		}
		if v == 7 {
			if err := ensureContextSchemaV7(db); err != nil {
				return err
			}
		}
		if v == 8 {
			if err := ensureContextSchemaV8(db); err != nil {
				return err
			}
		}
		if v == 9 {
			if err := ensureContextSchemaV9(db); err != nil {
				return err
			}
		}
		if v == 10 {
			if err := ensureContextSchemaV10(db); err != nil {
				return err
			}
		}
		if v == 11 {
			if err := ensureContextSchemaV11(db); err != nil {
				return err
			}
		}
		if v == 13 {
			if err := ensureContextSchemaV13(db); err != nil {
				return err
			}
		}
		if v == 14 {
			if err := ensureContextSchemaV14(db); err != nil {
				return err
			}
		}
	}
	return nil
}

// finalizeContextVersion re-runs migration v's finalize phase when v's tables
// are present, and reports false without changing anything when they are not.
// current is the version the caller already read; user_version only moves
// forward, so a store already past v is not rewound by an old dirty row.
func finalizeContextVersion(db *sql.DB, v, current int) (bool, error) {
	// The witness read is deliberately outside the transaction: an unreadable
	// sqlite_master means the store is unusable for reasons repair cannot fix,
	// and "no witness" is the same conservative answer either way.
	if !contextVersionTablePresent(db, v) {
		return false, nil
	}
	statements := []migrationStatement{{sql: `UPDATE context_schema_migrations SET dirty = 0 WHERE version = ?`, args: []any{v}}}
	if current < v {
		// PRAGMA takes no bind parameters; v is a compiled-in int.
		statements = append([]migrationStatement{{sql: fmt.Sprintf(`PRAGMA user_version = %d`, v)}}, statements...)
	}
	if err := inMigrationTx(db, v, "repair", func(tx *sql.Tx) error {
		return execAll(tx, statements...)
	}); err != nil {
		return false, err
	}
	return true, nil
}

// contextVersionTablePresent reports whether the table proving migration v's
// apply phase committed exists.
func contextVersionTablePresent(db *sql.DB, v int) bool {
	table := contextVersionTable(v)
	if table == "" {
		return false
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name = ?`, table).Scan(&count); err != nil {
		return false
	}
	return count > 0
}

// contextVersionTable names the table whose presence proves migration v's apply
// phase committed. An unknown version has no witness, so repair must not act.
//
// Every version listed here must have exactly one witness, and every migration
// must be listed: a missing row silently disables repair for that version,
// which is only observable as a store that stays dirty forever.
func contextVersionTable(v int) string {
	switch v {
	case 1:
		return "context_sessions"
	case 2:
		return "chat_sessions"
	case 3:
		return "context_payload_chunks"
	case 4:
		return "chat_session_admissions"
	case 5:
		return "chat_session_dirs"
	case 6:
		return "worktree_routes"
	case 7:
		return "worktree_instances"
	case 8:
		return "worktree_catalog_keys"
	case 9:
		return "worktree_routes_v9_contract"
	case 10:
		return "context_sessions_v10_contract"
	case 11:
		return "chat_sessions_v11_contract"
	case 12:
		return "token_usage_events"
	case 13:
		return "chat_sessions_v13_contract"
	case 14:
		return "context_sessions_v14_contract"
	default:
		return ""
	}
}
