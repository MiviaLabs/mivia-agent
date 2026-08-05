//go:build unix

package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
	"github.com/MiviaLabs/mivia-agent/internal/vcs"
)

func TestLifecycleExactLockErrors(t *testing.T) {
	repo := newWorktreeCommandRepo(t)
	if _, err := recoverManagedWorktreeRemovalLocked(repo, strings.Repeat("x", 65), "mivia/", nil); err == nil {
		t.Fatal("invalid locked recovery name succeeded")
	}
	lock, err := lockWorktreeLifecycle(repo, "busy")
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	store, err := openRepositoryContextStore(repo)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	info := contextstate.WorktreeInstanceInfo{Instance: contextstate.WorktreeInstance{Worktree: "busy", ID: "wt_1111111111111111"}, State: contextstate.WorktreeDeleting}
	if err := recoverManagedWorktreeRemovalInfoInStore(store, repo, info, "mivia/"); err == nil {
		t.Fatal("busy recovery lock succeeded")
	}
}

func TestLifecycleLockRejectsFinalSymlinkRace(t *testing.T) {
	repo := newWorktreeCommandRepo(t)
	original := lstatLifecyclePath
	lstatLifecyclePath = func(root *os.Root, path string) (os.FileInfo, error) {
		if path != filepath.Join(worktreeLifecycleLockDir, "victim.lock") {
			return original(root, path)
		}
		dir := filepath.Join(root.Name(), worktreeLifecycleLockDir)
		if err := os.WriteFile(filepath.Join(dir, "target.lock"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("target.lock", filepath.Join(dir, "victim.lock")); err != nil {
			t.Fatal(err)
		}
		return nil, os.ErrNotExist
	}
	t.Cleanup(func() { lstatLifecyclePath = original })

	lock, err := lockWorktreeLifecycle(repo, "victim")
	if lock != nil {
		lock.Close()
	}
	if err == nil {
		t.Fatal("lifecycle lock succeeded through a final symlink")
	}
}

func TestLifecycleExactAdoptRejectsNilWorktree(t *testing.T) {
	if _, err := adoptManagedWorktree(t.TempDir(), nil); err == nil {
		t.Fatal("nil worktree adoption succeeded")
	}
}

func TestLifecycleExactRecoverySkipsOtherDeletingName(t *testing.T) {
	repo := newWorktreeCommandRepo(t)
	store, err := openRepositoryContextStore(repo)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	principal, err := worktreeRoutePrincipal(repo)
	if err != nil {
		t.Fatal(err)
	}
	instance := contextstate.WorktreeInstance{Worktree: "other", ID: "wt_5555555555555555"}
	path := filepath.Join(repo, ".mivia", "worktrees", "other")
	if err := store.BeginWorktreeCreation(context.Background(), principal, instance, path); err != nil {
		t.Fatal(err)
	}
	if err := store.RegisterWorktreeInstance(context.Background(), principal, instance, path); err != nil {
		t.Fatal(err)
	}
	if err := store.BeginWorktreeDeletion(context.Background(), principal, instance); err != nil {
		t.Fatal(err)
	}
	recovered, err := recoverManagedWorktreeRemovalInStoreLocked(store, repo, "target", "mivia/", nil)
	if err != nil || recovered {
		t.Fatalf("unrelated recovery = %t, %v; want false, nil", recovered, err)
	}
}

func TestLifecycleExactUnixFileErrors(t *testing.T) {
	rootPath := t.TempDir()
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := root.Close(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := openWorktreeLifecycleLockFile(root, "lock"); err == nil {
		t.Fatal("closed root opened a lifecycle lock")
	}
	file, err := os.CreateTemp(t.TempDir(), "marker-lock")
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := lockWorktreeMarkerFile(file); err == nil {
		t.Fatal("closed marker descriptor acquired a lock")
	}
}

func TestLifecycleExactClosedRootWithValidLockPath(t *testing.T) {
	rootPath := t.TempDir()
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := root.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(worktreeLifecycleLockDir, "victim.lock")
	if _, _, err := openWorktreeLifecycleLockFile(root, path); err == nil {
		t.Fatal("closed root opened a lifecycle lock")
	}
}

func TestLifecycleLockRejectsMovedCommonDirectory(t *testing.T) {
	base := t.TempDir()
	original := filepath.Join(base, "common")
	if err := os.Mkdir(original, 0o700); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(original)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err := os.Rename(original, filepath.Join(base, "moved")); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(worktreeLifecycleLockDir, "victim.lock")
	if _, _, err := openWorktreeLifecycleLockFile(root, path); err == nil {
		t.Fatal("lifecycle lock opened in a moved-away common directory")
	}
}

func TestLifecycleLockRejectsReplacedCommonDirectory(t *testing.T) {
	base := t.TempDir()
	original := filepath.Join(base, "common")
	if err := os.Mkdir(original, 0o700); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(original)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err := os.Mkdir(filepath.Join(original, worktreeLifecycleLockDir), 0o700); err != nil {
		t.Fatal(err)
	}
	moved := filepath.Join(base, "moved")
	if err := os.Rename(original, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(original, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(original, worktreeLifecycleLockDir), 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(worktreeLifecycleLockDir, "victim.lock")
	file, unlock, err := openWorktreeLifecycleLockFile(root, path)
	if file != nil {
		_ = file.Close()
	}
	if unlock != nil {
		unlock()
	}
	if err == nil || !strings.Contains(err.Error(), "identity") {
		t.Fatalf("replaced common directory error = %v, want identity failure", err)
	}
	if _, statErr := os.Stat(filepath.Join(original, worktreeLifecycleLockDir, "victim.lock")); statErr == nil {
		t.Fatal("lifecycle lock leaked into the replacement common directory")
	}
}

func TestLifecycleLockRejectsSymlinkLockDirectory(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(base, worktreeLifecycleLockDir)); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(base)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	path := filepath.Join(worktreeLifecycleLockDir, "victim.lock")
	if _, _, err := openWorktreeLifecycleLockFile(root, path); err == nil {
		t.Fatal("lifecycle lock opened through a symlinked lock directory")
	}
}

func TestLifecycleFaultSeamLockDirectoryFirstUseRace(t *testing.T) {
	repo := newWorktreeCommandRepo(t)
	commonDir, err := worktreeGitCommonDir(repo)
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(commonDir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	originalLstat, originalMkdir := lstatLifecyclePath, mkdirLifecycleDir
	t.Cleanup(func() {
		lstatLifecyclePath, mkdirLifecycleDir = originalLstat, originalMkdir
	})
	realDir := t.TempDir()
	dirInfo, err := os.Lstat(realDir)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	lstatLifecyclePath = func(*os.Root, string) (os.FileInfo, error) {
		calls++
		if calls == 1 {
			return nil, os.ErrNotExist
		}
		return dirInfo, nil
	}
	mkdirLifecycleDir = func(*os.Root, string, os.FileMode) error { return os.ErrExist }
	if err := ensureRegularLifecycleLockDir(root); err != nil {
		t.Fatalf("concurrent first-use lock directory creation failed: %v", err)
	}
}

func TestLifecycleFaultSeamLockDirectoryFirstUseRaceKeepsFailClosed(t *testing.T) {
	repo := newWorktreeCommandRepo(t)
	commonDir, err := worktreeGitCommonDir(repo)
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(commonDir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	originalLstat, originalMkdir := lstatLifecyclePath, mkdirLifecycleDir
	t.Cleanup(func() {
		lstatLifecyclePath, mkdirLifecycleDir = originalLstat, originalMkdir
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
	lstatLifecyclePath = func(*os.Root, string) (os.FileInfo, error) {
		calls++
		if calls == 1 {
			return nil, os.ErrNotExist
		}
		return symlinkInfo, nil
	}
	mkdirLifecycleDir = func(*os.Root, string, os.FileMode) error { return os.ErrExist }
	if err := ensureRegularLifecycleLockDir(root); err == nil {
		t.Fatal("concurrent symlink planting at the lock directory was accepted")
	}
}

func TestLifecycleFaultSeamLockFileStatError(t *testing.T) {
	base := t.TempDir()
	if err := os.Mkdir(filepath.Join(base, worktreeLifecycleLockDir), 0o700); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(base)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	sentinel := errors.New("lock file stat fault")
	original := statLifecycleLockFile
	statLifecycleLockFile = func(*os.File) (os.FileInfo, error) { return nil, sentinel }
	t.Cleanup(func() { statLifecycleLockFile = original })
	path := filepath.Join(worktreeLifecycleLockDir, "victim.lock")
	file, unlock, err := openWorktreeLifecycleLockFile(root, path)
	if file != nil {
		_ = file.Close()
	}
	if unlock != nil {
		unlock()
	}
	if !errors.Is(err, sentinel) {
		t.Fatalf("lock file stat fault error = %v", err)
	}
}

func TestLifecycleLockRejectsFifoLockFile(t *testing.T) {
	base := t.TempDir()
	if err := os.Mkdir(filepath.Join(base, worktreeLifecycleLockDir), 0o700); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(base)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	lockPath := filepath.Join(worktreeLifecycleLockDir, "victim.lock")
	if err := syscall.Mkfifo(filepath.Join(base, lockPath), 0o600); err != nil {
		t.Fatal(err)
	}
	file, unlock, err := openWorktreeLifecycleLockFile(root, lockPath)
	if file != nil {
		_ = file.Close()
	}
	if unlock != nil {
		unlock()
	}
	if err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("FIFO lifecycle lock error = %v, want not-a-regular-file", err)
	}
}

func TestLifecycleExactClosedStoreAndResolveErrors(t *testing.T) {
	repo := newWorktreeCommandRepo(t)
	store, err := openRepositoryContextStore(repo)
	if err != nil {
		t.Fatal(err)
	}
	info := contextstate.WorktreeInstanceInfo{Instance: contextstate.WorktreeInstance{Worktree: "wt-a", ID: "wt_1111111111111111"}, State: contextstate.WorktreeDeleting}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := recoverManagedWorktreeRemovalInfoInStoreLocked(store, repo, info, "mivia/", nil); err == nil {
		t.Fatal("closed deletion store succeeded")
	}

	store, err = openRepositoryContextStore(repo)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	principal, err := worktreeRoutePrincipal(repo)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(repo, ".mivia", "worktrees", "wt-a")
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := store.BeginWorktreeCreation(context.Background(), principal, info.Instance, path); err != nil {
		t.Fatal(err)
	}
	if err := store.RegisterWorktreeInstance(context.Background(), principal, info.Instance, path); err != nil {
		t.Fatal(err)
	}
	if err := store.BeginWorktreeDeletion(context.Background(), principal, info.Instance); err != nil {
		t.Fatal(err)
	}
	info.CanonicalPath = path
	t.Setenv("PATH", t.TempDir())
	if err := recoverManagedWorktreeRemovalInfoInStoreLocked(store, repo, info, "mivia/", nil); err == nil {
		t.Fatal("Git resolution failure was hidden")
	}
}

func TestLifecycleExactRemovalAndCreationFailures(t *testing.T) {
	repo := newWorktreeCommandRepo(t)
	worktree, err := createManagedWorktree(repo, "remove-error", "HEAD", "mivia/")
	if err != nil {
		t.Fatal(err)
	}
	store, err := openRepositoryContextStore(repo)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	principal, err := worktreeRoutePrincipal(repo)
	if err != nil {
		t.Fatal(err)
	}
	instance, err := readWorktreeMarker(worktree.Path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.BeginWorktreeDeletion(context.Background(), principal, instance); err != nil {
		t.Fatal(err)
	}
	info := contextstate.WorktreeInstanceInfo{Instance: instance, CanonicalPath: worktree.Path, State: contextstate.WorktreeDeleting}
	if err := recoverManagedWorktreeRemovalInfoInStoreLocked(store, repo, info, "bad-prefix", nil); err == nil {
		t.Fatal("invalid removal prefix succeeded")
	}

	created, err := vcs.CreateWithPrefix(context.Background(), repo, "create-error", "HEAD", "mivia/")
	if err != nil {
		t.Fatal(err)
	}
	createInstance := contextstate.WorktreeInstance{Worktree: created.Name, ID: "wt_2222222222222222"}
	createInfo := contextstate.WorktreeInstanceInfo{Instance: createInstance, CanonicalPath: created.Path, State: contextstate.WorktreeCreating}
	if err := store.BeginWorktreeCreation(context.Background(), principal, createInstance, created.Path); err != nil {
		t.Fatal(err)
	}
	markerDir := filepath.Join(created.Path, ".mivia")
	if err := os.MkdirAll(markerDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(markerDir, worktreeMarkerName), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := recoverManagedWorktreeCreationInStoreLocked(store, repo, createInfo); err == nil || errors.Is(err, contextstate.ErrWorktreeDeleted) {
		t.Fatalf("malformed creation marker error = %v", err)
	}
}

func TestLifecycleFaultSeamsLockErrors(t *testing.T) {
	repo := newWorktreeCommandRepo(t)
	sentinel := errors.New("lifecycle fault")
	originalOpen := openLifecycleGitRoot
	openLifecycleGitRoot = func(string) (*os.Root, error) { return nil, sentinel }
	t.Cleanup(func() { openLifecycleGitRoot = originalOpen })
	if _, err := lockWorktreeLifecycle(repo, "open-error"); !errors.Is(err, sentinel) {
		t.Fatalf("open root error = %v", err)
	}
	openLifecycleGitRoot = originalOpen

	originalLstat := lstatLifecyclePath
	lstatLifecyclePath = func(root *os.Root, path string) (os.FileInfo, error) {
		if strings.HasSuffix(path, ".lock") {
			return nil, sentinel
		}
		return root.Lstat(path)
	}
	t.Cleanup(func() { lstatLifecyclePath = originalLstat })
	if _, err := lockWorktreeLifecycle(repo, "stat-error"); !errors.Is(err, sentinel) {
		t.Fatalf("lock stat error = %v", err)
	}
}

func TestLifecycleFaultSeamsLockDirectoryErrors(t *testing.T) {
	repo := newWorktreeCommandRepo(t)
	commonDir, err := worktreeGitCommonDir(repo)
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(commonDir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	sentinel := errors.New("lifecycle directory fault")
	originalLstat, originalMkdir := lstatLifecyclePath, mkdirLifecycleDir
	t.Cleanup(func() {
		lstatLifecyclePath, mkdirLifecycleDir = originalLstat, originalMkdir
	})
	lstatLifecyclePath = func(*os.Root, string) (os.FileInfo, error) { return nil, os.ErrNotExist }
	mkdirLifecycleDir = func(*os.Root, string, os.FileMode) error { return sentinel }
	if err := ensureRegularLifecycleLockDir(root); !errors.Is(err, sentinel) {
		t.Fatalf("mkdir error = %v", err)
	}

	calls := 0
	lstatLifecyclePath = func(*os.Root, string) (os.FileInfo, error) {
		calls++
		if calls == 1 {
			return nil, os.ErrNotExist
		}
		return nil, sentinel
	}
	mkdirLifecycleDir = func(*os.Root, string, os.FileMode) error { return nil }
	if err := ensureRegularLifecycleLockDir(root); !errors.Is(err, sentinel) {
		t.Fatalf("post-mkdir stat error = %v", err)
	}
}

func TestLifecycleFaultSeamsRecoveryPrincipalErrors(t *testing.T) {
	repo := newWorktreeCommandRepo(t)
	store, err := openRepositoryContextStore(repo)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	sentinel := errors.New("principal fault")
	original := lifecycleRoutePrincipal
	lifecycleRoutePrincipal = func(string) (contextstate.Principal, error) {
		return contextstate.Principal{}, sentinel
	}
	t.Cleanup(func() { lifecycleRoutePrincipal = original })
	instance := contextstate.WorktreeInstance{Worktree: "wt-a", ID: "wt_1111111111111111"}
	deleting := contextstate.WorktreeInstanceInfo{Instance: instance, State: contextstate.WorktreeDeleting}
	if err := recoverManagedWorktreeRemovalInfoInStoreLocked(store, repo, deleting, "mivia/", nil); !errors.Is(err, sentinel) {
		t.Fatalf("removal principal error = %v", err)
	}
	creating := contextstate.WorktreeInstanceInfo{Instance: instance, State: contextstate.WorktreeCreating}
	if _, err := recoverManagedWorktreeCreationInStoreLocked(store, repo, creating); !errors.Is(err, sentinel) {
		t.Fatalf("creation principal error = %v", err)
	}
}

func TestLifecycleFaultSeamsRecoveryPathAndResolveErrors(t *testing.T) {
	repo := newWorktreeCommandRepo(t)
	store, info := prepareDeletingLifecycleInfo(t, repo)
	defer store.Close()
	sentinel := errors.New("recovery fault")
	originalResolve, originalCanonical := lifecycleResolveWorktree, lifecycleCanonicalMarkerRoot
	t.Cleanup(func() {
		lifecycleResolveWorktree, lifecycleCanonicalMarkerRoot = originalResolve, originalCanonical
	})
	lifecycleResolveWorktree = func(context.Context, string, string) (*vcs.WorktreeInfo, error) {
		return &vcs.WorktreeInfo{Name: info.Instance.Worktree, Path: info.CanonicalPath}, nil
	}
	lifecycleCanonicalMarkerRoot = func(string) (string, error) { return "", sentinel }
	if err := recoverManagedWorktreeRemovalInfoInStoreLocked(store, repo, info, "mivia/", nil); !errors.Is(err, sentinel) {
		t.Fatalf("canonical path error = %v", err)
	}

	creating := contextstate.WorktreeInstanceInfo{Instance: contextstate.WorktreeInstance{Worktree: "new", ID: "wt_2222222222222222"}, State: contextstate.WorktreeCreating}
	creating.CanonicalPath = filepath.Join(repo, ".mivia", "worktrees", "new")
	principal, err := worktreeRoutePrincipal(repo)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.BeginWorktreeCreation(context.Background(), principal, creating.Instance, creating.CanonicalPath); err != nil {
		t.Fatal(err)
	}
	lifecycleResolveWorktree = func(context.Context, string, string) (*vcs.WorktreeInfo, error) { return nil, sentinel }
	if _, err := recoverManagedWorktreeCreationInStoreLocked(store, repo, creating); !errors.Is(err, sentinel) {
		t.Fatalf("resolve error = %v", err)
	}
}

func prepareDeletingLifecycleInfo(t *testing.T, repo string) (*storage.SQLite, contextstate.WorktreeInstanceInfo) {
	t.Helper()
	store, err := openRepositoryContextStore(repo)
	if err != nil {
		t.Fatal(err)
	}
	principal, err := worktreeRoutePrincipal(repo)
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	instance := contextstate.WorktreeInstance{Worktree: "delete", ID: "wt_3333333333333333"}
	path := filepath.Join(repo, ".mivia", "worktrees", instance.Worktree)
	if err := store.BeginWorktreeCreation(context.Background(), principal, instance, path); err != nil {
		store.Close()
		t.Fatal(err)
	}
	if err := store.RegisterWorktreeInstance(context.Background(), principal, instance, path); err != nil {
		store.Close()
		t.Fatal(err)
	}
	if err := store.BeginWorktreeDeletion(context.Background(), principal, instance); err != nil {
		store.Close()
		t.Fatal(err)
	}
	return store, contextstate.WorktreeInstanceInfo{Instance: instance, CanonicalPath: path, State: contextstate.WorktreeDeleting}
}
