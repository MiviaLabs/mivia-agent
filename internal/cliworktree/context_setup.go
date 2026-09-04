package cliworktree

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
	"github.com/MiviaLabs/mivia-agent/internal/vcs"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
	"github.com/MiviaLabs/mivia-agent/internal/worktreeroute"
)

var contextSetupRoutePrincipal = WorktreeRoutePrincipal

var abandonContextWorktreeCreation = func(store *storage.SQLite, principal contextstate.Principal, instance contextstate.WorktreeInstance) error {
	return store.AbandonWorktreeCreation(context.Background(), principal, instance)
}

// OpenRepositoryContextStoreFunc opens the repository-level context store for
// root. Wired by internal/cli's init (see cli.wireCliworktree) rather than
// imported directly: cli's own repository-store resolution
// (repositorySessionStorePath in chat_repository_binding.go) needs worktree
// helpers this package exports (WorktreeRoutePrincipal, ReadWorktreeMarker,
// CanonicalMarkerRoot), so cliworktree importing internal/cli here would
// close an import cycle (cli -> cliworktree -> cli). BLOCKER: this
// indirection is a stopgap, not a design decision - the task briefing for
// this slice assumed internal/composition already imported internal/cli
// (false, verified) and did not anticipate this specific cycle. Flagged for
// follow-up: a real fix likely needs repositorySessionStorePath's
// non-worktree config-resolution logic split out of chat_repository_binding.go
// so cliworktree can depend on it directly without pulling in the rest of
// that file's worktree-session-binding logic.
var OpenRepositoryContextStoreFunc func(root string) (*storage.SQLite, error)

// WorktreeRoutePrincipal implements worktree route principal.
//
// Delegates to internal/worktreeroute: uiadapter must reach route identity
// without importing cliworktree (UI isolation policy), so the shared leaf
// owns the derivation. Its hash must stay byte-identical to internal/cli's
// contextWorkspaceID or previously stored catalog rows strand.
func WorktreeRoutePrincipal(root string) (contextstate.Principal, error) {
	return worktreeroute.Principal(root)
}

// openRepositoryContextStore opens the repository context store via the func
// internal/cli wires at init. See OpenRepositoryContextStoreFunc.
func openRepositoryContextStore(root string) (*storage.SQLite, error) {
	if OpenRepositoryContextStoreFunc == nil {
		return nil, fmt.Errorf("cliworktree: OpenRepositoryContextStoreFunc not wired")
	}
	return OpenRepositoryContextStoreFunc(root)
}

// RegisterWorktreeRoute creates the route shown in /sessions for a worktree.
func RegisterWorktreeRoute(root string, wt *vcs.WorktreeInfo) error {
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
	return RegisterWorktreeRoute(root, wt)
}

func registerWorktreeRouteInStore(store *storage.SQLite, root string, wt *vcs.WorktreeInfo) error {
	if wt == nil {
		return fmt.Errorf("worktree route requires a worktree")
	}
	principal, _ := WorktreeRoutePrincipal(root)
	return store.SaveWorktreeRoute(context.Background(), principal, wt.Name, wt.Path)
}

func registerManagedWorktree(root string, wt *vcs.WorktreeInfo) (contextstate.WorktreeInstance, error) {
	store, err := openRepositoryContextStore(root)
	if err != nil {
		return contextstate.WorktreeInstance{}, err
	}
	defer store.Close()
	return RegisterManagedWorktreeInStore(store, root, wt)
}

func RegisterManagedWorktreeInStore(store *storage.SQLite, root string, wt *vcs.WorktreeInfo) (contextstate.WorktreeInstance, error) {
	if wt == nil {
		return contextstate.WorktreeInstance{}, fmt.Errorf("worktree route requires a worktree")
	}
	instance, err := newManagedWorktreeInstance(wt.Name)
	if err != nil {
		return contextstate.WorktreeInstance{}, err
	}
	principal, err := WorktreeRoutePrincipal(root)
	if err != nil {
		return contextstate.WorktreeInstance{}, err
	}
	canonicalPath, err := CanonicalMarkerRoot(wt.Path)
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
	principal, err := WorktreeRoutePrincipal(root)
	if err != nil {
		return err
	}
	marker, err := ReadWorktreeMarker(wt.Path)
	if err == nil && marker != instance {
		return fmt.Errorf("worktree marker does not match its creation instance")
	}
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err := WriteWorktreeMarker(wt.Path, instance); err != nil {
			return err
		}
	}
	canonicalPath, err := CanonicalMarkerRoot(wt.Path)
	if err != nil {
		return err
	}
	return store.RegisterWorktreeInstance(context.Background(), principal, instance, canonicalPath)
}

