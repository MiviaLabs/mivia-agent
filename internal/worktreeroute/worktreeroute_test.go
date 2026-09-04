package worktreeroute

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
)

// routeFixture registers one active managed worktree ("wt1") with a launch
// route in a fresh repository store rooted at mainDir.
func routeFixture(t *testing.T) (store *storage.SQLite, principal contextstate.Principal, mainDir, wtDir string) {
	t.Helper()
	mainDir = filepath.Join(t.TempDir(), "main")
	wtDir = filepath.Join(filepath.Dir(mainDir), ".mivia", "worktrees", "wt1")
	for _, dir := range []string{mainDir, wtDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("create fixture dirs: %v", err)
		}
	}
	canonical, err := CanonicalDir(wtDir)
	if err != nil {
		t.Fatalf("canonicalize worktree dir: %v", err)
	}
	store, err = storage.OpenSQLite(filepath.Join(t.TempDir(), "ctx.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	principal, err = Principal(mainDir)
	if err != nil {
		t.Fatalf("derive principal: %v", err)
	}
	instance := contextstate.WorktreeInstance{Worktree: "wt1", ID: "wt_0001020304050607"}
	// Mirror the managed lifecycle: reserve creation, then activate - the
	// activation also upserts the launch route row.
	if err := store.BeginWorktreeCreation(context.Background(), principal, instance, canonical); err != nil {
		t.Fatalf("begin worktree creation: %v", err)
	}
	if err := store.RegisterWorktreeInstance(context.Background(), principal, instance, canonical); err != nil {
		t.Fatalf("register instance: %v", err)
	}
	return store, principal, mainDir, canonical
}

func TestStartInRoute_BindsBeforeContextSetup_WithoutUpsertingRoute(t *testing.T) {
	store, _, mainDir, wtDir := routeFixture(t)
	ctx := context.Background()

	sess := chat.NewSession(&config.Resolved{Model: "test-model"}, nil)
	if _, err := StartInRoute(ctx, sess, store, mainDir, Route{Worktree: "wt1", Dir: wtDir}); err != nil {
		t.Fatalf("StartInRoute: %v", err)
	}

	// Binding precedes context setup: installing the store afterwards must
	// be accepted, and the catalog keeps exactly one route row for wt1 -
	// StartInRoute validates the existing row rather than adding another.
	if err := sess.SetContextStore(store); err != nil {
		t.Fatalf("install context store after binding: %v", err)
	}
	live, principal := storeListRouteCount(t, store, mainDir)
	if live != 1 {
		t.Fatalf("catalog holds %d route rows for wt1, want exactly 1", live)
	}
	_ = principal
}

// storeListRouteCount lists the catalog under repo root and returns how
// many synthesized route pseudo-rows storage surfaces.
func storeListRouteCount(t *testing.T, store *storage.SQLite, mainDir string) (int, contextstate.Principal) {
	t.Helper()
	principal, err := Principal(mainDir)
	if err != nil {
		t.Fatalf("derive principal: %v", err)
	}
	infos, err := store.ListSessions(context.Background(), principal)
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	n := 0
	for _, info := range infos {
		if info.WorktreeRoute && info.Worktree == "wt1" {
			n++
		}
	}
	return n, principal
}

func TestStartInRoute_FailsClosedOnUnknownOrStaleRoute(t *testing.T) {
	store, _, mainDir, wtDir := routeFixture(t)
	ctx := context.Background()

	newSess := func() *chat.Session { return chat.NewSession(&config.Resolved{}, nil) }

	if _, err := StartInRoute(ctx, newSess(), store, mainDir, Route{Worktree: "ghost", Dir: wtDir}); err == nil {
		t.Fatal("StartInRoute bound an unmanaged worktree name")
	}
	stale := contextstate.WorktreeInstance{Worktree: "wt1", ID: "wt_stale"}
	if _, err := StartInRoute(ctx, newSess(), store, mainDir, Route{Worktree: "wt1", Dir: wtDir, Instance: stale}); err == nil {
		t.Fatal("StartInRoute accepted a stale instance over the live one")
	}
}

func TestRoot_FailsClosedOutsideARepository(t *testing.T) {
	outside := t.TempDir()
	_, err := Root(outside)
	if err == nil {
		t.Fatal("Root succeeded outside any git repository")
	}
	if !strings.Contains(err.Error(), outside) {
		t.Errorf("error %q does not name the offending directory", err)
	}
}

func TestCanonicalDir_Guards(t *testing.T) {
	if _, err := CanonicalDir("relative/path"); err == nil {
		t.Fatal("CanonicalDir accepted a relative path")
	}
	if _, err := CanonicalDir(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("CanonicalDir accepted a directory that does not exist")
	}
	canonical, err := CanonicalDir(t.TempDir())
	if err != nil || !filepath.IsAbs(canonical) {
		t.Fatalf("CanonicalDir(TempDir) = %q, %v", canonical, err)
	}
}

func TestStartInRoute_GuardsEmptyInputs(t *testing.T) {
	store, _, mainDir, wtDir := routeFixture(t)
	ctx := context.Background()

	if _, err := StartInRoute(ctx, nil, store, mainDir, Route{Worktree: "wt1", Dir: wtDir}); err == nil {
		t.Fatal("StartInRoute accepted a nil session")
	}
	sess := chat.NewSession(&config.Resolved{}, nil)
	if _, err := StartInRoute(ctx, sess, store, mainDir, Route{Worktree: "", Dir: wtDir}); err == nil {
		t.Fatal("StartInRoute accepted an empty worktree name")
	}
	if _, err := StartInRoute(ctx, sess, store, mainDir, Route{Worktree: "wt1", Dir: ""}); err == nil {
		t.Fatal("StartInRoute accepted an empty directory")
	}
}

func TestStartInRoute_FailsOnUndecodableDirAndDriftedPath(t *testing.T) {
	store, _, mainDir, _ := routeFixture(t)
	ctx := context.Background()
	newSess := func() *chat.Session { return chat.NewSession(&config.Resolved{}, nil) }

	// A directory string storage would never have recorded fails before
	// any binding is retained.
	if _, err := StartInRoute(ctx, newSess(), store, mainDir, Route{Worktree: "wt1", Dir: "relative/dir"}); err == nil {
		t.Fatal("StartInRoute accepted a relative worktree dir")
	}
	// A decodable directory that is NOT the registered canonical path
	// drifts from the instance registration - fail closed rather than bind
	// a session to a moved worktree.
	elsewhere := filepath.Join(t.TempDir(), "elsewhere-wt1")
	if err := os.MkdirAll(elsewhere, 0o755); err != nil {
		t.Fatalf("create drifted dir: %v", err)
	}
	if _, err := StartInRoute(ctx, newSess(), store, mainDir, Route{Worktree: "wt1", Dir: elsewhere}); err == nil {
		t.Fatal("StartInRoute bound a directory drifting from its registration")
	}
}

// TestWorkspaceID_FallsBackToFallbackRootWhenCwdVanishes exercises the
// filepath.Abs error branch: with the working directory deleted beneath
// us, Abs of a relative path fails and WorkspaceID must still return the
// cleaned-root digest instead of panicking or producing an empty id.
func TestWorkspaceID_FallsBackToFallbackRootWhenCwdVanishes(t *testing.T) {
	previous, err := os.Getwd()
	if err != nil {
		t.Fatalf("capture cwd: %v", err)
	}
	vanishing := t.TempDir()
	if err := os.Chdir(vanishing); err != nil {
		t.Skipf("platform cannot chdir into a tempdir: %v", err)
	}
	defer func() { _ = os.Chdir(previous) }()
	if err := os.Remove(vanishing); err != nil {
		t.Skipf("platform cannot remove the working directory: %v", err)
	}

	// With the cwd gone, filepath.Abs fails and the fallback digests the
	// CLEANED relative input itself; EvalSymlinks cannot resolve either,
	// so the digest is exactly sha256("relative-child") - assert the full
	// value, not just the prefix (every return path carries the prefix).
	digest := sha256.Sum256([]byte("relative-child"))
	want := "workspace-" + hex.EncodeToString(digest[:8])
	if got := WorkspaceID("relative-child"); got != want {
		t.Fatalf("WorkspaceID after cwd removal = %q, want %q", got, want)
	}
}

// TestStartInRoute_BindsSubdirectoryAgainstWorktreeRoot pins the root/dir
// split: a bound session saved from a SUBDIRECTORY of the worktree must
// bind (validated against the instance's canonical root), with the
// returned Route carrying the live instance and the worktree ROOT - not
// the subdirectory - so callers can run physical-identity checks against
// the exact bound target.
func TestStartInRoute_BindsSubdirectoryAgainstWorktreeRoot(t *testing.T) {
	store, _, mainDir, wtDir := routeFixture(t)
	ctx := context.Background()

	sub := filepath.Join(wtDir, "internal", "pkg")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("create subdir: %v", err)
	}
	sess := chat.NewSession(&config.Resolved{Model: "test-model"}, nil)
	bound, err := StartInRoute(ctx, sess, store, mainDir, Route{Worktree: "wt1", Dir: sub})
	if err != nil {
		t.Fatalf("StartInRoute(subdir): %v", err)
	}
	if bound.Dir != wtDir {
		t.Fatalf("returned route dir = %q, want the worktree root %q", bound.Dir, wtDir)
	}
	if bound.Instance.Worktree != "wt1" || bound.Instance.ID == "" {
		t.Fatalf("returned route instance = %+v, want the live wt1 instance", bound.Instance)
	}
	if got := sess.ContextWorktreeBinding(); got != bound.Instance {
		t.Fatalf("session binding = %+v, want %+v", got, bound.Instance)
	}
}

