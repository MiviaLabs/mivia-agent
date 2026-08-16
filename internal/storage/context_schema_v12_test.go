package storage

import (
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

// TestMigrationV12AddsTokenUsageEventsTable confirms a fresh store and a
// store already at v11 both converge on v12 with token_usage_events present -
// the two paths migrateContextSchema actually takes (steady-state create vs.
// ladder continuation), matching the pattern
// TestMigrationV11ConvergesFromEveryPriorVersion already uses for v11.
func TestMigrationV12AddsTokenUsageEventsTable(t *testing.T) {
	apply := []func(*sql.DB) error{
		applyContextSchemaV1, applyContextSchemaV2, applyContextSchemaV3, applyContextSchemaV4,
		applyContextSchemaV5, applyContextSchemaV6, applyContextSchemaV7, applyContextSchemaV8,
		applyContextSchemaV9, applyContextSchemaV10, applyContextSchemaV11,
	}
	tests := []struct {
		name     string
		versions int
	}{
		{name: "fresh", versions: 0},
		{name: "v11", versions: 11},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "context.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if test.versions > 0 {
				if _, err := db.Exec(`CREATE TABLE context_schema_migrations(version INTEGER PRIMARY KEY, dirty INTEGER NOT NULL CHECK(dirty IN (0,1)))`); err != nil {
					t.Fatal(err)
				}
				for i := 0; i < test.versions; i++ {
					if err := apply[i](db); err != nil {
						t.Fatal(err)
					}
				}
			}
			if err := migrateContextSchema(db); err != nil {
				t.Fatalf("migrateContextSchema from %s: %v", test.name, err)
			}
			var version int
			if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
				t.Fatal(err)
			}
			if version != 12 {
				t.Fatalf("user_version = %d after %s, want 12", version, test.name)
			}
			if !contextVersionTablePresent(db, 12) {
				t.Fatalf("token_usage_events is missing after %s", test.name)
			}
			// Round-trip a row through the real column set, confirming the DDL
			// actually accepts the shape RecordUsageEvent writes.
			if _, err := db.Exec(`INSERT INTO token_usage_events(
				workspace_id, session_id, turn_id, kind, provider, model,
				input_tokens, output_tokens, estimated_tokens, calibration_ratio,
				cached_input_tokens, cache_write_tokens,
				before_tokens, after_tokens, elided_messages, elided_bytes,
				summarized, reason, agent_task, agent_name, agent_depth, created_at
			) VALUES ('ws', 'sess', 'turn:1', 'token_usage', 'deepseek', 'deepseek-v4-flash',
				100, 50, 95, 1.05, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, 0, 1700000000)`); err != nil {
				t.Fatalf("insert into token_usage_events: %v", err)
			}
			var kind string
			if err := db.QueryRow(`SELECT kind FROM token_usage_events WHERE session_id = 'sess'`).Scan(&kind); err != nil {
				t.Fatal(err)
			}
			if kind != "token_usage" {
				t.Fatalf("kind = %q, want token_usage", kind)
			}
		})
	}
}

// TestMigrationV12RejectsUnknownKind pins the CHECK constraint: a row whose
// kind isn't one of the three known values must be refused, not silently
// accepted as free text that a query later can't classify.
func TestMigrationV12RejectsUnknownKind(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := migrateContextSchema(db); err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO token_usage_events(workspace_id, session_id, turn_id, kind, created_at)
		VALUES ('ws', 'sess', 'turn:1', 'not_a_real_kind', 1700000000)`)
	if err == nil {
		t.Fatal("insert with an unknown kind must fail the CHECK constraint")
	}
}
