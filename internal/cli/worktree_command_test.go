package cli

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/vcs"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
	_ "modernc.org/sqlite"
)

func TestExecuteWorktreeCommandIsRegistered(t *testing.T) {
	err := Execute([]string{"worktree"})
	if err == nil || !strings.Contains(err.Error(), "expected create, list, remove, or adopt") {
		t.Fatalf("error = %v, want worktree usage error", err)
	}
}

func TestWorktreeCommandAdoptAddsMarker(t *testing.T) {
	repoRoot := newWorktreeCommandRepo(t)
	writeWorktreeStoreConfig(t, repoRoot, "repository.db")
	worktree, err := vcs.Create(context.Background(), repoRoot, "legacy", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if err := registerWorktreeRoute(repoRoot, worktree); err != nil {
		t.Fatalf("seed legacy route: %v", err)
	}
	var output bytes.Buffer
	if err := runWorktreeWithIO([]string{"adopt", "legacy", "--workspace", repoRoot}, &output); err != nil {
		t.Fatalf("adopt: %v", err)
	}
	if !strings.Contains(output.String(), "adopted worktree \"legacy\"") {
		t.Fatalf("adopt output = %q", output.String())
	}
	if _, err := readWorktreeMarker(worktree.Path); err != nil {
		t.Fatalf("read adopted marker: %v", err)
	}
}

func TestWorktreeCommandAdoptRecoversAfterMarkerWriteCrash(t *testing.T) {
	repoRoot := newWorktreeCommandRepo(t)
	writeWorktreeStoreConfig(t, repoRoot, "repository.db")
	worktree, err := vcs.Create(context.Background(), repoRoot, "legacy-crash", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if err := registerWorktreeRoute(repoRoot, worktree); err != nil {
		t.Fatal(err)
	}
	store, err := openRepositoryContextStore(repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	principal, err := worktreeRoutePrincipal(repoRoot)
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	instance := contextstate.WorktreeInstance{Worktree: worktree.Name, ID: "wt_1234567890abcdef"}
	if err := store.BeginWorktreeAdoption(context.Background(), principal, instance, worktree.Path); err != nil {
		store.Close()
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := runWorktreeWithIO([]string{"adopt", worktree.Name, "--workspace", repoRoot}, &output); err != nil {
		t.Fatalf("recover adoption: %v", err)
	}
	marker, err := readWorktreeMarker(worktree.Path)
	if err != nil || marker != instance {
		t.Fatalf("recovered marker = %+v, %v", marker, err)
	}
}

func TestWorktreeCommandRemoveRecoversDeletingLiveWorktree(t *testing.T) {
	repoRoot := newWorktreeCommandRepo(t)
	writeWorktreeStoreConfig(t, repoRoot, "repository.db")
	worktree, err := createManagedWorktree(repoRoot, "recover-live", "HEAD", "mivia/")
	if err != nil {
		t.Fatalf("createManagedWorktree: %v", err)
	}
	if _, err := beginManagedWorktreeRemoval(repoRoot, worktree); err != nil {
		t.Fatalf("beginManagedWorktreeRemoval: %v", err)
	}
	var output bytes.Buffer
	if err := runWorktreeWithIO([]string{"remove", "recover-live", "--workspace", repoRoot}, &output); err != nil {
		t.Fatalf("remove recovery: %v", err)
	}
	if output.String() != "removed worktree \"recover-live\"\n" {
		t.Fatalf("remove output = %q", output.String())
	}
	resolved, err := vcs.Resolve(context.Background(), repoRoot, "recover-live")
	if err != nil || resolved != nil {
		t.Fatalf("resolve recovered worktree = %v, %v", resolved, err)
	}
}

func TestWorktreeCommandRemoveRecoversAfterGitRemovalBeforeCleanup(t *testing.T) {
	repoRoot := newWorktreeCommandRepo(t)
	writeWorktreeStoreConfig(t, repoRoot, "repository.db")
	worktree, err := createManagedWorktree(repoRoot, "recover-gone", "HEAD", "mivia/")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := beginManagedWorktreeRemoval(repoRoot, worktree); err != nil {
		t.Fatal(err)
	}
	if err := vcs.RemoveWithPrefix(context.Background(), repoRoot, worktree.Name, "mivia/"); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := runWorktreeWithIO([]string{"remove", worktree.Name, "--workspace", repoRoot}, &output); err != nil {
		t.Fatalf("remove recovery: %v", err)
	}
	if output.String() != "removed worktree \"recover-gone\"\n" {
		t.Fatalf("remove output = %q", output.String())
	}
	store, err := openRepositoryContextStore(repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	principal, err := worktreeRoutePrincipal(repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.DeletingWorktreeInstance(context.Background(), principal, worktree.Name); !errors.Is(err, contextstate.ErrWorktreeDeleted) {
		t.Fatalf("deleting record = %v, want ErrWorktreeDeleted", err)
	}
}

func TestWorktreeCommandRemoveRecoveryKeepsSameNameReplacement(t *testing.T) {
	repoRoot := newWorktreeCommandRepo(t)
	writeWorktreeStoreConfig(t, repoRoot, "repository.db")
	old, err := createManagedWorktree(repoRoot, "recover-replacement", "HEAD", "mivia/")
	if err != nil {
		t.Fatal(err)
	}
	oldInstance, err := beginManagedWorktreeRemoval(repoRoot, old)
	if err != nil {
		t.Fatal(err)
	}
	if err := vcs.RemoveWithPrefix(context.Background(), repoRoot, old.Name, "mivia/"); err != nil {
		t.Fatal(err)
	}
	_, err = vcs.Create(context.Background(), repoRoot, old.Name, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := runWorktreeWithIO([]string{"remove", old.Name, "--workspace", repoRoot}, &output); err != nil {
		t.Fatalf("remove recovery: %v", err)
	}
	if output.String() != "removed worktree \"recover-replacement\"\n" {
		t.Fatalf("remove output = %q", output.String())
	}
	resolved, err := vcs.Resolve(context.Background(), repoRoot, old.Name)
	if err != nil || resolved == nil {
		t.Fatalf("replacement worktree = %v, %v", resolved, err)
	}
	if _, err := readWorktreeMarker(resolved.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("replacement marker = %v, want missing marker", err)
	}
	store, err := openRepositoryContextStore(repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	principal, err := worktreeRoutePrincipal(repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.DeletingWorktreeInstance(context.Background(), principal, oldInstance.Worktree); !errors.Is(err, contextstate.ErrWorktreeDeleted) {
		t.Fatalf("old deleting record = %v, want ErrWorktreeDeleted", err)
	}
}

func TestCreateManagedWorktreeRecoversAfterGitCreateBeforeMarker(t *testing.T) {
	repoRoot := newWorktreeCommandRepo(t)
	writeWorktreeStoreConfig(t, repoRoot, "repository.db")
	store, err := openRepositoryContextStore(repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	principal, err := worktreeRoutePrincipal(repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	instance := contextstate.WorktreeInstance{Worktree: "crash-create", ID: "wt_1234567890abcdef"}
	expectedPath := filepath.Join(workspace.WorktreesDir(repoRoot), instance.Worktree)
	if err := store.BeginWorktreeCreation(context.Background(), principal, instance, expectedPath); err != nil {
		t.Fatal(err)
	}
	created, err := vcs.Create(context.Background(), repoRoot, instance.Worktree, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(worktreeMarkerPath(created.Path)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("marker before recovery = %v, want not exist", err)
	}
	if err := runWorktreeWithIO([]string{"adopt", instance.Worktree, "--workspace", repoRoot}, &bytes.Buffer{}); !errors.Is(err, contextstate.ErrWorktreeDeleted) {
		t.Fatalf("adopt interrupted creation = %v, want ErrWorktreeDeleted", err)
	}
	recovered, err := createManagedWorktreeInStore(store, repoRoot, instance.Worktree, "HEAD", "mivia/")
	if err != nil {
		t.Fatalf("recover creation: %v", err)
	}
	if recovered.Path != created.Path {
		t.Fatalf("recovered path = %q, want %q", recovered.Path, created.Path)
	}
	marker, err := readWorktreeMarker(created.Path)
	if err != nil || marker != instance {
		t.Fatalf("recovered marker = %+v, %v", marker, err)
	}
	if err := store.ValidateActiveWorktreeInstance(context.Background(), principal, instance, expectedPath); err != nil {
		t.Fatalf("recovered instance is not active: %v", err)
	}
}

func TestBindManagedWorktreeSessionRejectsMismatchedCatalogPath(t *testing.T) {
	repoRoot := newWorktreeCommandRepo(t)
	writeWorktreeStoreConfig(t, repoRoot, "repository.db")
	worktree, err := createManagedWorktree(repoRoot, "binding-path", "HEAD", "mivia/")
	if err != nil {
		t.Fatal(err)
	}
	store, err := openRepositoryContextStore(repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	principal, err := worktreeRoutePrincipal(repoRoot)
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	otherPath := t.TempDir()
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(repoRoot, "repository.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE worktree_instances SET canonical_path=? WHERE workspace_id=? AND worktree=? AND state='active'`, otherPath, principal.WorkspaceID, worktree.Name); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	m := newReadyChatModel(30, 90)
	if err := bindManagedWorktreeSession(m.session, repoRoot, worktree.Path, filepath.Join(repoRoot, "repository.db")); !errors.Is(err, contextstate.ErrWorktreeDeleted) {
		t.Fatalf("bindManagedWorktreeSession = %v, want ErrWorktreeDeleted", err)
	}
}

func TestWorktreeCommandCreateListRemove(t *testing.T) {
	repoRoot := newWorktreeCommandRepo(t)
	storePath := filepath.Join(repoRoot, "repository.db")
	writeWorktreeStoreConfig(t, repoRoot, "repository.db")
	var output bytes.Buffer
	if err := runWorktreeWithIO([]string{"create", "Feature One", "--branch", "HEAD", "--workspace", repoRoot}, &output); err != nil {
		t.Fatalf("create: %v", err)
	}
	if !strings.Contains(output.String(), "created worktree \"feature-one\"") {
		t.Fatalf("create output = %q", output.String())
	}

	worktree, err := vcs.Resolve(context.Background(), repoRoot, "feature-one")
	if err != nil || worktree == nil {
		t.Fatalf("resolve created worktree = %v, %v", worktree, err)
	}
	routeStore, err := openContextStorePath(storePath)
	if err != nil {
		t.Fatalf("open route store: %v", err)
	}
	principal, err := worktreeRoutePrincipal(repoRoot)
	if err != nil {
		routeStore.Close()
		t.Fatal(err)
	}
	routes, err := routeStore.ListSessions(context.Background(), principal)
	routeStore.Close()
	if err != nil || len(routes) != 1 || !routes[0].WorktreeRoute || routes[0].Dir != worktree.Path {
		t.Fatalf("worktree route = %+v, err=%v", routes, err)
	}

	output.Reset()
	if err := runWorktreeWithIO([]string{"list", "--workspace", worktree.Path}, &output); err != nil {
		t.Fatalf("list from linked worktree: %v", err)
	}
	if !strings.Contains(output.String(), "feature-one\tmivia/feature-one\t"+worktree.Path) {
		t.Fatalf("list output = %q", output.String())
	}

	output.Reset()
	if err := runWorktreeWithIO([]string{"remove", "feature-one", "--workspace", repoRoot}, &output); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if output.String() != "removed worktree \"feature-one\"\n" {
		t.Fatalf("remove output = %q", output.String())
	}
	worktree, err = vcs.Resolve(context.Background(), repoRoot, "feature-one")
	if err != nil || worktree != nil {
		t.Fatalf("resolve removed worktree = %v, %v", worktree, err)
	}
	routeStore, err = openContextStorePath(storePath)
	if err != nil {
		t.Fatalf("reopen route store: %v", err)
	}
	routes, err = routeStore.ListSessions(context.Background(), principal)
	routeStore.Close()
	if err != nil || len(routes) != 0 {
		t.Fatalf("routes after remove = %+v, err=%v", routes, err)
	}
}

func TestWorktreeCommandUsesConfiguredBranchPrefixOutsideWorkspace(t *testing.T) {
	repoRoot := newWorktreeCommandRepo(t)
	writeWorktreeStoreConfig(t, repoRoot, "repository.db")
	configPath := filepath.Join(repoRoot, ".mivia", "mivia.toml")
	configText := worktreeStoreConfig("repository.db") + `
[worktrees]
branch_prefix = "team/"
`
	if err := os.WriteFile(configPath, []byte(configText), 0o600); err != nil {
		t.Fatalf("write worktree config: %v", err)
	}

	previousDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working directory: %v", err)
	}
	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatalf("change outside workspace: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(previousDir); err != nil {
			t.Errorf("restore working directory: %v", err)
		}
	})

	var output bytes.Buffer
	if err := runWorktreeWithIO([]string{"create", "Prefix Target", "--branch", "HEAD", "--workspace", repoRoot}, &output); err != nil {
		t.Fatalf("create: %v", err)
	}

	output.Reset()
	if err := runWorktreeWithIO([]string{"list", "--workspace", repoRoot}, &output); err != nil {
		t.Fatalf("list: %v", err)
	}
	if !strings.Contains(output.String(), "prefix-target\tteam/prefix-target\t") {
		t.Fatalf("list output = %q, want configured team/ branch", output.String())
	}
	if strings.Contains(output.String(), "wt/prefix-target") {
		t.Fatalf("list output = %q, must not use default wt/ branch", output.String())
	}

	output.Reset()
	if err := runWorktreeWithIO([]string{"remove", "Prefix Target", "--workspace", repoRoot}, &output); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if output.String() != "removed worktree \"prefix-target\"\n" {
		t.Fatalf("remove output = %q", output.String())
	}
	worktree, err := vcs.Resolve(context.Background(), repoRoot, "prefix-target")
	if err != nil || worktree != nil {
		t.Fatalf("resolve removed worktree = %v, %v", worktree, err)
	}
}

func TestWorktreeCommandPreservesExistingBranchWhenPrefixChanges(t *testing.T) {
	repoRoot := newWorktreeCommandRepo(t)
	configPath := filepath.Join(repoRoot, ".mivia", "mivia.toml")
	configText := worktreeStoreConfig("repository.db") + `
[worktrees]
branch_prefix = "team/"
`
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatalf("make config directory: %v", err)
	}
	if err := os.WriteFile(configPath, []byte(configText), 0o600); err != nil {
		t.Fatalf("write initial worktree config: %v", err)
	}

	if err := runWorktreeWithIO([]string{"create", "Preserved Branch", "--branch", "HEAD", "--workspace", repoRoot}, &bytes.Buffer{}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := os.WriteFile(configPath, []byte(worktreeStoreConfig("repository.db")), 0o600); err != nil {
		t.Fatalf("change worktree config: %v", err)
	}

	if err := runWorktreeWithIO([]string{"remove", "Preserved Branch", "--workspace", repoRoot}, &bytes.Buffer{}); err != nil {
		t.Fatalf("remove after prefix change: %v", err)
	}
	worktree, err := vcs.Resolve(context.Background(), repoRoot, "preserved-branch")
	if err != nil || worktree != nil {
		t.Fatalf("resolve removed worktree = %v, %v", worktree, err)
	}
	if err := exec.Command("git", "-C", repoRoot, "show-ref", "--verify", "--quiet", "refs/heads/team/preserved-branch").Run(); err != nil {
		t.Fatalf("old branch was removed after prefix change: %v", err)
	}
}

func writeWorktreeStoreConfig(t *testing.T, root, storePath string) {
	t.Helper()
	configDir := filepath.Join(root, ".mivia")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	configText := worktreeStoreConfig(storePath)
	if err := os.WriteFile(filepath.Join(configDir, "mivia.toml"), []byte(configText), 0o600); err != nil {
		t.Fatal(err)
	}
}

func worktreeStoreConfig(storePath string) string {
	return `[provider]
name = "deepseek"

[providers.deepseek]
models = [{ name = "deepseek-v4-flash", context_window_tokens = 128000 }]

[subagents]
store_backend = "sqlite"
store_path = "` + storePath + `"
`
}

func TestWorktreeCommandRefusesCurrentWorktreeRemoval(t *testing.T) {
	repoRoot := newWorktreeCommandRepo(t)
	worktree, err := vcs.Create(context.Background(), repoRoot, "protected", "HEAD")
	if err != nil {
		t.Fatalf("create worktree: %v", err)
	}

	previousDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working directory: %v", err)
	}
	if err := os.Chdir(worktree.Path); err != nil {
		t.Fatalf("change to worktree: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(previousDir); err != nil {
			t.Errorf("restore working directory: %v", err)
		}
	})

	err = runWorktreeWithIO([]string{"remove", "protected", "--workspace", repoRoot}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "cannot remove the current worktree") {
		t.Fatalf("remove current worktree error = %v", err)
	}
}

func TestWorktreeCommandRejectsInvalidFlags(t *testing.T) {
	tests := [][]string{
		{"create", "--unknown"},
		{"create", "feature", "--branch"},
		{"list", "--branch", "HEAD"},
		{"remove", "feature", "--workspace="},
	}
	for _, args := range tests {
		t.Run(strings.Join(args, "_"), func(t *testing.T) {
			err := runWorktreeWithIO(args, &bytes.Buffer{})
			if err == nil {
				t.Fatalf("runWorktreeWithIO(%q) succeeds", args)
			}
		})
	}
}

func newWorktreeCommandRepo(t *testing.T) string {
	t.Helper()
	repoRoot := t.TempDir()
	runGit(t, repoRoot, "init")
	runGit(t, repoRoot, "config", "user.email", "test@example.com")
	runGit(t, repoRoot, "config", "user.name", "Test User")
	path := filepath.Join(repoRoot, "README.md")
	if err := os.WriteFile(path, []byte("test\n"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	runGit(t, repoRoot, "add", "README.md")
	runGit(t, repoRoot, "commit", "-m", "initial")
	return repoRoot
}
