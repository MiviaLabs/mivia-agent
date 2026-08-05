package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/vcs"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
)

// The tests in this file cover the branches the diff-coverage gate requires:
// CLI dispatch, dialog routing, status chrome, history paging edges, and the
// worktree dialog's error paths.

func TestExecuteWorkflowsSubcommand(t *testing.T) {
	err := Execute([]string{"workflows"})
	if err == nil || !strings.Contains(err.Error(), "expected list, show, validate, or explain") {
		t.Fatalf("Execute(workflows) = %v, want a usage error", err)
	}
}

func TestRouteModalKeyModelAndAgentDialogs(t *testing.T) {
	m := newReadyChatModel(30, 90)
	m.modelDlg = newModelDialog(nil, chat.Selection{}, "", false)
	handled, _, _ := m.handleChatKey("j", false)
	if !handled {
		t.Fatal("model dialog must consume keys")
	}
	m.modelDlg = nil
	m.agentDlg = newAgentDialog(nil, false)
	handled, _, _ = m.handleChatKey("j", false)
	if !handled {
		t.Fatal("agent dialog must consume keys")
	}
}

func TestUpdateWorktreeCreatedMsg(t *testing.T) {
	m := newReadyChatModel(30, 90)
	openWorktreeDialogOnModel(m, 0)
	dlg := m.worktreeDlg
	model, _ := m.Update(worktreeCreatedMsg{
		wt:       &vcs.WorktreeInfo{Name: "wt-x", Path: "/tmp/wt-x", Branch: "main"},
		instance: testWorktreeInstance("wt-x"),
		err:      nil,
		dlg:      dlg,
	})
	after := model.(*tuiModel)
	if after.worktreeDlg == nil || len(after.worktreeDlg.worktrees) != 1 {
		t.Fatalf("worktreeCreatedMsg via Update must append the worktree: %+v", after.worktreeDlg)
	}
}

func TestUpdateWelcomeCtrlCQuits(t *testing.T) {
	m := newReadyChatModel(30, 90)
	m.mode = modeWelcome
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd == nil {
		t.Fatal("ctrl+c on the welcome screen must produce a quit command")
	}
}

func TestUpdateChatModeReachesFootDrain(t *testing.T) {
	m := newReadyChatModel(30, 90)
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	// Any cmd (or none) is fine; the point is the Update path runs to the end
	// without panicking and the bridge drain executes.
	_ = cmd
}

func TestBrandWorktreeChrome(t *testing.T) {
	withANSI256(t)
	work := renderWorkChrome(0, phaseStreaming, "m", time.Second, 0, 0, 0, 0, 60, "", "master", "wt-x")
	if !strings.Contains(work, "⊞ wt-x") {
		t.Fatalf("renderWorkChrome must show the worktree: %q", work)
	}
	status := renderStatusBar(0, phaseIdle, "m", false, time.Second, 0, 0, 0, 0, 3, 60, "", "master", "wt-x")
	if !strings.Contains(status, "⊞ wt-x") {
		t.Fatalf("renderStatusBar must show the worktree: %q", status)
	}
}

func TestSlashWorktreesOpensDialog(t *testing.T) {
	root := t.TempDir()
	runGit(t, root, "init")
	runGit(t, root, "config", "user.email", "test@example.com")
	runGit(t, root, "config", "user.name", "test")
	runGit(t, root, "commit", "--allow-empty", "-m", "init")
	m := newReadyChatModel(30, 90)
	m.workspaceDir = root
	if !m.handleSlash("/worktrees") {
		t.Fatal("handleSlash must handle /worktrees")
	}
	if m.worktreeDlg == nil {
		t.Fatal("dialog must open after /worktrees")
	}
}

