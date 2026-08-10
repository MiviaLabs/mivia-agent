package cli

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/contextmgr"
	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
	"github.com/MiviaLabs/mivia-agent/internal/vcs"
)

func TestRuntimeCoverageRestartRejectsInvalidRepositoryConfig(t *testing.T) {
	repo := newWorktreeCommandRepo(t)
	configPath := filepath.Join(repo, "invalid.toml")
	if err := os.WriteFile(configPath, []byte("[broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	expected := contextstate.WorktreeInstance{Worktree: "wt-a", ID: "wt_1234567890abcdef"}
	err := validateWorkspaceRestart(workspaceRestart{dir: repo, worktreeInstance: expected}, chatInvocation{configPath: configPath})
	if err == nil {
		t.Fatal("restart with invalid repository config succeeded")
	}
}

func TestRuntimeCoverageBindingRejectsMarkerNameMismatch(t *testing.T) {
	repo := newWorktreeCommandRepo(t)
	worktree, err := vcs.CreateWithPrefix(context.Background(), repo, "marker-name", "HEAD", "mivia/")
	if err != nil {
		t.Fatal(err)
	}
	marker := contextstate.WorktreeInstance{Worktree: "other", ID: "wt_1234567890abcdef"}
	if err := writeWorktreeMarker(worktree.Path, marker); err != nil {
		t.Fatal(err)
	}
	session := chat.NewSession(&config.Resolved{Model: "model"}, nullCompleter{})
	err = bindManagedWorktreeSession(session, repo, worktree.Path, "")
	if err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("marker mismatch error = %v", err)
	}
}

func TestRuntimeCoverageBindingRejectsNoncanonicalSessionDirectory(t *testing.T) {
	repo := newWorktreeCommandRepo(t)
	worktree, _, err := createManagedWorktreeWithInstance(repo, "session-link", "HEAD", "mivia/")
	if err != nil {
		t.Fatal(err)
	}
	realDir := filepath.Join(worktree.Path, "real")
	if err := os.Mkdir(realDir, 0o700); err != nil {
		t.Fatal(err)
	}
	linkedDir := filepath.Join(worktree.Path, "linked")
	if err := os.Symlink(realDir, linkedDir); err != nil {
		t.Fatal(err)
	}
	session := chat.NewSession(&config.Resolved{Model: "model"}, nullCompleter{})
	if err := bindManagedWorktreeSession(session, repo, linkedDir, ""); err == nil {
		t.Fatal("managed binding accepted a noncanonical session directory")
	}
}

func TestRuntimeCoverageBindingReturnsClassificationFailure(t *testing.T) {
	original := classifyMissingMarkerForBind
	want := errors.New("classification failed")
	classifyMissingMarkerForBind = func(*storage.SQLite, contextstate.Principal, string, string) (contextstate.WorktreeInstanceInfo, bool, error) {
		return contextstate.WorktreeInstanceInfo{}, false, want
	}
	t.Cleanup(func() { classifyMissingMarkerForBind = original })
	repo := newWorktreeCommandRepo(t)
	worktree, err := vcs.CreateWithPrefix(context.Background(), repo, "classification", "HEAD", "mivia/")
	if err != nil {
		t.Fatal(err)
	}
	session := chat.NewSession(&config.Resolved{Model: "model"}, nullCompleter{})
	if err := bindManagedWorktreeSession(session, repo, worktree.Path, ""); !errors.Is(err, want) {
		t.Fatalf("classification error = %v, want %v", err, want)
	}
}

func TestRuntimeCoverageCreateRecoversReservations(t *testing.T) {
	for _, state := range []contextstate.WorktreeInstanceState{contextstate.WorktreeCreating, contextstate.WorktreeDeleting} {
		t.Run(string(state), func(t *testing.T) {
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
			instance := contextstate.WorktreeInstance{Worktree: "recover", ID: "wt_1234567890abcdef"}
			path := filepath.Join(repo, ".mivia", "worktrees", instance.Worktree)
			if err := store.BeginWorktreeCreation(context.Background(), principal, instance, path); err != nil {
				t.Fatal(err)
			}
			if state == contextstate.WorktreeDeleting {
				if err := store.RegisterWorktreeInstance(context.Background(), principal, instance, path); err != nil {
					t.Fatal(err)
				}
				if err := store.BeginWorktreeDeletion(context.Background(), principal, instance); err != nil {
					t.Fatal(err)
				}
			}
			worktree, err := createManagedWorktreeInStore(store, repo, instance.Worktree, "HEAD", "mivia/")
			if err != nil || worktree == nil {
				t.Fatalf("recover %s reservation = %+v, %v", state, worktree, err)
			}
		})
	}
}

func TestRuntimeCoverageRemovalAndReactivationWrappers(t *testing.T) {
	repo := newWorktreeCommandRepo(t)
	worktree, instance, err := createManagedWorktreeWithInstance(repo, "wrapper", "HEAD", "mivia/")
	if err != nil {
		t.Fatal(err)
	}
	session := chat.NewSession(&config.Resolved{Model: "model"}, nullCompleter{})
	got, err := beginManagedWorktreeRemovalForSession(session, repo, worktree)
	if err != nil || got != instance {
		t.Fatalf("begin removal = %+v, %v", got, err)
	}
	if err := reactivateManagedWorktreeForSession(session, repo, instance); err != nil {
		t.Fatalf("reactivate wrapper: %v", err)
	}

	store, err := openRepositoryContextStore(repo)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := reactivateManagedWorktreeInStore(store, repo, instance); err == nil {
		t.Fatal("reactivation with a closed store succeeded")
	}
}

func TestRuntimeCoverageReactivationRejectsBrokenGitAndPath(t *testing.T) {
	t.Run("broken git", func(t *testing.T) {
		repo := newWorktreeCommandRepo(t)
		_, instance, err := createManagedWorktreeWithInstance(repo, "broken-git", "HEAD", "mivia/")
		if err != nil {
			t.Fatal(err)
		}
		store, err := openRepositoryContextStore(repo)
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close()
		principal, _ := worktreeRoutePrincipal(repo)
		if err := store.BeginWorktreeDeletion(context.Background(), principal, instance); err != nil {
			t.Fatal(err)
		}
		gitDir := filepath.Join(repo, ".git")
		moved := filepath.Join(repo, ".git-moved")
		if err := os.Rename(gitDir, moved); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Rename(moved, gitDir) })
		if err := reactivateManagedWorktreeInStore(store, repo, instance); err == nil {
			t.Fatal("reactivation with broken Git metadata succeeded")
		}
	})

	t.Run("catalog path mismatch", func(t *testing.T) {
		repo := newWorktreeCommandRepo(t)
		worktree, err := vcs.CreateWithPrefix(context.Background(), repo, "path-mismatch", "HEAD", "mivia/")
		if err != nil {
			t.Fatal(err)
		}
		store, err := openRepositoryContextStore(repo)
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close()
		principal, _ := worktreeRoutePrincipal(repo)
		instance := contextstate.WorktreeInstance{Worktree: worktree.Name, ID: "wt_1234567890abcdef"}
		wrongPath := filepath.Join(repo, ".mivia", "worktrees", "other")
		if err := store.BeginWorktreeCreation(context.Background(), principal, instance, wrongPath); err != nil {
			t.Fatal(err)
		}
		if err := store.RegisterWorktreeInstance(context.Background(), principal, instance, wrongPath); err != nil {
			t.Fatal(err)
		}
		if err := writeWorktreeMarker(worktree.Path, instance); err != nil {
			t.Fatal(err)
		}
		if err := store.BeginWorktreeDeletion(context.Background(), principal, instance); err != nil {
			t.Fatal(err)
		}
		if err := reactivateManagedWorktreeInStore(store, repo, instance); !errors.Is(err, contextstate.ErrWorktreeDeleted) {
			t.Fatalf("path mismatch error = %v", err)
		}
	})
}

