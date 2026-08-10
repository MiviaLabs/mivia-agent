package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/vcs"
)

type nonSQLiteContextStore struct{}

func (nonSQLiteContextStore) EnsureSession(context.Context, contextstate.EnsureSessionRequest) error {
	return nil
}

func (nonSQLiteContextStore) Commit(context.Context, contextstate.CommitRequest) error { return nil }

func (nonSQLiteContextStore) Advance(context.Context, contextstate.AdvanceRequest) error { return nil }

func (nonSQLiteContextStore) Load(context.Context, contextstate.Principal, string) (contextstate.Snapshot, error) {
	return contextstate.Snapshot{}, nil
}

func TestDialogCoverageRecoveryHelpers(t *testing.T) {
	dialog := newWorktreeDialog(nil)
	if _, ok := dialog.selectedRecovery(); ok {
		t.Fatal("empty dialog returned recovery")
	}
	if got := worktreeRecoveryLabel(contextstate.WorktreeCreating); got != "creation recovery required" {
		t.Fatalf("creating label = %q", got)
	}
	if got := worktreeRecoveryLabel(contextstate.WorktreeDeleting); got != "recovery required" {
		t.Fatalf("deleting label = %q", got)
	}
	info := contextstate.WorktreeInstanceInfo{
		Instance: contextstate.WorktreeInstance{Worktree: "wt-a", ID: "wt_1234567890abcdef"},
		State:    contextstate.WorktreeDeleting,
	}
	dialog.worktrees = []vcs.WorktreeInfo{{Name: "wt-a"}}
	dialog.setRecovery(info)
	if got, ok := dialog.selectedRecovery(); !ok || got.Info != info {
		t.Fatalf("selected recovery = %+v, %v", got, ok)
	}
}

func TestDialogCoverageAddsOnlyMissingRecoveryRows(t *testing.T) {
	list := []vcs.WorktreeInfo{{Name: "same", Path: "/same"}}
	infos := []contextstate.WorktreeInstanceInfo{
		{Instance: contextstate.WorktreeInstance{Worktree: "same", ID: "wt_1111111111111111"}, CanonicalPath: "/old"},
		{Instance: contextstate.WorktreeInstance{Worktree: "new", ID: "wt_2222222222222222"}, CanonicalPath: "/new"},
	}
	got := addWorktreeRecoveryRows(list, infos)
	if len(got) != 2 || got[1].Name != "new" || got[1].Path != "/new" {
		t.Fatalf("recovery rows = %+v", got)
	}
}

func TestDialogCoverageOpenOutsideRepository(t *testing.T) {
	m := newReadyChatModel(30, 90)
	m.workspaceDir = t.TempDir()
	m.openWorktreeDialog()
	if m.worktreeDlg == nil || !m.worktreeDlg.noticeErr || m.worktreeDlg.notice == "" {
		t.Fatalf("dialog = %+v", m.worktreeDlg)
	}
}

func TestDialogCoverageSnapshotBindings(t *testing.T) {
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
	bad := snapshotWorktreeDialogBinding(store, principal, vcs.WorktreeInfo{Name: "bad", Path: filepath.Join(repo, "missing")})
	if bad.Err == nil {
		t.Fatal("missing path binding succeeded")
	}
	unmanaged, err := vcs.CreateWithPrefix(context.Background(), repo, "unmanaged", "HEAD", "mivia/")
	if err != nil {
		t.Fatal(err)
	}
	plain := snapshotWorktreeDialogBinding(store, principal, *unmanaged)
	if plain.Err != nil || !plain.Instance.IsZero() {
		t.Fatalf("unmanaged binding = %+v", plain)
	}
	if err := store.SaveWorktreeRoute(context.Background(), principal, unmanaged.Name, unmanaged.Path); err != nil {
		t.Fatal(err)
	}
	legacy := snapshotWorktreeDialogBinding(store, principal, *unmanaged)
	if legacy.Err == nil || !strings.Contains(legacy.Err.Error(), "adoption") {
		t.Fatalf("legacy binding error = %v", legacy.Err)
	}
}

