package cli

import (
	"context"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/vcs"
)

func TestWorktreeDialogSwitchRealDir(t *testing.T) {
	m := newReadyChatModel(30, 90)
	repoRoot := newWorktreeCommandRepo(t)
	worktree, err := vcs.Create(context.Background(), repoRoot, "real-wt", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	m.workspaceDir = repoRoot
	m.worktreeDlg = newWorktreeDialog([]vcs.WorktreeInfo{*worktree})
	m.hitMap.invalidate()

	m.handleChatKey("enter", false)
	if m.worktreeDlg != nil {
		t.Fatalf("dialog should close on successful switch, notice: %q", m.worktreeDlg.notice)
	}
	if m.workspaceDir != worktree.Path {
		t.Fatalf("workspace = %q, want %q", m.workspaceDir, worktree.Path)
	}
	if m.restartWorkspace != worktree.Path {
		t.Fatalf("restart workspace = %q, want %q", m.restartWorkspace, worktree.Path)
	}
}
