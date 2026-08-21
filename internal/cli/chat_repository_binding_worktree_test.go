package cli

// Split out of internal/cliworktree/worktree_command_test.go during the
// cliworktree extraction: these tests exercise bindManagedWorktreeSession
// (chat_repository_binding.go), which stays in internal/cli, even though
// they need cliworktree's worktree-creation helpers to set up fixtures.

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/cliworktree"
	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/vcs"
	_ "modernc.org/sqlite"
)

func TestBindManagedWorktreeSessionRejectsMismatchedCatalogPath(t *testing.T) {
	repoRoot := newWorktreeCommandRepo(t)
	writeWorktreeStoreConfig(t, repoRoot, "repository.db")
	worktree, err := cliworktree.CreateManagedWorktree(repoRoot, "binding-path", "HEAD", "mivia/")
	if err != nil {
		t.Fatal(err)
	}
	store, err := OpenRepositoryContextStore(repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	principal, err := cliworktree.WorktreeRoutePrincipal(repoRoot)
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	otherPath := t.TempDir()
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(repoRoot, "repository.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE worktree_instances SET canonical_path=? WHERE workspace_id=? AND worktree=? AND state='active'`, otherPath, principal.WorkspaceID, worktree.Name); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	sess := newTestSessionForModel("test")
	if err := bindManagedWorktreeSession(sess, repoRoot, worktree.Path, filepath.Join(repoRoot, "repository.db")); !errors.Is(err, contextstate.ErrWorktreeDeleted) {
		t.Fatalf("bindManagedWorktreeSession = %v, want ErrWorktreeDeleted", err)
	}
}

func TestBindManagedWorktreeSessionLeavesUnmanagedWorktreeUnbound(t *testing.T) {
	repoRoot := newWorktreeCommandRepo(t)
	worktree, err := vcs.Create(context.Background(), repoRoot, "manual", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	sess := newTestSessionForModel("test")
	if err := bindManagedWorktreeSession(sess, repoRoot, worktree.Path, filepath.Join(repoRoot, "repository.db")); err != nil {
		t.Fatalf("bind unmanaged worktree: %v", err)
	}
}

func TestBindManagedWorktreeSessionRejectsActiveInstanceWithoutMarker(t *testing.T) {
	repoRoot := newWorktreeCommandRepo(t)
	writeWorktreeStoreConfig(t, repoRoot, "repository.db")
	worktree, err := cliworktree.CreateManagedWorktree(repoRoot, "missing-marker", "HEAD", "mivia/")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(cliworktree.WorktreeMarkerPath(worktree.Path)); err != nil {
		t.Fatal(err)
	}
	sess := newTestSessionForModel("test")
	err = bindManagedWorktreeSession(sess, repoRoot, worktree.Path, filepath.Join(repoRoot, "repository.db"))
	if !errors.Is(err, contextstate.ErrWorktreeDeleted) {
		t.Fatalf("bind active worktree without marker = %v, want ErrWorktreeDeleted", err)
	}
}
