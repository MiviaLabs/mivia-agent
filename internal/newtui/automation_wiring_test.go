package newtui

import (
	"context"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/automation"
	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/cli"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/uiadapter"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
)

// TestNewAutomationSpawnerReachesPoolCreateFreshInDir covers
// newAutomationSpawner's own closure body (the one wireAutomationBackend
// installs on the Service, distinct from the standalone
// automationSpawnerFunc adapter TestAutomationSpawnerFuncCallsUnderlyingClosure
// below already proves forwards its arguments correctly): a SessionPool
// built with a nil config (uiadapter.NewSessionPool(nil, nil, nil, ...))
// makes CreateFreshInDir fail fast and cheaply (SessionPool.res == nil
// guard, session_pool_worktree.go) without spawning a real session,
// which is enough to prove the closure genuinely reaches
// pool.CreateFreshInDir - not merely that it type-checks.
func TestNewAutomationSpawnerReachesPoolCreateFreshInDir(t *testing.T) {
	pool := uiadapter.NewSessionPool(nil, nil, nil, false)
	spawn := newAutomationSpawner(pool)
	bindCalled := false
	bindFn := func(*chat.Session) (string, error) {
		bindCalled = true
		return "", nil
	}
	if _, err := spawn.CreateFreshInDir(bindFn, "/some/dir"); err == nil {
		t.Fatal("CreateFreshInDir through the wired spawner with a nil-config pool: got nil error, want the pool's own 'no config provided' failure")
	}
	if bindCalled {
		t.Fatal("bind was called despite the pool's own nil-config guard firing first")
	}
}

// TestAutomationSpawnerFuncCallsUnderlyingClosure covers
// automationSpawnerFunc.CreateFreshInDir's own method body (run.go): the
// standalone adapter type, tested in isolation from newAutomationSpawner's
// own pool-specific closure above, proving the plain-func-to-
// uiadapter.BindFunc conversion (D4's compile-hazard fix) forwards both
// arguments and both return values unchanged.
func TestAutomationSpawnerFuncCallsUnderlyingClosure(t *testing.T) {
	var gotBind func(*chat.Session) (string, error)
	var gotDir string
	wantConv := (ports.Conversation)(nil)
	wantErr := context.Canceled

	f := automationSpawnerFunc(func(bind func(*chat.Session) (string, error), dir string) (ports.Conversation, error) {
		gotBind = bind
		gotDir = dir
		return wantConv, wantErr
	})

	bindFn := func(*chat.Session) (string, error) { return "bound-root", nil }
	var target automation.SessionSpawner = f // pins that automationSpawnerFunc satisfies automation.SessionSpawner
	conv, err := target.CreateFreshInDir(bindFn, "/some/dir")

	if gotDir != "/some/dir" {
		t.Fatalf("underlying closure saw dir = %q, want /some/dir", gotDir)
	}
	if gotBind == nil {
		t.Fatal("underlying closure did not receive the bind func")
	}
	if conv != wantConv || err != wantErr {
		t.Fatalf("CreateFreshInDir returned (%v, %v), want (%v, %v)", conv, err, wantConv, wantErr)
	}
}

// TestWireAutomationBackendInstallsBackendOnSuccess covers
// wireAutomationBackend's success path directly (run.go), independent
// of buildApp's own end-to-end coverage of the same call: a real
// SettingsStore/SessionPool/Session/AgentSessionState quadruple is
// built the same way TestBuildApp's fixtures are, and the test asserts
// the backend actually landed on the store (SetAutomationBackend was
// reached), not merely that buildApp returned no error.
func TestWireAutomationBackendInstallsBackendOnSuccess(t *testing.T) {
	res := &config.Resolved{}
	sess := chat.NewSession(res, nil)
	agentState := &cli.AgentSessionState{WorkspaceRoot: t.TempDir()}
	store := uiadapter.NewSettingsStore(sess, res, agentState)
	runner := uiadapter.NewCommandRunner(sess, res, agentState)
	pool := runner.Pool()

	wireAutomationBackend(store, pool, sess, agentState)

	// Automations() delegating to a real backend (rather than the
	// in-memory fallback settings_automations.go carries when
	// automationBackend is nil) returns an empty, non-nil-erroring
	// slice for a workspace with no automations.toml yet - the
	// in-memory fallback would ALSO return empty for a fresh store, so
	// this alone would not distinguish backend-installed from
	// backend-absent. Apply a real edit instead: only a genuine
	// automation.Service backend persists it to automations.toml AND
	// makes it resolvable through LoadSpecs directly, independent of
	// the SettingsStore's own in-memory slice.
	h, err := store.Settings().Automations.Apply(context.Background(), ports.ScopeProject, ports.UpsertAutomation{
		Automation: ports.Automation{ID: "wired", Name: "wired", Action: ports.ActionRef{
			Steps: []ports.ActionStep{{Kind: ports.ActionStepPrompt, Prompt: "p"}},
		}},
	})
	if err != nil {
		t.Fatalf("Apply through the wired store: %v", err)
	}
	for range h.Events() {
	}
	specs, err := automation.LoadSpecs(ports.ScopeProject, agentState.WorkspaceRoot)
	if err != nil {
		t.Fatalf("LoadSpecs after Apply: %v", err)
	}
	if len(specs) != 1 || specs[0].ID != "wired" {
		t.Fatalf("automations.toml after Apply = %+v, want one automation persisted by the real backend Service", specs)
	}
}
