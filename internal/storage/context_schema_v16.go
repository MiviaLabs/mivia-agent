package storage

import (
	"database/sql"
	"fmt"
	"strings"
)

func memoryIndexSchemaStatements() []string {
	return []string{
		`CREATE TABLE IF NOT EXISTS memory_sources (
  scope TEXT NOT NULL CHECK(scope IN ('project','org')),
  project_id TEXT NOT NULL DEFAULT '',
  org_id TEXT NOT NULL DEFAULT '',
  source_path TEXT NOT NULL CHECK(source_path <> ''),
  source_hash TEXT NOT NULL CHECK(source_hash <> ''),
  indexed_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  CHECK((scope='project' AND project_id <> '' AND org_id='') OR (scope='org' AND project_id='' AND org_id <> '')),
  PRIMARY KEY(scope, project_id, org_id, source_path)
)`,
		`CREATE TABLE IF NOT EXISTS memory_entries (
  id TEXT NOT NULL CHECK(id <> ''),
  scope TEXT NOT NULL CHECK(scope IN ('project','org')),
  project_id TEXT NOT NULL DEFAULT '',
  org_id TEXT NOT NULL DEFAULT '',
  source_path TEXT NOT NULL CHECK(source_path <> ''),
  source_hash TEXT NOT NULL CHECK(source_hash <> ''),
  title TEXT NOT NULL,
  summary TEXT NOT NULL,
  verdict TEXT NOT NULL DEFAULT 'neutral' CHECK(verdict IN ('good','bad','mixed','neutral')),
  tags TEXT NOT NULL DEFAULT '',
  created TEXT NOT NULL DEFAULT '',
  content TEXT NOT NULL,
  tier TEXT NOT NULL DEFAULT 'archive' CHECK(tier IN ('core','archive')),
  indexed_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  CHECK((scope='project' AND project_id <> '' AND org_id='') OR (scope='org' AND project_id='' AND org_id <> '')),
  PRIMARY KEY(id, scope, project_id, org_id)
)`,
		`CREATE INDEX IF NOT EXISTS memory_entries_project_idx ON memory_entries(scope, project_id)`,
		`CREATE INDEX IF NOT EXISTS memory_entries_org_idx ON memory_entries(scope, org_id)`,
		`CREATE INDEX IF NOT EXISTS memory_sources_project_idx ON memory_sources(scope, project_id)`,
	}
}

func applyContextSchemaV16(db *sql.DB) error {
	if err := applyContextMigration(db, 16, memoryIndexSchemaStatements()...); err != nil {
		return err
	}
	return validateMemoryIndexSchema(db)
}

func ensureContextSchemaV16(db *sql.DB) error {
	if err := ensureContextSchemaV15(db); err != nil {
		return err
	}
	if err := inMigrationTx(db, 16, "repair-contract", func(tx *sql.Tx) error {
		statements := make([]migrationStatement, 0, len(memoryIndexSchemaStatements()))
		for _, statement := range memoryIndexSchemaStatements() {
			statements = append(statements, migrationStatement{sql: statement})
		}
		return execAll(tx, statements...)
	}); err != nil {
		return err
	}
	return validateMemoryIndexSchema(db)
}

func validateMemoryIndexSchema(db *sql.DB) error {
	for table, columns := range map[string][]string{
		"memory_sources": {"scope", "project_id", "org_id", "source_path", "source_hash", "indexed_at"},
		"memory_entries": {"id", "scope", "project_id", "org_id", "source_path", "source_hash", "title", "summary", "verdict", "tags", "created", "content", "tier", "indexed_at"},
	} {
		rows, err := db.Query(`PRAGMA table_info(` + table + `)`)
		if err != nil {
			return fmt.Errorf("inspect %s: %w", table, err)
		}
		found := map[string]bool{}
		for rows.Next() {
			var cid int
			var name, typ string
			var notNull, pk int
			var defaultValue any
			if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil {
				rows.Close()
				return err
			}
			found[name] = true
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		for _, column := range columns {
			if !found[column] {
				return fmt.Errorf("memory index table %s is missing column %s", table, column)
			}
		}
	}
	for _, index := range []string{"memory_entries_project_idx", "memory_entries_org_idx", "memory_sources_project_idx"} {
		var count int
		if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='index' AND name=?`, index).Scan(&count); err != nil {
			return err
		}
		if count != 1 {
			return fmt.Errorf("memory index %s is missing", index)
		}
	}
	for table, requiredChecks := range map[string][]string{
		"memory_sources": {"CHECK((scope='project' AND project_id <> '' AND org_id='')", "CHECK(source_path <> '')", "CHECK(source_hash <> '')"},
		"memory_entries": {"CHECK((scope='project' AND project_id <> '' AND org_id='')", "CHECK(id <> '')", "CHECK(source_path <> '')", "CHECK(source_hash <> '')", "CHECK(verdict IN ('good','bad','mixed','neutral'))", "CHECK(tier IN ('core','archive'))"},
	} {
		var sqlText string
		if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&sqlText); err != nil {
			return err
		}
		for _, required := range requiredChecks {
			if !strings.Contains(sqlText, required) {
				return fmt.Errorf("memory index table %s is missing constraint %s", table, required)
			}
		}
	}
	return nil
}