func TestDialogCoverageValidatesMissingMarkerStates(t *testing.T) {
	repo := newWorktreeCommandRepo(t)
	unmanaged, err := vcs.CreateWithPrefix(context.Background(), repo, "plain", "HEAD", "mivia/")
	if err != nil {
		t.Fatal(err)
	}
	m := newReadyChatModel(30, 90)
	m.workspaceDir = repo
	if err := m.validateWorktreeWithoutMarker(*unmanaged); err != nil {
		t.Fatalf("unmanaged worktree: %v", err)
	}
	store, err := openRepositoryContextStore(repo)
	if err != nil {
		t.Fatal(err)
	}
	principal, err := worktreeRoutePrincipal(repo)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveWorktreeRoute(context.Background(), principal, unmanaged.Name, unmanaged.Path); err != nil {
		t.Fatal(err)
	}
	if err := m.validateWorktreeWithoutMarker(*unmanaged); err == nil || !strings.Contains(err.Error(), "adoption") {
		t.Fatalf("legacy validation error = %v", err)
	}
	if _, err := store.DeleteWorktreeRoute(context.Background(), principal, unmanaged.Name); err != nil {
		t.Fatal(err)
	}
	instance := contextstate.WorktreeInstance{Worktree: unmanaged.Name, ID: "wt_1234567890abcdef"}
	if err := store.BeginWorktreeCreation(context.Background(), principal, instance, unmanaged.Path); err != nil {
		t.Fatal(err)
	}
	if err := m.validateWorktreeWithoutMarker(*unmanaged); !errors.Is(err, contextstate.ErrWorktreeDeleted) {
		t.Fatalf("managed missing marker error = %v", err)
	}
	store.Close()
}

