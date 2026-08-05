package storage

import (
	"database/sql"
	"fmt"
)

const currentContextSchemaVersion = 8

func migrateContextSchema(db *sql.DB) error {
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
		return ensureContextSchemaV8(db)
	}
	if version == 0 {
		if err := applyContextSchemaV1(db); err != nil {
			return err
		}
		version = 1
	}
	if version == 1 {
		if err := applyContextSchemaV2(db); err != nil {
			return err
		}
		version = 2
	}
	if version == 2 {
		if err := applyContextSchemaV3(db); err != nil {
			return err
		}
		version = 3
	}
	if version == 3 {
		if err := applyContextSchemaV4(db); err != nil {
			return err
		}
		version = 4
	}
	if version == 4 {
		if err := applyContextSchemaV5(db); err != nil {
			return err
		}
		version = 5
	}
	if version == 5 {
		if err := applyContextSchemaV6(db); err != nil {
			return err
		}
		version = 6
	}
	if version == 6 {
		if err := applyContextSchemaV7(db); err != nil {
			return err
		}
		version = 7
	}
	if version == 7 {
		return applyContextSchemaV8(db)
	}
	return fmt.Errorf("unsupported context schema version %d", version)
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

// repairContextSchema recovers from a crash between a migration's apply phase
// and its finalize phase. The apply phase commits the DDL plus the (version,
// dirty=1) row; the finalize phase commits `PRAGMA user_version = version` and
// the dirty clear together, and PRAGMA user_version is transactional, so the
// only reachable interrupted state is "tables present, dirty=1, user_version
// still version-1". Repair re-drives the whole finalize phase for that version,
// which is what lets migrateContextSchema resume from the repaired version
// instead of re-applying DDL that already exists and failing forever on
// "table already exists". Returns nil when no repair is needed.
//
// It also covers the legacy window "user_version published, dirty still 1":
// finalizeContextVersion omits the PRAGMA when the store is already at or past
// v, so the dirty row is cleared without rewinding a newer version.
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
	default:
		return ""
	}
}

// applyContextSchemaV3 adds ordered payload chunks for multi-chunk source
// payloads. Version bump and dirty-clear are atomic in the finalize transaction
// (same pattern as v2 — no crash window between publish and clear).
func applyContextSchemaV3(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin context migration v3: %w", err)
	}
	if _, err := tx.Exec(`CREATE TABLE context_payload_chunks(
            ref TEXT NOT NULL,
            chunk_index INTEGER NOT NULL,
            chunk_count INTEGER NOT NULL,
            data BLOB NOT NULL,
            PRIMARY KEY(ref, chunk_index),
            CHECK(chunk_index >= 0 AND chunk_count > 0 AND chunk_index < chunk_count),
            CHECK(length(data) >= 0),
            FOREIGN KEY(ref) REFERENCES context_payloads(ref))`); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("create context_payload_chunks: %w", err)
	}
	if _, err := tx.Exec(`CREATE INDEX context_payload_chunks_ref_idx ON context_payload_chunks(ref)`); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("index context_payload_chunks: %w", err)
	}
	if _, err := tx.Exec(`INSERT OR REPLACE INTO context_schema_migrations(version, dirty) VALUES(3, 1)`); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("mark context migration v3 dirty: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit context migration v3: %w", err)
	}
	// Atomic version bump + dirty clear (no intermediate published-dirty state).
	tx2, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin context migration v3 finalize: %w", err)
	}
	if _, err := tx2.Exec(`PRAGMA user_version = 3`); err != nil {
		_ = tx2.Rollback()
		return fmt.Errorf("publish context schema v3: %w", err)
	}
	if _, err := tx2.Exec(`UPDATE context_schema_migrations SET dirty = 0 WHERE version = 3`); err != nil {
		_ = tx2.Rollback()
		return fmt.Errorf("clear context migration v3 dirty flag: %w", err)
	}
	if err := tx2.Commit(); err != nil {
		return fmt.Errorf("commit context migration v3 finalize: %w", err)
	}
	return nil
}

