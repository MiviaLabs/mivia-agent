package cli

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
	"github.com/MiviaLabs/mivia-agent/internal/vcs"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

var contextSetupRoutePrincipal = worktreeRoutePrincipal

var abandonContextWorktreeCreation = func(store *storage.SQLite, principal contextstate.Principal, instance contextstate.WorktreeInstance) error {
	return store.AbandonWorktreeCreation(context.Background(), principal, instance)
}

func contextStorePath(root string, cfg config.SubagentConfig) string {
	if cfg.StoreBackend == "sqlite" && cfg.StorePath != "" {
		return cfg.StorePath
	}
	return workspace.ContextStorePath(root)
}

func openContextStore(root string, cfg config.SubagentConfig) (*storage.SQLite, error) {
	return openContextStorePath(contextStorePath(root, cfg))
}

func openContextStorePath(path string) (*storage.SQLite, error) {
	store, err := storage.OpenSQLite(path)
	if err != nil {
		return nil, fmt.Errorf("open context store %q: %w", path, err)
	}
	return store, nil
}

func worktreeRoutePrincipal(root string) (contextstate.Principal, error) {
	return contextstate.NewPrincipal(contextWorkspaceID(root), "worktree-routes", "local-user")
}

// registerWorktreeRoute creates the route shown in /sessions for a worktree.
func registerWorktreeRoute(root string, wt *vcs.WorktreeInfo) error {
	if wt == nil {
		return fmt.Errorf("worktree route requires a worktree")
	}
	store, err := openRepositoryContextStore(root)
	if err != nil {
		return err
	}
	defer store.Close()
	return registerWorktreeRouteInStore(store, root, wt)
}

func registerWorktreeRouteForSession(sess *chat.Session, root string, wt *vcs.WorktreeInfo) error {
	if store, ok := sess.ContextStore().(*storage.SQLite); ok && store != nil {
		return registerWorktreeRouteInStore(store, root, wt)
	}
	return registerWorktreeRoute(root, wt)
}

func registerWorktreeRouteInStore(store *storage.SQLite, root string, wt *vcs.WorktreeInfo) error {
	if wt == nil {
		return fmt.Errorf("worktree route requires a worktree")
	}
	principal, _ := worktreeRoutePrincipal(root)
	return store.SaveWorktreeRoute(context.Background(), principal, wt.Name, wt.Path)
}

func registerManagedWorktree(root string, wt *vcs.WorktreeInfo) (contextstate.WorktreeInstance, error) {
	store, err := openRepositoryContextStore(root)
	if err != nil {
		return contextstate.WorktreeInstance{}, err
	}
	defer store.Close()
	return registerManagedWorktreeInStore(store, root, wt)
}

func registerManagedWorktreeInStore(store *storage.SQLite, root string, wt *vcs.WorktreeInfo) (contextstate.WorktreeInstance, error) {
	if wt == nil {
		return contextstate.WorktreeInstance{}, fmt.Errorf("worktree route requires a worktree")
	}
	instance, err := newManagedWorktreeInstance(wt.Name)
	if err != nil {
		return contextstate.WorktreeInstance{}, err
	}
	principal, err := worktreeRoutePrincipal(root)
	if err != nil {
		return contextstate.WorktreeInstance{}, err
	}
	canonicalPath, err := canonicalMarkerRoot(wt.Path)
	if err != nil {
		return contextstate.WorktreeInstance{}, err
	}
	if err := store.BeginWorktreeCreation(context.Background(), principal, instance, canonicalPath); err != nil {
		return contextstate.WorktreeInstance{}, err
	}
	if err := completeManagedWorktreeCreationInStore(store, root, wt, instance); err != nil {
		return contextstate.WorktreeInstance{}, err
	}
	return instance, nil
}

