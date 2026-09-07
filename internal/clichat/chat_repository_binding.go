package clichat

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
	// Read only the one key this path needs, without provider resolution: a
	// repo or pin config may legitimately declare no [providers] section
	// (the user's provider lives in ~/.mivia/mivia.toml), and a full Load
	// here would hard-fail with "[providers.openrouter]: models must be
	// non-empty" - blocking chat startup over a key that has nothing to do
	// with providers.
	//
	// Precedence, highest first: the repository's own .mivia/mivia.toml,
	// then an explicit --config/$MIVIA_CONFIG pin, then the user-level
	// config - the repo file overlays the pin the same way loadFile's own
	// workspace overlay wins on overlap, and the user file is the final
	// fallback when neither sets [subagents] store_path. Each candidate is
	// tried in order; the first that actually SETS the key wins, so a repo
	// file present but silent on store_path (writeRepoConfig's "[workflows]"
	// case) falls through to the pin, then to the user file, instead of
	// stopping at the repo file the way repositoryConfigPath's single-base
	// resolution does for provider lookups.
	// storePathCandidates already drops every blank path, so no
	// candidate here is empty.
	for _, candidatePath := range storePathCandidates(root, invocation) {
		if info, err := os.Stat(candidatePath); err != nil || !info.Mode().IsRegular() {
			continue
		}
		storePath, set, err := config.LoadSubagentStorePath(candidatePath)
		if err != nil {
			return "", err
		}
		if !set {
			continue
		}
		path := config.ExpandPath(storePath)
		if filepath.IsAbs(path) {
			return path, nil
		}
		return filepath.Join(root, path), nil
	}
	return workspace.GlobalContextStorePath(root), nil
}

// storePathCandidates returns the ordered, deduplicated set of config files
// repositorySessionStorePath consults: the repository file, the explicit
// pin (invocation.configPath or $MIVIA_CONFIG, absolute-expanded), and the
// user-level config. A blank or duplicate candidate is dropped so the same
// file is never read twice.
func storePathCandidates(root string, invocation chatInvocation) []string {
	ordered := []string{workspace.NamespacePath(root, "mivia.toml")}
	pin := invocation.configPath
	if pin == "" {
		pin = os.Getenv("MIVIA_CONFIG")
	}
	if pin != "" {
		path := config.ExpandPath(pin)
		if !filepath.IsAbs(path) {
			if absolute, err := filepath.Abs(path); err == nil {
				path = absolute
			}
		}
		ordered = append(ordered, path)
	}
	ordered = append(ordered, config.UserConfigPath())
	seen := make(map[string]bool, len(ordered))
	out := make([]string, 0, len(ordered))
	for _, p := range ordered {
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	return out
}
