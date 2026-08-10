package storage

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
)

func TestGenericCatalogAPIsRejectManagedStorageKey(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(context.Context, *SQLite, contextstate.Principal, string) error
	}{
		{"save snapshot", func(ctx context.Context, store *SQLite, principal contextstate.Principal, key string) error {
			return store.SaveSession(ctx, principal, key, []byte(`[{"unbound":true}]`), "other", "other", 9, 9, 1, contextstate.SessionSaveOptions{Dir: "/unbound"})
		}},
		{"load snapshot", func(ctx context.Context, store *SQLite, principal contextstate.Principal, key string) error {
			_, _, err := store.LoadSession(ctx, principal, key)
			return err
		}},
		{"delete snapshot", func(ctx context.Context, store *SQLite, principal contextstate.Principal, key string) error {
			return store.DeleteSessionSnapshot(ctx, principal, key)
		}},
		{"prune snapshot", func(ctx context.Context, store *SQLite, principal contextstate.Principal, key string) error {
			return store.PruneSessionSnapshots(ctx, principal, []string{key})
		}},
		{"save admission", func(ctx context.Context, store *SQLite, principal contextstate.Principal, key string) error {
			return store.SaveSessionAdmission(ctx, principal, key, contextstate.SessionAdmission{Agent: "other", Digest: "other", Names: []string{"other"}})
		}},
		{"delete admission", func(ctx context.Context, store *SQLite, principal contextstate.Principal, key string) error {
			return store.SaveSessionAdmission(ctx, principal, key, contextstate.SessionAdmission{})
		}},
		{"load admission", func(ctx context.Context, store *SQLite, principal contextstate.Principal, key string) error {
			_, err := store.LoadSessionAdmission(ctx, principal, key)
			return err
		}},
		{"ensure directory", func(ctx context.Context, store *SQLite, principal contextstate.Principal, key string) error {
			unbound, err := contextstate.NewPrincipal(principal.WorkspaceID, key, principal.SubjectID)
			if err != nil {
				return err
			}
			return store.EnsureSession(ctx, contextstate.EnsureSessionRequest{Principal: unbound, Binding: mustBinding(t), Dir: "/unbound"})
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store, principal, instance, key := seedManagedCatalogKey(t)
			defer store.Close()
			if err := test.mutate(context.Background(), store, principal, key); !errors.Is(err, contextstate.ErrWorktreeDeleted) {
				t.Fatalf("generic operation error = %v, want ErrWorktreeDeleted", err)
			}
			assertManagedCatalogKeyState(t, store, principal, instance, key)
		})
	}
}

func seedManagedCatalogKey(t *testing.T) (*SQLite, contextstate.Principal, contextstate.WorktreeInstance, string) {
	t.Helper()
	store, err := OpenSQLite(filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	principal, err := contextstate.NewPrincipal("workspace", "session", "subject")
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	instance := contextstate.WorktreeInstance{Worktree: "wt-a", ID: "wt_1111111111111111"}
	worktreeDir := filepath.Join(t.TempDir(), "worktrees", instance.Worktree)
	if err := registerCleanupInstance(context.Background(), store, principal, instance, worktreeDir); err != nil {
		store.Close()
		t.Fatal(err)
	}
	if err := store.SaveSession(context.Background(), principal, "managed", []byte(`[{"managed":true}]`), "model", "provider", 1, 2, 1, contextstate.SessionSaveOptions{Dir: "/managed", Worktree: instance.Worktree, WorktreeInstance: instance}); err != nil {
		store.Close()
		t.Fatal(err)
	}
	if err := store.SaveWorktreeSessionAdmission(context.Background(), principal, "managed", contextstate.SessionAdmission{Agent: "managed", Digest: "managed", Names: []string{"managed"}}, instance); err != nil {
		store.Close()
		t.Fatal(err)
	}
	var key string
	if err := store.db.QueryRow(`SELECT storage_key FROM worktree_catalog_keys WHERE workspace_id=? AND subject_id=? AND instance_id=? AND entity='snapshot' AND name='managed'`, principal.WorkspaceID, principal.SubjectID, instance.ID).Scan(&key); err != nil {
		store.Close()
		t.Fatal(err)
	}
	return store, principal, instance, key
}

func assertManagedCatalogKeyState(t *testing.T, store *SQLite, principal contextstate.Principal, instance contextstate.WorktreeInstance, key string) {
	t.Helper()
	payload, info, err := store.LoadWorktreeSession(context.Background(), principal, "managed", instance)
	if err != nil {
		t.Fatalf("load managed snapshot: %v", err)
	}
	if string(payload) != `[{"managed":true}]` || info.Dir != "/managed" || info.WorktreeInstance != instance {
		t.Fatalf("managed snapshot changed: %s, %+v", payload, info)
	}
	admission, err := store.LoadWorktreeSessionAdmission(context.Background(), principal, "managed", instance)
	if err != nil || admission.Agent != "managed" || len(admission.Names) != 1 || admission.Names[0] != "managed" {
		t.Fatalf("managed admission changed: %+v, %v", admission, err)
	}
	var snapshotInstance, dirInstance, admissionInstance string
	if err := store.db.QueryRow(`SELECT instance_id FROM chat_sessions WHERE workspace_id=? AND subject_id=? AND name=?`, principal.WorkspaceID, principal.SubjectID, key).Scan(&snapshotInstance); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT instance_id FROM chat_session_dirs WHERE workspace_id=? AND subject_id=? AND name=?`, principal.WorkspaceID, principal.SubjectID, key).Scan(&dirInstance); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT instance_id FROM chat_session_admissions WHERE workspace_id=? AND subject_id=? AND name=?`, principal.WorkspaceID, principal.SubjectID, key).Scan(&admissionInstance); err != nil {
		t.Fatal(err)
	}
	if snapshotInstance != instance.ID || dirInstance != instance.ID || admissionInstance != instance.ID {
		t.Fatalf("managed instance IDs changed: snapshot=%q dir=%q admission=%q", snapshotInstance, dirInstance, admissionInstance)
	}
}