func TestWorktreeDialogCoverageBranches(t *testing.T) {
	// clampScroll with an explicit visible count.
	d := newWorktreeDialog([]vcs.WorktreeInfo{{Name: "a"}, {Name: "b"}, {Name: "c"}})
	d.clampScroll(2)

	// clampScrollTo clamps a negative scroll to 0 (visible=10 leaves scroll
	// below zero so the final clamp branch fires).
	d = newWorktreeDialog([]vcs.WorktreeInfo{{Name: "a"}})
	d.scroll = -3
	d.clampScrollTo(10)
	if d.scroll != 0 {
		t.Fatalf("scroll = %d, want 0 after clamp", d.scroll)
	}

	// rowLines reserves a row when more worktrees exist below the page.
	d = newWorktreeDialog([]vcs.WorktreeInfo{{Name: "a"}, {Name: "b"}, {Name: "c"}, {Name: "d"}, {Name: "e"}})
	rows := d.rowLines(50, 2)
	if len(rows) != 1 {
		t.Fatalf("rowLines with pageH=2 and 5 rows must reserve a footer row, got %d", len(rows))
	}

	// applyWorktreeConfirm with delete confirmed but nothing selected: inert.
	m2 := newReadyChatModel(30, 90)
	m2.worktreeDlg = newWorktreeDialog(nil)
	m2.worktreeDlg.confirm = wtConfirmDelete
	m2.applyWorktreeConfirm()
	if m2.worktreeDlg.confirm != wtConfirmNone {
		t.Fatal("confirm must reset even when nothing is selected")
	}

	// resolveWorkspaceDir with an empty workspaceDir uses the cwd.
	m := newReadyChatModel(30, 90)
	m.workspaceDir = ""
	if got := m.resolveWorkspaceDir(); got == "" {
		t.Fatal("resolveWorkspaceDir must fall back to the cwd")
	}

	// resolveRepoRoot falls back to the workspace dir when it is not a repo.
	m.workspaceDir = t.TempDir()
	if got := m.resolveRepoRoot(); got != m.workspaceDir {
		t.Fatalf("resolveRepoRoot = %q, want the fallback %q", got, m.workspaceDir)
	}
}

func TestOpenWorktreeDialogListError(t *testing.T) {
	m := newReadyChatModel(30, 90)
	m.workspaceDir = t.TempDir() // not a git repo
	m.openWorktreeDialog()
	if m.worktreeDlg == nil {
		t.Fatal("dialog must open with an error notice")
	}
	if m.worktreeDlg.notice == "" {
		t.Fatal("dialog must show the list error")
	}
}

func TestWorktreeDialogDeleteFailedNotice(t *testing.T) {
	m := newReadyChatModel(30, 90)
	// Seed a row whose worktree does not exist on disk, then confirm delete.
	m.worktreeDlg = newWorktreeDialog([]vcs.WorktreeInfo{{Name: "ghost", Path: "/nonexistent", Branch: "x"}})
	m.worktreeDlg.cursor = 0
	m.worktreeDlg.confirm = wtConfirmDelete
	m.applyWorktreeConfirm()
	if !strings.Contains(m.worktreeDlg.notice, "delete failed") {
		t.Fatalf("notice = %q, want delete failed", m.worktreeDlg.notice)
	}
}

func TestSwitchToWorktreeWaiting(t *testing.T) {
	m := newReadyChatModel(30, 90)
	openWorktreeDialogOnModel(m, 1)
	m.waiting = true
	wt, _ := m.worktreeDlg.selected()
	m.switchToWorktree(wt)
	if !strings.Contains(m.worktreeDlg.notice, "cannot switch") {
		t.Fatalf("notice = %q, want a refusal", m.worktreeDlg.notice)
	}
}

func TestWorktreeDialogWheelAndClampModalState(t *testing.T) {
	m := newReadyChatModel(30, 90)
	openWorktreeDialogOnModel(m, 3)
	// WindowSizeMsg runs clampModalState with the worktree dialog open.
	_, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	// A wheel event while the dialog is open drives its navigation branch.
	_, _ = m.Update(tea.MouseMsg{Type: tea.MouseWheelUp})
	if m.worktreeDlg == nil {
		t.Fatal("dialog must remain open")
	}
}

func TestSwitchToMainTreeWaiting(t *testing.T) {
	m := newReadyChatModel(30, 90)
	openWorktreeDialogOnModel(m, 1)
	m.waiting = true
	m.switchToMainTree()
	if !strings.Contains(m.worktreeDlg.notice, "cannot switch") {
		t.Fatalf("notice = %q, want a refusal", m.worktreeDlg.notice)
	}
}

func TestRestoreSessionDirChdirFailure(t *testing.T) {
	m := newReadyChatModel(30, 90)
	// Dir that exists but is a regular file: os.Chdir fails with ENOTDIR.
	file := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	m.restoreSessionDir(file)
	found := false
	for _, msg := range m.messages {
		if strings.Contains(msg, "switch failed") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a switch-failed notice, messages: %q", m.messages)
	}
}

