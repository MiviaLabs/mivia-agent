package cli

import (
	"context"
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
	return openRepositorySessionStore(root, path)
}

func openRepositorySessionStore(root, path string) (*storage.SQLite, error) {
	_, err := os.Stat(path)
	targetMissing := errors.Is(err, os.ErrNotExist)
	if err != nil && !targetMissing {
		return nil, fmt.Errorf("inspect repository session store %q: %w", path, err)
	}
	if !targetMissing {
		return openContextStorePath(path)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create repository session store directory: %w", err)
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".mivia-session-import-*.db")
	if err != nil {
		return nil, fmt.Errorf("create temporary repository session store: %w", err)
	}
	temporaryPath := temporary.Name()
	if err := temporary.Close(); err != nil {
		cleanupSQLiteArtifacts(temporaryPath)
		return nil, fmt.Errorf("close temporary repository session store: %w", err)
	}
	store, err := openContextStorePath(temporaryPath)
	if err != nil {
		cleanupSQLiteArtifacts(temporaryPath)
		return nil, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = store.Close()
			cleanupSQLiteArtifacts(temporaryPath)
		}
	}()
	for _, legacy := range legacyRepositoryStores(root, path) {
		if _, err := os.Stat(legacy.path); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return nil, fmt.Errorf("inspect legacy session store %q: %w", legacy.path, err)
		}
		if err := store.Import(context.Background(), legacy.path); err != nil {
			return nil, fmt.Errorf("import legacy session store %q: %w", legacy.path, err)
		}
		if err := store.ReassignWorkspace(context.Background(), contextWorkspaceID(legacy.workspaceRoot), contextWorkspaceID(root)); err != nil {
			return nil, fmt.Errorf("move legacy session workspace %q: %w", legacy.workspaceRoot, err)
		}
	}
	if err := store.Close(); err != nil {
		return nil, fmt.Errorf("close imported repository session store: %w", err)
	}
	if err := os.Link(temporaryPath, path); err != nil {
		if !errors.Is(err, os.ErrExist) {
			return nil, fmt.Errorf("publish repository session store: %w", err)
		}
	}
	cleanupSQLiteArtifacts(temporaryPath)
	committed = true
	return openContextStorePath(path)
}

func cleanupSQLiteArtifacts(path string) {
	for _, artifact := range []string{path, path + "-wal", path + "-shm"} {
		_ = os.Remove(artifact)
	}
}

type legacyRepositoryStore struct {
	path          string
	workspaceRoot string
}

func legacyRepositoryStores(root, target string) []legacyRepositoryStore {
	roots := []string{root}
	worktrees, err := vcs.List(context.Background(), root)
	if err == nil {
		for _, worktree := range worktrees {
			roots = append(roots, worktree.Path)
		}
	}
	stores := make([]legacyRepositoryStore, 0, len(roots)*2)
	seen := map[string]bool{filepath.Clean(target): true}
	for _, workspaceRoot := range roots {
		for _, path := range []string{
			workspace.ContextStorePath(workspaceRoot),
			config.DefaultStorePathForWorkspace(workspaceRoot),
		} {
			path = filepath.Clean(path)
			if !seen[path] {
				seen[path] = true
				stores = append(stores, legacyRepositoryStore{path: path, workspaceRoot: workspaceRoot})
			}
		}
	}
	return stores
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
	store, err := openRepositorySessionStore(repositoryRoot, storePath)
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
