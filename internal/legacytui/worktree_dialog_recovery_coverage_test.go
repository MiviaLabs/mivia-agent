package legacytui

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/cli"
	"github.com/MiviaLabs/mivia-agent/internal/cliworktree"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/vcs"
)

func TestDialogCoverageDeleteAbandonsPhantomCreatingRow(t *testing.T) {
	repo := newWorktreeCommandRepo(t)
	store, err := cli.OpenRepositoryContextStore(repo)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	principal, err := cliworktree.WorktreeRoutePrincipal(repo)
	if err != nil {
		t.Fatal(err)
	}
	instance := contextstate.WorktreeInstance{Worktree: "phantom", ID: "wt_1234567890abcdef"}
	expectedPath := filepath.Join(repo, ".mivia", "worktrees", "phantom")
	if err := store.BeginWorktreeCreation(context.Background(), principal, instance, expectedPath); err != nil {
		t.Fatal(err)
	}
	m := newReadyChatModel(30, 90)
	m.workspaceDir = repo
	if err := m.session.SetContextStore(store); err != nil {
		t.Fatal(err)
	}
	m.worktreeDlg = newWorktreeDialog([]vcs.WorktreeInfo{{Name: instance.Worktree, Path: expectedPath}})
	m.worktreeDlg.setRecovery(contextstate.WorktreeInstanceInfo{Instance: instance, CanonicalPath: expectedPath, State: contextstate.WorktreeCreating})
	m.worktreeDlg.confirm = wtConfirmDelete
	m.applyWorktreeConfirm()
	if got := m.worktreeDlg.notice; !strings.Contains(got, "abandoned incomplete creation") {
		t.Fatalf("delete notice = %q, want abandoned incomplete creation", got)
	}
	if len(m.worktreeDlg.worktrees) != 0 {
		t.Fatalf("phantom creating row survived delete: %+v", m.worktreeDlg.worktrees)
	}
	creating, err := store.ListCreatingWorktreeInstances(context.Background(), principal)
	if err != nil {
		t.Fatal(err)
	}
	if len(creating) != 0 {
		t.Fatalf("creating instance survived abandon: %+v", creating)
	}
}

func TestDialogCoverageRecoverAbandonsPhantomCreatingRow(t *testing.T) {
	repo := newWorktreeCommandRepo(t)
	store, err := cli.OpenRepositoryContextStore(repo)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	principal, err := cliworktree.WorktreeRoutePrincipal(repo)
	if err != nil {
		t.Fatal(err)
	}
	instance := contextstate.WorktreeInstance{Worktree: "phantom", ID: "wt_1234567890abcdef"}
	expectedPath := filepath.Join(repo, ".mivia", "worktrees", "phantom")
	if err := store.BeginWorktreeCreation(context.Background(), principal, instance, expectedPath); err != nil {
		t.Fatal(err)
	}
	m := newReadyChatModel(30, 90)
	m.workspaceDir = repo
	if err := m.session.SetContextStore(store); err != nil {
		t.Fatal(err)
	}
	m.worktreeDlg = newWorktreeDialog([]vcs.WorktreeInfo{{Name: instance.Worktree, Path: expectedPath}})
	info := contextstate.WorktreeInstanceInfo{Instance: instance, CanonicalPath: expectedPath, State: contextstate.WorktreeCreating}
	m.recoverCreatingWorktree(m.worktreeDlg.worktrees[0], info)
	if got := m.worktreeDlg.notice; !strings.Contains(got, "abandoned incomplete creation") {
		t.Fatalf("recovery notice = %q, want abandoned incomplete creation", got)
	}
	if len(m.worktreeDlg.worktrees) != 0 {
		t.Fatalf("phantom creating row survived recovery: %+v", m.worktreeDlg.worktrees)
	}
	creating, err := store.ListCreatingWorktreeInstances(context.Background(), principal)
	if err != nil {
		t.Fatal(err)
	}
	if len(creating) != 0 {
		t.Fatalf("creating instance survived recovery abandon: %+v", creating)
	}
}

func TestDialogCoveragePhantomCreationPathExistsNotAbandoned(t *testing.T) {
	repo := newWorktreeCommandRepo(t)
	store, err := cli.OpenRepositoryContextStore(repo)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	principal, err := cliworktree.WorktreeRoutePrincipal(repo)
	if err != nil {
		t.Fatal(err)
	}
	instance := contextstate.WorktreeInstance{Worktree: "phantom", ID: "wt_1234567890abcdef"}
	expectedPath := filepath.Join(repo, ".mivia", "worktrees", "phantom")
	if err := store.BeginWorktreeCreation(context.Background(), principal, instance, expectedPath); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(expectedPath, 0o700); err != nil {
		t.Fatal(err)
	}
	m := newReadyChatModel(30, 90)
	m.workspaceDir = repo
	if err := m.session.SetContextStore(store); err != nil {
		t.Fatal(err)
	}
	m.worktreeDlg = newWorktreeDialog([]vcs.WorktreeInfo{{Name: instance.Worktree, Path: expectedPath}})
	info := contextstate.WorktreeInstanceInfo{Instance: instance, CanonicalPath: expectedPath, State: contextstate.WorktreeCreating}
	m.recoverCreatingWorktree(m.worktreeDlg.worktrees[0], info)
	if got := m.worktreeDlg.notice; !strings.Contains(got, "recovery failed") {
		t.Fatalf("recovery notice = %q, want recovery failed", got)
	}
	if len(m.worktreeDlg.worktrees) != 1 {
		t.Fatalf("live creating row was removed: %+v", m.worktreeDlg.worktrees)
	}
	creating, err := store.ListCreatingWorktreeInstances(context.Background(), principal)
	if err != nil {
		t.Fatal(err)
	}
	if len(creating) != 1 {
		t.Fatalf("live creating instance was abandoned: %+v", creating)
	}
}

