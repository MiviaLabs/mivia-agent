package cliworktree

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
)

func TestMarkerCoverageInjectedPublishFailures(t *testing.T) {
	repo := newWorktreeCommandRepo(t)
	instance := contextstate.WorktreeInstance{Worktree: "wt-a", ID: "wt_1234567890abcdef"}
	originalWrite := writeWorktreeMarkerTemp
	originalClose := closeWorktreeMarkerTemp
	originalRename := renameWorktreeMarker
	writeWorktreeMarkerTemp = func(*os.File, []byte) (int, error) {
		return 0, errors.New("write failure")
	}
	if err := WriteWorktreeMarker(repo, instance); err == nil || !strings.Contains(err.Error(), "write worktree marker") {
		t.Fatalf("injected marker write error = %v", err)
	}
	writeWorktreeMarkerTemp = originalWrite
	closeWorktreeMarkerTemp = func(file *os.File) error {
		_ = file.Close()
		return errors.New("close failure")
	}
	if err := WriteWorktreeMarker(repo, instance); err == nil || !strings.Contains(err.Error(), "close worktree marker") {
		t.Fatalf("injected marker close error = %v", err)
	}
	closeWorktreeMarkerTemp = originalClose
	renameWorktreeMarker = func(string, string) error { return errors.New("rename failure") }
	t.Cleanup(func() {
		writeWorktreeMarkerTemp = originalWrite
		closeWorktreeMarkerTemp = originalClose
		renameWorktreeMarker = originalRename
	})
	if err := WriteWorktreeMarker(repo, instance); err == nil || !strings.Contains(err.Error(), "publish worktree marker") {
		t.Fatalf("injected marker publish error = %v", err)
	}
}

func TestMarkerCoverageInjectedOpenFailure(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".mivia"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(WorktreeMarkerPath(root), []byte(`{"version":1,"worktree":"wt-a","id":"wt_1234567890abcdef"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	original := openWorktreeMarkerFile
	openWorktreeMarkerFile = func(*os.Root, string) (*os.File, error) {
		return nil, errors.New("open failure")
	}
	t.Cleanup(func() { openWorktreeMarkerFile = original })
	if _, err := ReadWorktreeMarker(root); err == nil || !strings.Contains(err.Error(), "read worktree marker") {
		t.Fatalf("injected marker open error = %v", err)
	}
}

func TestMarkerCoverageGitInfoDirFirstUseRace(t *testing.T) {
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	originalLstat, originalMkdir := lstatGitInfoDir, mkdirGitInfoDir
	t.Cleanup(func() {
		lstatGitInfoDir, mkdirGitInfoDir = originalLstat, originalMkdir
	})
	realDir := t.TempDir()
	dirInfo, err := os.Lstat(realDir)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	lstatGitInfoDir = func(*os.Root, string) (os.FileInfo, error) {
		calls++
		if calls == 1 {
			return nil, os.ErrNotExist
		}
		return dirInfo, nil
	}
	mkdirGitInfoDir = func(*os.Root, string, os.FileMode) error { return os.ErrExist }
	if err := ensureRegularGitInfoDir(root); err != nil {
		t.Fatalf("concurrent first-use Git info directory creation failed: %v", err)
	}
}

func TestMarkerCoverageGitInfoDirFirstUseRaceKeepsFailClosed(t *testing.T) {
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	originalLstat, originalMkdir := lstatGitInfoDir, mkdirGitInfoDir
	t.Cleanup(func() {
		lstatGitInfoDir, mkdirGitInfoDir = originalLstat, originalMkdir
	})
	planted := filepath.Join(t.TempDir(), "planted")
	if err := os.Symlink("elsewhere", planted); err != nil {
		t.Fatal(err)
	}
	symlinkInfo, err := os.Lstat(planted)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	lstatGitInfoDir = func(*os.Root, string) (os.FileInfo, error) {
		calls++
		if calls == 1 {
			return nil, os.ErrNotExist
		}
		return symlinkInfo, nil
	}
	mkdirGitInfoDir = func(*os.Root, string, os.FileMode) error { return os.ErrExist }
	if err := ensureRegularGitInfoDir(root); err == nil {
		t.Fatal("concurrent symlink planting at the Git info directory was accepted")
	}
}

func TestMarkerCoverageGitInfoDirMkdirError(t *testing.T) {
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	originalLstat, originalMkdir := lstatGitInfoDir, mkdirGitInfoDir
	t.Cleanup(func() {
		lstatGitInfoDir, mkdirGitInfoDir = originalLstat, originalMkdir
	})
	lstatGitInfoDir = func(*os.Root, string) (os.FileInfo, error) { return nil, os.ErrNotExist }
	sentinel := errors.New("Git info mkdir fault")
	mkdirGitInfoDir = func(*os.Root, string, os.FileMode) error { return sentinel }
	if err := ensureRegularGitInfoDir(root); !errors.Is(err, sentinel) {
		t.Fatalf("Git info mkdir fault error = %v", err)
	}
}

func TestMarkerExcludeLockRejectsFinalSymlinkOpen(t *testing.T) {
	base := t.TempDir()
	if err := os.MkdirAll(filepath.Join(base, "info"), 0o700); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(base)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	lockPath := filepath.Join("info", "exclude.lock")
	if err := os.Symlink("elsewhere", filepath.Join(base, lockPath)); err != nil {
		t.Fatal(err)
	}
	file, err := openMarkerExcludeLock(root, lockPath)
	if file != nil {
		_ = file.Close()
	}
	if err == nil {
		t.Fatal("Git exclude lock opened through a final-component symlink")
	}
}

func TestMarkerFaultExcludeLockOpenError(t *testing.T) {
	base := t.TempDir()
	if err := os.MkdirAll(filepath.Join(base, "info"), 0o700); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(base)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	sentinel := errors.New("exclude lock open fault")
	original := openMarkerExcludeLock
	openMarkerExcludeLock = func(*os.Root, string) (*os.File, error) { return nil, sentinel }
	t.Cleanup(func() { openMarkerExcludeLock = original })
	unlock, err := lockWorktreeMarkerExclude(root, filepath.Join("info", "exclude.lock"))
	if unlock != nil {
		unlock()
	}
	if !errors.Is(err, sentinel) {
		t.Fatalf("exclude lock open fault error = %v", err)
	}
}

func TestMarkerFaultExcludeLockClosedRoot(t *testing.T) {
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := root.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := openMarkerExcludeLock(root, filepath.Join("info", "exclude.lock")); err == nil {
		t.Fatal("closed root opened the Git exclude lock")
	}
}

func TestMarkerFaultExcludeLockStatError(t *testing.T) {
	base := t.TempDir()
	if err := os.MkdirAll(filepath.Join(base, "info"), 0o700); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(base)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	sentinel := errors.New("exclude lock stat fault")
	original := statWorktreeMarkerFile
	statWorktreeMarkerFile = func(*os.File) (os.FileInfo, error) { return nil, sentinel }
	t.Cleanup(func() { statWorktreeMarkerFile = original })
	unlock, err := lockWorktreeMarkerExclude(root, filepath.Join("info", "exclude.lock"))
	if unlock != nil {
		unlock()
	}
	if !errors.Is(err, sentinel) {
		t.Fatalf("exclude lock stat fault error = %v", err)
	}
}
