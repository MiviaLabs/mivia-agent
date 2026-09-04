package storage

import "database/sql"

func memoryIndexSchemaStatements() []string {
	return []string{
		`CREATE TABLE IF NOT EXISTS memory_sources (
  scope TEXT NOT NULL CHECK(scope IN ('project','org')),
  project_id TEXT NOT NULL DEFAULT '',
  org_id TEXT NOT NULL DEFAULT '',
  source_path TEXT NOT NULL,
  source_hash TEXT NOT NULL,
  indexed_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY(scope, project_id, org_id, source_path)
)`,
		`CREATE TABLE IF NOT EXISTS memory_entries (
  id TEXT NOT NULL,
  scope TEXT NOT NULL CHECK(scope IN ('project','org')),
  project_id TEXT NOT NULL DEFAULT '',
  org_id TEXT NOT NULL DEFAULT '',
  source_path TEXT NOT NULL,
  source_hash TEXT NOT NULL,
  title TEXT NOT NULL,
  summary TEXT NOT NULL,
  verdict TEXT NOT NULL DEFAULT 'neutral',
  tags TEXT NOT NULL DEFAULT '',
  created TEXT NOT NULL DEFAULT '',
  content TEXT NOT NULL,
  tier TEXT NOT NULL DEFAULT 'archive' CHECK(tier IN ('core','archive')),
  indexed_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY(id, scope, project_id, org_id)
)`,
		`CREATE INDEX IF NOT EXISTS memory_entries_project_idx ON memory_entries(scope, project_id)`,
		`CREATE INDEX IF NOT EXISTS memory_entries_org_idx ON memory_entries(scope, org_id)`,
		`CREATE INDEX IF NOT EXISTS memory_sources_project_idx ON memory_sources(scope, project_id)`,
	}
}

func applyContextSchemaV16(db *sql.DB) error {
	return applyContextMigration(db, 16, memoryIndexSchemaStatements()...)
}

func ensureContextSchemaV16(db *sql.DB) error {
	if err := ensureContextSchemaV15(db); err != nil {
		return err
	}
	return inMigrationTx(db, 16, "repair-contract", func(tx *sql.Tx) error {
		statements := make([]migrationStatement, 0, len(memoryIndexSchemaStatements()))
		for _, statement := range memoryIndexSchemaStatements() {
			statements = append(statements, migrationStatement{sql: statement})
		}
		return execAll(tx, statements...)
	})
}
