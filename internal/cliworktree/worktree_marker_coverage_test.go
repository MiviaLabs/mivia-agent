package cliworktree

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
)

func TestCoverageWriteMarkerRejectsMissingRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "missing")
	instance := contextstate.WorktreeInstance{Worktree: "wt-a", ID: "wt_1234567890abcdef"}
	if err := WriteWorktreeMarker(root, instance); err == nil {
		t.Fatal("marker write to missing root succeeded")
	}
}

type markerSizeInfo struct {
	os.FileInfo
	size int64
}

func (info markerSizeInfo) Size() int64 { return info.size }

func TestMarkerCoverageRejectsInvalidFilesystemShapes(t *testing.T) {
	if err := ensureWorktreeMarkerExcluded(t.TempDir()); err == nil {
		t.Fatal("non-repository marker exclusion succeeded")
	}
	rootPath := t.TempDir()
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err := ensureRegularGitInfoDir(root); err != nil {
		t.Fatalf("create info directory: %v", err)
	}
	if err := root.Remove("info"); err != nil {
		t.Fatal(err)
	}
	if err := root.WriteFile("info", []byte("file"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ensureRegularGitInfoDir(root); err == nil {
		t.Fatal("regular file accepted as info directory")
	}
}

func TestMarkerCoverageExcludeReadAndReplaceErrors(t *testing.T) {
	rootPath := t.TempDir()
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	content, mode, err := readWorktreeMarkerExclude(root, "missing")
	if err != nil || content != nil || (runtime.GOOS != "windows" && mode != 0o600) {
		t.Fatalf("missing exclude = %q, %o, %v", content, mode, err)
	}
	if err := root.Mkdir("directory", 0o700); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readWorktreeMarkerExclude(root, "directory"); err == nil {
		t.Fatal("directory exclude succeeded")
	}
	if err := root.WriteFile("exclude", []byte("initial"), 0o640); err != nil {
		t.Fatal(err)
	}
	content, mode, err = readWorktreeMarkerExclude(root, "exclude")
	// Windows cannot express Unix permission bits: a 0640 write reports
	// 0666 there because the mode only tracks the read-only attribute.
	if err != nil || string(content) != "initial" || (runtime.GOOS != "windows" && mode != 0o640) {
		t.Fatalf("exclude = %q, %o, %v", content, mode, err)
	}
	if _, _, err := createWorktreeMarkerExcludeTemp(root, "missing", 0o600); err == nil {
		t.Fatal("temporary file in missing directory succeeded")
	}
	if err := root.Mkdir("target", 0o700); err != nil {
		t.Fatal(err)
	}
	if err := replaceWorktreeMarkerExclude(root, "target", []byte("data"), 0o600); err == nil {
		t.Fatal("replace over directory succeeded")
	}
}

func TestMarkerCoverageExcludeLockAndAppend(t *testing.T) {
	rootPath := t.TempDir()
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err := os.Symlink("missing", filepath.Join(rootPath, "exclude.lock")); err != nil {
		t.Fatal(err)
	}
	if _, err := lockWorktreeMarkerExclude(root, "exclude.lock"); err == nil {
		t.Fatal("symlink lock succeeded")
	}
	if err := root.Remove("exclude.lock"); err != nil {
		t.Fatal(err)
	}
	if err := root.WriteFile("exclude", []byte("line"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := updateWorktreeMarkerExclude(root, "exclude"); err != nil {
		t.Fatal(err)
	}
	data, err := root.ReadFile("exclude")
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "line\n"+worktreeMarkerExclude+"\n" {
		t.Fatalf("exclude content = %q", data)
	}
	if err := updateWorktreeMarkerExclude(root, "exclude"); err != nil {
		t.Fatal(err)
	}
}

func TestMarkerCoverageReadFailures(t *testing.T) {
	missingRoot := filepath.Join(t.TempDir(), "missing")
	if _, err := ReadWorktreeMarker(missingRoot); err == nil {
		t.Fatal("missing root marker read succeeded")
	}
	root := t.TempDir()
	if _, err := ReadWorktreeMarker(root); err == nil {
		t.Fatal("missing marker directory succeeded")
	}
	if err := os.Mkdir(filepath.Join(root, ".mivia"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadWorktreeMarker(root); err == nil {
		t.Fatal("missing marker succeeded")
	}
	markerPath := WorktreeMarkerPath(root)
	if err := os.Mkdir(markerPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadWorktreeMarker(root); err == nil {
		t.Fatal("directory marker succeeded")
	}
	if err := os.Remove(markerPath); err != nil {
		t.Fatal(err)
	}
	for _, data := range []string{"{", `{"version":2,"worktree":"wt-a","id":"wt_1234567890abcdef"}`} {
		if err := os.WriteFile(markerPath, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadWorktreeMarker(root); err == nil {
			t.Fatalf("invalid marker %q succeeded", data)
		}
	}
}

func TestLifecycleLockCoverageFilesystemFailures(t *testing.T) {
	if _, err := LockWorktreeLifecycle(t.TempDir(), "wt-a"); err == nil {
		t.Fatal("lifecycle lock outside a repository succeeded")
	}
	repo := newWorktreeCommandRepo(t)
	common := markerGitOutput(t, repo, "rev-parse", "--git-common-dir")
	if !filepath.IsAbs(common) {
		common = filepath.Join(repo, common)
	}
	lockDir := filepath.Join(common, "mivia-worktree-locks")
	if err := os.WriteFile(lockDir, []byte("file"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LockWorktreeLifecycle(repo, "wt-a"); err == nil {
		t.Fatal("file lock directory succeeded")
	}
	if err := os.Remove(lockDir); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(lockDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(lockDir, "wt-a.lock"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := LockWorktreeLifecycle(repo, "wt-a"); err == nil {
		t.Fatal("directory lock file succeeded")
	}
}

func TestLifecycleLockCoverageBusyAndClose(t *testing.T) {
	repo := newWorktreeCommandRepo(t)
	first, err := LockWorktreeLifecycle(repo, "wt-a")
	if err != nil {
		t.Fatal(err)
	}
	if first.File() == nil {
		t.Fatal("lock file is nil")
	}
	if _, err := LockWorktreeLifecycle(repo, "wt-a"); err == nil || !strings.Contains(err.Error(), "busy") {
		t.Fatalf("second lock error = %v", err)
	}
	first.Close()
	second, err := LockWorktreeLifecycle(repo, "wt-a")
	if err != nil {
		t.Fatal(err)
	}
	second.Close()
}

func TestMarkerCoverageInvalidWriteInstance(t *testing.T) {
	root := t.TempDir()
	if err := WriteWorktreeMarker(root, contextstate.WorktreeInstance{}); err == nil {
		t.Fatal("zero marker instance succeeded")
	}
}

func TestMarkerCoverageClosedRootErrors(t *testing.T) {
	rootPath := t.TempDir()
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := root.Close(); err != nil {
		t.Fatal(err)
	}
	if err := ensureRegularGitInfoDir(root); err == nil {
		t.Fatal("closed root inspected info")
	}
	if err := updateWorktreeMarkerExclude(root, "exclude"); err == nil {
		t.Fatal("closed root updated exclude")
	}
	if _, _, err := readWorktreeMarkerExclude(root, "exclude"); err == nil {
		t.Fatal("closed root read exclude")
	}
}

func TestMarkerCoverageOpenAndReadPermissionErrors(t *testing.T) {
	rootPath := t.TempDir()
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if _, err := lockWorktreeMarkerExclude(root, filepath.Join("missing", "exclude.lock")); err == nil {
		t.Fatal("lock in missing directory succeeded")
	}
	if runtime.GOOS != "windows" {
		if err := root.WriteFile("exclude", []byte("data"), 0o000); err != nil {
			t.Fatal(err)
		}
		if _, _, err := readWorktreeMarkerExclude(root, "exclude"); err == nil {
			t.Fatal("unreadable exclude succeeded")
		}
	}
	if err := root.WriteFile("parent", []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := createWorktreeMarkerExcludeTemp(root, "parent", 0o600); err == nil {
		t.Fatal("temporary file below regular file succeeded")
	}
}

func TestMarkerCoverageGitCommonDirResolutionFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake Git executable is a POSIX shell script")
	}
	bin := t.TempDir()
	script := filepath.Join(bin, "git")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf 'missing-git-dir\\n'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	if _, err := worktreeGitCommonDir(t.TempDir()); err == nil {
		t.Fatal("missing Git common directory succeeded")
	}
}

func TestMarkerCoverageReadFromRegularFileRoot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(path, []byte("file"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadWorktreeMarker(path); err == nil {
		t.Fatal("regular file accepted as marker root")
	}
}

func TestMarkerCoverageFileLockRejectsClosedDescriptor(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "lock")
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := LockWorktreeMarkerFile(file); err == nil {
		t.Fatal("closed marker lock descriptor succeeded")
	}
}

func TestMarkerCoverageOpenRootRejectsRegularCommonPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake Git executable is a POSIX shell script")
	}
	root := t.TempDir()
	common := filepath.Join(root, "common-file")
	if err := os.WriteFile(common, []byte("file"), 0o600); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	script := filepath.Join(bin, "git")
	body := "#!/bin/sh\nprintf '%s\\n' '" + common + "'\n"
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	if err := ensureWorktreeMarkerExcluded(root); err == nil {
		t.Fatal("regular Git common path opened as a root")
	}
}

func TestMarkerCoverageInfoCreationPermissionError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directory mode does not deny writes on Windows")
	}
	rootPath := t.TempDir()
	if err := os.Chmod(rootPath, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(rootPath, 0o700) })
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err := ensureRegularGitInfoDir(root); err == nil {
		t.Fatal("info directory creation succeeded without write permission")
	}
}

func TestMarkerCoverageUpdatePropagatesReadAndCreateErrors(t *testing.T) {
	rootPath := t.TempDir()
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err := root.Mkdir("exclude", 0o700); err != nil {
		t.Fatal(err)
	}
	if err := updateWorktreeMarkerExclude(root, "exclude"); err == nil {
		t.Fatal("directory exclude update succeeded")
	}
	if err := replaceWorktreeMarkerExclude(root, filepath.Join("missing", "exclude"), []byte("data"), 0o600); err == nil {
		t.Fatal("replace in missing directory succeeded")
	}
}

func TestMarkerCoverageInjectedLockFailure(t *testing.T) {
	original := lockWorktreeMarkerForUpdate
	lockWorktreeMarkerForUpdate = func(*os.File) (func(), error) {
		return nil, errors.New("lock failure")
	}
	t.Cleanup(func() { lockWorktreeMarkerForUpdate = original })
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if _, err := lockWorktreeMarkerExclude(root, "exclude.lock"); err == nil {
		t.Fatal("injected marker lock failure succeeded")
	}
}

func TestMarkerCoverageInjectedWriteAndCloseFailures(t *testing.T) {
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	originalWrite := writeWorktreeExcludeTemp
	writeWorktreeExcludeTemp = func(*os.File, []byte) (int, error) {
		return 0, errors.New("write failure")
	}
	if err := replaceWorktreeMarkerExclude(root, "exclude", []byte("data"), 0o600); err == nil {
		t.Fatal("injected exclude write failure succeeded")
	}
	writeWorktreeExcludeTemp = originalWrite
	originalClose := closeWorktreeExcludeTemp
	closeWorktreeExcludeTemp = func(file *os.File) error {
		_ = file.Close()
		return errors.New("close failure")
	}
	t.Cleanup(func() {
		writeWorktreeExcludeTemp = originalWrite
		closeWorktreeExcludeTemp = originalClose
	})
	if err := replaceWorktreeMarkerExclude(root, "exclude", []byte("data"), 0o600); err == nil {
		t.Fatal("injected exclude close failure succeeded")
	}
}

func TestMarkerCoverageInjectedRandomFailureAndCollisions(t *testing.T) {
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	original := readWorktreeMarkerRandom
	readWorktreeMarkerRandom = func([]byte) (int, error) { return 0, errors.New("random failure") }
	if _, _, err := createWorktreeMarkerExcludeTemp(root, ".", 0o600); err == nil {
		t.Fatal("injected random failure succeeded")
	}
	readWorktreeMarkerRandom = func(data []byte) (int, error) {
		clear(data)
		return len(data), nil
	}
	t.Cleanup(func() { readWorktreeMarkerRandom = original })
	if err := root.WriteFile(".exclude-0000000000000000", []byte("collision"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := createWorktreeMarkerExcludeTemp(root, ".", 0o600); err == nil {
		t.Fatal("ten injected temporary-name collisions succeeded")
	}
}

func TestMarkerCoverageInjectedStatAndReadFailures(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".mivia"), 0o700); err != nil {
		t.Fatal(err)
	}
	markerPath := WorktreeMarkerPath(root)
	valid := []byte(`{"version":1,"worktree":"wt-a","id":"wt_1234567890abcdef"}`)
	if err := os.WriteFile(markerPath, valid, 0o600); err != nil {
		t.Fatal(err)
	}
	originalStat := statWorktreeMarkerFile
	originalRead := readWorktreeMarkerFile
	statWorktreeMarkerFile = func(*os.File) (os.FileInfo, error) { return nil, errors.New("stat failure") }
	if _, err := ReadWorktreeMarker(root); err == nil {
		t.Fatal("injected marker stat failure succeeded")
	}
	statWorktreeMarkerFile = originalStat
	readWorktreeMarkerFile = func(io.Reader) ([]byte, error) { return nil, errors.New("read failure") }
	if _, err := ReadWorktreeMarker(root); err == nil {
		t.Fatal("injected marker read failure succeeded")
	}
	readWorktreeMarkerFile = originalRead
	info, err := os.Stat(markerPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(markerPath, append(valid, make([]byte, maxWorktreeMarkerBytes)...), 0o600); err != nil {
		t.Fatal(err)
	}
	statWorktreeMarkerFile = func(*os.File) (os.FileInfo, error) {
		return markerSizeInfo{FileInfo: info, size: 0}, nil
	}
	t.Cleanup(func() {
		statWorktreeMarkerFile = originalStat
		readWorktreeMarkerFile = originalRead
	})
	if _, err := ReadWorktreeMarker(root); err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("post-stat oversized marker error = %v", err)
	}
}
