package cli

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/contextmgr"
	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
	"github.com/MiviaLabs/mivia-agent/internal/vcs"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

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
	principal, err := worktreeRoutePrincipal(root)
	if err != nil {
		return err
	}
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
	if err := store.BeginWorktreeCreation(context.Background(), principal, instance, wt.Path); err != nil {
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
	return store.RegisterWorktreeInstance(context.Background(), principal, instance, wt.Path)
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
	principal, err := worktreeRoutePrincipal(root)
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
		if findErr != nil || filepath.Clean(creating.CanonicalPath) != filepath.Clean(expectedPath) {
			return nil, err
		}
		worktree, resolveErr := vcs.Resolve(context.Background(), root, sanitised)
		if resolveErr != nil {
			return nil, resolveErr
		}
		if worktree == nil || filepath.Clean(worktree.Path) != filepath.Clean(expectedPath) {
			if worktree == nil {
				if err := store.AbandonWorktreeCreation(context.Background(), principal, creating.Instance); err != nil {
					return nil, err
				}
				return createManagedWorktreeInStore(store, root, name, baseRef, branchPrefix)
			}
			return nil, fmt.Errorf("worktree creation recovery requires Git worktree %q", expectedPath)
		}
		if err := completeManagedWorktreeCreationInStore(store, root, worktree, creating.Instance); err != nil {
			return nil, err
		}
		return worktree, nil
	}
	worktree, err := vcs.CreateWithPrefix(context.Background(), root, sanitised, baseRef, branchPrefix)
	if err != nil {
		if abandonErr := store.AbandonWorktreeCreation(context.Background(), principal, instance); abandonErr != nil {
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
	return worktree, nil
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
	if store, ok := sess.ContextStore().(*storage.SQLite); ok && store != nil {
		return beginManagedWorktreeRemovalInStore(store, root, wt)
	}
	return beginManagedWorktreeRemoval(root, wt)
}

func beginManagedWorktreeRemovalInStore(store *storage.SQLite, root string, wt *vcs.WorktreeInfo) (contextstate.WorktreeInstance, error) {
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
	return instance, store.BeginWorktreeDeletion(context.Background(), principal, instance)
}

func finishManagedWorktreeRemoval(root string, instance contextstate.WorktreeInstance) error {
	return finishManagedWorktreeRemovalInStore(nil, root, instance)
}

func recoverManagedWorktreeRemoval(root, name string) error {
	store, err := openRepositoryContextStore(root)
	if err != nil {
		return err
	}
	defer store.Close()
	principal, err := worktreeRoutePrincipal(root)
	if err != nil {
		return err
	}
	instance, err := store.DeletingWorktreeInstance(context.Background(), principal, name)
	if err != nil {
		return err
	}
	_, err = store.DeleteWorktreeSessions(context.Background(), principal, instance)
	return err
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
	store, err := openRepositoryContextStore(root)
	if err != nil {
		return err
	}
	defer store.Close()
	principal, err := worktreeRoutePrincipal(root)
	if err != nil {
		return err
	}
	return store.ReactivateWorktreeInstance(context.Background(), principal, instance)
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
	principal, err := worktreeRoutePrincipal(root)
	if err != nil {
		return err
	}
	return store.DeleteWorktreeRoute(context.Background(), principal, name)
}

func openRepositoryContextStore(root string) (*storage.SQLite, error) {
	path, err := repositorySessionStorePath(root, chatInvocation{}, &config.Resolved{})
	if err != nil {
		return nil, err
	}
	return openContextStorePath(path)
}

func setupSessionContext(sess *chat.Session, root string, res *config.Resolved) (*storage.SQLite, error) {
	store, err := openContextStore(root, res.Subagents)
	if err != nil {
		return nil, err
	}
	return configureSessionContext(sess, root, store, res)
}

// setupRepositorySessionContext stores sessions under the main repository.
// The active workspace supplies each session's directory metadata.
func setupRepositorySessionContext(sess *chat.Session, repositoryRoot, storePath string, res *config.Resolved) (*storage.SQLite, error) {
	store, err := openContextStorePath(storePath)
	if err != nil {
		return nil, err
	}
	return configureSessionContext(sess, repositoryRoot, store, res)
}

func configureSessionContext(sess *chat.Session, catalogRoot string, store *storage.SQLite, res *config.Resolved) (*storage.SQLite, error) {
	if err := enableSessionContext(sess, catalogRoot, store); err != nil {
		_ = store.Close()
		return nil, err
	}
	sess.SetContextRedactionPolicy(contextRedactionPolicy(res))
	return store, nil
}

// contextRedactionPolicy carries the workspace's [privacy] rules to the durable
// source projector, which had no caller installing them at all - so a
// configured policy classified tool previews and event bodies while context
// payloads went unclassified.
//
// The redactor is the SAME compiled policy the rest of the process uses, passed
// as a function rather than re-implemented, because four hand-rolled pattern
// lists drifting apart is the failure this repo already paid for once. An
// unconfigured workspace yields the zero policy, which stores metadata only.
func contextRedactionPolicy(res *config.Resolved) contextstate.RedactionPolicy {
	if res == nil || res.RedactionPolicy == nil {
		return contextstate.RedactionPolicy{}
	}
	patterns := res.Privacy.RedactionPatterns
	keyNames := res.Privacy.RedactionKeyNames
	if len(patterns) == 0 && len(keyNames) == 0 {
		return contextstate.RedactionPolicy{}
	}
	policy := res.RedactionPolicy
	return contextstate.RedactionPolicy{
		Configured: true, Patterns: patterns, KeyNames: keyNames,
		Redactor: func(data []byte) []byte { return []byte(policy.Text(string(data))) },
	}
}

// contextWorkspaceID is a workspace's durable identity, derived from the
// directory itself rather than from how its path was spelled.
//
// It used to hash the cleaned root as given, and `mivia chat` passes "." when
// no --workspace is set, so every project on the machine resolved to the hash
// of "." - one identity shared by all of them. That is the label the durable
// context owns state by, and `chat_sessions` is keyed on it, so two projects
// pointed at one store would have addressed each other's saved sessions.
//
// A path that cannot be resolved falls back to the cleaned input: an id that is
// merely too coarse is better than refusing to start.
func contextWorkspaceID(root string) string {
	resolved, err := filepath.Abs(root)
	if err != nil {
		resolved = filepath.Clean(root)
	}
	if linked, err := filepath.EvalSymlinks(resolved); err == nil {
		resolved = linked
	}
	digest := sha256.Sum256([]byte(resolved))
	return "workspace-" + hex.EncodeToString(digest[:8])
}

func enableSessionContext(sess *chat.Session, root string, store *storage.SQLite) error {
	if sess == nil || store == nil {
		return fmt.Errorf("context session and store are required")
	}
	principal, err := contextstate.NewPrincipal(contextWorkspaceID(root), sess.SessionID, "local-user")
	if err != nil {
		return err
	}
	manager := &contextmgr.ContextManager{
		PreparationManager:  contextmgr.StructuralPreparationManager{},
		CheckpointPublisher: contextmgr.PreparationCommitter{Store: store},
		Enabled:             true,
	}
	if err := sess.SetContextManager(manager, principal); err != nil {
		return err
	}
	return sess.SetContextStore(store)
}

type contextDispatcherWiring struct {
	preparation      contextmgr.PreparationManager
	preparationInput contextmgr.PrepareInput
	sharedSQLite     *storage.SQLite
}

func contextDispatcherFor(sess *chat.Session, _ config.SubagentConfig) contextDispatcherWiring {
	manager, input, ok := sess.ContextPreparation()
	if !ok {
		return contextDispatcherWiring{}
	}
	wiring := contextDispatcherWiring{preparation: manager, preparationInput: input}
	wiring.sharedSQLite, _ = sess.ContextStore().(*storage.SQLite)
	return wiring
}
