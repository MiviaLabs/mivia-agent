package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
)

func blockedContextRoot(t *testing.T) string {
	t.Helper()
	root := newWorktreeCommandRepo(t)
	blocker := filepath.Join(root, "blocked")
	if err := os.WriteFile(blocker, []byte("blocked"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeWorktreeStoreConfig(t, root, filepath.Join(blocker, "context.db"))
	return root
}

func TestContextSetupCoverageOpenErrors(t *testing.T) {
	blocked := blockedContextRoot(t)
	if _, err := openRepositoryContextStore(blocked); err == nil {
		t.Fatal("repository store opened through a file")
	}
	res := &config.Resolved{Subagents: config.SubagentConfig{StoreBackend: "sqlite", StorePath: filepath.Join(blocked, "blocked", "store.db")}}
	if _, err := setupSessionContext(chat.NewSession(&config.Resolved{Model: "model"}, nullCompleter{}), blocked, res); err == nil {
		t.Fatal("session store opened through a file")
	}
	if _, err := setupRepositorySessionContext(chat.NewSession(&config.Resolved{Model: "model"}, nullCompleter{}), blocked, blocked, &config.Resolved{}); err == nil {
		t.Fatal("directory opened as a database")
	}
}

func TestContextSetupCoverageConfigureErrorsAndZeroPolicy(t *testing.T) {
	store, err := storage.OpenSQLite(filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := configureSessionContext(nil, t.TempDir(), store, &config.Resolved{}); err == nil {
		t.Fatal("nil session configuration succeeded")
	}
	if policy := contextRedactionPolicy(nil); policy.Configured {
		t.Fatal("nil redaction policy is configured")
	}
	if policy := contextRedactionPolicy(&config.Resolved{}); policy.Configured {
		t.Fatal("empty redaction policy is configured")
	}
	if err := enableSessionContext(nil, "", nil); err == nil {
		t.Fatal("nil context components succeeded")
	}
	session := chat.NewSession(&config.Resolved{Model: "model"}, nullCompleter{})
	if wiring := contextDispatcherFor(session, config.SubagentConfig{}); wiring.preparation != nil || wiring.sharedSQLite != nil {
		t.Fatalf("disabled context wiring = %+v", wiring)
	}
}

func TestContextSetupCoverageWorkspaceIdentityCanonicalizesSymlink(t *testing.T) {
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if contextWorkspaceID(real) != contextWorkspaceID(link) {
		t.Fatal("symlink changed workspace identity")
	}
}

func TestContextSetupCoverageRemovalGuards(t *testing.T) {
	repo := newWorktreeCommandRepo(t)
	mismatchedWorktree, _, err := createManagedWorktreeWithInstance(repo, "mismatched", "HEAD", "mivia/")
	if err != nil {
		t.Fatal(err)
	}
	mismatchedMarker := contextstate.WorktreeInstance{Worktree: "other", ID: "wt_1111111111111111"}
	if err := writeWorktreeMarker(mismatchedWorktree.Path, mismatchedMarker); err != nil {
		t.Fatal(err)
	}
	if _, err := beginManagedWorktreeRemovalInStore(nil, repo, mismatchedWorktree); err == nil {
		t.Fatal("mismatched marker name removal succeeded")
	}
	worktree, instance, err := createManagedWorktreeWithInstance(repo, "guarded", "HEAD", "mivia/")
	if err != nil {
		t.Fatal(err)
	}
	wrong := contextstate.WorktreeInstance{Worktree: worktree.Name, ID: "wt_0000000000000000"}
	if _, err := beginManagedWorktreeRemovalInStoreExpected(nil, repo, nil, wrong, true); err == nil {
		t.Fatal("nil worktree removal succeeded")
	}
	if _, err := beginManagedWorktreeRemovalInStoreExpected(nil, repo, worktree, wrong, true); !errors.Is(err, contextstate.ErrWorktreeDeleted) {
		t.Fatalf("wrong expected instance error = %v", err)
	}
	if err := finishManagedWorktreeRemoval(blockedContextRoot(t), instance); err == nil {
		t.Fatal("finish removal opened blocked store")
	}
	if err := reactivateManagedWorktreeInStore(nil, blockedContextRoot(t), instance); err == nil {
		t.Fatal("reactivation opened blocked store")
	}
	store, err := openRepositoryContextStore(repo)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := reactivateManagedWorktreeInStore(store, repo, wrong); !errors.Is(err, contextstate.ErrWorktreeDeleted) {
		t.Fatalf("unknown reactivation error = %v", err)
	}
}

func TestContextSetupCoverageMarkerClassification(t *testing.T) {
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
	path := filepath.Join(repo, ".mivia", "worktrees", "wt-a")
	if info, legacy, err := classifyMissingWorktreeMarker(store, principal, "wt-a", path); err != nil || legacy || !info.Instance.IsZero() {
		t.Fatalf("empty classification = %+v, %v, %v", info, legacy, err)
	}
	if err := store.SaveWorktreeRoute(context.Background(), principal, "wt-a", path); err != nil {
		t.Fatal(err)
	}
	if _, legacy, err := classifyMissingWorktreeMarker(store, principal, "wt-a", path); err != nil || !legacy {
		t.Fatalf("legacy classification = %v, %v", legacy, err)
	}
	instance := contextstate.WorktreeInstance{Worktree: "wt-b", ID: "wt_1234567890abcdef"}
	pathB := filepath.Join(repo, ".mivia", "worktrees", "wt-b")
	if err := store.BeginWorktreeCreation(context.Background(), principal, instance, pathB); err != nil {
		t.Fatal(err)
	}
	info, legacy, err := classifyMissingWorktreeMarker(store, principal, "wt-b", pathB)
	if err != nil || legacy || info.Instance != instance {
		t.Fatalf("live classification = %+v, %v, %v", info, legacy, err)
	}
}

func TestContextSetupCoverageExpectedInstanceValidation(t *testing.T) {
	repo := newWorktreeCommandRepo(t)
	worktree, instance, err := createManagedWorktreeWithInstance(repo, "expected", "HEAD", "mivia/")
	if err != nil {
		t.Fatal(err)
	}
	store, err := openRepositoryContextStore(repo)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := validateExpectedWorktreeInstanceInStore(store, repo, worktree.Path, contextstate.WorktreeInstance{}); err != nil {
		t.Fatal(err)
	}
	missing := contextstate.WorktreeInstance{Worktree: "missing", ID: "wt_1234567890abcdef"}
	if err := validateExpectedWorktreeInstanceInStore(store, repo, repo, missing); !errors.Is(err, contextstate.ErrWorktreeDeleted) {
		t.Fatalf("missing expected instance error = %v", err)
	}
	if err := validateExpectedWorktreeInstanceInStore(store, repo, repo, instance); !errors.Is(err, contextstate.ErrWorktreeDeleted) {
		t.Fatalf("outside worktree error = %v", err)
	}
	child := filepath.Join(worktree.Path, "child")
	if err := os.Mkdir(child, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := validateExpectedWorktreeInstanceInStore(store, repo, child, instance); err != nil {
		t.Fatalf("valid expected instance: %v", err)
	}
}

func TestContextSetupCoverageRoutePrincipalErrors(t *testing.T) {
	repo := newWorktreeCommandRepo(t)
	worktree, instance, err := createManagedWorktreeWithInstance(repo, "route-error", "HEAD", "mivia/")
	if err != nil {
		t.Fatal(err)
	}
	store, err := openRepositoryContextStore(repo)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	original := contextSetupRoutePrincipal
	injected := errors.New("route principal failure")
	contextSetupRoutePrincipal = func(string) (contextstate.Principal, error) {
		return contextstate.Principal{}, injected
	}
	t.Cleanup(func() { contextSetupRoutePrincipal = original })

	if _, err := createManagedWorktreeInStoreLocked(store, repo, "new", "HEAD", "mivia/", nil, nil); !errors.Is(err, injected) {
		t.Fatalf("create route error = %v", err)
	}
	if err := reactivateManagedWorktreeInStore(store, repo, instance); !errors.Is(err, injected) {
		t.Fatalf("reactivate route error = %v", err)
	}
	if err := validateExpectedWorktreeInstanceInStore(store, repo, worktree.Path, instance); !errors.Is(err, injected) {
		t.Fatalf("validate route error = %v", err)
	}
}

func TestContextSetupCoverageCreationAbandonError(t *testing.T) {
	repo := newWorktreeCommandRepo(t)
	store, err := openRepositoryContextStore(repo)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	original := abandonContextWorktreeCreation
	injected := errors.New("abandon failure")
	abandonContextWorktreeCreation = func(*storage.SQLite, contextstate.Principal, contextstate.WorktreeInstance) error {
		return injected
	}
	t.Cleanup(func() { abandonContextWorktreeCreation = original })

	_, err = createManagedWorktreeInStoreWithInstance(store, repo, "bad-base", "refs/heads/does-not-exist", "mivia/", nil)
	if err == nil || !strings.Contains(err.Error(), injected.Error()) {
		t.Fatalf("create abandon error = %v", err)
	}
}

func TestContextSetupCoverageRegisterForSQLiteSessionRejectsNilWorktree(t *testing.T) {
	store, err := storage.OpenSQLite(filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	session := chat.NewSession(&config.Resolved{Model: "model"}, nullCompleter{})
	if err := session.SetContextStore(store); err != nil {
		t.Fatal(err)
	}
	if _, err := registerManagedWorktreeForSession(session, t.TempDir(), nil); err == nil {
		t.Fatal("nil worktree registration succeeded")
	}
}

func TestContextSetupCoverageRouteRemovalWrappers(t *testing.T) {
	repo := newWorktreeCommandRepo(t)
	store, err := openRepositoryContextStore(repo)
	if err != nil {
		t.Fatal(err)
	}
	principal, err := worktreeRoutePrincipal(repo)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveWorktreeRoute(context.Background(), principal, "route", filepath.Join(repo, "route")); err != nil {
		t.Fatal(err)
	}
	session := chat.NewSession(&config.Resolved{Model: "model"}, nullCompleter{})
	if err := session.SetContextStore(store); err != nil {
		t.Fatal(err)
	}
	if err := removeWorktreeRouteForSession(session, repo, "route"); err != nil {
		t.Fatal(err)
	}
	store.Close()
	if err := removeWorktreeRoute(blockedContextRoot(t), "route"); err == nil {
		t.Fatal("route removal opened blocked store")
	}
}
