package legacytui

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/cliworktree"

	"github.com/MiviaLabs/mivia-agent/internal/vcs"
)

func TestDialogCoverageLifecycleStoreOpenError(t *testing.T) {
	m := newReadyChatModel(30, 90)
	m.session = nil
	if _, _, err := m.worktreeLifecycleStore(blockedContextRoot(t)); err == nil {
		t.Fatal("blocked lifecycle store opened")
	}
}

func TestDialogCoverageInjectedCanonicalRootFailure(t *testing.T) {
	repo := newWorktreeCommandRepo(t)
	worktree, err := cliworktree.CreateManagedWorktree(repo, "canonical-failure", "HEAD", "mivia/")
	if err != nil {
		t.Fatal(err)
	}
	m := newReadyChatModel(30, 90)
	m.workspaceDir = repo
	original := cliworktree.CanonicalWorktreeDialogRoot
	cliworktree.CanonicalWorktreeDialogRoot = func(string) (string, error) {
		return "", errors.New("canonical failure")
	}
	t.Cleanup(func() { cliworktree.CanonicalWorktreeDialogRoot = original })
	if _, err := m.validateWorktreeSwitch(*worktree); err == nil {
		t.Fatal("injected canonical switch failure succeeded")
	}
	if err := os.Remove(cliworktree.WorktreeMarkerPath(worktree.Path)); err != nil {
		t.Fatal(err)
	}
	if err := m.validateWorktreeWithoutMarker(*worktree); err == nil {
		t.Fatal("injected markerless canonical failure succeeded")
	}
}

func TestDialogCoverageInjectedSwitchStatFailures(t *testing.T) {
	repo := newWorktreeCommandRepo(t)
	worktree, err := cliworktree.CreateManagedWorktree(repo, "stat-failure", "HEAD", "mivia/")
	if err != nil {
		t.Fatal(err)
	}
	m := newReadyChatModel(30, 90)
	m.workspaceDir = repo
	m.worktreeDlg = newWorktreeDialog([]vcs.WorktreeInfo{*worktree})
	original := cliworktree.StatWorktreeSwitchPath
	cliworktree.StatWorktreeSwitchPath = func(string) (os.FileInfo, error) {
		return nil, errors.New("stat failure")
	}
	m.switchToWorktree(*worktree)
	if !strings.Contains(m.worktreeDlg.notice, "stat failure") {
		t.Fatalf("switch stat notice = %q", m.worktreeDlg.notice)
	}
	regular := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(regular, []byte("file"), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(regular)
	if err != nil {
		t.Fatal(err)
	}
	cliworktree.StatWorktreeSwitchPath = func(string) (os.FileInfo, error) { return info, nil }
	t.Cleanup(func() { cliworktree.StatWorktreeSwitchPath = original })
	m.switchToWorktree(*worktree)
	if !strings.Contains(m.worktreeDlg.notice, "not a directory") {
		t.Fatalf("switch non-directory notice = %q", m.worktreeDlg.notice)
	}
}

func TestDialogCoverageInjectedPathUtilityFailures(t *testing.T) {
	originalAbs := cliworktree.AbsWorktreeSwitchPath
	originalEval := cliworktree.EvalWorktreeSwitchPath
	originalGetwd := cliworktree.GetwdWorktreeSwitch
	originalRel := cliworktree.RelWorktreeSwitchPath
	t.Cleanup(func() {
		cliworktree.AbsWorktreeSwitchPath = originalAbs
		cliworktree.EvalWorktreeSwitchPath = originalEval
		cliworktree.GetwdWorktreeSwitch = originalGetwd
		cliworktree.RelWorktreeSwitchPath = originalRel
	})
	m := newReadyChatModel(30, 90)
	m.worktreeDlg = newWorktreeDialog(nil)
	cliworktree.AbsWorktreeSwitchPath = func(string) (string, error) { return "", errors.New("absolute failure") }
	m.restartInWorkspace("relative")
	if !strings.Contains(m.worktreeDlg.notice, "absolute failure") {
		t.Fatalf("absolute restart notice = %q", m.worktreeDlg.notice)
	}
	if !cliworktree.WorktreeContainsCurrentDir("relative") {
		t.Fatal("absolute containment failure did not fail closed")
	}
	cliworktree.AbsWorktreeSwitchPath = originalAbs
	cliworktree.EvalWorktreeSwitchPath = func(string) (string, error) { return "", errors.New("evaluation failure") }
	if !cliworktree.WorktreeContainsCurrentDir(t.TempDir()) {
		t.Fatal("current-directory evaluation failure did not fail closed")
	}
	cliworktree.EvalWorktreeSwitchPath = originalEval
	cliworktree.RelWorktreeSwitchPath = func(string, string) (string, error) { return "", errors.New("relative failure") }
	if !cliworktree.WorktreeContainsCurrentDir(t.TempDir()) {
		t.Fatal("relative-path failure did not fail closed")
	}
}