func completeManagedWorktreeCreationInStore(store *storage.SQLite, root string, wt *vcs.WorktreeInfo, instance contextstate.WorktreeInstance) error {
	if wt == nil || wt.Name != instance.Worktree {
		return fmt.Errorf("worktree creation does not match its instance")
	}
	principal, err := worktreeRoutePrincipal(root)
	if err != nil {
		return err
	}
	marker, err := readWorktreeMarker(wt.Path)
	if err == nil && marker != instance {
		return fmt.Errorf("worktree marker does not match its creation instance")
	}
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err := writeWorktreeMarker(wt.Path, instance); err != nil {
			return err
		}
	}
	canonicalPath, err := canonicalMarkerRoot(wt.Path)
	if err != nil {
		return err
	}
	return store.RegisterWorktreeInstance(context.Background(), principal, instance, canonicalPath)
}

// createManagedWorktree reserves lifecycle state before it creates Git state.
// A retry completes a retained creation with the same instance ID.
func createManagedWorktree(root, name, baseRef, branchPrefix string) (*vcs.WorktreeInfo, error) {
	store, err := openRepositoryContextStore(root)
	if err != nil {
		return nil, err
	}
	defer store.Close()
	return createManagedWorktreeInStore(store, root, name, baseRef, branchPrefix)
}

func createManagedWorktreeInStore(store *storage.SQLite, root, name, baseRef, branchPrefix string) (*vcs.WorktreeInfo, error) {
	return createManagedWorktreeInStoreWithInstance(store, root, name, baseRef, branchPrefix, nil)
}

func createManagedWorktreeWithInstance(root, name, baseRef, branchPrefix string) (*vcs.WorktreeInfo, contextstate.WorktreeInstance, error) {
	store, err := openRepositoryContextStore(root)
	if err != nil {
		return nil, contextstate.WorktreeInstance{}, err
	}
	defer store.Close()
	var instance contextstate.WorktreeInstance
	worktree, err := createManagedWorktreeInStoreWithInstance(store, root, name, baseRef, branchPrefix, &instance)
	return worktree, instance, err
}

func createManagedWorktreeInStoreWithInstance(store *storage.SQLite, root, name, baseRef, branchPrefix string, result *contextstate.WorktreeInstance) (*vcs.WorktreeInfo, error) {
	lock, err := lockWorktreeLifecycle(root, name)
	if err != nil {
		return nil, err
	}
	defer lock.Close()
	return createManagedWorktreeInStoreLocked(store, root, name, baseRef, branchPrefix, result, lock.File())
}