// applyContextSchemaV4 adds the deferred-tool admission record (plan tools/05
// D3). It is one row per named session: the admitted tool names plus the agent
// and tier digest they were admitted against, so a resume whose tier split has
// changed drops the set fail-closed instead of re-advertising names that may no
// longer mean what they meant.
//
// It is v4, not v3, because plan tools/05 was developed in parallel with the
// multi-chunk payload migration and both branches independently claimed v3.
// v3 shipped to master first, so the admission table is renumbered here rather
// than colliding: a store already at v3 has context_payload_chunks and is
// migrated forward to v4, and a merged binary can never mistake one for the
// other. Never re-point v3 at this table.
func applyContextSchemaV4(db *sql.DB) error {
	// IF NOT EXISTS so that two processes opening the same store concurrently
	// lose the race harmlessly instead of one failing on "table already exists".
	return applyContextMigration(db, 4, `CREATE TABLE IF NOT EXISTS chat_session_admissions(
            workspace_id TEXT NOT NULL, subject_id TEXT NOT NULL, name TEXT NOT NULL,
            agent TEXT NOT NULL, digest TEXT NOT NULL, names TEXT NOT NULL,
            updated_at TEXT NOT NULL,
            PRIMARY KEY(workspace_id, subject_id, name))`)
}

// applyContextSchemaV5 adds the per-session directory record (session worktree
// restore). It is a side table keyed by the same (workspace_id, subject_id,
// name) triple as chat_sessions and by session_id for live context rows, so no
// ALTER on either existing table is needed, and the table itself is the version
// witness for repairContextSchema.
func applyContextSchemaV5(db *sql.DB) error {
	// IF NOT EXISTS so that two processes opening the same store concurrently
	// lose the race harmlessly instead of one failing on "table already exists".
	return applyContextMigration(db, 5, `CREATE TABLE IF NOT EXISTS chat_session_dirs(
            workspace_id TEXT NOT NULL, subject_id TEXT NOT NULL, name TEXT NOT NULL,
            dir TEXT NOT NULL DEFAULT '', worktree TEXT NOT NULL DEFAULT '',
            PRIMARY KEY(workspace_id, subject_id, name))`)
}

// applyContextSchemaV6 adds worktree launch routes. A route has no model
// binding or chat content. It only lets the session picker restart in the
// selected worktree with the current configuration.
func applyContextSchemaV6(db *sql.DB) error {
	return applyContextMigration(db, 6, `CREATE TABLE IF NOT EXISTS worktree_routes(
            workspace_id TEXT NOT NULL, subject_id TEXT NOT NULL, worktree TEXT NOT NULL,
            dir TEXT NOT NULL, created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
            PRIMARY KEY(workspace_id, subject_id, worktree))`)
}

func applyContextSchemaV2(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin context migration v2: %w", err)
	}
	if _, err := tx.Exec(`CREATE TABLE chat_sessions(
            workspace_id TEXT NOT NULL, subject_id TEXT NOT NULL, name TEXT NOT NULL,
            model TEXT NOT NULL, provider TEXT NOT NULL, messages BLOB NOT NULL,
            created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
            turn_count INTEGER NOT NULL, token_count INTEGER NOT NULL,
            message_count INTEGER NOT NULL,
            PRIMARY KEY(workspace_id, subject_id, name))`); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("create chat session catalog: %w", err)
	}
	if _, err := tx.Exec(`INSERT OR REPLACE INTO context_schema_migrations(version, dirty) VALUES(2, 1)`); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("mark context migration v2 dirty: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit context migration v2: %w", err)
	}
	// Second transaction: atomically bump user_version and clear dirty flag.
	tx2, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin context migration v2 finalize: %w", err)
	}
	if _, err := tx2.Exec(`PRAGMA user_version = 2`); err != nil {
		_ = tx2.Rollback()
		return fmt.Errorf("publish context schema v2: %w", err)
	}
	if _, err := tx2.Exec(`UPDATE context_schema_migrations SET dirty = 0 WHERE version = 2`); err != nil {
		_ = tx2.Rollback()
		return fmt.Errorf("clear context migration v2 dirty flag: %w", err)
	}
	if err := tx2.Commit(); err != nil {
		return fmt.Errorf("commit context migration v2 finalize: %w", err)
	}
	return nil
}

