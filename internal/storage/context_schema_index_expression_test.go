package storage

import (
	"database/sql"
	"path/filepath"
	"testing"
)

func TestSchemaRepairReplacesQuotedCombinedIdentifierIndexes(t *testing.T) {
	tests := []struct {
		name       string
		indexName  string
		definition string
	}{
		{
			name:       "instance ID",
			indexName:  "worktree_instances_id_idx",
			definition: `CREATE UNIQUE INDEX worktree_instances_id_idx ON worktree_instances("workspace_id,instance_id")`,
		},
		{
			name:       "live name",
			indexName:  "worktree_instances_live_name_idx",
			definition: `CREATE UNIQUE INDEX worktree_instances_live_name_idx ON worktree_instances("workspace_id,worktree") WHERE state != 'deleted'`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "context.db")
			store, err := OpenSQLite(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			db, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`DROP INDEX ` + test.indexName); err != nil {
				db.Close()
				t.Fatal(err)
			}
			if _, err := db.Exec(test.definition); err != nil {
				db.Close()
				t.Fatal(err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			store, err = OpenSQLite(path)
			if err != nil {
				t.Fatalf("repair quoted combined identifier: %v", err)
			}
			defer store.Close()
			rows := []struct {
				workspace string
				worktree  string
				id        string
			}{
				{"workspace-a", "wt-a", "wt_1111111111111111"},
				{"workspace-b", "wt-b", "wt_2222222222222222"},
			}
			for _, row := range rows {
				if _, err := store.db.Exec(`INSERT INTO worktree_instances(workspace_id,worktree,instance_id,canonical_path,state,created_at,updated_at) VALUES(?,?,?,?, 'creating',CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)`, row.workspace, row.worktree, row.id, "/tmp/"+row.worktree); err != nil {
					t.Fatalf("insert distinct lifecycle row: %v", err)
				}
			}
		})
	}
}
