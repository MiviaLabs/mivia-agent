package legacytui

import (
	"context"
	"errors"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/cli"
	"github.com/MiviaLabs/mivia-agent/internal/cliworktree"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/vcs"
)

func testWorktreeInstance(name string) contextstate.WorktreeInstance {
	return contextstate.WorktreeInstance{Worktree: name, ID: "wt_1234567890abcdef"}
}

func TestStaleRoutePickerRejectsSameNameReplacement(t *testing.T) {
	repoRoot := newWorktreeCommandRepo(t)
	old, err := cliworktree.CreateManagedWorktree(repoRoot, "picker-replacement", "HEAD", "mivia/")
	if err != nil {
		t.Fatal(err)
	}
	oldInstance, err := cliworktree.BeginManagedWorktreeRemoval(repoRoot, old)
	if err != nil {
		t.Fatal(err)
	}
	if err := vcs.RemoveWithPrefix(context.Background(), repoRoot, old.Name, "mivia/"); err != nil {
		t.Fatal(err)
	}
	if err := cliworktree.FinishManagedWorktreeRemoval(repoRoot, oldInstance); err != nil {
		t.Fatal(err)
	}
	replacement, err := cliworktree.CreateManagedWorktree(repoRoot, old.Name, "HEAD", "mivia/")
	if err != nil {
		t.Fatal(err)
	}
	m := newReadyChatModel(30, 90)
	m.workspaceDir = repoRoot
	err = m.openSessionInfo(chat.SessionInfo{Name: "worktree:" + old.Name, Dir: replacement.Path, Worktree: old.Name, WorktreeRoute: true, WorktreeInstance: oldInstance})
	if !errors.Is(err, contextstate.ErrWorktreeDeleted) {
		t.Fatalf("stale route picker = %v, want ErrWorktreeDeleted", err)
	}
	if m.restartWorkspace != "" || m.workspaceDir != repoRoot {
		t.Fatalf("stale route changed workspace to %q and restart to %q", m.workspaceDir, m.restartWorkspace)
	}
}

func TestWorkspaceRestartRevalidatesSelectedInstance(t *testing.T) {
	repoRoot := newWorktreeCommandRepo(t)
	old, err := cliworktree.CreateManagedWorktree(repoRoot, "restart-replacement", "HEAD", "mivia/")
	if err != nil {
		t.Fatal(err)
	}
	oldInstance, err := cliworktree.ReadWorktreeMarker(old.Path)
	if err != nil {
		t.Fatal(err)
	}
	m := newReadyChatModel(30, 90)
	m.workspaceDir = repoRoot
	if err := m.openSessionInfo(chat.SessionInfo{Name: "worktree:" + old.Name, Dir: old.Path, Worktree: old.Name, WorktreeRoute: true, WorktreeInstance: oldInstance}); err != nil {
		t.Fatal(err)
	}
	if _, err := cliworktree.BeginManagedWorktreeRemoval(repoRoot, old); err != nil {
		t.Fatal(err)
	}
	if err := vcs.RemoveWithPrefix(context.Background(), repoRoot, old.Name, "mivia/"); err != nil {
		t.Fatal(err)
	}
	if err := cliworktree.FinishManagedWorktreeRemoval(repoRoot, oldInstance); err != nil {
		t.Fatal(err)
	}
	if _, err := cliworktree.CreateManagedWorktree(repoRoot, old.Name, "HEAD", "mivia/"); err != nil {
		t.Fatal(err)
	}
	restart := stubWorkspaceRestart{dir: m.restartWorkspace, wt: m.restartWorktreeInstance}
	err = cli.ValidateWorkspaceRestart(restart, cli.NewChatInvocationWorkspacePath(repoRoot))
	if !errors.Is(err, contextstate.ErrWorktreeDeleted) {
		t.Fatalf("restart revalidation = %v, want ErrWorktreeDeleted", err)
	}
	m2 := newReadyChatModel(30, 90)
	err = cli.BindManagedWorktreeSessionExpected(m2.session, repoRoot, restart.dir, "", oldInstance)
	if !errors.Is(err, contextstate.ErrWorktreeDeleted) {
		t.Fatalf("setup revalidation = %v, want ErrWorktreeDeleted", err)
	}
}

