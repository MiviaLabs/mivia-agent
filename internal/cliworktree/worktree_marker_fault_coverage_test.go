package cliworktree

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
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

// TestMarkerPublishRetriesTransientRenameContention pins the publish retry
// contract deterministically on every platform. Windows fails a rename over
// a file a concurrent writer is renaming onto, so the publish must absorb a
// transient failure within the attempt budget, surface a persistent one
// after the budget, and never retry when the platform does not need it.
// Without this test the contract is exercised only by intermittent Windows
// CI contention.
func TestMarkerPublishRetriesTransientRenameContention(t *testing.T) {
	originalRename := renameWorktreeMarker
	originalRetry := retryMarkerPublishRenames
	t.Cleanup(func() {
		renameWorktreeMarker = originalRename
		retryMarkerPublishRenames = originalRetry
	})
	retryMarkerPublishRenames = true
	calls := 0
	renameWorktreeMarker = func(string, string) error {
		calls++
		if calls < 3 {
			return errors.New("Access is denied.")
		}
		return nil
	}
	if err := publishWorktreeMarker("temp", "target"); err != nil {
		t.Fatalf("transient contention must be absorbed, got %v", err)
	}
	if calls != 3 {
		t.Fatalf("rename attempts = %d, want 3", calls)
	}

	calls = 0
	renameWorktreeMarker = func(string, string) error { calls++; return errors.New("Access is denied.") }
	if err := publishWorktreeMarker("temp", "target"); err == nil {
		t.Fatal("a persistent failure must surface after the attempt budget")
	}
	if calls != maxMarkerPublishAttempts {
		t.Fatalf("rename attempts = %d, want %d", calls, maxMarkerPublishAttempts)
	}

	retryMarkerPublishRenames = false
	calls = 0
	if err := publishWorktreeMarker("temp", "target"); err == nil {
		t.Fatal("without retries the first failure must surface")
	}
	if calls != 1 {
		t.Fatalf("rename attempts without retry = %d, want 1", calls)
	}
}

// TestMarkerExcludeRetriesTransientNotExist pins ensureWorktreeMarkerExcluded's
// retry contract deterministically. See ensureWorktreeMarkerExcluded's doc
// comment: under CI-runner load, 8 goroutines racing WriteWorktreeMarker
// against one shared .git directory intermittently hit a spurious not-exist
// error opening the exclude lock. Without this test the retry loop's bounds
// and its not-exist gate are exercised only by that intermittent contention.
func TestMarkerExcludeRetriesTransientNotExist(t *testing.T) {
	original := ensureWorktreeMarkerExcludedOnce
	t.Cleanup(func() { ensureWorktreeMarkerExcludedOnce = original })

	// worktreeGitCommonDir shells out to `git`, so the loop under test needs
	// a real repository root; the retry itself is exercised through the
	// stubbed seam below, not through this fixture.
	root := t.TempDir()
	if out, err := exec.Command("git", "init", "-q", root).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}

	// Fails on every attempt up to and including the last one the budget
	// allows (attempts 0..maxMarkerExcludeAttempts-1): the loop must exhaust
	// its budget and return the error, never reaching a call the budget
	// does not grant. attempt<max off-by-one (<=) would let a 6th call
	// through and see it "succeed" on a call this contract never makes.
	calls := 0
	ensureWorktreeMarkerExcludedOnce = func(string) error {
		calls++
		if calls > maxMarkerExcludeAttempts {
			return nil
		}
		return fmt.Errorf("wrap: %w", os.ErrNotExist)
	}
	if err := ensureWorktreeMarkerExcluded(root); err == nil {
		t.Fatal("a not-exist error persisting past the attempt budget must surface, not succeed")
	}
	if calls != maxMarkerExcludeAttempts {
		t.Fatalf("attempts = %d, want exactly %d", calls, maxMarkerExcludeAttempts)
	}

	// Recovers within the budget: fails twice, then succeeds on the third
	// call.
	calls = 0
	ensureWorktreeMarkerExcludedOnce = func(string) error {
		calls++
		if calls < 3 {
			return fmt.Errorf("wrap: %w", os.ErrNotExist)
		}
		return nil
	}
	if err := ensureWorktreeMarkerExcluded(root); err != nil {
		t.Fatalf("transient not-exist within the budget must be absorbed, got %v", err)
	}
	if calls != 3 {
		t.Fatalf("attempts = %d, want 3", calls)
	}

	// A non-not-exist error must stop immediately, never retried: this is
	// the branch the ==/||/! mutants each flip.
	calls = 0
	ensureWorktreeMarkerExcludedOnce = func(string) error {
		calls++
		return errors.New("permission denied")
	}
	if err := ensureWorktreeMarkerExcluded(root); err == nil {
		t.Fatal("a non-not-exist error must surface")
	}
	if calls != 1 {
		t.Fatalf("attempts = %d, want 1: a non-not-exist error must not be retried", calls)
	}
}
