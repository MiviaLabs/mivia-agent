package cli

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
	worktree, err := createManagedWorktree(repo, "canonical-failure", "HEAD", "mivia/")
	if err != nil {
		t.Fatal(err)
	}
	m := newReadyChatModel(30, 90)
	m.workspaceDir = repo
	original := canonicalWorktreeDialogRoot
	canonicalWorktreeDialogRoot = func(string) (string, error) {
		return "", errors.New("canonical failure")
	}
	t.Cleanup(func() { canonicalWorktreeDialogRoot = original })
	if _, err := m.validateWorktreeSwitch(*worktree); err == nil {
		t.Fatal("injected canonical switch failure succeeded")
	}
	if err := os.Remove(worktreeMarkerPath(worktree.Path)); err != nil {
		t.Fatal(err)
	}
	if err := m.validateWorktreeWithoutMarker(*worktree); err == nil {
		t.Fatal("injected markerless canonical failure succeeded")
	}
}

func TestDialogCoverageInjectedSwitchStatFailures(t *testing.T) {
	repo := newWorktreeCommandRepo(t)
	worktree, err := createManagedWorktree(repo, "stat-failure", "HEAD", "mivia/")
	if err != nil {
		t.Fatal(err)
	}
	m := newReadyChatModel(30, 90)
	m.workspaceDir = repo
	m.worktreeDlg = newWorktreeDialog([]vcs.WorktreeInfo{*worktree})
	original := statWorktreeSwitchPath
	statWorktreeSwitchPath = func(string) (os.FileInfo, error) {
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
	statWorktreeSwitchPath = func(string) (os.FileInfo, error) { return info, nil }
	t.Cleanup(func() { statWorktreeSwitchPath = original })
	m.switchToWorktree(*worktree)
	if !strings.Contains(m.worktreeDlg.notice, "not a directory") {
		t.Fatalf("switch non-directory notice = %q", m.worktreeDlg.notice)
	}
}

func TestDialogCoverageInjectedPathUtilityFailures(t *testing.T) {
	originalAbs := absWorktreeSwitchPath
	originalEval := evalWorktreeSwitchPath
	originalGetwd := getwdWorktreeSwitch
	originalRel := relWorktreeSwitchPath
	t.Cleanup(func() {
		absWorktreeSwitchPath = originalAbs
		evalWorktreeSwitchPath = originalEval
		getwdWorktreeSwitch = originalGetwd
		relWorktreeSwitchPath = originalRel
	})
	m := newReadyChatModel(30, 90)
	m.worktreeDlg = newWorktreeDialog(nil)
	absWorktreeSwitchPath = func(string) (string, error) { return "", errors.New("absolute failure") }
	m.restartInWorkspace("relative")
	if !strings.Contains(m.worktreeDlg.notice, "absolute failure") {
		t.Fatalf("absolute restart notice = %q", m.worktreeDlg.notice)
	}
	if !worktreeContainsCurrentDir("relative") {
		t.Fatal("absolute containment failure did not fail closed")
	}
	absWorktreeSwitchPath = originalAbs
	evalWorktreeSwitchPath = func(string) (string, error) { return "", errors.New("evaluation failure") }
	if !worktreeContainsCurrentDir(t.TempDir()) {
		t.Fatal("current-directory evaluation failure did not fail closed")
	}
	evalWorktreeSwitchPath = originalEval
	relWorktreeSwitchPath = func(string, string) (string, error) { return "", errors.New("relative failure") }
	if !worktreeContainsCurrentDir(t.TempDir()) {
		t.Fatal("relative-path failure did not fail closed")
	}
}
