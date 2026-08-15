package storage

import (
	"database/sql"
	"fmt"
)

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
