package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/vcs"
)

// TestWorktreeDialogNoticeRendersSingleLine ensures a multi-line git error
// stored in notice cannot break the dialog frame: the footer renders exactly
// one line.
func TestWorktreeDialogNoticeRendersSingleLine(t *testing.T) {
	d := newWorktreeDialog(nil)
	d.notice = "git worktree add: exit status 255\nfatal: a branch named 'wt/wt-1' already exists"
	footer := d.footer()
	if strings.Contains(footer, "\n") {
		t.Fatalf("footer must be one line, got embedded newline: %q", footer)
	}
}

// TestWorktreeDialogFailureNoticeUsesErrorStyle ensures a failure notice is
// visually distinct from an info notice, so a failed create is not a silent
// flash.
func TestWorktreeDialogFailureNoticeUsesErrorStyle(t *testing.T) {
	withANSI256(t)
	d := newWorktreeDialog(nil)
	d.setNotice("create failed: boom", true)
	footer := d.footer()
	if !strings.Contains(footer, tuiErrorStyle.Render("create failed: boom")) {
		t.Fatalf("failure notice must render in error style: %q", footer)
	}
	if strings.Contains(footer, tuiInfoStyle.Render("create failed: boom")) {
		t.Fatalf("failure notice must not render in info style: %q", footer)
	}
}

// TestWorktreeDialogInfoNoticeUsesInfoStyle ensures a success notice keeps
// the neutral info style and is not painted as an error.
func TestWorktreeDialogInfoNoticeUsesInfoStyle(t *testing.T) {
	withANSI256(t)
	d := newWorktreeDialog(nil)
	d.setNotice(`created "wt-abc" at /tmp/project/.mivia/worktrees/wt-abc`, false)
	footer := d.footer()
	if !strings.Contains(footer, tuiInfoStyle.Render(`created "wt-abc" at /tmp/project/.mivia/worktrees/wt-abc`)) {
		t.Fatalf("info notice must render in info style: %q", footer)
	}
}

// TestWorktreeDialogCreateNamesUniqueAfterDelete is the regression test for
// the reported bug: deleting a worktree and pressing c again must produce a
// NEW name, never the same name that a leftover branch may still hold.
func TestWorktreeDialogCreateNamesUniqueAfterDelete(t *testing.T) {
	tmpDir := t.TempDir()
	runGit(t, tmpDir, "init")
	runGit(t, tmpDir, "config", "user.email", "test@example.com")
	runGit(t, tmpDir, "config", "user.name", "test")
	runGit(t, tmpDir, "commit", "--allow-empty", "-m", "initial")

	m := newReadyChatModel(30, 90)
	m.workspaceDir = tmpDir
	m.openWorktreeDialog()
	if m.worktreeDlg == nil {
		t.Fatal("dialog must be open after openWorktreeDialog")
	}

	create := func() *vcs.WorktreeInfo {
		t.Helper()
		_, _, cmds := m.handleChatKey("c", false)
		if len(cmds) == 0 || cmds[0] == nil {
			t.Fatal("c key must return a non-nil cmd")
		}
		wtMsg, ok := cmds[0]().(worktreeCreatedMsg)
		if !ok {
			t.Fatal("cmd must return worktreeCreatedMsg")
		}
		if wtMsg.err != nil {
			t.Fatalf("create should succeed: %v", wtMsg.err)
		}
		if wtMsg.wt == nil {
			t.Fatal("wt must not be nil on success")
		}
		m.applyWorktreeCreated(wtMsg)
		m.workspaceDir = tmpDir
		m.openWorktreeDialog()
		return wtMsg.wt
	}

	first := create()
	if len(m.worktreeDlg.worktrees) != 1 {
		t.Fatalf("worktree count = %d, want 1", len(m.worktreeDlg.worktrees))
	}

	// Delete the worktree through the dialog (d then y), exactly as the user
	// did before hitting the bug.
	m.handleChatKey("d", false)
	m.handleChatKey("y", false)
	if len(m.worktreeDlg.worktrees) != 0 {
		t.Fatalf("worktree count after delete = %d, want 0", len(m.worktreeDlg.worktrees))
	}

	second := create()
	if second.Name == first.Name {
		t.Fatalf("create after delete reused name %q; a leftover branch can block it forever", first.Name)
	}
}

