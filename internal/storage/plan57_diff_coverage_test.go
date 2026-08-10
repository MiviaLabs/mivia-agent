package storage

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
)

type coverageResult struct {
	rows int64
	err  error
}

func (r coverageResult) LastInsertId() (int64, error) { return 0, nil }
func (r coverageResult) RowsAffected() (int64, error) { return r.rows, r.err }

func TestCatalogMutationCoverageErrors(t *testing.T) {
	if err := requireCatalogMutation(coverageResult{err: errors.New("rows failed")}); err == nil {
		t.Fatal("RowsAffected error was accepted")
	}
	if err := requireCatalogMutation(coverageResult{}); !errors.Is(err, contextstate.ErrWorktreeDeleted) {
		t.Fatalf("zero rows = %v", err)
	}
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "closed.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	principal, err := contextstate.NewPrincipal("workspace", "session", "subject")
	if err != nil {
		t.Fatal(err)
	}
	if err := rejectManagedCatalogKey(context.Background(), db, principal, "name"); err == nil {
		t.Fatal("closed database error was hidden")
	}
}

func TestPlan57ValidationCoverage(t *testing.T) {
	binding := mustBinding(t)
	valid, err := contextstate.NewPrincipal("workspace", "session", "subject")
	if err != nil {
		t.Fatal(err)
	}
	tests := []contextstate.EnsureSessionRequest{
		{},
		{Principal: contextstate.Principal{WorkspaceID: "workspace", SessionID: "session", SubjectID: "subject"}, Binding: binding},
		{Principal: valid, Binding: binding, Dir: "bad\x00dir"},
		{Principal: valid, Binding: binding, WorktreeInstance: contextstate.WorktreeInstance{Worktree: "bad/name", ID: "bad"}},
	}
	for index, request := range tests {
		if err := validateEnsureRequest(request); err == nil {
			t.Fatalf("invalid request %d was accepted", index)
		}
	}
}

func TestSchemaHelperErrorCoverage(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "schema.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := rejectNewerContextSchema(db); err == nil {
		t.Fatal("closed schema database was accepted")
	}
	if err := migrateContextSchema(db); err == nil {
		t.Fatal("closed schema migration was accepted")
	}

	live, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "tx.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer live.Close()
	tx, err := live.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := contextInstanceColumnContract(tx, "missing"); err == nil {
		t.Fatal("finished transaction column query was accepted")
	}
	if err := ensureWorktreeInstancesTable(tx); err == nil {
		t.Fatal("finished transaction table repair was accepted")
	}
	if err := ensureExactContextIndex(tx, "index", "CREATE INDEX index ON missing(value)"); err == nil {
		t.Fatal("finished transaction index repair was accepted")
	}
	if _, err := worktreeRoutesV9Ready(tx); err == nil {
		t.Fatal("finished transaction route inspection was accepted")
	}
}

func TestUnboundAuthorizationPropagatesOwnerError(t *testing.T) {
	store, principal := openContextTestStore(t)
	defer store.Close()
	tx, err := store.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	foreign, err := contextstate.NewPrincipal(principal.WorkspaceID, principal.SessionID, "foreign")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := authorizeUnboundContextSessionTx(context.Background(), tx, foreign, foreign.SessionID); err == nil {
		t.Fatal("foreign principal was accepted")
	}
}