// CreateManagedWorktree reserves lifecycle state before it creates Git state.
// A retry completes a retained creation with the same instance ID.
func CreateManagedWorktree(root, name, baseRef, branchPrefix string) (*vcs.WorktreeInfo, error) {
	store, err := openRepositoryContextStore(root)
	if err != nil {
		return nil, err
	}
	defer store.Close()
	return CreateManagedWorktreeInStore(store, root, name, baseRef, branchPrefix)
}

func CreateManagedWorktreeInStore(store *storage.SQLite, root, name, baseRef, branchPrefix string) (*vcs.WorktreeInfo, error) {
	return CreateManagedWorktreeInStoreWithInstance(store, root, name, baseRef, branchPrefix, nil)
}

// CreateManagedWorktreeInStoreWithInstance creates a managed worktree in
// store and records the resulting instance in result. Relocated from
// internal/legacytui/worktree_dialog_create.go: it is pure business logic
// with no TUI dependency, needed unqualified there and here.
func CreateManagedWorktreeInStoreWithInstance(store *storage.SQLite, root, name, baseRef, branchPrefix string, result *contextstate.WorktreeInstance) (*vcs.WorktreeInfo, error) {
	lock, err := LockWorktreeLifecycle(root, name)
	if err != nil {
		return nil, err
	}
	defer lock.Close()
	return CreateManagedWorktreeInStoreLocked(store, root, name, baseRef, branchPrefix, result, lock.File())
}