func TestWorktreeDialogCreateCannotCloseWhileCreating(t *testing.T) {
	m := newReadyChatModel(30, 90)
	openWorktreeDialogOnModel(m, 0)
	_, _, cmds := m.handleChatKey("c", false)
	if len(cmds) == 0 {
		t.Fatal("create must return a command")
	}
	issuingDialog := m.worktreeDlg

	m.handleChatKey("esc", false)

	if m.worktreeDlg != issuingDialog || !m.worktreeDlg.creating {
		t.Fatal("dialog must stay open while worktree creation runs")
	}

	worktreePath := t.TempDir()
	m.applyWorktreeCreated(worktreeCreatedMsg{
		wt:  &vcs.WorktreeInfo{Name: "wt-created", Path: worktreePath},
		dlg: issuingDialog,
	})
	if m.restartWorkspace != worktreePath {
		t.Fatalf("restart workspace = %q, want %q", m.restartWorkspace, worktreePath)
	}
}

// TestStaleWorktreeCreateMsgDroppedForReopenedDialog is the BUG-3 regression:
// a create result delivered after its dialog was closed and a new one opened
// must be dropped, or the worktree appears twice in the new dialog.
func TestStaleWorktreeCreateMsgDroppedForReopenedDialog(t *testing.T) {
	m := newReadyChatModel(30, 90)
	openWorktreeDialogOnModel(m, 0)

	// Issue a create and capture the issuing dialog pointer.
	_, _, cmds := m.handleChatKey("c", false)
	if len(cmds) == 0 {
		t.Fatal("c key must return a cmd")
	}
	issuingDlg := m.worktreeDlg

	// The user closes the dialog and opens a fresh one before the result
	// arrives.
	m.worktreeDlg = nil
	m.hitMap.invalidate()
	openWorktreeDialogOnModel(m, 1)
	reopened := m.worktreeDlg
	if reopened == issuingDlg {
		t.Fatal("reopened dialog must be a new instance")
	}

	// Deliver the stale result from the issuing dialog.
	wt := &vcs.WorktreeInfo{Name: "wt-stale", Path: "/tmp/wt", Branch: "main"}
	m.applyWorktreeCreated(worktreeCreatedMsg{wt: wt, err: nil, dlg: issuingDlg})

	if len(m.worktreeDlg.worktrees) != 1 {
		t.Fatalf("stale create message must not append to the reopened dialog: %+v", m.worktreeDlg.worktrees)
	}
	if m.worktreeDlg.worktrees[0].Name == "wt-stale" {
		t.Fatal("stale worktree must not appear in the reopened dialog")
	}
}

// TestWorktreeCreateFullFlow exercises the entire c-key → bubbletea Update
// → vcs.Create → worktreeCreatedMsg → applyWorktreeCreated path with a
// real git repo. This is the test that catches regressions where the worktree
// is created on disk but the TUI never delivers the message back to Update.
func TestWorktreeCreateFullFlow(t *testing.T) {
	tmpDir := t.TempDir()
	runGit(t, tmpDir, "init")
	runGit(t, tmpDir, "config", "user.email", "test@example.com")
	runGit(t, tmpDir, "config", "user.name", "test")
	runGit(t, tmpDir, "commit", "--allow-empty", "-m", "initial")

	m := newReadyChatModel(30, 90)
	m.workspaceDir = tmpDir

	// Open the worktree dialog via the slash handler path.
	m.openWorktreeDialog()
	if m.worktreeDlg == nil {
		t.Fatal("dialog must be open after openWorktreeDialog")
	}

	// Press "c" through handleChatKey (the real key routing path).
	_, _, cmds := m.handleChatKey("c", false)
	if len(cmds) == 0 || cmds[0] == nil {
		t.Fatal("c key must return a non-nil cmd")
	}
	if !m.worktreeDlg.creating {
		t.Fatal("dialog must be in creating state")
	}

	// Execute the returned command (runs vcs.Create in a goroutine).
	msg := cmds[0]()
	wtMsg, ok := msg.(worktreeCreatedMsg)
	if !ok {
		t.Fatalf("cmd must return worktreeCreatedMsg, got %T", msg)
	}
	if wtMsg.err != nil {
		t.Fatalf("vcs.Create should succeed: %v", wtMsg.err)
	}
	if wtMsg.wt == nil {
		t.Fatal("wt must not be nil on success")
	}

	// Deliver the message through the Update path (same as bubbletea runtime).
	m.applyWorktreeCreated(wtMsg)

	if m.worktreeDlg != nil {
		t.Fatal("successful creation must leave the dialog and start the worktree session")
	}
	if m.workspaceDir != wtMsg.wt.Path {
		t.Fatalf("workspace directory = %q, want created worktree %q", m.workspaceDir, wtMsg.wt.Path)
	}
	if !strings.HasPrefix(wtMsg.wt.Name, "wt-") {
		t.Fatalf("worktree name = %q, want a wt- prefixed unique name", wtMsg.wt.Name)
	}

	// The worktree must actually exist on disk at the reported path.
	if _, err := os.Stat(wtMsg.wt.Path); err != nil {
		t.Errorf("worktree does not exist on disk at %s: %v", wtMsg.wt.Path, err)
	}

	// Clean up the worktree so t.TempDir() can remove it.
	_ = exec.Command("git", "-C", tmpDir, "worktree", "remove", wtMsg.wt.Path, "--force").Run()
	_ = exec.Command("git", "-C", tmpDir, "worktree", "prune").Run()
}

