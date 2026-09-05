package uiadapter

import (
	"context"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/cliagents"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
)

// Tightening the approval default is an OPERATOR setting, not a per-session
// one: it must reach every live pooled session, exactly as the full-disk
// toggle does. Applying it only to the focused session leaves a background or
// worktree session executing tool calls - real edits and run_command against a
// real checkout - under the looser posture the operator believes they just
// revoked, with the UI showing the tightened value.
func TestApprovalDefaultReachesEveryPooledSession(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	res := &config.Resolved{ProviderName: "fake", Model: "m1", Approvals: config.ApprovalsConfig{DefaultMode: "auto"}}
	store := approvalTestStore(t)
	launch := contextBoundSession(t, res, store, "launch")
	state := &cliagents.AgentSessionState{Registry: agents.NewRegistry(), WorkspaceRoot: t.TempDir()}
	pool := NewSessionPool(launch, res, state, false)
	t.Cleanup(pool.CloseAll)
	background, err := pool.CreateFresh()
	if err != nil {
		t.Fatal(err)
	}
	backgroundSess := background.(*Conversation).Session()

	// Wired the way production does it (CommandRunner.SetSettingsStore), not
	// by reaching into the field: a store built without a runner has a nil
	// pool and silently degrades to the focused session alone, which is the
	// original defect and what assigning the field directly hides.
	runner := NewCommandRunnerWithPool(launch, pool, res, state)
	store2 := NewSettingsStore(launch, res, state)
	runner.SetSettingsStore(store2)
	if store2.pool == nil {
		t.Fatal("SetSettingsStore did not hand the store its pool: the fan-out degrades to the active session")
	}
	handle, err := store2.Settings().General.Apply(context.Background(), ports.ScopeUser, ports.SetApprovalDefault{Mode: "deny"})
	if err != nil {
		t.Fatal(err)
	}
	var last ports.SaveEvent
	for event := range handle.Events() {
		last = event
	}
	if last.State != ports.SaveSaved {
		t.Fatalf("settings save = %v: %s", last.State, last.Message)
	}
	for name, sess := range map[string]*chat.Session{"active": launch, "background": backgroundSess} {
		if got := sess.ApprovalPolicyValue(); got != "deny" {
			t.Errorf("%s session approval policy = %q, want deny", name, got)
		}
	}
}