func TestDialogCoveragePhantomCreationDeletePathExistsNotAbandoned(t *testing.T) {
	repo := newWorktreeCommandRepo(t)
	store, err := cli.OpenRepositoryContextStore(repo)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	principal, err := cliworktree.WorktreeRoutePrincipal(repo)
	if err != nil {
		t.Fatal(err)
	}
	instance := contextstate.WorktreeInstance{Worktree: "phantom", ID: "wt_1234567890abcdef"}
	expectedPath := filepath.Join(repo, ".mivia", "worktrees", "phantom")
	if err := store.BeginWorktreeCreation(context.Background(), principal, instance, expectedPath); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(expectedPath, 0o700); err != nil {
		t.Fatal(err)
	}
	m := newReadyChatModel(30, 90)
	m.workspaceDir = repo
	if err := m.session.SetContextStore(store); err != nil {
		t.Fatal(err)
	}
	m.worktreeDlg = newWorktreeDialog([]vcs.WorktreeInfo{{Name: instance.Worktree, Path: expectedPath}})
	m.worktreeDlg.setRecovery(contextstate.WorktreeInstanceInfo{Instance: instance, CanonicalPath: expectedPath, State: contextstate.WorktreeCreating})
	m.worktreeDlg.confirm = wtConfirmDelete
	m.applyWorktreeConfirm()
	if got := m.worktreeDlg.notice; !strings.Contains(got, "delete failed") {
		t.Fatalf("delete notice = %q, want delete failed", got)
	}
	if len(m.worktreeDlg.worktrees) != 1 {
		t.Fatalf("live creating row was removed: %+v", m.worktreeDlg.worktrees)
	}
	creating, err := store.ListCreatingWorktreeInstances(context.Background(), principal)
	if err != nil {
		t.Fatal(err)
	}
	if len(creating) != 1 {
		t.Fatalf("live creating instance was abandoned: %+v", creating)
	}
}

func TestDialogFaultSeamAbandonStaleCreation(t *testing.T) {
	repo := newWorktreeCommandRepo(t)
	store, err := cli.OpenRepositoryContextStore(repo)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	principal, err := cliworktree.WorktreeRoutePrincipal(repo)
	if err != nil {
		t.Fatal(err)
	}
	instance := contextstate.WorktreeInstance{Worktree: "phantom", ID: "wt_1234567890abcdef"}
	canonicalPath := filepath.Join(repo, ".mivia", "worktrees", "phantom")
	if err := store.BeginWorktreeCreation(context.Background(), principal, instance, canonicalPath); err != nil {
		t.Fatal(err)
	}
	m := newReadyChatModel(30, 90)
	m.worktreeDlg = newWorktreeDialog([]vcs.WorktreeInfo{{Name: instance.Worktree, Path: canonicalPath}})
	info := contextstate.WorktreeInstanceInfo{Instance: instance, CanonicalPath: canonicalPath, State: contextstate.WorktreeCreating}
	handled, err := m.abandonStaleWorktreeCreation(store, repo, m.worktreeDlg.worktrees[0], info)
	if !handled || err != nil {
		t.Fatalf("first abandon = %t, %v", handled, err)
	}
	if len(m.worktreeDlg.worktrees) != 0 {
		t.Fatalf("row survived abandon: %+v", m.worktreeDlg.worktrees)
	}
	handled, err = m.abandonStaleWorktreeCreation(store, repo, vcs.WorktreeInfo{Name: instance.Worktree, Path: canonicalPath}, info)
	if !handled || !errors.Is(err, contextstate.ErrWorktreeDeleted) {
		t.Fatalf("second abandon = %t, %v; want ErrWorktreeDeleted", handled, err)
	}
}

