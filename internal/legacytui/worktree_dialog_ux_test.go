package legacytui

import (
	"context"
	"errors"
	"github.com/MiviaLabs/mivia-agent/internal/cli"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/vcs"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
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
	if !strings.Contains(footer, TUIErrorStyle.Render("create failed: boom")) {
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

func TestWorktreeDialogEnterRecoveryRowShowsRecoveryNotice(t *testing.T) {
	m := newReadyChatModel(30, 90)
	m.worktreeDlg = newWorktreeDialog([]vcs.WorktreeInfo{{Name: "wt-a", Path: "/missing/wt-a"}})
	m.worktreeDlg.setRecovery(contextstate.WorktreeInstanceInfo{
		Instance:      contextstate.WorktreeInstance{Worktree: "wt-a", ID: "wt_1234567890abcdef"},
		CanonicalPath: "/missing/wt-a", State: contextstate.WorktreeDeleting,
	})
	m.hitMap.invalidate()
	m.handleChatKey("enter", false)
	if m.worktreeDlg == nil {
		t.Fatal("recovery row must keep the dialog open")
	}
	if m.worktreeDlg.notice != "remove this row to recover deletion" {
		t.Fatalf("notice = %q", m.worktreeDlg.notice)
	}
}

func TestWorktreeDialogMarksLiveDeletingWorktreeForRecovery(t *testing.T) {
	repoRoot := newWorktreeCommandRepo(t)
	writeWorktreeStoreConfig(t, repoRoot, "repository.db")
	worktree, err := cli.CreateManagedWorktree(repoRoot, "recover-live", "HEAD", "mivia/")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cli.BeginManagedWorktreeRemoval(repoRoot, worktree); err != nil {
		t.Fatal(err)
	}
	m := newReadyChatModel(30, 90)
	m.workspaceDir = repoRoot
	m.openWorktreeDialog()
	if m.worktreeDlg == nil || len(m.worktreeDlg.worktrees) != 1 {
		t.Fatalf("dialog rows = %#v", m.worktreeDlg)
	}
	if recovery, ok := m.worktreeDlg.selectedRecovery(); !ok || recovery.Info.State != contextstate.WorktreeDeleting {
		t.Fatalf("live deleting recovery = %#v, %v", recovery, ok)
	}
	m.handleChatKey("enter", false)
	if m.worktreeDlg.notice != "remove this row to recover deletion" {
		t.Fatalf("recovery enter notice = %q", m.worktreeDlg.notice)
	}
}

func TestWorktreeDialogRecoversCreatingWorktreeBeforeRestart(t *testing.T) {
	repoRoot := newWorktreeCommandRepo(t)
	writeWorktreeStoreConfig(t, repoRoot, "repository.db")
	store, err := cli.OpenRepositoryContextStore(repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	principal, err := cli.WorktreeRoutePrincipal(repoRoot)
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	instance := contextstate.WorktreeInstance{Worktree: "recover-create", ID: "wt_1234567890abcdef"}
	expectedPath := filepath.Join(workspace.WorktreesDir(repoRoot), instance.Worktree)
	if err := store.BeginWorktreeCreation(context.Background(), principal, instance, expectedPath); err != nil {
		store.Close()
		t.Fatal(err)
	}
	worktree, err := vcs.CreateWithPrefix(context.Background(), repoRoot, instance.Worktree, "HEAD", "mivia/")
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := cli.ReadWorktreeMarker(worktree.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("marker before recovery = %v, want missing marker", err)
	}

	m := newReadyChatModel(30, 90)
	m.workspaceDir = repoRoot
	m.openWorktreeDialog()
	if m.worktreeDlg == nil || len(m.worktreeDlg.worktrees) != 1 {
		t.Fatalf("dialog rows = %#v", m.worktreeDlg)
	}
	if recovery, ok := m.worktreeDlg.selectedRecovery(); !ok || recovery.Info != (contextstate.WorktreeInstanceInfo{Instance: instance, CanonicalPath: expectedPath, State: contextstate.WorktreeCreating}) {
		t.Fatalf("creating recovery = %#v, %v", recovery, ok)
	}
	m.handleChatKey("enter", false)
	marker, err := cli.ReadWorktreeMarker(worktree.Path)
	if err != nil {
		t.Fatalf("marker after recovery = %v", err)
	}
	if marker != instance {
		t.Fatalf("recovered marker = %+v, want %+v", marker, instance)
	}
	if m.restartWorkspace != worktree.Path {
		t.Fatalf("restart workspace = %q, want %q after recovery", m.restartWorkspace, worktree.Path)
	}
	store, err = cli.OpenRepositoryContextStore(repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.ValidateActiveWorktreeInstance(context.Background(), principal, instance, expectedPath); err != nil {
		t.Fatalf("stored instance was not reused: %v", err)
	}
}

func TestWorktreeDialogRefusesCreatingRecoveryWhileWorkspaceSwitchIsBusy(t *testing.T) {
	tests := []struct {
		name       string
		waiting    bool
		cancelling bool
	}{
		{name: "waiting", waiting: true},
		{name: "cancelling", cancelling: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repoRoot := newWorktreeCommandRepo(t)
			writeWorktreeStoreConfig(t, repoRoot, "repository.db")
			store, err := cli.OpenRepositoryContextStore(repoRoot)
			if err != nil {
				t.Fatal(err)
			}
			principal, err := cli.WorktreeRoutePrincipal(repoRoot)
			if err != nil {
				t.Fatal(err)
			}
			instance := contextstate.WorktreeInstance{Worktree: "recover-create", ID: "wt_1234567890abcdef"}
			expectedPath := filepath.Join(workspace.WorktreesDir(repoRoot), instance.Worktree)
			if err := store.BeginWorktreeCreation(context.Background(), principal, instance, expectedPath); err != nil {
				t.Fatal(err)
			}
			worktree, err := vcs.CreateWithPrefix(context.Background(), repoRoot, instance.Worktree, "HEAD", "mivia/")
			if err != nil {
				t.Fatal(err)
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}

			m := newReadyChatModel(30, 90)
			m.workspaceDir = repoRoot
			m.waiting = test.waiting
			m.cancelling = test.cancelling
			m.openWorktreeDialog()
			m.handleChatKey("enter", false)

			if _, err := cli.ReadWorktreeMarker(worktree.Path); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("busy recovery marker error = %v, want missing marker", err)
			}
			if m.restartWorkspace != "" {
				t.Errorf("busy recovery restart workspace = %q, want empty", m.restartWorkspace)
			}
			if m.worktreeDlg == nil {
				t.Error("busy recovery closed the dialog")
			} else if m.worktreeDlg.notice != "cannot switch while agent is running" {
				t.Errorf("busy recovery notice = %q, want standard busy notice", m.worktreeDlg.notice)
			}
			store, err = cli.OpenRepositoryContextStore(repoRoot)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			creating, err := store.CreatingWorktreeInstance(context.Background(), principal, instance.Worktree)
			if err != nil || creating.Instance != instance {
				t.Errorf("busy recovery creation = %+v, %v", creating, err)
			}
		})
	}
}

func TestWorktreeDialogRecoveryUsesBorrowedSessionStore(t *testing.T) {
	repoRoot := newWorktreeCommandRepo(t)
	customStore, err := cli.OpenContextStorePath(filepath.Join(t.TempDir(), "custom.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer customStore.Close()
	worktree, err := cli.CreateManagedWorktreeInStore(customStore, repoRoot, "custom-delete", "HEAD", "mivia/")
	if err != nil {
		t.Fatal(err)
	}
	instance, err := cli.BeginManagedWorktreeRemovalInStore(customStore, repoRoot, worktree)
	if err != nil {
		t.Fatal(err)
	}

	m := newReadyChatModel(30, 90)
	if err := m.session.SetContextStore(customStore); err != nil {
		t.Fatal(err)
	}
	m.workspaceDir = repoRoot
	m.openWorktreeDialog()
	if m.worktreeDlg == nil || len(m.worktreeDlg.worktrees) != 1 {
		t.Fatalf("dialog rows = %#v", m.worktreeDlg)
	}
	if recovery, ok := m.worktreeDlg.selectedRecovery(); !ok || recovery.Info.Instance != instance || recovery.Info.State != contextstate.WorktreeDeleting {
		t.Fatalf("custom-store deleting recovery = %#v, %v", recovery, ok)
	}
	m.handleChatKey("enter", false)
	if m.restartWorkspace != "" {
		t.Fatalf("recovery row requested restart in %q", m.restartWorkspace)
	}
	m.handleChatKey("d", false)
	m.handleChatKey("y", false)

	principal, err := cli.WorktreeRoutePrincipal(repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	deleting, err := customStore.ListDeletingWorktreeInstances(context.Background(), principal)
	if err != nil {
		t.Fatal(err)
	}
	if len(deleting) != 0 {
		t.Fatalf("borrowed store still has deleting rows: %+v", deleting)
	}
	if err := customStore.ValidateActiveWorktreeInstance(context.Background(), principal, instance, worktree.Path); !errors.Is(err, contextstate.ErrWorktreeDeleted) {
		t.Fatalf("custom-store instance = %v, want deleted fence", err)
	}
	defaultStore, err := cli.OpenRepositoryContextStore(repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer defaultStore.Close()
	defaultDeleting, err := defaultStore.ListDeletingWorktreeInstances(context.Background(), principal)
	if err != nil {
		t.Fatal(err)
	}
	if len(defaultDeleting) != 0 {
		t.Fatalf("repository default store was changed: %+v", defaultDeleting)
	}
}

func TestWorktreeDialogRecoveryKeepsSameNameReplacementRow(t *testing.T) {
	repoRoot := newWorktreeCommandRepo(t)
	writeWorktreeStoreConfig(t, repoRoot, "repository.db")
	old, err := cli.CreateManagedWorktree(repoRoot, "recover-replacement", "HEAD", "mivia/")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cli.BeginManagedWorktreeRemoval(repoRoot, old); err != nil {
		t.Fatal(err)
	}
	if err := vcs.RemoveWithPrefix(context.Background(), repoRoot, old.Name, "mivia/"); err != nil {
		t.Fatal(err)
	}
	if _, err := vcs.Create(context.Background(), repoRoot, old.Name, "HEAD"); err != nil {
		t.Fatal(err)
	}
	m := newReadyChatModel(30, 90)
	m.workspaceDir = repoRoot
	m.openWorktreeDialog()
	if recovery, ok := m.worktreeDlg.selectedRecovery(); len(m.worktreeDlg.worktrees) != 1 || !ok || recovery.Info.State != contextstate.WorktreeDeleting {
		t.Fatalf("recovery row = %#v, recovery=%#v, ok=%v", m.worktreeDlg.worktrees, recovery, ok)
	}
	m.handleChatKey("d", false)
	m.handleChatKey("y", false)
	if m.worktreeDlg == nil || len(m.worktreeDlg.worktrees) != 1 {
		t.Fatalf("rows after recovery = %#v", m.worktreeDlg)
	}
	if _, ok := m.worktreeDlg.selectedRecovery(); ok {
		t.Fatalf("replacement row remains a recovery row: %#v", m.worktreeDlg.worktrees[0])
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
		wt:       &vcs.WorktreeInfo{Name: "wt-created", Path: worktreePath},
		instance: testWorktreeInstance("wt-created"),
		dlg:      issuingDialog,
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

	if !cli.WorktreeContainsCurrentDir(worktreePath) {
		t.Fatal("symbolic-link working directory must count as the current worktree")
	}
}

func TestWorktreeDialogFailsClosedWhenBorrowedStoreQueriesFail(t *testing.T) {
	repoRoot := newWorktreeCommandRepo(t)
	worktree, err := vcs.CreateWithPrefix(context.Background(), repoRoot, "closed-store", "HEAD", "mivia/")
	if err != nil {
		t.Fatal(err)
	}
	store, err := cli.OpenContextStorePath(filepath.Join(t.TempDir(), "closed.db"))
	if err != nil {
		t.Fatal(err)
	}
	m := newReadyChatModel(30, 90)
	if err := m.session.SetContextStore(store); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	m.workspaceDir = repoRoot
	m.openWorktreeDialog()
	if m.worktreeDlg == nil {
		t.Fatal("dialog is not open")
	}
	if !m.worktreeDlg.noticeErr || !strings.Contains(m.worktreeDlg.notice, "closed") {
		t.Errorf("dialog notice = %q, error = %v", m.worktreeDlg.notice, m.worktreeDlg.noticeErr)
	}
	for index, row := range m.worktreeDlg.worktrees {
		if row.Name == worktree.Name {
			m.worktreeDlg.cursor = index
			break
		}
	}
	m.handleChatKey("enter", false)
	if m.restartWorkspace != "" {
		t.Errorf("closed lifecycle store requested restart in %q", m.restartWorkspace)
	}
}

func TestWorktreeDialogSwitchesToUnmanagedReplacementAfterCleanup(t *testing.T) {
	repoRoot := newWorktreeCommandRepo(t)
	writeWorktreeStoreConfig(t, repoRoot, "repository.db")
	old, err := cli.CreateManagedWorktree(repoRoot, "replacement", "HEAD", "mivia/")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cli.BeginManagedWorktreeRemoval(repoRoot, old); err != nil {
		t.Fatal(err)
	}
	if err := vcs.RemoveWithPrefix(context.Background(), repoRoot, old.Name, "mivia/"); err != nil {
		t.Fatal(err)
	}
	replacement, err := vcs.Create(context.Background(), repoRoot, old.Name, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cli.ReadWorktreeMarker(replacement.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("replacement marker = %v, want missing marker", err)
	}
	recovered, err := cli.RecoverManagedWorktreeRemoval(repoRoot, old.Name, "mivia/")
	if err != nil || !recovered {
		t.Fatalf("recover removal = %v, %v", recovered, err)
	}
	m := newReadyChatModel(30, 90)
	m.workspaceDir = repoRoot
	m.openWorktreeDialog()
	if m.worktreeDlg == nil {
		t.Fatal("dialog is not open")
	}
	for index, row := range m.worktreeDlg.worktrees {
		if row.Name == replacement.Name {
			m.worktreeDlg.cursor = index
			break
		}
	}
	m.handleChatKey("enter", false)
	if m.restartWorkspace != replacement.Path {
		t.Errorf("restart workspace = %q, want %q", m.restartWorkspace, replacement.Path)
	}
	if m.worktreeDlg != nil {
		t.Errorf("unmanaged replacement did not close the dialog: %q", m.worktreeDlg.notice)
	}
}

func TestWorktreeDialogSwitchesToValidManagedWorktree(t *testing.T) {
	repoRoot := newWorktreeCommandRepo(t)
	writeWorktreeStoreConfig(t, repoRoot, "repository.db")
	worktree, err := cli.CreateManagedWorktree(repoRoot, "managed-switch", "HEAD", "mivia/")
	if err != nil {
		t.Fatal(err)
	}
	m := newReadyChatModel(30, 90)
	m.workspaceDir = repoRoot
	m.openWorktreeDialog()
	if m.worktreeDlg == nil {
		t.Fatal("dialog is not open")
	}
	for index, row := range m.worktreeDlg.worktrees {
		if row.Name == worktree.Name {
			m.worktreeDlg.cursor = index
			break
		}
	}
	m.handleChatKey("enter", false)
	if m.restartWorkspace != worktree.Path {
		t.Fatalf("restart workspace = %q, want %q", m.restartWorkspace, worktree.Path)
	}
}

func TestWorktreeDialogReactivatesBorrowedStoreAfterGitRemovalFailure(t *testing.T) {
	repoRoot := newWorktreeCommandRepo(t)
	customStore, err := cli.OpenContextStorePath(filepath.Join(t.TempDir(), "custom.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer customStore.Close()
	worktree, err := cli.CreateManagedWorktreeInStore(customStore, repoRoot, "reactivate-custom", "HEAD", "mivia/")
	if err != nil {
		t.Fatal(err)
	}
	configText := worktreeStoreConfig("default.db") + "\n[worktrees]\nbranch_prefix = \"mivia/\"\n"
	if err := os.WriteFile(filepath.Join(repoRoot, ".mivia", "mivia.toml"), []byte(configText), 0o600); err != nil {
		t.Fatal(err)
	}
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	binDir := t.TempDir()
	var gitWrapper string
	var wrapperPath string
	if runtime.GOOS == "windows" {
		// Git is resolved via PATHEXT, so the shim must carry a .cmd extension
		// to intercept lookups. %* preserves the original command line.
		wrapperPath = filepath.Join(binDir, "git.cmd")
		gitWrapper = "@echo off\r\nif \"%1\"==\"worktree\" if \"%2\"==\"remove\" exit /b 1\r\n\"" + gitPath + "\" %*\r\n"
	} else {
		wrapperPath = filepath.Join(binDir, "git")
		gitWrapper = "#!/bin/sh\nif [ \"$1\" = worktree ] && [ \"$2\" = remove ]; then exit 1; fi\nexec \"" + gitPath + "\" \"$@\"\n"
	}
	if err := os.WriteFile(wrapperPath, []byte(gitWrapper), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	m := newReadyChatModel(30, 90)
	if err := m.session.SetContextStore(customStore); err != nil {
		t.Fatal(err)
	}
	m.workspaceDir = repoRoot
	m.worktreeDlg = newWorktreeDialog([]vcs.WorktreeInfo{*worktree})
	m.worktreeDlg.confirm = wtConfirmDelete
	m.applyWorktreeConfirm()
	principal, err := cli.WorktreeRoutePrincipal(repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	deleting, err := customStore.ListDeletingWorktreeInstances(context.Background(), principal)
	if err != nil {
		t.Fatal(err)
	}
	if len(deleting) != 0 {
		t.Errorf("custom store deleting rows = %d, want 0", len(deleting))
	}
	instance, err := cli.ReadWorktreeMarker(worktree.Path)
	if err != nil {
		t.Fatal(err)
	}
	if err := customStore.ValidateActiveWorktreeInstance(context.Background(), principal, instance, worktree.Path); err != nil {
		t.Errorf("custom store instance is not active: %v", err)
	}
	defaultStore, err := cli.OpenRepositoryContextStore(repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer defaultStore.Close()
	defaultDeleting, err := defaultStore.ListDeletingWorktreeInstances(context.Background(), principal)
	if err != nil {
		t.Fatal(err)
	}
	defaultCreating, err := defaultStore.ListCreatingWorktreeInstances(context.Background(), principal)
	if err != nil {
		t.Fatal(err)
	}
	if len(defaultDeleting) != 0 || len(defaultCreating) != 0 {
		t.Errorf("default store lifecycle rows = deleting %d, creating %d", len(defaultDeleting), len(defaultCreating))
	}
}
