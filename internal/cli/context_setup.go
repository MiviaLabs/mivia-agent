package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

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
	path := contextStorePath(root, cfg)
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
	store, err := openContextStore(root, config.DefaultSubagentConfig)
	if err != nil {
		return err
	}
	defer store.Close()
	principal, err := worktreeRoutePrincipal(root)
	if err != nil {
		return err
	}
	return store.SaveWorktreeRoute(context.Background(), principal, wt.Name, wt.Path)
}

// removeWorktreeRoute removes the route after Git has removed its worktree.
func removeWorktreeRoute(root, name string) error {
	store, err := openContextStore(root, config.DefaultSubagentConfig)
	if err != nil {
		return err
	}
	defer store.Close()
	principal, err := worktreeRoutePrincipal(root)
	if err != nil {
		return err
	}
	return store.DeleteWorktreeRoute(context.Background(), principal, name)
}

func listRepositorySessions(root, path string) ([]chat.SessionInfo, error) {
	if path == "" {
		path = workspace.ContextStorePath(root)
	}
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, fmt.Errorf("stat route store %q: %w", path, err)
	}
	store, err := storage.OpenSQLite(path)
	if err != nil {
		return nil, err
	}
	defer store.Close()
	principal, err := worktreeRoutePrincipal(root)
	if err != nil {
		return nil, err
	}
	infos, err := store.ListSessions(context.Background(), principal)
	if err != nil {
		return nil, err
	}
	sessions := make([]chat.SessionInfo, 0, len(infos))
	for _, info := range infos {
		created, _ := time.Parse(time.RFC3339Nano, info.CreatedAt)
		updated, _ := time.Parse(time.RFC3339Nano, info.UpdatedAt)
		sessions = append(sessions, chat.SessionInfo{
			Name:          info.Name,
			Model:         info.Model,
			Provider:      info.Provider,
			CreatedAt:     created,
			UpdatedAt:     updated,
			TurnCount:     info.TurnCount,
			TokenCount:    info.TokenCount,
			MessageCount:  info.MessageCount,
			ChunkCount:    1,
			Dir:           info.Dir,
			Worktree:      info.Worktree,
			WorktreeRoute: info.WorktreeRoute,
		})
	}
	return sessions, nil
}

func listWorktreeRoutes(root string) ([]chat.SessionInfo, error) {
	infos, err := listRepositorySessions(root, workspace.ContextStorePath(root))
	if err != nil {
		return nil, err
	}
	routes := make([]chat.SessionInfo, 0, len(infos))
	for _, info := range infos {
		if info.WorktreeRoute {
			routes = append(routes, info)
		}
	}
	return routes, nil
}

func setupSessionContext(sess *chat.Session, root string, res *config.Resolved) (*storage.SQLite, error) {
	store, err := openContextStore(root, res.Subagents)
	if err != nil {
		return nil, err
	}
	if err := enableSessionContext(sess, root, store); err != nil {
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

func contextDispatcherFor(sess *chat.Session, cfg config.SubagentConfig) contextDispatcherWiring {
	manager, input, ok := sess.ContextPreparation()
	if !ok {
		return contextDispatcherWiring{}
	}
	wiring := contextDispatcherWiring{preparation: manager, preparationInput: input}
	if cfg.StoreBackend == "sqlite" {
		wiring.sharedSQLite, _ = sess.ContextStore().(*storage.SQLite)
	}
	return wiring
}