func applyContextSchemaV1(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin context schema migration: %w", err)
	}
	if err := execContextSchemaStatements(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	if _, err := tx.Exec(`INSERT OR REPLACE INTO context_schema_migrations(version, dirty) VALUES(1, 1)`); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("mark context schema dirty: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit context schema migration: %w", err)
	}
	// Second transaction: atomically bump user_version and clear dirty flag.
	tx2, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin context migration v1 finalize: %w", err)
	}
	if _, err := tx2.Exec(`PRAGMA user_version = 1`); err != nil {
		_ = tx2.Rollback()
		return fmt.Errorf("publish context schema version: %w", err)
	}
	if _, err := tx2.Exec(`UPDATE context_schema_migrations SET dirty = 0 WHERE version = 1`); err != nil {
		_ = tx2.Rollback()
		return fmt.Errorf("clear context schema dirty flag: %w", err)
	}
	if err := tx2.Commit(); err != nil {
		return fmt.Errorf("commit context migration v1 finalize: %w", err)
	}
	return nil
}

func execContextSchemaStatements(tx *sql.Tx) error {
	for _, statement := range contextSchemaStatements() {
		if _, err := tx.Exec(statement); err != nil {
			return fmt.Errorf("apply context schema statement: %w", err)
		}
	}
	return nil
}

func contextSchemaStatements() []string {
	statements := append([]string{}, contextSchemaCoreStatements()...)
	statements = append(statements, contextSchemaRevisionStatements()...)
	return append(statements, contextSchemaGuardStatements()...)
}

func contextSchemaCoreStatements() []string {
	return []string{
		`CREATE TABLE context_sessions(
            workspace_id TEXT NOT NULL, subject_id TEXT NOT NULL, session_id TEXT NOT NULL,
            capability_digest TEXT NOT NULL, session_revision INTEGER NOT NULL,
            durable_revision INTEGER NOT NULL, source_sequence INTEGER NOT NULL,
            provider TEXT NOT NULL, model TEXT NOT NULL, binding_generation INTEGER NOT NULL,
            active_checkpoint_id TEXT, tombstoned INTEGER NOT NULL DEFAULT 0,
            PRIMARY KEY(workspace_id, session_id), UNIQUE(session_id),
            UNIQUE(workspace_id, session_id, subject_id),
            CHECK(session_revision >= 0 AND durable_revision >= 0 AND source_sequence >= 0),
            CHECK(tombstoned IN (0,1)))`,
		`CREATE TABLE context_payloads(
            ref TEXT PRIMARY KEY, namespace TEXT NOT NULL,
            workspace_id TEXT NOT NULL, session_id TEXT NOT NULL, subject_id TEXT NOT NULL,
            sha256 TEXT NOT NULL, size INTEGER NOT NULL, redaction_status TEXT NOT NULL,
            retention_class TEXT NOT NULL, revoked INTEGER NOT NULL DEFAULT 0,
            data BLOB, created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
            expires_at TEXT NOT NULL DEFAULT '9999-12-31T23:59:59Z',
            UNIQUE(ref, namespace), CHECK(namespace = 'mivia.context.payload.v1'),
            CHECK(size >= 0), CHECK(revoked IN (0,1)),
            FOREIGN KEY(workspace_id, session_id, subject_id)
                REFERENCES context_sessions(workspace_id, session_id, subject_id))`,
		`CREATE TABLE context_source_events(
            workspace_id TEXT NOT NULL, session_id TEXT NOT NULL, subject_id TEXT NOT NULL,
            sequence INTEGER NOT NULL, event_id TEXT NOT NULL UNIQUE, kind TEXT NOT NULL,
            role TEXT NOT NULL, tool_call_id TEXT, payload_ref TEXT, payload_namespace TEXT,
            payload_size INTEGER NOT NULL, provenance TEXT NOT NULL, redaction_status TEXT NOT NULL,
            PRIMARY KEY(session_id, sequence),
            FOREIGN KEY(workspace_id, session_id, subject_id)
                REFERENCES context_sessions(workspace_id, session_id, subject_id))`,
	}
}