// TestStartInRoute_RejectsDirOutsideWorktreeRootByName pins the message of
// the containment arm: an existing directory that is neither the worktree
// root nor inside it must be named as outside, not misreported as a
// deleted worktree.
func TestStartInRoute_RejectsDirOutsideWorktreeRootByName(t *testing.T) {
	store, _, mainDir, _ := routeFixture(t)
	ctx := context.Background()

	elsewhere := filepath.Join(t.TempDir(), "elsewhere-wt1")
	if err := os.MkdirAll(elsewhere, 0o755); err != nil {
		t.Fatalf("create outside dir: %v", err)
	}
	sess := chat.NewSession(&config.Resolved{Model: "test-model"}, nil)
	_, err := StartInRoute(ctx, sess, store, mainDir, Route{Worktree: "wt1", Dir: elsewhere})
	if err == nil {
		t.Fatal("StartInRoute bound a directory outside the worktree root")
	}
	if !strings.Contains(err.Error(), "outside the worktree root") {
		t.Fatalf("outside-dir error = %q, want it named as outside", err)
	}
}

// TestStartInRoute_RefusesSessionWithContextAlreadyInstalled pins the
// pre-context precondition end to end: every validation can pass, but a
// session that already carries a context store must refuse the binding
// (bindings are only retainable on a bare session; the pool's pre-bind
// hook guarantees that ordering).
func TestStartInRoute_RefusesSessionWithContextAlreadyInstalled(t *testing.T) {
	store, _, mainDir, wtDir := routeFixture(t)
	ctx := context.Background()

	sess := chat.NewSession(&config.Resolved{Model: "test-model"}, nil)
	if err := sess.SetContextStore(store); err != nil {
		t.Fatalf("install context store: %v", err)
	}
	_, err := StartInRoute(ctx, sess, store, mainDir, Route{Worktree: "wt1", Dir: wtDir})
	if err == nil {
		t.Fatal("StartInRoute bound a session that already has context installed")
	}
	if !strings.Contains(err.Error(), "before context setup") {
		t.Fatalf("refusal reason = %q, want the pre-context precondition", err)
	}
}

