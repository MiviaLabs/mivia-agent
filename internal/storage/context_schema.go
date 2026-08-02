package storage

import (
	"database/sql"
	"fmt"
	"strings"
)

const currentContextSchemaVersion = 3

func sqliteDSN(path string) string {
	separator := "?"
	if strings.Contains(path, "?") {
		separator = "&"
	}
	return path + separator + "_pragma=journal_mode(WAL)&_pragma=synchronous(FULL)&_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)"
}

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
		return nil
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
		return applyContextSchemaV3(db)
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

// repairContextSchema recovers from a crash between the version bump and the
// dirty-flag clear in older versions of applyContextSchemaV1/V2. If the store
// is stuck at version N with dirty=true, it clears dirty (the schema tables
// are already committed in the first transaction). Returns nil when no repair
// is needed.
func repairContextSchema(db *sql.DB) error {
	for _, v := range []int{1, 2, 3} {
		var dirty int
		err := db.QueryRow(`SELECT COALESCE(dirty, 0) FROM context_schema_migrations WHERE version = ?`, v).Scan(&dirty)
		if err != nil {
			continue // row doesn't exist yet, nothing to repair
		}
		if dirty == 0 {
			continue
		}
		// Verify the expected tables exist so we know the first tx committed.
		tx, err := db.Begin()
		if err != nil {
			return fmt.Errorf("begin context repair tx version %d: %w", v, err)
		}
		var count int
		switch v {
		case 1:
			_ = tx.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name='context_sessions'`).Scan(&count)
		case 2:
			_ = tx.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name='chat_sessions'`).Scan(&count)
		case 3:
			_ = tx.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name='chat_session_admissions'`).Scan(&count)
		}
		if count == 0 {
			// Table is missing — first transaction never committed; rollback
			// leaves the dirty row untouched so migrateContextSchema will
			// correctly report a dirty schema.
			_ = tx.Rollback()
			return nil
		}
		if _, err := tx.Exec(`UPDATE context_schema_migrations SET dirty = 0 WHERE version = ?`, v); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("repair context dirty flag version %d: %w", v, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit context repair version %d: %w", v, err)
		}
	}
	return nil
}

// applyContextSchemaV3 adds the deferred-tool admission record (plan tools/05
// D3). It is one row per named session: the admitted tool names plus the agent
// and tier digest they were admitted against, so a resume whose tier split has
// changed drops the set fail-closed instead of re-advertising names that may no
// longer mean what they meant.
func applyContextSchemaV3(db *sql.DB) error {
	return applyContextMigration(db, 3, `CREATE TABLE chat_session_admissions(
            workspace_id TEXT NOT NULL, subject_id TEXT NOT NULL, name TEXT NOT NULL,
            agent TEXT NOT NULL, digest TEXT NOT NULL, names TEXT NOT NULL,
            updated_at TEXT NOT NULL,
            PRIMARY KEY(workspace_id, subject_id, name))`)
}

// applyContextMigration runs one DDL statement as a migration: schema in the
// first transaction, version bump and dirty clear in the second, matching the
// crash-recovery contract repairContextSchema depends on. A crash between the
// two leaves the schema committed and the dirty flag set, which is exactly what
// repairContextSchema knows how to recover.
func applyContextMigration(db *sql.DB, version int, ddl string) error {
	if err := inMigrationTx(db, version, "apply", func(tx *sql.Tx) error {
		return execAll(tx,
			migrationStatement{sql: ddl},
			migrationStatement{sql: `INSERT OR REPLACE INTO context_schema_migrations(version, dirty) VALUES(?, 1)`, args: []any{version}},
		)
	}); err != nil {
		return err
	}
	return inMigrationTx(db, version, "finalize", func(tx *sql.Tx) error {
		return execAll(tx,
			// PRAGMA takes no bind parameters; version is a compiled-in int.
			migrationStatement{sql: fmt.Sprintf(`PRAGMA user_version = %d`, version)},
			migrationStatement{sql: `UPDATE context_schema_migrations SET dirty = 0 WHERE version = ?`, args: []any{version}},
		)
	})
}

// migrationStatement is one statement of a migration phase.
type migrationStatement struct {
	sql  string
	args []any
}

// execAll runs statements in order and stops at the first failure. The caller's
// transaction wrapper decides what a failure means.
func execAll(tx *sql.Tx, statements ...migrationStatement) error {
	for _, statement := range statements {
		if _, err := tx.Exec(statement.sql, statement.args...); err != nil {
			return err
		}
	}
	return nil
}

// inMigrationTx runs one migration phase in its own transaction. A body failure
// and a commit failure are the same outcome - the phase did not land - so they
// share one rollback-and-report path rather than two that cannot both be
// exercised.
func inMigrationTx(db *sql.DB, version int, phase string, body func(*sql.Tx) error) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin context migration v%d %s: %w", version, phase, err)
	}
	if err = body(tx); err == nil {
		err = tx.Commit()
	} else {
		_ = tx.Rollback()
	}
	if err != nil {
		return fmt.Errorf("context migration v%d %s: %w", version, phase, err)
	}
	return nil
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