func contextSchemaRevisionStatements() []string {
	return []string{
		`CREATE TABLE context_checkpoints(
            checkpoint_id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL, session_id TEXT NOT NULL,
            subject_id TEXT NOT NULL, source_start INTEGER NOT NULL, source_end INTEGER NOT NULL,
            algorithm TEXT NOT NULL, schema_version INTEGER NOT NULL, summary_model TEXT NOT NULL,
            operation_id TEXT NOT NULL, idempotency_key TEXT NOT NULL,
            session_revision INTEGER NOT NULL, durable_revision INTEGER NOT NULL,
            binding_generation INTEGER NOT NULL, turn_id INTEGER NOT NULL,
            summary_metadata BLOB NOT NULL, active_context BLOB NOT NULL,
            content_fingerprint TEXT NOT NULL, complete INTEGER NOT NULL DEFAULT 0,
            created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
            FOREIGN KEY(workspace_id, session_id, subject_id)
                REFERENCES context_sessions(workspace_id, session_id, subject_id),
            UNIQUE(session_id, operation_id), UNIQUE(session_id, idempotency_key),
            CHECK(source_start <= source_end), CHECK(complete IN (0,1)))`,
		`CREATE TABLE context_audits(
            audit_id TEXT PRIMARY KEY, action TEXT NOT NULL, workspace_id TEXT NOT NULL,
            session_id TEXT NOT NULL, subject_id TEXT NOT NULL, revision INTEGER NOT NULL,
            size INTEGER NOT NULL, retention_class TEXT NOT NULL, expires_at TEXT NOT NULL,
            created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
            FOREIGN KEY(workspace_id, session_id, subject_id)
                REFERENCES context_sessions(workspace_id, session_id, subject_id))`,
		`CREATE TABLE context_tombstones(
            session_id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL, subject_id TEXT NOT NULL,
            revision INTEGER NOT NULL, retention_class TEXT NOT NULL, expires_at TEXT NOT NULL,
            audit_id TEXT NOT NULL, created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
            FOREIGN KEY(workspace_id, session_id, subject_id)
                REFERENCES context_sessions(workspace_id, session_id, subject_id))`,
		`CREATE TABLE context_operations(
            session_id TEXT NOT NULL, operation_id TEXT NOT NULL, fingerprint TEXT NOT NULL,
            kind TEXT NOT NULL, result BLOB, created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
            PRIMARY KEY(session_id, operation_id))`,
		`CREATE TABLE context_imports(
            workspace_id TEXT NOT NULL, session_id TEXT NOT NULL, subject_id TEXT NOT NULL,
            idempotency_key TEXT NOT NULL, fingerprint TEXT NOT NULL, result BLOB NOT NULL,
            created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
            PRIMARY KEY(session_id, idempotency_key),
            FOREIGN KEY(workspace_id, session_id, subject_id)
                REFERENCES context_sessions(workspace_id, session_id, subject_id))`,
	}
}

func contextSchemaGuardStatements() []string {
	return []string{
		`CREATE TRIGGER context_session_active_checkpoint_guard
            BEFORE UPDATE OF active_checkpoint_id ON context_sessions
            WHEN NEW.active_checkpoint_id IS NOT NULL AND NOT EXISTS(
                SELECT 1 FROM context_checkpoints c
                WHERE c.checkpoint_id = NEW.active_checkpoint_id
                  AND c.workspace_id = NEW.workspace_id AND c.session_id = NEW.session_id
                  AND c.subject_id = NEW.subject_id AND c.complete = 1
                  AND c.source_end <= NEW.source_sequence)
            BEGIN SELECT RAISE(ABORT, 'active checkpoint is not complete or owner scoped'); END`,
		`CREATE TRIGGER context_source_payload_guard
            BEFORE INSERT ON context_source_events
            WHEN NEW.payload_ref IS NOT NULL AND NOT EXISTS(
                SELECT 1 FROM context_payloads p
                WHERE p.ref = NEW.payload_ref AND p.namespace = COALESCE(NEW.payload_namespace, '')
                  AND p.workspace_id = NEW.workspace_id AND p.session_id = NEW.session_id
                  AND p.subject_id = NEW.subject_id AND p.revoked = 0)
            BEGIN SELECT RAISE(ABORT, 'source payload is not an active owner-scoped payload'); END`,
	}
}
