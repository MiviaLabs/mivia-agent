package uiadapter_test

// Shared fixtures and helpers for the worktree-session tests, split out of
// worktree_sessions_test.go to keep it under the go-structure soft cap.

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/cliagents"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/contextmgr"
	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/events"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
	"github.com/MiviaLabs/mivia-agent/internal/uiadapter"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
	"github.com/MiviaLabs/mivia-agent/internal/worktreeroute"
)

// worktreeCatalogFixture registers one active managed worktree with a
// launch route in a fresh repository store, mirroring the lifecycle the
// CLI runs at worktree creation time.
func worktreeCatalogFixture(t *testing.T) (store *storage.SQLite, principal contextstate.Principal, mainDir, wtDir string) {
	t.Helper()
	mainDir = filepath.Join(t.TempDir(), "main")
	wtDir = filepath.Join(filepath.Dir(mainDir), ".mivia", "worktrees", "wt1")
	for _, dir := range []string{mainDir, wtDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("create fixture dirs: %v", err)
		}
	}
	canonical, err := worktreeroute.CanonicalDir(wtDir)
	if err != nil {
		t.Fatalf("canonicalize worktree dir: %v", err)
	}
	store, err = storage.OpenSQLite(filepath.Join(t.TempDir(), "ctx.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	principal, err = worktreeroute.Principal(mainDir)
	if err != nil {
		t.Fatalf("derive principal: %v", err)
	}
	instance := contextstate.WorktreeInstance{Worktree: "wt1", ID: "wt_0001020304050607"}
	if err := store.BeginWorktreeCreation(context.Background(), principal, instance, canonical); err != nil {
		t.Fatalf("begin creation: %v", err)
	}
	if err := store.RegisterWorktreeInstance(context.Background(), principal, instance, canonical); err != nil {
		t.Fatalf("register instance: %v", err)
	}
	writeTestWorktreeMarker(t, canonical, instance)
	return store, principal, mainDir, canonical
}

// catalogSession is a context-enabled session over the fixture store, the
// shape a CLI-launched session carries when /resume lists its catalog.
func catalogSession(t *testing.T, store *storage.SQLite, mainDir string) (*chat.Session, contextstate.Principal) {
	t.Helper()
	res := &config.Resolved{ProviderName: "fake", Model: "m1", SystemPrompt: "sys"}
	sess := chat.NewSession(res, &nullCompleter{})
	sess.SessionID = "session-main"
	principal, err := contextstate.NewPrincipal(worktreeroute.WorkspaceID(mainDir), sess.SessionID, "local-user")
	if err != nil {
		t.Fatalf("mint session principal: %v", err)
	}
	manager := &contextmgr.ContextManager{
		PreparationManager:  contextmgr.StructuralPreparationManager{},
		CheckpointPublisher: contextmgr.PreparationCommitter{Store: store},
		Enabled:             true,
	}
	if err := sess.SetContextManager(manager, principal); err != nil {
		t.Fatalf("enable session context: %v", err)
	}
	if err := sess.SetContextStore(store); err != nil {
		t.Fatalf("install context store: %v", err)
	}
	return sess, principal
}

// gitInitTempRepo turns mainDir into a minimal real git repository so
// worktreeroute.Root's discovery-based resolution behaves as it does for
// the shipped binary.
// stubWorkflowWiring neutralizes internal/cli's workflow-tool wiring for
// this test binary slice: BuildToolsForRoot builds real registries without
// calling a nil seam.
func stubWorkflowWiring(t *testing.T) {
	t.Helper()
	prev := cliagents.WireWorkflowToolOptionsVar
	cliagents.WireWorkflowToolOptionsVar = func(
		*tools.DefaultOptions, string, *config.Resolved, func() *events.Bus, bool, ledger.LedgerRepository,
	) {
	}
	t.Cleanup(func() { cliagents.WireWorkflowToolOptionsVar = prev })
}

// toolRegistryAt builds a minimal default registry confined to root -
// the launch-posture stand-in catalog sessions run with.
func toolRegistryAt(t *testing.T, root string) *tools.Registry {
	t.Helper()
	ws, err := workspace.Open(root)
	if err != nil {
		t.Fatalf("workspace %s: %v", root, err)
	}
	return tools.NewDefaultRegistry(tools.DefaultOptions{Workspace: ws})
}

func gitInitTempRepo(t *testing.T, mainDir string) {
	t.Helper()
	run := func(args ...string) {
		out, err := exec.Command("git", append([]string{"-C", mainDir}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	run("config", "user.email", "test@example.invalid")
	run("config", "user.name", "test")
	if err := os.WriteFile(filepath.Join(mainDir, "README"), []byte("fixture\n"), 0o644); err != nil {
		t.Fatalf("write fixture file: %v", err)
	}
	run("add", ".")
	run("commit", "-qm", "fixture")
}

// startInRouteErrOnly adapts StartInRoute's (Route, error) return for
// direct callers that only propagate the error.
func startInRouteErrOnly(ctx context.Context, sess *chat.Session, store *storage.SQLite, root string, rt worktreeroute.Route) error {
	_, err := worktreeroute.StartInRoute(ctx, sess, store, root, rt)
	return err
}

// startInRouteBind mirrors the production bind closure
// (worktree_sessions.go): it returns the validated worktree ROOT as the
// tool scope, so pool tests exercise the same contract the runner does.
func startInRouteBind(ctx context.Context, store *storage.SQLite, root string, rt worktreeroute.Route) uiadapter.BindFunc {
	return func(sess *chat.Session) (string, error) {
		bound, err := worktreeroute.StartInRoute(ctx, sess, store, root, rt)
		if err != nil {
			return "", err
		}
		return bound.Dir, nil
	}
}

// writeTestWorktreeMarker seeds the on-disk marker a managed worktree
// carries in production (CreateManagedWorktreeInStore writes it via
// cliworktree.WriteWorktreeMarker, which needs a real git checkout the
// plain-tempdir fixtures here do not have). Format pinned by
// cliworktree's worktreeMarker JSON schema, version 1.
func writeTestWorktreeMarker(t *testing.T, dir string, instance contextstate.WorktreeInstance) {
	t.Helper()
	markerDir := filepath.Join(dir, ".mivia")
	if err := os.MkdirAll(markerDir, 0o700); err != nil {
		t.Fatalf("marker dir: %v", err)
	}
	payload := []byte(`{"version":1,"worktree":"` + instance.Worktree + `","id":"` + instance.ID + `"}`)
	if err := os.WriteFile(filepath.Join(markerDir, "worktree-instance.json"), payload, 0o600); err != nil {
		t.Fatalf("write marker: %v", err)
	}
}
