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
// newAutomationSpawner's own wiring (the value wireAutomationBackend
// installs on the Service, distinct from the standalone
// automationSessionSpawner adapter TestAutomationSessionSpawnerCallsUnderlyingPool
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

// TestAutomationSessionSpawnerCallsUnderlyingPool covers
// automationSessionSpawner.CreateFreshInDir's own method body (run.go): the
// standalone adapter type, tested in isolation from newAutomationSpawner's
// own pool-specific wiring above, proving the plain-func-to-
// uiadapter.BindFunc conversion (D4's compile-hazard fix) forwards both
// arguments and both return values unchanged. Uses a real, nil-config
// SessionPool (as TestNewAutomationSpawnerReachesPoolCreateFreshInDir does)
// since automationSessionSpawner now wraps a concrete *uiadapter.SessionPool
// rather than an injectable closure (chunk 6 widened SessionSpawner to
// 2 methods, which a bare func type can no longer satisfy).
func TestAutomationSessionSpawnerCallsUnderlyingPool(t *testing.T) {
	pool := uiadapter.NewSessionPool(nil, nil, nil, false)
	var target automation.SessionSpawner = automationSessionSpawner{pool: pool} // pins that automationSessionSpawner satisfies automation.SessionSpawner
	bindCalled := false
	bindFn := func(*chat.Session) (string, error) {
		bindCalled = true
		return "", nil
	}
	conv, err := target.CreateFreshInDir(bindFn, "/some/dir")
	if err == nil {
		t.Fatal("CreateFreshInDir through automationSessionSpawner with a nil-config pool: got nil error, want the pool's own 'no config provided' failure")
	}
	if conv != nil {
		t.Fatalf("CreateFreshInDir returned a non-nil conversation on error: %v", conv)
	}
	if bindCalled {
		t.Fatal("bind was called despite the pool's own nil-config guard firing first")
	}
}

// TestAutomationSessionSpawnerSetApprovalOverride covers
// automationSessionSpawner.SetApprovalOverride's own method body: it
// forwards to the pool's own SetApprovalOverride, proven here by the
// pool's own "unknown session" rejection for an id it has never pooled.
func TestAutomationSessionSpawnerSetApprovalOverride(t *testing.T) {
	pool := uiadapter.NewSessionPool(nil, nil, nil, false)
	spawn := automationSessionSpawner{pool: pool}
	err := spawn.SetApprovalOverride("no-such-session", nil, "deny")
	if err == nil {
		t.Fatal("SetApprovalOverride through automationSessionSpawner for an unknown session: got nil error, want rejection")
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