// makeOffsetSession builds a session with n user/assistant messages plus a
// tool-only message that hydrates to no visible block, for history paging.
func makeOffsetSession(n int) *chat.Session {
	sess := &chat.Session{Messages: make([]provider.Message, 0, n+1)}
	for i := 0; i < n; i++ {
		role := provider.RoleUser
		if i%2 == 1 {
			role = provider.RoleAssistant
		}
		sess.Messages = append(sess.Messages, provider.Message{Role: role, Content: "line-" + string(rune('a'+i%26))})
	}
	// A tool message hydrates to no chat block: exercises the continue branch.
	sess.Messages = append(sess.Messages, provider.Message{Role: provider.RoleTool, Content: "tool output"})
	return sess
}

func TestLoadMoreMessagesOffsetZero(t *testing.T) {
	m := &tuiModel{
		session:   makeSession(5),
		msgOffset: 0,
		width:     80,
		height:    24,
		viewport:  viewport.New(80, 12),
	}
	m.loadMoreMessages() // must return immediately
	if m.msgOffset != 0 {
		t.Fatalf("msgOffset = %d, want 0", m.msgOffset)
	}
}

func TestLoadMoreMessagesToolOnlyTail(t *testing.T) {
	sess := makeOffsetSession(3)
	m := &tuiModel{
		session:   sess,
		msgOffset: 4,
		width:     80,
		height:    24,
		viewport:  viewport.New(80, 12),
	}
	m.loadMoreMessages()
	// The tool message hydrates to nothing; the remaining messages still load.
	if m.msgOffset >= 4 {
		t.Fatalf("msgOffset = %d, want < 4 after load", m.msgOffset)
	}
}

func TestLoadMoreMessagesSkipsSystemMessage(t *testing.T) {
	// A system-only message hydrates to no blocks: the loop continues and the
	// empty batch marks everything as loaded.
	sess := &chat.Session{Messages: []provider.Message{{Role: provider.RoleSystem, Content: "sys"}}}
	m := &tuiModel{
		session:   sess,
		msgOffset: 1,
		width:     80,
		height:    24,
		viewport:  viewport.New(80, 12),
	}
	m.loadMoreMessages()
	if m.msgOffset != 0 {
		t.Fatalf("msgOffset = %d, want 0 after loading only a system message", m.msgOffset)
	}
}

func TestLoadMoreMessagesRemovesShowingLastNotice(t *testing.T) {
	// hydrateHistory opens a long transcript with a "showing last N" notice;
	// once loadMore reaches offset 0 the notice block must be dropped.
	sess := &chat.Session{Messages: []provider.Message{{Role: provider.RoleUser, Content: "hi"}}}
	m := &tuiModel{
		session: sess,
		blocks: []ChatBlock{{
			Kind: ChatBlockSystem,
			Text: "  (showing last 5 messages, scroll up for more)",
		}},
		msgOffset: 1,
		width:     80,
		height:    24,
		viewport:  viewport.New(80, 12),
	}
	m.messages = m.renderBlocksForView().Lines
	m.loadMoreMessages()
	for _, b := range m.blocks {
		if strings.Contains(b.Text, "showing last") {
			t.Fatalf("notice must be removed after loading everything: %+v", m.blocks)
		}
	}
}

func TestVisualLineCount(t *testing.T) {
	if got := visualLineCount([]string{"a\nb", "c"}); got != 3 {
		t.Fatalf("visualLineCount = %d, want 3", got)
	}
	if got := visualLineCount(nil); got != 0 {
		t.Fatalf("visualLineCount(nil) = %d, want 0", got)
	}
}

// --- workflows CLI error branches ---

func TestRunWorkflowsNoArgs(t *testing.T) {
	var out, errOut strings.Builder
	if err := runWorkflowsWithIO([]string{}, &out, &errOut); err == nil {
		t.Fatal("workflows with no subcommand must error")
	}
}

// TestRunWorkflowsFallbackWorkspace covers the "." workspace fallback in
// list and validate when no --workspace flag is given.
func TestRunWorkflowsFallbackWorkspace(t *testing.T) {
	var out, errOut strings.Builder
	if err := runWorkflowsWithIO([]string{"list"}, &out, &errOut); err != nil {
		t.Fatalf("list with default workspace: %v", err)
	}
	out.Reset()
	errOut.Reset()
	if err := runWorkflowsWithIO([]string{"validate"}, &out, &errOut); err != nil {
		t.Fatalf("validate with default workspace: %v", err)
	}
}