// CreateManagedWorktreeInStoreLocked implements create managed worktree in store locked.
func CreateManagedWorktreeInStoreLocked(store *storage.SQLite, root, name, baseRef, branchPrefix string, result *contextstate.WorktreeInstance, lease *os.File) (*vcs.WorktreeInfo, error) {
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
				return CreateManagedWorktreeInStoreLocked(store, root, name, baseRef, branchPrefix, result, lease)
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
				return CreateManagedWorktreeInStoreLocked(store, root, name, baseRef, branchPrefix, result, lease)
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
		return RegisterManagedWorktreeInStore(store, root, wt)
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

func BeginManagedWorktreeRemoval(root string, wt *vcs.WorktreeInfo) (contextstate.WorktreeInstance, error) {
	return BeginManagedWorktreeRemovalInStore(nil, root, wt)
}

func BeginManagedWorktreeRemovalForSession(sess *chat.Session, root string, wt *vcs.WorktreeInfo) (contextstate.WorktreeInstance, error) {
	return BeginManagedWorktreeRemovalForSessionExpected(sess, root, wt, contextstate.WorktreeInstance{}, false)
}

// BeginManagedWorktreeRemovalForSessionExpected is relocated from
// internal/legacytui/worktree_dialog.go: it is pure business logic with no
// TUI dependency, needed unqualified there and here.
func BeginManagedWorktreeRemovalForSessionExpected(sess *chat.Session, root string, wt *vcs.WorktreeInfo, expected contextstate.WorktreeInstance, requireExpected bool) (contextstate.WorktreeInstance, error) {
	if store, ok := sess.ContextStore().(*storage.SQLite); ok && store != nil {
		return BeginManagedWorktreeRemovalInStoreExpected(store, root, wt, expected, requireExpected)
	}
	return BeginManagedWorktreeRemovalInStoreExpected(nil, root, wt, expected, requireExpected)
}

func BeginManagedWorktreeRemovalInStore(store *storage.SQLite, root string, wt *vcs.WorktreeInfo) (contextstate.WorktreeInstance, error) {
	return BeginManagedWorktreeRemovalInStoreExpected(store, root, wt, contextstate.WorktreeInstance{}, false)
}

// BeginManagedWorktreeRemovalInStoreExpected implements begin managed worktree removal in store expected.
func BeginManagedWorktreeRemovalInStoreExpected(store *storage.SQLite, root string, wt *vcs.WorktreeInfo, expected contextstate.WorktreeInstance, requireExpected bool) (contextstate.WorktreeInstance, error) {
	if wt == nil {
		return contextstate.WorktreeInstance{}, fmt.Errorf("worktree route requires a worktree")
	}
	instance, err := ReadWorktreeMarker(wt.Path)
	if err != nil {
		if requireExpected {
			// A bound instance must still match its marker. Fail closed so a
			// same-name replacement is never removed through a stale binding.
			return contextstate.WorktreeInstance{}, contextstate.ErrWorktreeDeleted
		}
		// The marker is missing, malformed, or unreadable. The worktree may
		// still occupy HDD space, so removal falls back to the unmanaged path.
		return contextstate.WorktreeInstance{}, ErrUnmanagedWorktree
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
	principal, err := WorktreeRoutePrincipal(root)
	if err != nil {
		return contextstate.WorktreeInstance{}, err
	}
	canonicalPath, err := CanonicalMarkerRoot(wt.Path)
	if err != nil {
		return contextstate.WorktreeInstance{}, err
	}
	if err := store.ValidateActiveWorktreeInstance(context.Background(), principal, instance, canonicalPath); err != nil {
		if errors.Is(err, contextstate.ErrWorktreeDeleted) && !requireExpected {
			// The marker names an instance storage no longer tracks. The
			// physical worktree can still be removed to free HDD space.
			return contextstate.WorktreeInstance{}, ErrUnmanagedWorktree
		}
		return contextstate.WorktreeInstance{}, err
	}
	return instance, store.BeginWorktreeDeletion(context.Background(), principal, instance)
}

// ReactivateManagedWorktree implements reactivate managed worktree.
func ReactivateManagedWorktree(root string, instance contextstate.WorktreeInstance) error {
	return ReactivateManagedWorktreeInStore(nil, root, instance)
}

// ReactivateManagedWorktreeInStore implements reactivate managed worktree in store.
func ReactivateManagedWorktreeInStore(store *storage.SQLite, root string, instance contextstate.WorktreeInstance) error {
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
	canonicalPath, err := CanonicalMarkerRoot(worktree.Path)
	if err != nil || filepath.Clean(canonicalPath) != filepath.Clean(info.CanonicalPath) {
		return contextstate.ErrWorktreeDeleted
	}
	marker, err := ReadWorktreeMarker(worktree.Path)
	if err != nil || marker != instance {
		return contextstate.ErrWorktreeDeleted
	}
	return store.ReactivateWorktreeInstance(context.Background(), principal, instance)
}

// ClassifyMissingWorktreeMarker implements classify missing worktree marker.
func ClassifyMissingWorktreeMarker(store *storage.SQLite, principal contextstate.Principal, worktree, canonicalPath string) (contextstate.WorktreeInstanceInfo, bool, error) {
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

// ValidateExpectedWorktreeInstanceInStore implements validate expected worktree instance in store.
func ValidateExpectedWorktreeInstanceInStore(store *storage.SQLite, root, dir string, expected contextstate.WorktreeInstance) error {
	if expected.IsZero() {
		return nil
	}
	worktree, err := vcs.Resolve(context.Background(), root, expected.Worktree)
	if err != nil || worktree == nil {
		return contextstate.ErrWorktreeDeleted
	}
	marker, canonicalRoot, err := ReadMarkerAtCanonicalRoot(worktree.Path)
	if err != nil || marker != expected {
		return contextstate.ErrWorktreeDeleted
	}
	canonicalDir, err := CanonicalMarkerRoot(dir)
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

func ReadMarkerAtCanonicalRoot(root string) (contextstate.WorktreeInstance, string, error) {
	canonicalRoot, err := CanonicalMarkerRoot(root)
	if err != nil {
		return contextstate.WorktreeInstance{}, "", err
	}
	marker, err := ReadWorktreeMarker(canonicalRoot)
	return marker, canonicalRoot, err
}

// RemoveWorktreeRoute removes the route after Git has removed its worktree.
func RemoveWorktreeRoute(root, name string) error {
	store, err := openRepositoryContextStore(root)
	if err != nil {
		return err
	}
	defer store.Close()
	return removeWorktreeRouteInStore(store, root, name)
}

func RemoveWorktreeRouteForSession(sess *chat.Session, root, name string) error {
	if store, ok := sess.ContextStore().(*storage.SQLite); ok && store != nil {
		return removeWorktreeRouteInStore(store, root, name)
	}
	return RemoveWorktreeRoute(root, name)
}

func removeWorktreeRouteInStore(store *storage.SQLite, root, name string) error {
	// WorktreeRoutePrincipal uses fixed valid identity fields and a fixed-size
	// workspace digest. It cannot fail for a caller-supplied root.
	principal, _ := WorktreeRoutePrincipal(root)
	_, err := store.DeleteWorktreeRoute(context.Background(), principal, name)
	return err
}