func TestDialogCoverageValidateSwitchMarkerFailures(t *testing.T) {
	repo := newWorktreeCommandRepo(t)
	worktree, err := vcs.CreateWithPrefix(context.Background(), repo, "wrong", "HEAD", "mivia/")
	if err != nil {
		t.Fatal(err)
	}
	m := newReadyChatModel(30, 90)
	m.workspaceDir = repo
	if err := writeWorktreeMarker(worktree.Path, contextstate.WorktreeInstance{Worktree: "other", ID: "wt_1234567890abcdef"}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.validateWorktreeSwitch(*worktree); !errors.Is(err, contextstate.ErrWorktreeDeleted) {
		t.Fatalf("wrong marker error = %v", err)
	}
	if err := os.WriteFile(worktreeMarkerPath(worktree.Path), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := m.validateWorktreeSwitch(*worktree); err == nil {
		t.Fatal("malformed marker switch succeeded")
	}
}

func TestDialogCoverageSessionValidationAndRecoveryErrors(t *testing.T) {
	m := newReadyChatModel(30, 90)
	if err := m.validateSessionWorktree("", contextstate.WorktreeInstance{}); err != nil {
		t.Fatal(err)
	}
	m.workspaceDir = t.TempDir()
	m.worktreeDlg = newWorktreeDialog([]vcs.WorktreeInfo{{Name: "wt-a", Path: "/missing"}})
	info := contextstate.WorktreeInstanceInfo{
		Instance:      contextstate.WorktreeInstance{Worktree: "wt-a", ID: "wt_1234567890abcdef"},
		CanonicalPath: "/missing",
		State:         contextstate.WorktreeCreating,
	}
	m.waiting = true
	m.recoverCreatingWorktree(m.worktreeDlg.worktrees[0], info)
	if !strings.Contains(m.worktreeDlg.notice, "cannot switch") {
		t.Fatalf("busy notice = %q", m.worktreeDlg.notice)
	}
	m.waiting = false
	m.recoverCreatingWorktree(m.worktreeDlg.worktrees[0], info)
	if !strings.Contains(m.worktreeDlg.notice, "recovery failed") {
		t.Fatalf("recovery notice = %q", m.worktreeDlg.notice)
	}
}

func TestDialogCoverageSwitchUtilities(t *testing.T) {
	m := newReadyChatModel(30, 90)
	m.workspaceDir = "~/workspace"
	if !strings.HasSuffix(m.resolveWorkspaceDir(), "/workspace") {
		t.Fatalf("resolved home path = %q", m.resolveWorkspaceDir())
	}
	m.workspaceDir = t.TempDir()
	if got := m.resolveRepoRoot(); got != m.workspaceDir {
		t.Fatalf("non-repository root = %q", got)
	}
	m.worktreeDlg = newWorktreeDialog(nil)
	dir := t.TempDir()
	m.restartInWorkspace(dir)
	if m.restartWorkspace != dir || m.worktreeDlg != nil {
		t.Fatalf("restart state = %q, %+v", m.restartWorkspace, m.worktreeDlg)
	}
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	cwd := filepath.Join(t.TempDir(), "cwd")
	if err := os.Mkdir(cwd, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(cwd); err != nil {
		t.Fatal(err)
	}
	if !worktreeContainsCurrentDir(cwd) {
		t.Fatal("current directory was not detected")
	}
	if runtime.GOOS == "windows" {
		// Windows cannot delete its process working directory; inject the
		// deleted-CWD state through the getwd seam instead.
		orig := getwdWorktreeSwitch
		getwdWorktreeSwitch = func() (string, error) { return "", os.ErrNotExist }
		t.Cleanup(func() { getwdWorktreeSwitch = orig })
	} else if err := os.Remove(cwd); err != nil {
		t.Fatal(err)
	}
	if !worktreeContainsCurrentDir(t.TempDir()) {
		t.Fatal("deleted current directory did not fail closed")
	}
	if err := os.Chdir(old); err != nil {
		t.Fatal(err)
	}
}

func TestDialogCoverageUnavailableKeys(t *testing.T) {
	for _, key := range []string{"enter", "d", "c"} {
		m := newReadyChatModel(30, 90)
		m.worktreeDlg = newWorktreeDialog([]vcs.WorktreeInfo{{Name: "wt-a", Path: "/missing"}})
		m.worktreeDlg.lifecycleUnavailable = true
		m.handleWorktreeDialogKey(key)
		if !m.worktreeDlg.noticeErr || !strings.Contains(m.worktreeDlg.notice, "unavailable") {
			t.Fatalf("%s notice = %q", key, m.worktreeDlg.notice)
		}
	}
}

func TestDialogCoverageBusyCreateAndInvalidCreatedMessage(t *testing.T) {
	m := newReadyChatModel(30, 90)
	m.worktreeDlg = newWorktreeDialog(nil)
	m.waiting = true
	m.handleWorktreeDialogKey("c")
	if !strings.Contains(m.worktreeDlg.notice, "agent is running") {
		t.Fatalf("busy create notice = %q", m.worktreeDlg.notice)
	}
	m.waiting = false
	dialog := m.worktreeDlg
	m.applyWorktreeCreated(worktreeCreatedMsg{dlg: dialog})
	if !strings.Contains(dialog.notice, "invalid worktree instance") {
		t.Fatalf("invalid result notice = %q", dialog.notice)
	}
}

func TestDialogCoverageDeleteReportsBusyLifecycleLock(t *testing.T) {
	repo := newWorktreeCommandRepo(t)
	worktree, err := createManagedWorktree(repo, "busy-delete", "HEAD", "mivia/")
	if err != nil {
		t.Fatal(err)
	}
	m := newReadyChatModel(30, 90)
	m.workspaceDir = repo
	m.openWorktreeDialog()
	for index, item := range m.worktreeDlg.worktrees {
		if item.Name == worktree.Name {
			m.worktreeDlg.cursor = index
		}
	}
	lock, err := lockWorktreeLifecycle(repo, worktree.Name)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	m.worktreeDlg.confirm = wtConfirmDelete
	m.applyWorktreeConfirm()
	if !strings.Contains(m.worktreeDlg.notice, "lock is busy") {
		t.Fatalf("busy delete notice = %q", m.worktreeDlg.notice)
	}
}

func TestDialogCoverageWriteDetachedRecoveryRow(t *testing.T) {
	info := contextstate.WorktreeInstanceInfo{
		Instance:      contextstate.WorktreeInstance{Worktree: "gone", ID: "wt_1234567890abcdef"},
		CanonicalPath: "/gone",
		State:         contextstate.WorktreeDeleting,
	}
	var output strings.Builder
	writeWorktreeList(&output, nil, []contextstate.WorktreeInstanceInfo{info})
	if !strings.Contains(output.String(), "gone\trecovery required\t/gone") {
		t.Fatalf("recovery list = %q", output.String())
	}
}

func TestDialogCoverageSnapshotMarkerBranches(t *testing.T) {
	repo := newWorktreeCommandRepo(t)
	store, err := openRepositoryContextStore(repo)
	if err != nil {
		t.Fatal(err)
	}
	principal, err := worktreeRoutePrincipal(repo)
	if err != nil {
		t.Fatal(err)
	}
	makeWorktree := func(name string) *vcs.WorktreeInfo {
		t.Helper()
		worktree, err := vcs.CreateWithPrefix(context.Background(), repo, name, "HEAD", "mivia/")
		if err != nil {
			t.Fatal(err)
		}
		return worktree
	}
	wrong := makeWorktree("wrong-marker")
	if err := writeWorktreeMarker(wrong.Path, contextstate.WorktreeInstance{Worktree: "other", ID: "wt_1111111111111111"}); err != nil {
		t.Fatal(err)
	}
	if got := snapshotWorktreeDialogBinding(store, principal, *wrong); !errors.Is(got.Err, contextstate.ErrWorktreeDeleted) {
		t.Fatalf("wrong marker binding = %+v", got)
	}
	unregistered := makeWorktree("unregistered-marker")
	if err := writeWorktreeMarker(unregistered.Path, contextstate.WorktreeInstance{Worktree: unregistered.Name, ID: "wt_2222222222222222"}); err != nil {
		t.Fatal(err)
	}
	if got := snapshotWorktreeDialogBinding(store, principal, *unregistered); !errors.Is(got.Err, contextstate.ErrWorktreeDeleted) {
		t.Fatalf("unregistered marker binding = %+v", got)
	}
	malformed := makeWorktree("malformed-marker")
	if err := os.MkdirAll(filepath.Dir(worktreeMarkerPath(malformed.Path)), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(worktreeMarkerPath(malformed.Path), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := snapshotWorktreeDialogBinding(store, principal, *malformed); got.Err == nil {
		t.Fatal("malformed marker binding succeeded")
	}
	live := makeWorktree("live-no-marker")
	instance := contextstate.WorktreeInstance{Worktree: live.Name, ID: "wt_3333333333333333"}
	if err := store.BeginWorktreeCreation(context.Background(), principal, instance, live.Path); err != nil {
		t.Fatal(err)
	}
	if got := snapshotWorktreeDialogBinding(store, principal, *live); !errors.Is(got.Err, contextstate.ErrWorktreeDeleted) {
		t.Fatalf("live missing marker binding = %+v", got)
	}
	store.Close()
	closed := makeWorktree("closed-store")
	if got := snapshotWorktreeDialogBinding(store, principal, *closed); got.Err == nil {
		t.Fatal("closed store binding succeeded")
	}
}

func TestDialogCoverageRejectsNonSQLiteLifecycleStore(t *testing.T) {
	m := newReadyChatModel(30, 90)
	if err := m.session.SetContextStore(nonSQLiteContextStore{}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.worktreeLifecycleStore(t.TempDir()); err == nil {
		t.Fatal("non-SQLite lifecycle store succeeded")
	}
	repo := newWorktreeCommandRepo(t)
	worktree, err := vcs.CreateWithPrefix(context.Background(), repo, "non-sqlite", "HEAD", "mivia/")
	if err != nil {
		t.Fatal(err)
	}
	if err := writeWorktreeMarker(worktree.Path, contextstate.WorktreeInstance{Worktree: worktree.Name, ID: "wt_9999999999999999"}); err != nil {
		t.Fatal(err)
	}
	m.workspaceDir = repo
	if _, err := m.validateWorktreeSwitch(*worktree); err == nil {
		t.Fatal("marker switch with non-SQLite store succeeded")
	}
	instance := contextstate.WorktreeInstance{Worktree: "wt-a", ID: "wt_1234567890abcdef"}
	m.workspaceDir = t.TempDir()
	m.worktreeDlg = newWorktreeDialog([]vcs.WorktreeInfo{{Name: "wt-a", Path: m.workspaceDir}})
	if _, err := m.validateWorktreeSwitch(vcs.WorktreeInfo{Name: "wt-a", Path: m.workspaceDir}); err == nil {
		t.Fatal("switch with non-SQLite store succeeded")
	}
	if err := m.validateSessionWorktree(m.workspaceDir, instance); err == nil {
		t.Fatal("session validation with non-SQLite store succeeded")
	}
	info := contextstate.WorktreeInstanceInfo{Instance: instance, CanonicalPath: m.workspaceDir, State: contextstate.WorktreeCreating}
	m.recoverCreatingWorktree(m.worktreeDlg.worktrees[0], info)
	if !strings.Contains(m.worktreeDlg.notice, "lifecycle requires") {
		t.Fatalf("recovery notice = %q", m.worktreeDlg.notice)
	}
}

func TestDialogCoverageValidateSwitchRejectsUnregisteredMarker(t *testing.T) {
	repo := newWorktreeCommandRepo(t)
	worktree, err := vcs.CreateWithPrefix(context.Background(), repo, "unregistered-switch", "HEAD", "mivia/")
	if err != nil {
		t.Fatal(err)
	}
	if err := writeWorktreeMarker(worktree.Path, contextstate.WorktreeInstance{Worktree: worktree.Name, ID: "wt_8888888888888888"}); err != nil {
		t.Fatal(err)
	}
	m := newReadyChatModel(30, 90)
	m.workspaceDir = repo
	if _, err := m.validateWorktreeSwitch(*worktree); !errors.Is(err, contextstate.ErrWorktreeDeleted) {
		t.Fatalf("unregistered switch error = %v", err)
	}
}

func TestDialogCoverageMissingMarkerRejectsClosedStore(t *testing.T) {
	repo := newWorktreeCommandRepo(t)
	worktree, err := vcs.CreateWithPrefix(context.Background(), repo, "closed-validation", "HEAD", "mivia/")
	if err != nil {
		t.Fatal(err)
	}
	store, err := openRepositoryContextStore(repo)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	m := newReadyChatModel(30, 90)
	m.workspaceDir = repo
	if err := m.session.SetContextStore(store); err != nil {
		t.Fatal(err)
	}
	if err := m.validateWorktreeWithoutMarker(*worktree); err == nil {
		t.Fatal("missing marker with closed store succeeded")
	}
}

func TestDialogCoverageRelativePathsFailWithDeletedWorkingDirectory(t *testing.T) {
	m := newReadyChatModel(30, 90)
	m.worktreeDlg = newWorktreeDialog(nil)
	if runtime.GOOS == "windows" {
		// Windows cannot delete its process working directory, so the
		// deleted-CWD state is injected through the seam the production code
		// uses: a working directory that can no longer be resolved. This
		// exercises the same fail-closed branch the Unix test reaches by
		// removing the directory.
		orig := getwdWorktreeSwitch
		getwdWorktreeSwitch = func() (string, error) { return "", os.ErrNotExist }
		t.Cleanup(func() { getwdWorktreeSwitch = orig })
		m.restartInWorkspace("relative")
		if m.worktreeDlg == nil || !m.worktreeDlg.noticeErr {
			t.Fatal("relative restart did not fail closed")
		}
		if !worktreeContainsCurrentDir("relative") {
			t.Fatal("relative containment did not fail closed")
		}
		return
	}
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	cwd := filepath.Join(t.TempDir(), "cwd-relative")
	if err := os.Mkdir(cwd, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(cwd); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(old); err != nil {
			t.Errorf("restore working directory: %v", err)
		}
	})
	if err := os.Remove(cwd); err != nil {
		t.Fatal(err)
	}
	m.restartInWorkspace("relative")
	if m.worktreeDlg == nil || !m.worktreeDlg.noticeErr {
		t.Fatal("relative restart did not fail closed")
	}
	if !worktreeContainsCurrentDir("relative") {
		t.Fatal("relative containment did not fail closed")
	}
}

func TestDialogCoverageRecoveryConfirmStoreError(t *testing.T) {
	m := newReadyChatModel(30, 90)
	if err := m.session.SetContextStore(nonSQLiteContextStore{}); err != nil {
		t.Fatal(err)
	}
	m.workspaceDir = t.TempDir()
	instance := contextstate.WorktreeInstance{Worktree: "wt-a", ID: "wt_1234567890abcdef"}
	m.worktreeDlg = newWorktreeDialog([]vcs.WorktreeInfo{{Name: "wt-a", Path: m.workspaceDir}})
	m.worktreeDlg.setRecovery(contextstate.WorktreeInstanceInfo{Instance: instance, CanonicalPath: m.workspaceDir, State: contextstate.WorktreeDeleting})
	m.worktreeDlg.confirm = wtConfirmDelete
	m.applyWorktreeConfirm()
	if !strings.Contains(m.worktreeDlg.notice, "recovery failed") {
		t.Fatalf("confirm notice = %q", m.worktreeDlg.notice)
	}
}

func TestDialogCoverageCreatingRecoveryReturnedWorktreeMismatch(t *testing.T) {
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
	instance := contextstate.WorktreeInstance{Worktree: "recover-mismatch", ID: "wt_1234567890abcdef"}
	expectedPath := filepath.Join(repo, ".mivia", "worktrees", instance.Worktree)
	info := contextstate.WorktreeInstanceInfo{Instance: instance, CanonicalPath: expectedPath, State: contextstate.WorktreeCreating}
	if err := store.BeginWorktreeCreation(context.Background(), principal, instance, expectedPath); err != nil {
		t.Fatal(err)
	}
	if _, err := vcs.CreateWithPrefix(context.Background(), repo, instance.Worktree, "HEAD", "mivia/"); err != nil {
		t.Fatal(err)
	}
	m := newReadyChatModel(30, 90)
	m.workspaceDir = repo
	m.worktreeDlg = newWorktreeDialog([]vcs.WorktreeInfo{{Name: instance.Worktree, Path: filepath.Join(repo, "different")}})
	m.recoverCreatingWorktree(m.worktreeDlg.worktrees[0], info)
	if got := m.worktreeDlg.notice; got != "creation recovery returned a different worktree" {
		t.Fatalf("mismatch notice = %q", got)
	}
}