func TestLiveWorktreeInstanceErrorCoverage(t *testing.T) {
	store, principal := openContextTestStore(t)
	defer store.Close()
	if _, err := store.LiveWorktreeInstance(context.Background(), contextstate.Principal{}, "wt"); err == nil {
		t.Fatal("invalid principal was accepted")
	}
	if _, err := store.LiveWorktreeInstance(context.Background(), principal, "missing"); !errors.Is(err, contextstate.ErrWorktreeDeleted) {
		t.Fatalf("missing instance = %v", err)
	}
	if _, err := store.db.Exec(`DROP INDEX worktree_instances_live_name_idx`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`PRAGMA ignore_check_constraints=ON`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`INSERT INTO worktree_instances(workspace_id,worktree,instance_id,canonical_path,state,created_at,updated_at) VALUES(?,?,?,?,?,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)`, principal.WorkspaceID, "bad", "wt_1111111111111111", "/tmp/bad", "corrupt"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LiveWorktreeInstance(context.Background(), principal, "bad"); !errors.Is(err, contextstate.ErrInvalidDTO) {
		t.Fatalf("invalid state = %v", err)
	}
	if _, err := store.db.Exec(`DROP TABLE worktree_instances`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LiveWorktreeInstance(context.Background(), principal, "bad"); err == nil {
		t.Fatal("missing lifecycle table was accepted")
	}
}

func TestSchemaRepairBranchCoverage(t *testing.T) {
	store, _ := openContextTestStore(t)
	defer store.Close()
	t.Run("repair table already exists", func(t *testing.T) {
		tx, err := store.db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		if _, err := tx.Exec(`ALTER TABLE worktree_instances ADD COLUMN extra TEXT`); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(`CREATE TABLE worktree_instances_v7_repair(value TEXT)`); err != nil {
			t.Fatal(err)
		}
		if err := ensureWorktreeInstancesTable(tx); err == nil {
			t.Fatal("existing repair table was accepted")
		}
	})
	t.Run("automatic index drop", func(t *testing.T) {
		tx, err := store.db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		if err := ensureExactContextIndex(tx, "sqlite_autoindex_worktree_instances_1", worktreeInstanceIDIndexSQL); err == nil {
			t.Fatal("automatic index replacement was accepted")
		}
	})
	t.Run("missing route table", func(t *testing.T) {
		tx, err := store.db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		if _, err := tx.Exec(`DROP TABLE worktree_routes`); err != nil {
			t.Fatal(err)
		}
		if err := ensureWorktreeRoutesV9Witness(tx); err == nil {
			t.Fatal("missing route table was accepted")
		}
	})
	t.Run("extra route column", func(t *testing.T) {
		tx, err := store.db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		if _, err := tx.Exec(`ALTER TABLE worktree_routes ADD COLUMN extra TEXT`); err != nil {
			t.Fatal(err)
		}
		ready, err := worktreeRoutesV9Ready(tx)
		if err != nil || ready {
			t.Fatalf("extra route column = %v, %v", ready, err)
		}
	})
}

func TestSchemaRepairWriteFailureCoverage(t *testing.T) {
	t.Run("referenced table drop", func(t *testing.T) {
		store, _ := openContextTestStore(t)
		defer store.Close()
		tx, err := store.db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		if _, err := tx.Exec(`ALTER TABLE worktree_instances ADD COLUMN extra TEXT`); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(`INSERT INTO worktree_instances(workspace_id,worktree,instance_id,canonical_path,state,created_at,updated_at) VALUES('workspace','wt','wt_1111111111111111','/tmp/wt','active',CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)`); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(`CREATE TABLE lifecycle_child(workspace_id TEXT,worktree TEXT,instance_id TEXT,FOREIGN KEY(workspace_id,worktree,instance_id) REFERENCES worktree_instances(workspace_id,worktree,instance_id))`); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(`INSERT INTO lifecycle_child VALUES('workspace','wt','wt_1111111111111111')`); err != nil {
			t.Fatal(err)
		}
		if err := ensureWorktreeInstancesTable(tx); err == nil {
			t.Fatal("referenced lifecycle table was replaced")
		}
	})
	t.Run("read only index drop", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "readonly.db")
		store, err := OpenSQLite(path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.db.Exec(`DROP INDEX worktree_instances_id_idx`); err != nil {
			t.Fatal(err)
		}
		if _, err := store.db.Exec(`CREATE INDEX worktree_instances_id_idx ON worktree_instances(instance_id)`); err != nil {
			t.Fatal(err)
		}
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
		readOnly, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
		if err != nil {
			t.Fatal(err)
		}
		defer readOnly.Close()
		tx, err := readOnly.Begin()
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		if err := ensureExactContextIndex(tx, "worktree_instances_id_idx", worktreeInstanceIDIndexSQL); err == nil {
			t.Fatal("read-only index was replaced")
		}
	})
}

func TestCatalogWriteFailureCoverage(t *testing.T) {
	t.Run("snapshot collision", func(t *testing.T) {
		store, principal := openContextTestStore(t)
		defer store.Close()
		if _, err := store.db.Exec(`INSERT INTO chat_sessions(workspace_id,subject_id,name,model,provider,messages,created_at,updated_at,turn_count,token_count,message_count,instance_id) VALUES(?,?,?,?,?,?,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP,0,0,0,?)`, principal.WorkspaceID, principal.SubjectID, "collision", "model", "provider", []byte(`[]`), "wt_1111111111111111"); err != nil {
			t.Fatal(err)
		}
		err := store.SaveSession(context.Background(), principal, "collision", []byte(`[]`), "model", "provider", 0, 0, 0, contextstate.SessionSaveOptions{})
		if !errors.Is(err, contextstate.ErrWorktreeDeleted) {
			t.Fatalf("collision = %v", err)
		}
	})
	t.Run("directory trigger", func(t *testing.T) {
		store, principal := openContextTestStore(t)
		defer store.Close()
		mustCoverageTrigger(t, store, `CREATE TRIGGER block_dir BEFORE INSERT ON chat_session_dirs BEGIN SELECT RAISE(ABORT,'blocked'); END`)
		if err := store.SaveSession(context.Background(), principal, "snapshot", []byte(`[]`), "model", "provider", 0, 0, 0, contextstate.SessionSaveOptions{}); err == nil {
			t.Fatal("directory failure was hidden")
		}
	})
	t.Run("admission trigger", func(t *testing.T) {
		store, principal := openContextTestStore(t)
		defer store.Close()
		mustCoverageTrigger(t, store, `CREATE TRIGGER block_admission BEFORE INSERT ON chat_session_admissions BEGIN SELECT RAISE(ABORT,'blocked'); END`)
		record := contextstate.SessionAdmission{Agent: "agent", Digest: "digest", Names: []string{"tool"}}
		if err := store.SaveSessionAdmission(context.Background(), principal, "snapshot", record); err == nil {
			t.Fatal("admission failure was hidden")
		}
	})
}

func TestWorktreeCatalogWriteFailureCoverage(t *testing.T) {
	t.Run("catalog prune", func(t *testing.T) {
		store, principal, instance, _ := seedManagedCatalogKey(t)
		defer store.Close()
		mustCoverageTrigger(t, store, `CREATE TRIGGER block_key_delete BEFORE DELETE ON worktree_catalog_keys BEGIN SELECT RAISE(ABORT,'blocked'); END`)
		if err := store.PruneWorktreeSessionSnapshots(context.Background(), principal, []string{"managed"}, instance); err == nil {
			t.Fatal("catalog key delete failure was hidden")
		}
	})
	t.Run("managed admission", func(t *testing.T) {
		store, principal := openContextTestStore(t)
		defer store.Close()
		instance := contextstate.WorktreeInstance{Worktree: "wt-a", ID: "wt_1111111111111111"}
		worktreeDir := filepath.Join(t.TempDir(), "worktrees", instance.Worktree)
		if err := registerCleanupInstance(context.Background(), store, principal, instance, worktreeDir); err != nil {
			t.Fatal(err)
		}
		mustCoverageTrigger(t, store, `CREATE TRIGGER block_managed_admission BEFORE INSERT ON chat_session_admissions BEGIN SELECT RAISE(ABORT,'blocked'); END`)
		record := contextstate.SessionAdmission{Agent: "agent", Digest: "digest", Names: []string{"tool"}}
		if err := store.SaveWorktreeSessionAdmission(context.Background(), principal, "managed", record, instance); err == nil {
			t.Fatal("managed admission failure was hidden")
		}
	})
}

func TestWorktreeCleanupFailureCoverage(t *testing.T) {
	tests := []struct {
		name    string
		trigger string
	}{
		{"catalog keys", `CREATE TRIGGER block_cleanup_keys BEFORE DELETE ON worktree_catalog_keys BEGIN SELECT RAISE(ABORT,'blocked'); END`},
		{"state update", `CREATE TRIGGER block_cleanup_update BEFORE UPDATE ON worktree_instances BEGIN SELECT RAISE(ABORT,'blocked'); END`},
		{"zero state update", `CREATE TRIGGER ignore_cleanup_update BEFORE UPDATE ON worktree_instances BEGIN SELECT RAISE(IGNORE); END`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store, principal := openContextTestStore(t)
			defer store.Close()
			instance := contextstate.WorktreeInstance{Worktree: "wt-a", ID: "wt_1111111111111111"}
			worktreeDir := filepath.Join(t.TempDir(), "worktrees", instance.Worktree)
			if err := registerCleanupInstance(context.Background(), store, principal, instance, worktreeDir); err != nil {
				t.Fatal(err)
			}
			if err := store.SaveSession(context.Background(), principal, "managed", []byte(`[]`), "model", "provider", 0, 0, 0, contextstate.SessionSaveOptions{WorktreeInstance: instance}); err != nil {
				t.Fatal(err)
			}
			if err := store.BeginWorktreeDeletion(context.Background(), principal, instance); err != nil {
				t.Fatal(err)
			}
			mustCoverageTrigger(t, store, test.trigger)
			if _, err := store.DeleteWorktreeSessions(context.Background(), principal, instance); err == nil {
				t.Fatal("cleanup failure was hidden")
			}
		})
	}
	t.Run("missing lifecycle table", func(t *testing.T) {
		store, principal := openContextTestStore(t)
		defer store.Close()
		if _, err := store.db.Exec(`DROP TABLE worktree_instances`); err != nil {
			t.Fatal(err)
		}
		instance := contextstate.WorktreeInstance{Worktree: "wt-a", ID: "wt_1111111111111111"}
		if _, err := store.DeleteWorktreeSessions(context.Background(), principal, instance); err == nil {
			t.Fatal("missing lifecycle table was hidden")
		}
	})
}

func mustCoverageTrigger(t *testing.T, store *SQLite, statement string) {
	t.Helper()
	if _, err := store.db.Exec(statement); err != nil {
		t.Fatal(err)
	}
}