func TestRunWorkflowsListErrorWithFileWorkspace(t *testing.T) {
	var out, errOut strings.Builder
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := runWorkflowsWithIO([]string{"list", "--workspace", file}, &out, &errOut)
	if err == nil || !strings.Contains(err.Error(), "workflows list:") {
		t.Fatalf("list with file workspace = %v, want a list error", err)
	}
}

func TestRunWorkflowsShowErrorBranches(t *testing.T) {
	var out, errOut strings.Builder
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := runWorkflowsWithIO([]string{"show", "x", "--workspace", file}, &out, &errOut); err == nil {
		t.Fatal("show with file workspace must error")
	}
	// A parse error in the discovered workflow.
	root := t.TempDir()
	writeWorkflowFixture(t, root, "broken", "version = 1\nname = [not a string")
	if err := runWorkflowsWithIO([]string{"show", "broken", "--workspace", root}, &out, &errOut); err == nil {
		t.Fatal("show with invalid TOML must error")
	}
	// A compile error: initial step that does not exist.
	root2 := t.TempDir()
	writeWorkflowFixture(t, root2, "bad", "version = 1\nname = \"bad\"\ninitial_step = \"missing\"\n\n[[steps]]\nid = \"plan\"\nkind = \"agent\"\nagent = \"planner\"\n")
	if err := runWorkflowsWithIO([]string{"show", "bad", "--workspace", root2}, &out, &errOut); err == nil {
		t.Fatal("show with a compile error must error")
	}
}

func TestRunWorkflowsValidateErrorBranches(t *testing.T) {
	var out, errOut strings.Builder
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := runWorkflowsWithIO([]string{"validate", "--workspace", file}, &out, &errOut); err == nil {
		t.Fatal("validate with file workspace must error")
	}
	// An invalid workflow sets hasError and the run reports it.
	root := t.TempDir()
	writeWorkflowFixture(t, root, "bad", "version = 1\nname = \"bad\"\ninitial_step = \"missing\"\n\n[[steps]]\nid = \"plan\"\nkind = \"agent\"\nagent = \"planner\"\n")
	err := runWorkflowsWithIO([]string{"validate", "--workspace", root}, &out, &errOut)
	if err == nil || !strings.Contains(err.Error(), "one or more workflows are invalid") {
		t.Fatalf("validate with invalid workflow = %v", err)
	}
}

func TestRunWorkflowsExplainErrorBranches(t *testing.T) {
	var out, errOut strings.Builder
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := runWorkflowsWithIO([]string{"explain", "x", "--workspace", file}, &out, &errOut); err == nil {
		t.Fatal("explain with file workspace must error")
	}
	root := t.TempDir()
	writeWorkflowFixture(t, root, "bad", "version = 1\nname = \"bad\"\ninitial_step = \"missing\"\n\n[[steps]]\nid = \"plan\"\nkind = \"agent\"\nagent = \"planner\"\n")
	if err := runWorkflowsWithIO([]string{"explain", "bad", "--workspace", root}, &out, &errOut); err == nil {
		t.Fatal("explain with a compile error must error")
	}
}

// TestWorkflowsExplainOutputSchemaReference covers the schema: reference branch
// of buildExplainView (workflows_command.go).
func TestWorkflowsExplainOutputSchemaReference(t *testing.T) {
	root := t.TempDir()
	writeWorkflowFixture(t, root, "w", `version = 1
name = "w"
initial_step = "plan"

[[steps]]
id = "plan"
kind = "agent"
agent = "planner"
template = "templates/plan.md"
output_schema = "schemas/plan.json"

[[steps]]
id = "review"
kind = "evidence_gate"
verifier = "go-test"

[[transitions]]
from = "plan"
to = "review"
match = { status = "succeeded" }

[[transitions]]
from = "review"
to = "success"
match = { status = "succeeded" }
`)
	var out, errOut strings.Builder
	if err := runWorkflowsWithIO([]string{"explain", "w", "--workspace", root}, &out, &errOut); err != nil {
		t.Fatalf("explain with output_schema step: %v", err)
	}
	if !strings.Contains(out.String(), "schema: schemas/plan.json") {
		t.Fatalf("explain output must list the schema reference, got: %s", out.String())
	}
}
