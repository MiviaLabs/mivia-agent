package cliworktree

// Package-local test-helper copies. internal/cli defines the originals
// (session_test_helpers_test.go, context_setup_coverage_test.go,
// legacytui_split_helpers_test.go, toml_helpers_test.go) for its own
// still-in-cli tests; a cliworktree test file cannot import those (they are
// unexported symbols in _test.go files of another package - Go does not
// allow cross-package _test.go imports at all, cycle or not). Duplicated
// here rather than shared, same pattern the codebase already uses for
// internal/legacytui's package-local copies (see cli's
// session_test_helpers_test.go doc comments).

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
	"github.com/MiviaLabs/mivia-agent/internal/vcs"
)

// newTestSessionForModel is a package-local copy of internal/cli's helper of
// the same name (session_test_helpers_test.go).
func newTestSessionForModel(model string) *chat.Session {
	return chat.NewSession(&config.Resolved{Model: model}, nil)
}

// tomlPathLiteral is a package-local copy of internal/cli's helper of the
// same name (toml_helpers_test.go).
func tomlPathLiteral(path string) string {
	return strings.ReplaceAll(path, `\`, `\\`)
}

// createManagedWorktreeWithInstance is a package-local copy of internal/cli's
// helper of the same name (session_test_helpers_test.go), itself a copy of
// internal/legacytui's worktree-dialog creation flow helper.
func createManagedWorktreeWithInstance(root, name, baseRef, branchPrefix string) (*vcs.WorktreeInfo, contextstate.WorktreeInstance, error) {
	store, err := openRepositoryContextStore(root)
	if err != nil {
		return nil, contextstate.WorktreeInstance{}, err
	}
	defer store.Close()
	var instance contextstate.WorktreeInstance
	worktree, err := CreateManagedWorktreeInStoreWithInstance(store, root, name, baseRef, branchPrefix, &instance)
	return worktree, instance, err
}

// recoverManagedWorktreeRemovalInfoInStore is a package-local copy of
// internal/cli's helper of the same name (session_test_helpers_test.go).
func recoverManagedWorktreeRemovalInfoInStore(store *storage.SQLite, root string, info contextstate.WorktreeInstanceInfo, branchPrefix string) error {
	lock, err := LockWorktreeLifecycle(root, info.Instance.Worktree)
	if err != nil {
		return err
	}
	defer lock.Close()
	return RecoverManagedWorktreeRemovalInfoInStoreLocked(store, root, info, branchPrefix, lock.File())
}

// reactivateManagedWorktreeForSession is a package-local copy of internal/cli's
// helper of the same name (session_test_helpers_test.go).
func reactivateManagedWorktreeForSession(sess *chat.Session, root string, instance contextstate.WorktreeInstance) error {
	if store, ok := sess.ContextStore().(*storage.SQLite); ok && store != nil {
		return ReactivateManagedWorktreeInStore(store, root, instance)
	}
	return ReactivateManagedWorktree(root, instance)
}

// blockedContextRoot is a package-local copy of internal/cli's helper of the
// same name (context_setup_coverage_test.go).
func blockedContextRoot(t *testing.T) string {
	t.Helper()
	root := newWorktreeCommandRepo(t)
	blocker := filepath.Join(root, "blocked")
	if err := os.WriteFile(blocker, []byte("blocked"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeWorktreeStoreConfig(t, root, filepath.Join(blocker, "context.db"))
	return root
}

// assertManagedWorktreeActive is a package-local copy of internal/cli's
// helper of the same name (legacytui_split_helpers_test.go), documented
// there as belonging to internal/legacytui's worktree_picker_instance_test.go
// - cliworktree's own worktree_lifecycle_lock_test.go and
// worktree_lifecycle_orphan_test.go need the same assertion.
func assertManagedWorktreeActive(t *testing.T, repoRoot string, worktree *vcs.WorktreeInfo) {
	t.Helper()
	resolved, err := vcs.Resolve(context.Background(), repoRoot, worktree.Name)
	if err != nil || resolved == nil {
		t.Fatalf("replacement worktree = %+v, %v", resolved, err)
	}
	instance, err := ReadWorktreeMarker(worktree.Path)
	if err != nil {
		t.Fatal(err)
	}
	store, err := openRepositoryContextStore(repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	principal, err := WorktreeRoutePrincipal(repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ValidateActiveWorktreeInstance(context.Background(), principal, instance, worktree.Path); err != nil {
		t.Fatalf("replacement is not active: %v", err)
	}
}