func createManagedWorktreeInStoreLocked(store *storage.SQLite, root, name, baseRef, branchPrefix string, result *contextstate.WorktreeInstance, lease *os.File) (*vcs.WorktreeInfo, error) {
	principal, err := contextSetupRoutePrincipal(root)
	if err != nil {
		return nil, err
	}
	sanitised, err := vcs.SanitizeName(name)
	if err != nil {
		return nil, err
	}
	expectedPath := filepath.Join(workspace.WorktreesDir(root), sanitised)
	instance, err := newManagedWorktreeInstance(sanitised)
	if err != nil {
		return nil, err
	}
	if err := store.BeginWorktreeCreation(context.Background(), principal, instance, expectedPath); err != nil {
		creating, findErr := store.CreatingWorktreeInstance(context.Background(), principal, sanitised)
		if findErr != nil {
			deleting, deleteErr := store.DeletingWorktreeInstance(context.Background(), principal, sanitised)
			if deleteErr == nil {
				live, resolveErr := vcs.Resolve(context.Background(), root, sanitised)
				if resolveErr != nil {
					return nil, resolveErr
				}
				if live != nil {
					return nil, fmt.Errorf("worktree %q requires removal recovery", sanitised)
				}
				if _, cleanupErr := store.DeleteWorktreeSessions(context.Background(), principal, deleting); cleanupErr != nil {
					return nil, cleanupErr
				}
				return createManagedWorktreeInStoreLocked(store, root, name, baseRef, branchPrefix, result, lease)
			}
			return nil, err
		}
		if filepath.Clean(creating.CanonicalPath) != filepath.Clean(expectedPath) {
			return nil, err
		}
		worktree, resolveErr := vcs.Resolve(context.Background(), root, sanitised)
		if resolveErr != nil {
			return nil, resolveErr
		}
		if worktree == nil || filepath.Clean(worktree.Path) != filepath.Clean(expectedPath) {
			if worktree == nil {
				if err := abandonContextWorktreeCreation(store, principal, creating.Instance); err != nil {
					return nil, err
				}
				return createManagedWorktreeInStoreLocked(store, root, name, baseRef, branchPrefix, result, lease)
			}
			return nil, fmt.Errorf("worktree creation recovery requires Git worktree %q", expectedPath)
		}
		if err := completeManagedWorktreeCreationInStore(store, root, worktree, creating.Instance); err != nil {
			return nil, err
		}
		setManagedWorktreeInstanceResult(result, creating.Instance)
		return worktree, nil
	}
	worktree, err := vcs.CreateWithPrefixLease(context.Background(), root, sanitised, baseRef, branchPrefix, lease)
	if err != nil {
		if abandonErr := abandonContextWorktreeCreation(store, principal, instance); abandonErr != nil {
			return nil, fmt.Errorf("%w; clear creation reservation: %v", err, abandonErr)
		}
		return nil, err
	}
	if filepath.Clean(worktree.Path) != filepath.Clean(expectedPath) {
		return nil, fmt.Errorf("created worktree path does not match its reserved path")
	}
	if err := completeManagedWorktreeCreationInStore(store, root, worktree, instance); err != nil {
		return nil, err
	}
	setManagedWorktreeInstanceResult(result, instance)
	return worktree, nil
}

func setManagedWorktreeInstanceResult(result *contextstate.WorktreeInstance, instance contextstate.WorktreeInstance) {
	if result != nil {
		*result = instance
	}
}

func registerManagedWorktreeForSession(sess *chat.Session, root string, wt *vcs.WorktreeInfo) (contextstate.WorktreeInstance, error) {
	if store, ok := sess.ContextStore().(*storage.SQLite); ok && store != nil {
		return registerManagedWorktreeInStore(store, root, wt)
	}
	return registerManagedWorktree(root, wt)
}

func newManagedWorktreeInstance(name string) (contextstate.WorktreeInstance, error) {
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return contextstate.WorktreeInstance{}, fmt.Errorf("generate worktree instance ID: %w", err)
	}
	instance := contextstate.WorktreeInstance{Worktree: name, ID: "wt_" + hex.EncodeToString(random[:])}
	return instance, instance.Validate()
}

func beginManagedWorktreeRemoval(root string, wt *vcs.WorktreeInfo) (contextstate.WorktreeInstance, error) {
	return beginManagedWorktreeRemovalInStore(nil, root, wt)
}

func beginManagedWorktreeRemovalForSession(sess *chat.Session, root string, wt *vcs.WorktreeInfo) (contextstate.WorktreeInstance, error) {
	return beginManagedWorktreeRemovalForSessionExpected(sess, root, wt, contextstate.WorktreeInstance{}, false)
}

func beginManagedWorktreeRemovalForSessionExpected(sess *chat.Session, root string, wt *vcs.WorktreeInfo, expected contextstate.WorktreeInstance, requireExpected bool) (contextstate.WorktreeInstance, error) {
	if store, ok := sess.ContextStore().(*storage.SQLite); ok && store != nil {
		return beginManagedWorktreeRemovalInStoreExpected(store, root, wt, expected, requireExpected)
	}
	return beginManagedWorktreeRemovalInStoreExpected(nil, root, wt, expected, requireExpected)
}

func beginManagedWorktreeRemovalInStore(store *storage.SQLite, root string, wt *vcs.WorktreeInfo) (contextstate.WorktreeInstance, error) {
	return beginManagedWorktreeRemovalInStoreExpected(store, root, wt, contextstate.WorktreeInstance{}, false)
}

