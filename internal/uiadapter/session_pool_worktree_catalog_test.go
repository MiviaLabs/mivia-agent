package uiadapter

// Regression coverage for the lost session-tool catalog on worktree adoption:
// adoptWorktreeToolsLocked swaps sess.Tools to a registry built by
// BuildToolsForRoot (composition.BuildRegistry only), which never registers
// the dispatcher-owned session tool catalog - dispatch_tasks, inspect_agents,
// join_run, cancel_run, post_message, run_messages, send_to_task, ledger_read,
// list_run_events, read_output and load_tools are registered by the session
// dispatcher at launch onto the launch registry only. A worktree session whose
// registry was swapped therefore ran with no orchestration surface at all.

import (
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/cliagents"
	clichat "github.com/MiviaLabs/mivia-agent/internal/clichat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/hooks"
	"github.com/MiviaLabs/mivia-agent/internal/skills"
)

// sessionToolCatalogNames is the dispatcher-owned catalog every root binding
// must be able to execute (load_tools ships only when the plan defers; the
// rest are unconditional). Mirrors internal/clichat/sessionToolCatalog.
var sessionToolCatalogNames = []string{
	"dispatch_tasks",
	"inspect_agents",
	"join_run",
	"cancel_run",
	"post_message",
	"run_messages",
	"send_to_task",
	"ledger_read",
	"list_run_events",
	"read_output",
}

// stubHookSession satisfies clichat.HookSessionState with no hooks, mirroring
// internal/clichat's own TestMain stub.
type stubHookSession struct{}

func (stubHookSession) RunnableGroups() []hooks.Group { return nil }
func (stubHookSession) NoteRunWarnings([]string)      {}

func newPoolWithAgentState(t *testing.T, launchRoot string) (*SessionPool, *chat.Session, *config.Resolved) {
	t.Helper()
	stubWorkflowWiring(t)
	// The dispatcher seam is wired at process start by internal/cli; this
	// package's tests run without that wiring, so mirror it here (the same
	// shape the production TUI binary installs).
	prevDispatcher := cliagents.NewSessionDispatcherVar
	prevSpool := cliagents.RemainderSpoolFromRegistryVar
	prevHooksConfigured := clichat.HookSessionConfiguredFunc
	prevHookState := clichat.CurrentHookSessionFunc
	cliagents.NewSessionDispatcherVar = clichat.NewSessionDispatcher
	cliagents.RemainderSpoolFromRegistryVar = clichat.RemainderSpoolFromRegistry
	clichat.HookSessionConfiguredFunc = func() bool { return false }
	clichat.CurrentHookSessionFunc = func() clichat.HookSessionState { return stubHookSession{} }
	t.Cleanup(func() {
		cliagents.NewSessionDispatcherVar = prevDispatcher
		cliagents.RemainderSpoolFromRegistryVar = prevSpool
		clichat.HookSessionConfiguredFunc = prevHooksConfigured
		clichat.CurrentHookSessionFunc = prevHookState
	})
	res := &config.Resolved{ProviderName: "fake", Model: "m1"}
	sess := chat.NewSession(res, fallbackCompleter{providerName: "fake"})
	sess.SessionID = "session-main"
	state := &cliagents.AgentSessionState{
		WorkspaceRoot: launchRoot,
		SkillRegFull:  skills.NewRegistry(),
	}
	pool := NewSessionPool(sess, res, state, true)
	sess.Tools = baseRegistryAt(t, launchRoot, false)
	return pool, sess, res
}

func TestWorktreeAdoptionKeepsSessionToolCatalog(t *testing.T) {
	rootA := t.TempDir()
	pool, _, _ := newPoolWithAgentState(t, rootA)
	t.Cleanup(pool.CloseAll)

	wtB := otherRoot(t, t.TempDir())
	if _, err := pool.CreateFreshInDir(nil, wtB); err != nil {
		t.Fatalf("CreateFreshInDir: %v", err)
	}
	adopted := pool.lastCreated.Session()
	t.Logf("tool scope notice: %q", pool.lastToolScopeNotice)

	for _, name := range sessionToolCatalogNames {
		if _, found := adopted.Tools.Get(name); !found {
			t.Errorf("adopted worktree registry is missing session tool %q", name)
		}
	}
	if adopted.CurrentBinding().Dispatcher == nil {
		t.Error("adopted worktree session has no dispatcher on its binding")
	}
}
