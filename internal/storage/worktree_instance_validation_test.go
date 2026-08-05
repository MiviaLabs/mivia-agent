package storage

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
)

func TestWorktreeCatalogRejectsPathBelowSymlink(t *testing.T) {
	store, err := OpenSQLite(filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	principal, err := contextstate.NewPrincipal("workspace", "session", "subject")
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(link, "new-worktree")
	instance := contextstate.WorktreeInstance{Worktree: "wt-a", ID: "wt_1234567890abcdef"}
	if err := store.BeginWorktreeCreation(context.Background(), principal, instance, path); !errors.Is(err, contextstate.ErrInvalidDTO) {
		t.Fatalf("BeginWorktreeCreation(%q) = %v", path, err)
	}
}

func TestWorktreeCatalogRejectsPathBelowDanglingSymlink(t *testing.T) {
	store, err := OpenSQLite(filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	principal, err := contextstate.NewPrincipal("workspace", "session", "subject")
	if err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(filepath.Join(t.TempDir(), "missing-target"), link); err != nil {
		t.Fatal(err)
	}
	instance := contextstate.WorktreeInstance{Worktree: "wt-a", ID: "wt_1234567890abcdef"}
	path := filepath.Join(link, "new-worktree")
	if err := store.BeginWorktreeCreation(context.Background(), principal, instance, path); !errors.Is(err, contextstate.ErrInvalidDTO) {
		t.Fatalf("BeginWorktreeCreation(%q) = %v", path, err)
	}
}

func TestRegisterAdoptedWorktreeInstanceFailsWhenLegacyRouteChanges(t *testing.T) {
	store, err := OpenSQLite(filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	principal, err := contextstate.NewPrincipal("workspace", "session", "subject")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "wt-a")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveWorktreeRoute(context.Background(), principal, "wt-a", path); err != nil {
		t.Fatal(err)
	}
	instance := contextstate.WorktreeInstance{Worktree: "wt-a", ID: "wt_1234567890abcdef"}
	if err := store.BeginWorktreeAdoption(context.Background(), principal, instance, path); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteWorktreeRoute(context.Background(), principal, instance.Worktree); err != nil {
		t.Fatal(err)
	}
	if err := store.RegisterAdoptedWorktreeInstance(context.Background(), principal, instance, path); !errors.Is(err, contextstate.ErrWorktreeDeleted) {
		t.Fatalf("RegisterAdoptedWorktreeInstance = %v", err)
	}
	var state string
	if err := store.db.QueryRow(`SELECT state FROM worktree_instances WHERE workspace_id=? AND worktree=? AND instance_id=?`, principal.WorkspaceID, instance.Worktree, instance.ID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != string(contextstate.WorktreeCreating) {
		t.Fatalf("adoption state = %q", state)
	}
}
