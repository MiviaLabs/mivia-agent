package storage

import "database/sql"

// applyContextSchemaV12 adds the durable usage-measurement table. It is a
// bare CREATE TABLE (plus its indexes) with no ALTER on an existing table and
// no data-shape invariant to heal, so - matching applyContextSchemaV5/V6 -
// it needs no bespoke ensureContextSchemaV12/repair-branch: the table's own
// presence is the version witness repairContextSchema checks.
func applyContextSchemaV12(db *sql.DB) error {
	return applyContextMigration(db, 12,
		`CREATE TABLE IF NOT EXISTS token_usage_events(
            id                  INTEGER PRIMARY KEY AUTOINCREMENT,
            workspace_id        TEXT NOT NULL,
            session_id          TEXT NOT NULL,
            turn_id             TEXT NOT NULL,
            kind                TEXT NOT NULL CHECK(kind IN ('token_usage','cache_usage','compaction')),
            provider            TEXT,
            model               TEXT,
            input_tokens        INTEGER,
            output_tokens       INTEGER,
            estimated_tokens    INTEGER,
            calibration_ratio   REAL,
            cached_input_tokens INTEGER,
            cache_write_tokens  INTEGER,
            before_tokens       INTEGER,
            after_tokens        INTEGER,
            elided_messages     INTEGER,
            elided_bytes        INTEGER,
            summarized          INTEGER,
            reason              TEXT,
            agent_task          TEXT,
            agent_name          TEXT,
            agent_depth         INTEGER,
            created_at          INTEGER NOT NULL)`,
		`CREATE INDEX IF NOT EXISTS idx_token_usage_events_session ON token_usage_events(workspace_id, session_id, created_at)`,
		`CREATE INDEX IF NOT EXISTS idx_token_usage_events_turn ON token_usage_events(workspace_id, session_id, turn_id)`,
	)
}