func TestStaleWorktreeDialogRowRejectsManagedReplacement(t *testing.T) {
	m, repoRoot, replacement := staleManagedWorktreeDialog(t, "stale-switch")
	m.handleChatKey("enter", false)
	if m.restartWorkspace != "" || m.workspaceDir != repoRoot {
		t.Fatalf("stale row switched to %q with restart %q", m.workspaceDir, m.restartWorkspace)
	}
	if m.worktreeDlg == nil || !m.worktreeDlg.noticeErr {
		t.Fatal("stale switch did not keep the dialog open with an error")
	}
	assertManagedWorktreeActive(t, repoRoot, replacement)
}

func TestStaleWorktreeDialogRowCannotDeleteManagedReplacement(t *testing.T) {
	m, repoRoot, replacement := staleManagedWorktreeDialog(t, "stale-delete")
	m.worktreeDlg.confirm = wtConfirmDelete
	m.applyWorktreeConfirm()
	assertManagedWorktreeActive(t, repoRoot, replacement)
}

func TestAsyncCreateMessageRetainsAllocatedInstance(t *testing.T) {
	repoRoot := newWorktreeCommandRepo(t)
	m := newReadyChatModel(30, 90)
	m.workspaceDir = repoRoot
	m.worktreeDlg = newWorktreeDialog(nil)
	msg := m.createWorktreeAsyncWithPrefix(repoRoot, "async-stale", "mivia/", m.worktreeDlg)().(worktreeCreatedMsg)
	if msg.err != nil {
		t.Fatal(msg.err)
	}
	marker, err := cliworktree.ReadWorktreeMarker(msg.wt.Path)
	if err != nil {
		t.Fatal(err)
	}
	if msg.instance != marker {
		t.Fatalf("create message instance = %+v, want allocated %+v", msg.instance, marker)
	}
	if _, err := cliworktree.BeginManagedWorktreeRemoval(repoRoot, msg.wt); err != nil {
		t.Fatal(err)
	}
	if err := vcs.RemoveWithPrefix(context.Background(), repoRoot, msg.wt.Name, "mivia/"); err != nil {
		t.Fatal(err)
	}
	if err := cliworktree.FinishManagedWorktreeRemoval(repoRoot, marker); err != nil {
		t.Fatal(err)
	}
	m.applyWorktreeCreated(msg)
	if _, err := cliworktree.CreateManagedWorktree(repoRoot, msg.wt.Name, "HEAD", "mivia/"); err != nil {
		t.Fatal(err)
	}
	m.handleChatKey("enter", false)
	if m.restartWorkspace != "" {
		t.Fatalf("stale create row restarted in %q", m.restartWorkspace)
	}
	if m.worktreeDlg == nil || !m.worktreeDlg.noticeErr {
		t.Fatal("stale create row did not remain open with an error")
	}
}

func staleManagedWorktreeDialog(t *testing.T, name string) (*TUIModel, string, *vcs.WorktreeInfo) {
	t.Helper()
	repoRoot := newWorktreeCommandRepo(t)
	old, err := cliworktree.CreateManagedWorktree(repoRoot, name, "HEAD", "mivia/")
	if err != nil {
		t.Fatal(err)
	}
	m := newReadyChatModel(30, 90)
	m.workspaceDir = repoRoot
	m.openWorktreeDialog()
	oldInstance, err := cliworktree.BeginManagedWorktreeRemoval(repoRoot, old)
	if err != nil {
		t.Fatal(err)
	}
	if err := vcs.RemoveWithPrefix(context.Background(), repoRoot, old.Name, "mivia/"); err != nil {
		t.Fatal(err)
	}
	if err := cliworktree.FinishManagedWorktreeRemoval(repoRoot, oldInstance); err != nil {
		t.Fatal(err)
	}
	replacement, err := cliworktree.CreateManagedWorktree(repoRoot, name, "HEAD", "mivia/")
	if err != nil {
		t.Fatal(err)
	}
	return m, repoRoot, replacement
}

func assertManagedWorktreeActive(t *testing.T, repoRoot string, worktree *vcs.WorktreeInfo) {
	t.Helper()
	resolved, err := vcs.Resolve(context.Background(), repoRoot, worktree.Name)
	if err != nil || resolved == nil {
		t.Fatalf("replacement worktree = %+v, %v", resolved, err)
	}
	instance, err := cliworktree.ReadWorktreeMarker(worktree.Path)
	if err != nil {
		t.Fatal(err)
	}
	store, err := cli.OpenRepositoryContextStore(repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	principal, err := cliworktree.WorktreeRoutePrincipal(repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ValidateActiveWorktreeInstance(context.Background(), principal, instance, worktree.Path); err != nil {
		t.Fatalf("replacement is not active: %v", err)
	}
}