// TestWorktreeDialogCreateUsesMainRootBranchPrefix verifies the normal TUI
// create path reads [worktrees].branch_prefix from the main repository.
func TestWorktreeDialogCreateUsesMainRootBranchPrefix(t *testing.T) {
	tmpDir := t.TempDir()
	runGit(t, tmpDir, "init")
	runGit(t, tmpDir, "config", "user.email", "test@example.com")
	runGit(t, tmpDir, "config", "user.name", "test")
	runGit(t, tmpDir, "commit", "--allow-empty", "-m", "initial")

	configDir := filepath.Join(tmpDir, ".mivia")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("create mivia config directory: %v", err)
	}
	configText := worktreeStoreConfig("repository.db") + "\n[worktrees]\nbranch_prefix = \"team/\"\n"
	if err := os.WriteFile(filepath.Join(configDir, "mivia.toml"), []byte(configText), 0o600); err != nil {
		t.Fatalf("write mivia config: %v", err)
	}

	m := newReadyChatModel(30, 90)
	m.workspaceDir = tmpDir
	m.openWorktreeDialog()
	if m.worktreeDlg == nil {
		t.Fatal("dialog must be open after openWorktreeDialog")
	}

	_, _, cmds := m.handleChatKey("c", false)
	if len(cmds) == 0 || cmds[0] == nil {
		t.Fatal("c key must return a non-nil cmd")
	}
	result := cmds[0]()
	msg, ok := result.(worktreeCreatedMsg)
	if !ok {
		t.Fatalf("cmd must return worktreeCreatedMsg, got %T", result)
	}
	if msg.err != nil {
		t.Fatalf("create worktree: %v", msg.err)
	}
	if msg.wt == nil {
		t.Fatal("worktree must not be nil")
	}
	if want := "team/" + msg.wt.Name; msg.wt.Branch != want {
		t.Fatalf("branch = %q, want %q", msg.wt.Branch, want)
	}
	branchOutput, err := exec.Command("git", "-C", msg.wt.Path, "branch", "--show-current").Output()
	if err != nil {
		t.Fatalf("read created worktree branch: %v", err)
	}
	wantBranch := "team/" + msg.wt.Name
	if got := strings.TrimSpace(string(branchOutput)); got != wantBranch {
		t.Fatalf("Git branch = %q, want %q", got, wantBranch)
	}

	_ = exec.Command("git", "-C", tmpDir, "worktree", "remove", msg.wt.Path, "--force").Run()
	_ = exec.Command("git", "-C", tmpDir, "worktree", "prune").Run()
}

func TestWorktreeDialogRefusesToDeleteCurrentDirectory(t *testing.T) {
	m := newReadyChatModel(30, 90)
	m.worktreeDlg = newWorktreeDialog([]vcs.WorktreeInfo{{
		Name: "active",
		Path: m.resolveWorkspaceDir(),
	}})
	m.worktreeDlg.confirm = wtConfirmDelete

	m.applyWorktreeConfirm()

	if !strings.Contains(m.worktreeDlg.notice, "current worktree") {
		t.Fatalf("notice = %q, want current-worktree refusal", m.worktreeDlg.notice)
	}
}

func TestWorktreeDialogRecognizesCurrentDirectoryViaSymlink(t *testing.T) {
	worktreePath := t.TempDir()
	linkPath := filepath.Join(t.TempDir(), "worktree-link")
	if err := os.Symlink(worktreePath, linkPath); err != nil {
		t.Skipf("create symbolic link: %v", err)
	}
	previousDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working directory: %v", err)
	}
	if err := os.Chdir(linkPath); err != nil {
		t.Fatalf("change to symbolic link: %v", err)
	}
	previousPWD, hadPWD := os.LookupEnv("PWD")
	if err := os.Setenv("PWD", linkPath); err != nil {
		t.Fatalf("set PWD: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(previousDir); err != nil {
			t.Errorf("restore working directory: %v", err)
		}
		if hadPWD {
			if err := os.Setenv("PWD", previousPWD); err != nil {
				t.Errorf("restore PWD: %v", err)
			}
			return
		}
		if err := os.Unsetenv("PWD"); err != nil {
			t.Errorf("clear PWD: %v", err)
		}
	})

	if !worktreeContainsCurrentDir(worktreePath) {
		t.Fatal("symbolic-link working directory must count as the current worktree")
	}
}