func TestDialogFaultSeamAbandonStaleCreationInspectError(t *testing.T) {
	repo := newWorktreeCommandRepo(t)
	store, err := cli.OpenRepositoryContextStore(repo)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	m := newReadyChatModel(30, 90)
	m.worktreeDlg = newWorktreeDialog(nil)
	info := contextstate.WorktreeInstanceInfo{
		Instance:      contextstate.WorktreeInstance{Worktree: "phantom", ID: "wt_1234567890abcdef"},
		CanonicalPath: filepath.Join(blocker, "child"),
		State:         contextstate.WorktreeCreating,
	}
	handled, err := m.abandonStaleWorktreeCreation(store, repo, vcs.WorktreeInfo{Name: "phantom"}, info)
	if !handled || err == nil || !strings.Contains(err.Error(), "inspect expected worktree path") {
		t.Fatalf("inspect error = %t, %v", handled, err)
	}
}

func TestDialogCoverageDeleteRecoveryUnknownState(t *testing.T) {
	m := newReadyChatModel(30, 90)
	m.worktreeDlg = newWorktreeDialog([]vcs.WorktreeInfo{{Name: "wt-a", Path: "/missing"}})
	m.worktreeDlg.setRecovery(contextstate.WorktreeInstanceInfo{
		Instance:      contextstate.WorktreeInstance{Worktree: "wt-a", ID: "wt_1234567890abcdef"},
		CanonicalPath: "/missing",
		State:         contextstate.WorktreeDeleted,
	})
	if m.handleWorktreeDeleteRecovery(t.TempDir(), m.worktreeDlg.worktrees[0], "mivia/") {
		t.Fatal("non-recovery state was handled")
	}
}

func TestDialogCoverageDeleteRecoveryMissingDeletingRow(t *testing.T) {
	repo := newWorktreeCommandRepo(t)
	store, err := cli.OpenRepositoryContextStore(repo)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	m := newReadyChatModel(30, 90)
	m.workspaceDir = repo
	if err := m.session.SetContextStore(store); err != nil {
		t.Fatal(err)
	}
	canonicalPath := filepath.Join(repo, ".mivia", "worktrees", "gone")
	m.worktreeDlg = newWorktreeDialog([]vcs.WorktreeInfo{{Name: "gone", Path: canonicalPath}})
	m.worktreeDlg.setRecovery(contextstate.WorktreeInstanceInfo{
		Instance:      contextstate.WorktreeInstance{Worktree: "gone", ID: "wt_1234567890abcdef"},
		CanonicalPath: canonicalPath,
		State:         contextstate.WorktreeDeleting,
	})
	m.worktreeDlg.confirm = wtConfirmDelete
	m.applyWorktreeConfirm()
	if got := m.worktreeDlg.notice; !strings.Contains(got, "recovery failed") {
		t.Fatalf("delete recovery notice = %q, want recovery failed", got)
	}
}

func TestDialogFaultSeamAbandonStoreError(t *testing.T) {
	m := newReadyChatModel(30, 90)
	if err := m.session.SetContextStore(nonSQLiteContextStore{}); err != nil {
		t.Fatal(err)
	}
	m.worktreeDlg = newWorktreeDialog([]vcs.WorktreeInfo{{Name: "wt-a", Path: "/missing"}})
	m.worktreeDlg.setRecovery(contextstate.WorktreeInstanceInfo{
		Instance:      contextstate.WorktreeInstance{Worktree: "wt-a", ID: "wt_1234567890abcdef"},
		CanonicalPath: "/missing",
		State:         contextstate.WorktreeCreating,
	})
	m.worktreeDlg.confirm = wtConfirmDelete
	m.applyWorktreeConfirm()
	if got := m.worktreeDlg.notice; !strings.Contains(got, "delete failed") {
		t.Fatalf("abandon store notice = %q, want delete failed", got)
	}
}

func TestDialogFaultSeamAbandonAlreadyGone(t *testing.T) {
	repo := newWorktreeCommandRepo(t)
	store, err := cli.OpenRepositoryContextStore(repo)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	principal, err := cliworktree.WorktreeRoutePrincipal(repo)
	if err != nil {
		t.Fatal(err)
	}
	instance := contextstate.WorktreeInstance{Worktree: "phantom", ID: "wt_1234567890abcdef"}
	canonicalPath := filepath.Join(repo, ".mivia", "worktrees", "phantom")
	if err := store.BeginWorktreeCreation(context.Background(), principal, instance, canonicalPath); err != nil {
		t.Fatal(err)
	}
	if err := store.AbandonWorktreeCreation(context.Background(), principal, instance); err != nil {
		t.Fatal(err)
	}
	m := newReadyChatModel(30, 90)
	m.workspaceDir = repo
	if err := m.session.SetContextStore(store); err != nil {
		t.Fatal(err)
	}
	m.worktreeDlg = newWorktreeDialog([]vcs.WorktreeInfo{{Name: instance.Worktree, Path: canonicalPath}})
	m.worktreeDlg.setRecovery(contextstate.WorktreeInstanceInfo{Instance: instance, CanonicalPath: canonicalPath, State: contextstate.WorktreeCreating})
	m.worktreeDlg.confirm = wtConfirmDelete
	m.applyWorktreeConfirm()
	if got := m.worktreeDlg.notice; !strings.Contains(got, "delete failed") {
		t.Fatalf("abandon gone notice = %q, want delete failed", got)
	}
}