func beginManagedWorktreeRemovalInStoreExpected(store *storage.SQLite, root string, wt *vcs.WorktreeInfo, expected contextstate.WorktreeInstance, requireExpected bool) (contextstate.WorktreeInstance, error) {
	if wt == nil {
		return contextstate.WorktreeInstance{}, fmt.Errorf("worktree route requires a worktree")
	}
	instance, err := readWorktreeMarker(wt.Path)
	if err != nil {
		return contextstate.WorktreeInstance{}, err
	}
	if instance.Worktree != wt.Name {
		return contextstate.WorktreeInstance{}, fmt.Errorf("worktree marker name does not match")
	}
	if requireExpected && instance != expected {
		return contextstate.WorktreeInstance{}, contextstate.ErrWorktreeDeleted
	}
	ownedStore := false
	if store == nil {
		var err error
		store, err = openRepositoryContextStore(root)
		if err != nil {
			return contextstate.WorktreeInstance{}, err
		}
		ownedStore = true
	}
	if ownedStore {
		defer store.Close()
	}
	principal, err := worktreeRoutePrincipal(root)
	if err != nil {
		return contextstate.WorktreeInstance{}, err
	}
	canonicalPath, err := canonicalMarkerRoot(wt.Path)
	if err != nil {
		return contextstate.WorktreeInstance{}, err
	}
	if err := store.ValidateActiveWorktreeInstance(context.Background(), principal, instance, canonicalPath); err != nil {
		return contextstate.WorktreeInstance{}, err
	}
	return instance, store.BeginWorktreeDeletion(context.Background(), principal, instance)
}

func finishManagedWorktreeRemoval(root string, instance contextstate.WorktreeInstance) error {
	return finishManagedWorktreeRemovalInStore(nil, root, instance)
}

func finishManagedWorktreeRemovalForSession(sess *chat.Session, root string, instance contextstate.WorktreeInstance) error {
	if store, ok := sess.ContextStore().(*storage.SQLite); ok && store != nil {
		return finishManagedWorktreeRemovalInStore(store, root, instance)
	}
	return finishManagedWorktreeRemoval(root, instance)
}

func finishManagedWorktreeRemovalInStore(store *storage.SQLite, root string, instance contextstate.WorktreeInstance) error {
	ownedStore := false
	if store == nil {
		var err error
		store, err = openRepositoryContextStore(root)
		if err != nil {
			return err
		}
		ownedStore = true
	}
	if ownedStore {
		defer store.Close()
	}
	principal, err := worktreeRoutePrincipal(root)
	if err != nil {
		return err
	}
	_, err = store.DeleteWorktreeSessions(context.Background(), principal, instance)
	return err
}

func reactivateManagedWorktree(root string, instance contextstate.WorktreeInstance) error {
	return reactivateManagedWorktreeInStore(nil, root, instance)
}

func reactivateManagedWorktreeForSession(sess *chat.Session, root string, instance contextstate.WorktreeInstance) error {
	if store, ok := sess.ContextStore().(*storage.SQLite); ok && store != nil {
		return reactivateManagedWorktreeInStore(store, root, instance)
	}
	return reactivateManagedWorktree(root, instance)
}

func reactivateManagedWorktreeInStore(store *storage.SQLite, root string, instance contextstate.WorktreeInstance) error {
	ownedStore := false
	if store == nil {
		var err error
		store, err = openRepositoryContextStore(root)
		if err != nil {
			return err
		}
		ownedStore = true
	}
	if ownedStore {
		defer store.Close()
	}
	principal, err := contextSetupRoutePrincipal(root)
	if err != nil {
		return err
	}
	info, err := store.LiveWorktreeInstance(context.Background(), principal, instance.Worktree)
	if err != nil {
		return err
	}
	if info.Instance != instance || info.State != contextstate.WorktreeDeleting {
		return contextstate.ErrWorktreeDeleted
	}
	worktree, err := vcs.Resolve(context.Background(), root, instance.Worktree)
	if err != nil {
		return err
	}
	if worktree == nil {
		return contextstate.ErrWorktreeDeleted
	}
	canonicalPath, err := canonicalMarkerRoot(worktree.Path)
	if err != nil || filepath.Clean(canonicalPath) != filepath.Clean(info.CanonicalPath) {
		return contextstate.ErrWorktreeDeleted
	}
	marker, err := readWorktreeMarker(worktree.Path)
	if err != nil || marker != instance {
		return contextstate.ErrWorktreeDeleted
	}
	return store.ReactivateWorktreeInstance(context.Background(), principal, instance)
}

