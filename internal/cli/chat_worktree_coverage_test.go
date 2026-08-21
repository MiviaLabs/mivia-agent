package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/cliworktree"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/vcs"
)

func TestChatWorktreeCoverageRestartValidationErrors(t *testing.T) {
	if err := validateWorkspaceRestart(stubWorkspaceRestart{dir: t.TempDir()}, chatInvocation{}); err != nil {
		t.Fatal(err)
	}
	expected := contextstate.WorktreeInstance{Worktree: "wt-a", ID: "wt_1234567890abcdef"}
	if err := validateWorkspaceRestart(stubWorkspaceRestart{dir: t.TempDir(), wt: expected}, chatInvocation{}); !errors.Is(err, contextstate.ErrWorktreeDeleted) {
		t.Fatalf("non-repository restart error = %v", err)
	}
	repo := newWorktreeCommandRepo(t)
	worktree, instance, err := createManagedWorktreeWithInstance(repo, "restart-store", "HEAD", "mivia/")
	if err != nil {
		t.Fatal(err)
	}
	if err := validateWorkspaceRestart(stubWorkspaceRestart{dir: worktree.Path, wt: instance}, chatInvocation{repositorySessionStorePath: t.TempDir()}); err == nil {
		t.Fatal("restart opened a directory as a store")
	}
}

func TestChatWorktreeCoverageRunRejectsStaleRestart(t *testing.T) {
	original := runConfiguredChatOnceImpl
	t.Cleanup(func() { runConfiguredChatOnceImpl = original })
	expected := contextstate.WorktreeInstance{Worktree: "wt-a", ID: "wt_1234567890abcdef"}
	runConfiguredChatOnceImpl = func(chatInvocation, *config.Resolved) error {
		return stubWorkspaceRestart{dir: t.TempDir(), wt: expected}
	}
	// workspacePath isolates this test from the ambient main repository's
	// real .mivia/mivia.toml (see the note in
	// TestRunConfiguredChatCarriesResumeSessionAcrossRestart).
	err := runConfiguredChat(chatInvocation{workspacePath: t.TempDir()}, &config.Resolved{})
	if !errors.Is(err, contextstate.ErrWorktreeDeleted) || !strings.Contains(err.Error(), "validate workspace restart") {
		t.Fatalf("stale restart error = %v", err)
	}
}

func TestChatWorktreeCoverageExpectedBindingOnMainTree(t *testing.T) {
	repo := newWorktreeCommandRepo(t)
	session := chat.NewSession(&config.Resolved{Model: "model"}, nullCompleter{})
	expected := contextstate.WorktreeInstance{Worktree: "wt-a", ID: "wt_1234567890abcdef"}
	if err := bindManagedWorktreeSessionExpected(session, repo, repo, "", expected); !errors.Is(err, contextstate.ErrWorktreeDeleted) {
		t.Fatalf("main-tree expected binding error = %v", err)
	}
}

func TestChatWorktreeCoverageExpectedNameAndMarkerMismatch(t *testing.T) {
	repo := newWorktreeCommandRepo(t)
	worktree, instance, err := createManagedWorktreeWithInstance(repo, "bound", "HEAD", "mivia/")
	if err != nil {
		t.Fatal(err)
	}
	session := chat.NewSession(&config.Resolved{Model: "model"}, nullCompleter{})
	wrongName := contextstate.WorktreeInstance{Worktree: "other", ID: instance.ID}
	if err := bindManagedWorktreeSessionExpected(session, repo, worktree.Path, "", wrongName); !errors.Is(err, contextstate.ErrWorktreeDeleted) {
		t.Fatalf("wrong expected name error = %v", err)
	}
	wrongID := contextstate.WorktreeInstance{Worktree: worktree.Name, ID: "wt_0000000000000000"}
	if err := bindManagedWorktreeSessionExpected(session, repo, worktree.Path, "", wrongID); !errors.Is(err, contextstate.ErrWorktreeDeleted) {
		t.Fatalf("wrong expected ID error = %v", err)
	}
	if err := os.Remove(cliworktree.WorktreeMarkerPath(worktree.Path)); err != nil {
		t.Fatal(err)
	}
	if err := bindManagedWorktreeSessionExpected(session, repo, worktree.Path, "", instance); !errors.Is(err, contextstate.ErrWorktreeDeleted) {
		t.Fatalf("missing expected marker error = %v", err)
	}
}

func TestChatWorktreeCoverageMissingMarkerLegacyAndMalformed(t *testing.T) {
	repo := newWorktreeCommandRepo(t)
	worktree, err := vcs.CreateWithPrefix(context.Background(), repo, "legacy", "HEAD", "mivia/")
	if err != nil {
		t.Fatal(err)
	}
	store, err := OpenRepositoryContextStore(repo)
	if err != nil {
		t.Fatal(err)
	}
	principal, err := cliworktree.WorktreeRoutePrincipal(repo)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveWorktreeRoute(context.Background(), principal, worktree.Name, worktree.Path); err != nil {
		t.Fatal(err)
	}
	store.Close()
	session := chat.NewSession(&config.Resolved{Model: "model"}, nullCompleter{})
	if err := bindManagedWorktreeSession(session, repo, worktree.Path, ""); err == nil || !strings.Contains(err.Error(), "adoption") {
		t.Fatalf("legacy bind error = %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(cliworktree.WorktreeMarkerPath(worktree.Path)), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cliworktree.WorktreeMarkerPath(worktree.Path), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := bindManagedWorktreeSession(session, repo, worktree.Path, ""); err == nil || !strings.Contains(err.Error(), "read worktree session marker") {
		t.Fatalf("malformed marker bind error = %v", err)
	}
}