func TestRuntimeCoverageReactivationRejectsMarkerReplacement(t *testing.T) {
	repo := newWorktreeCommandRepo(t)
	worktree, instance, err := createManagedWorktreeWithInstance(repo, "marker-replaced", "HEAD", "mivia/")
	if err != nil {
		t.Fatal(err)
	}
	store, err := openRepositoryContextStore(repo)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	principal, _ := worktreeRoutePrincipal(repo)
	if err := store.BeginWorktreeDeletion(context.Background(), principal, instance); err != nil {
		t.Fatal(err)
	}
	replacement := contextstate.WorktreeInstance{Worktree: instance.Worktree, ID: "wt_0000000000000000"}
	if err := writeWorktreeMarker(worktree.Path, replacement); err != nil {
		t.Fatal(err)
	}
	if err := reactivateManagedWorktreeInStore(store, repo, instance); !errors.Is(err, contextstate.ErrWorktreeDeleted) {
		t.Fatalf("replacement marker error = %v", err)
	}
}

func TestRuntimeCoverageMarkerClassificationDatabaseErrors(t *testing.T) {
	repo := newWorktreeCommandRepo(t)
	storePath := config.DefaultStorePathForWorkspace(repo)
	store, err := storage.OpenSQLite(storePath)
	if err != nil {
		t.Fatal(err)
	}
	principal, _ := worktreeRoutePrincipal(repo)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := classifyMissingWorktreeMarker(store, principal, "wt-a", filepath.Join(repo, "wt-a")); err == nil {
		t.Fatal("classification with a closed store succeeded")
	}

	store, err = storage.OpenSQLite(storePath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	db, err := sql.Open("sqlite", storePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DROP TABLE worktree_routes`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	db.Close()
	if _, _, err := classifyMissingWorktreeMarker(store, principal, "wt-a", filepath.Join(repo, "wt-a")); err == nil {
		t.Fatal("classification with a missing route table succeeded")
	}
}

func TestRuntimeCoverageExpectedInstanceRejectsDanglingSessionPath(t *testing.T) {
	repo := newWorktreeCommandRepo(t)
	worktree, instance, err := createManagedWorktreeWithInstance(repo, "dangling-dir", "HEAD", "mivia/")
	if err != nil {
		t.Fatal(err)
	}
	store, err := openRepositoryContextStore(repo)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	dangling := filepath.Join(worktree.Path, "dangling")
	if err := os.Symlink(filepath.Join(worktree.Path, "missing"), dangling); err != nil {
		t.Fatal(err)
	}
	if err := validateExpectedWorktreeInstanceInStore(store, repo, dangling, instance); !errors.Is(err, contextstate.ErrWorktreeDeleted) {
		t.Fatalf("dangling session path error = %v", err)
	}
}

func TestRuntimeCoverageMarkerRootHelperRejectsSymlink(t *testing.T) {
	realRoot := t.TempDir()
	linkedRoot := filepath.Join(t.TempDir(), "linked")
	if err := os.Symlink(realRoot, linkedRoot); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readMarkerAtCanonicalRoot(linkedRoot); err == nil {
		t.Fatal("marker helper accepted a symlink root")
	}
}

func TestRuntimeCoverageRouteRemovalSuccessWrappers(t *testing.T) {
	for _, withSession := range []bool{false, true} {
		repo := newWorktreeCommandRepo(t)
		store, err := openRepositoryContextStore(repo)
		if err != nil {
			t.Fatal(err)
		}
		principal, _ := worktreeRoutePrincipal(repo)
		if err := store.SaveWorktreeRoute(context.Background(), principal, "route", filepath.Join(repo, "route")); err != nil {
			t.Fatal(err)
		}
		store.Close()
		if withSession {
			session := chat.NewSession(&config.Resolved{Model: "model"}, nullCompleter{})
			if err := removeWorktreeRouteForSession(session, repo, "route"); err != nil {
				t.Fatal(err)
			}
		} else if err := removeWorktreeRoute(repo, "route"); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRuntimeCoverageRepositoryContextAndIdentityErrors(t *testing.T) {
	repo := newWorktreeCommandRepo(t)
	configDir := filepath.Join(repo, ".mivia")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "mivia.toml"), []byte("[broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := openRepositoryContextStore(repo); err == nil {
		t.Fatal("repository context store accepted invalid config")
	}

	oldDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	removed := filepath.Join(t.TempDir(), "removed")
	if err := os.Mkdir(removed, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(removed); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		// Windows cannot delete its process working directory; there the
		// identity probe simply runs from the (valid) directory it chdir'd
		// into, which is the closest supported-platform contract.
		if err := os.Remove(removed); err != nil {
			t.Fatal(err)
		}
	}
	_ = contextWorkspaceID(".")
	if err := os.Chdir(oldDir); err != nil {
		t.Fatal(err)
	}
}

func TestRuntimeCoverageCreateReportsRepositoryStoreError(t *testing.T) {
	repo := newWorktreeCommandRepo(t)
	blocker := filepath.Join(repo, "blocked")
	if err := os.WriteFile(blocker, []byte("blocked"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeWorktreeStoreConfig(t, repo, filepath.Join(blocker, "context.db"))
	if _, _, err := createManagedWorktreeWithInstance(repo, "blocked", "HEAD", "mivia/"); err == nil {
		t.Fatal("managed create opened a store through a regular file")
	}
}

func TestRuntimeCoverageEnableContextRejectsInvalidSessionID(t *testing.T) {
	store, err := storage.OpenSQLite(filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	session := chat.NewSession(&config.Resolved{Model: "model"}, nullCompleter{})
	session.SessionID = ""
	if err := enableSessionContext(session, t.TempDir(), store); err == nil {
		t.Fatal("context setup accepted an invalid session ID")
	}
}

func TestRuntimeCoverageEnableContextReturnsManagerFailure(t *testing.T) {
	original := setContextManagerForSetup
	want := errors.New("manager failed")
	setContextManagerForSetup = func(*chat.Session, *contextmgr.ContextManager, contextstate.Principal) error {
		return want
	}
	t.Cleanup(func() { setContextManagerForSetup = original })
	store, err := storage.OpenSQLite(filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	session := chat.NewSession(&config.Resolved{Model: "model"}, nullCompleter{})
	if err := enableSessionContext(session, t.TempDir(), store); !errors.Is(err, want) {
		t.Fatalf("manager error = %v, want %v", err, want)
	}
}