func classifyMissingWorktreeMarker(store *storage.SQLite, principal contextstate.Principal, worktree, canonicalPath string) (contextstate.WorktreeInstanceInfo, bool, error) {
	info, err := store.LiveWorktreeInstance(context.Background(), principal, worktree)
	if err == nil {
		return info, false, nil
	}
	if !errors.Is(err, contextstate.ErrWorktreeDeleted) {
		return contextstate.WorktreeInstanceInfo{}, false, err
	}
	err = store.RequireLegacyWorktreeRoute(context.Background(), principal, worktree, canonicalPath)
	if err == nil {
		return contextstate.WorktreeInstanceInfo{}, true, nil
	}
	if !errors.Is(err, contextstate.ErrWorktreeDeleted) {
		return contextstate.WorktreeInstanceInfo{}, false, err
	}
	return contextstate.WorktreeInstanceInfo{}, false, nil
}

func validateExpectedWorktreeInstanceInStore(store *storage.SQLite, root, dir string, expected contextstate.WorktreeInstance) error {
	if expected.IsZero() {
		return nil
	}
	worktree, err := vcs.Resolve(context.Background(), root, expected.Worktree)
	if err != nil || worktree == nil {
		return contextstate.ErrWorktreeDeleted
	}
	marker, canonicalRoot, err := readMarkerAtCanonicalRoot(worktree.Path)
	if err != nil || marker != expected {
		return contextstate.ErrWorktreeDeleted
	}
	canonicalDir, err := canonicalMarkerRoot(dir)
	if err != nil {
		return contextstate.ErrWorktreeDeleted
	}
	rel, err := filepath.Rel(canonicalRoot, canonicalDir)
	if err != nil || rel == ".." || filepath.IsAbs(rel) || len(rel) > 3 && rel[:3] == ".."+string(filepath.Separator) {
		return contextstate.ErrWorktreeDeleted
	}
	principal, err := contextSetupRoutePrincipal(root)
	if err != nil {
		return err
	}
	return store.ValidateActiveWorktreeInstance(context.Background(), principal, expected, canonicalRoot)
}

func readMarkerAtCanonicalRoot(root string) (contextstate.WorktreeInstance, string, error) {
	canonicalRoot, err := canonicalMarkerRoot(root)
	if err != nil {
		return contextstate.WorktreeInstance{}, "", err
	}
	marker, err := readWorktreeMarker(canonicalRoot)
	return marker, canonicalRoot, err
}

// removeWorktreeRoute removes the route after Git has removed its worktree.
func removeWorktreeRoute(root, name string) error {
	store, err := openRepositoryContextStore(root)
	if err != nil {
		return err
	}
	defer store.Close()
	return removeWorktreeRouteInStore(store, root, name)
}

func removeWorktreeRouteForSession(sess *chat.Session, root, name string) error {
	if store, ok := sess.ContextStore().(*storage.SQLite); ok && store != nil {
		return removeWorktreeRouteInStore(store, root, name)
	}
	return removeWorktreeRoute(root, name)
}

func removeWorktreeRouteInStore(store *storage.SQLite, root, name string) error {
	// worktreeRoutePrincipal uses fixed valid identity fields and a fixed-size
	// workspace digest. It cannot fail for a caller-supplied root.
	principal, _ := worktreeRoutePrincipal(root)
	return store.DeleteWorktreeRoute(context.Background(), principal, name)
}
