package cli

// Repository session-store and managed-worktree binding for chat sessions.
// Split out of chat_command.go so the chat entrypoint file stays under the
// go-structure soft line cap; these helpers resolve the repository session
// store and pin a session to its managed worktree instance.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/cliworktree"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
	"github.com/MiviaLabs/mivia-agent/internal/vcs"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

func chatRepositoryRoot(path string) (string, error) {
	if path == "" {
		path = "."
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return vcs.MainRepoRoot(abs)
}

func setupChatSessionContext(sess *chat.Session, workspaceRoot string, invocation chatInvocation, res *config.Resolved) (*storage.SQLite, error) {
	repositoryRoot, err := chatRepositoryRoot(workspaceRoot)
	if err == nil {
		if err := bindManagedWorktreeSessionExpected(sess, repositoryRoot, workspaceRoot, invocation.repositorySessionStorePath, invocation.expectedWorktreeInstance); err != nil {
			return nil, err
		}
	}
	if err != nil || invocation.repositorySessionStorePath == "" {
		return setupSessionContext(sess, workspaceRoot, res)
	}
	return setupRepositorySessionContext(sess, repositoryRoot, invocation.repositorySessionStorePath, res)
}

func bindManagedWorktreeSession(sess *chat.Session, repositoryRoot, workspaceRoot, storePath string) error {
	return bindManagedWorktreeSessionExpected(sess, repositoryRoot, workspaceRoot, storePath, contextstate.WorktreeInstance{})
}

func bindManagedWorktreeSessionExpected(sess *chat.Session, repositoryRoot, workspaceRoot, storePath string, expected contextstate.WorktreeInstance) error {
	name, err := vcs.CurrentWorktreeName(context.Background(), workspaceRoot)
	if err != nil {
		return err
	}
	if name == "" {
		if !expected.IsZero() {
			return contextstate.ErrWorktreeDeleted
		}
		return nil
	}
	if !expected.IsZero() && expected.Worktree != name {
		return contextstate.ErrWorktreeDeleted
	}
	worktree, err := vcs.Resolve(context.Background(), repositoryRoot, name)
	if err != nil {
		return err
	}
	if worktree == nil {
		return fmt.Errorf("managed worktree %q is not available", name)
	}
	canonicalPath, err := cliworktree.CanonicalMarkerRoot(worktree.Path)
	if err != nil {
		return err
	}
	if storePath == "" {
		storePath, err = repositorySessionStorePath(repositoryRoot, chatInvocation{}, &config.Resolved{})
		if err != nil {
			return err
		}
	}
	store, err := openContextStorePath(storePath)
	if err != nil {
		return err
	}
	defer store.Close()
	principal, _ := cliworktree.WorktreeRoutePrincipal(repositoryRoot)
	instance, markerErr := cliworktree.ReadWorktreeMarker(worktree.Path)
	if errors.Is(markerErr, os.ErrNotExist) {
		if !expected.IsZero() {
			return contextstate.ErrWorktreeDeleted
		}
		info, legacy, err := classifyMissingMarkerForBind(store, principal, name, canonicalPath)
		if err != nil {
			return err
		}
		if !info.Instance.IsZero() {
			return fmt.Errorf("managed worktree %q has state %q but no marker: %w", name, info.State, contextstate.ErrWorktreeDeleted)
		}
		if legacy {
			return fmt.Errorf("worktree %q requires adoption; run mivia worktree adopt %s", name, name)
		}
		return nil
	}
	if markerErr != nil {
		return fmt.Errorf("read worktree session marker: %w", markerErr)
	}
	if instance.Worktree != name {
		return fmt.Errorf("worktree session marker does not match %q", name)
	}
	if !expected.IsZero() && instance != expected {
		return contextstate.ErrWorktreeDeleted
	}
	if err := store.ValidateActiveWorktreeInstance(context.Background(), principal, instance, canonicalPath); err != nil {
		return fmt.Errorf("validate worktree session binding: %w", err)
	}
	sessionDir, err := cliworktree.CanonicalMarkerRoot(workspaceRoot)
	if err != nil {
		return err
	}
	return sess.SetContextWorktreeBindingAt(instance, canonicalPath, sessionDir)
}

func repositorySessionStorePath(root string, invocation chatInvocation, _ *config.Resolved) (string, error) {
	configPath, found := repositoryConfigPath(root, invocation)
	if !found {
		return workspace.GlobalContextStorePath(root), nil
	}
	resolved, err := config.Load(config.LoadOptions{ConfigPath: configPath, WorkspaceRoot: root, AllowMissingConfig: true})
	if err != nil {
		return "", err
	}
	if !resolved.StorePathSet {
		return workspace.GlobalContextStorePath(root), nil
	}
	path := config.ExpandPath(resolved.Subagents.StorePath)
	if filepath.IsAbs(path) {
		return path, nil
	}
	return filepath.Join(root, path), nil
}

func repositoryConfigPath(root string, invocation chatInvocation) (string, bool) {
	configPath := invocation.configPath
	if configPath == "" {
		configPath = os.Getenv("MIVIA_CONFIG")
	}
	if configPath != "" {
		path := config.ExpandPath(configPath)
		if !filepath.IsAbs(path) {
			absolute, err := filepath.Abs(path)
			if err != nil {
				return "", false
			}
			path = absolute
		}
		if info, err := os.Stat(path); err != nil || !info.Mode().IsRegular() {
			return "", false
		}
		return path, true
	}
	return config.FirstExisting([]string{
		workspace.NamespacePath(root, "mivia.toml"),
		config.UserConfigPath(),
	})
}