// TestStartInRoute_RefusesInstanceStillCreating pins the active-state
// validation arm: LiveWorktreeInstance returns creating/deleting rows
// (recovery flows need them), but only an ACTIVE instance may accept a
// session binding.
func TestStartInRoute_RefusesInstanceStillCreating(t *testing.T) {
	store, principal, mainDir, _ := routeFixture(t)
	ctx := context.Background()

	half := filepath.Join(filepath.Dir(mainDir), ".mivia", "worktrees", "wt-half")
	if err := os.MkdirAll(half, 0o755); err != nil {
		t.Fatalf("create dir: %v", err)
	}
	canonical, err := CanonicalDir(half)
	if err != nil {
		t.Fatalf("canonicalize: %v", err)
	}
	creating := contextstate.WorktreeInstance{Worktree: "wt-half", ID: "wt_00000000000000aa"}
	if err := store.BeginWorktreeCreation(ctx, principal, creating, canonical); err != nil {
		t.Fatalf("begin creation: %v", err)
	}

	sess := chat.NewSession(&config.Resolved{Model: "test-model"}, nil)
	_, err = StartInRoute(ctx, sess, store, mainDir, Route{Worktree: "wt-half", Dir: canonical})
	if err == nil {
		t.Fatal("StartInRoute bound a worktree still in creating state")
	}
	if !strings.Contains(err.Error(), `validate worktree "wt-half" binding`) {
		t.Fatalf("refusal = %q, want the validation arm's wrapper", err)
	}
}
