package uiadapter

import (
	"errors"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/cliagents"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
)

// recordEntryWiring swaps the two runtime-invoked closures (deferred-tool
// widener, /model binding factory) for recorders, returning the state each
// pooled session was wired to.
func recordEntryWiring(t *testing.T) (map[*chat.Session]*cliagents.AgentSessionState, map[*chat.Session]*cliagents.AgentSessionState) {
	t.Helper()
	prevW, prevB := newSurfaceWidenerVar, buildModelBindingVar
	t.Cleanup(func() { newSurfaceWidenerVar, buildModelBindingVar = prevW, prevB })
	widened := map[*chat.Session]*cliagents.AgentSessionState{}
	bound := map[*chat.Session]*cliagents.AgentSessionState{}
	newSurfaceWidenerVar = func(sess *chat.Session, res *config.Resolved, state *cliagents.AgentSessionState) chat.SurfaceWidener {
		widened[sess] = state
		return prevW(sess, res, state)
	}
	buildModelBindingVar = func(sess *chat.Session, res *config.Resolved, root, providerName, model string, state *cliagents.AgentSessionState) (chat.ModelBinding, error) {
		bound[sess] = state
		return chat.ModelBinding{}, errors.New("recorded")
	}
	return widened, bound
}

func assertOwnFork(t *testing.T, pool *SessionPool, conv ports.Conversation, widened, bound map[*chat.Session]*cliagents.AgentSessionState) {
	t.Helper()
	sess := conv.(*Conversation).Session()
	_, _, _ = sess.PrepareBinding("", "")
	fork := pool.AgentState(sess.SessionID)
	if fork == nil || fork == pool.agentState {
		t.Fatalf("entry %s has no private fork (%p vs base %p)", sess.SessionID, fork, pool.agentState)
	}
	if widened[sess] != fork {
		t.Errorf("entry %s widener wired to %p, want its fork %p", sess.SessionID, widened[sess], fork)
	}
	if bound[sess] != fork {
		t.Errorf("entry %s binding factory wired to %p, want its fork %p", sess.SessionID, bound[sess], fork)
	}
}

// Every pooled entry's runtime closures must be bound to that entry's own
// fork: bound to the shared base, a deferred-tool admission in one session
// rewrites the baseline every later session forks from, and a /model switch
// reads the base's stale Selected and drops the session's active agent.
func TestPoolEntriesWireWidenerAndBindingToOwnFork(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	res := &config.Resolved{ProviderName: "fake", Model: "m1"}
	store := approvalTestStore(t)
	launch := contextBoundSession(t, res, store, "launch")
	state := &cliagents.AgentSessionState{Registry: agents.NewRegistry(), WorkspaceRoot: t.TempDir()}
	pool := NewSessionPool(launch, res, state, false)
	t.Cleanup(pool.CloseAll)
	for _, id := range []string{"saved-a", "saved-b"} {
		if err := contextBoundSession(t, res, store, id).Save(id); err != nil {
			t.Fatal(err)
		}
	}
	widened, bound := recordEntryWiring(t)

	fresh, err := pool.CreateFresh()
	if err != nil {
		t.Fatal(err)
	}
	freshInDir, err := pool.CreateFreshInDir(nil, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	resumed, err := pool.GetOrCreate("saved-a")
	if err != nil {
		t.Fatal(err)
	}
	resumedInDir, err := pool.GetOrCreateInDir("saved-b", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, conv := range []ports.Conversation{fresh, freshInDir, resumed, resumedInDir} {
		assertOwnFork(t, pool, conv, widened, bound)
	}
}

// Switching to a session the pool never registered a fork for must not
// silently keep the previous session's agent state: that session's /agent,
// /model and admission then act on another conversation's policy.
func TestSetActiveSessionNeverKeepsAnotherEntrysState(t *testing.T) {
	r, _, res := statePoolFixture(t)
	selectStateAgent(t, r, "alpha")
	first := r.agentState
	orphan := chat.NewSession(res, noticeCompleter{})
	orphan.SessionID = "orphan"
	r.pool.mu.Lock()
	r.pool.sessions[orphan.SessionID] = orphan
	r.pool.convs[orphan.SessionID] = NewConversation(orphan)
	r.pool.mu.Unlock()

	r.SetActiveSession(orphan)
	if r.agentState == first {
		t.Fatal("switch kept the previous session's agent state")
	}
	if r.agentState == nil || r.agentState != r.pool.AgentState(orphan.SessionID) {
		t.Fatalf("switch bound %p, want the pool's state for %q (%p)", r.agentState, orphan.SessionID, r.pool.AgentState(orphan.SessionID))
	}
	if r.settingsStore.agentState != r.agentState {
		t.Fatal("settings store did not follow the switch")
	}
	// Isolation, not inheritance, is the contract: a fork starts from the
	// pool's current baseline (alpha here) but mutating it must not reach
	// the entry that was active before the switch.
	selectStateAgent(t, r, "beta")
	if got := first.DisplayName(); got != "alpha" {
		t.Fatalf("switching the orphan rewrote the previous entry's agent to %q", got)
	}
}

// A resume whose requested id spells an already-live entry differently
// (sanitization) must join that entry, never publish a second copy over it.
func TestGetOrCreateSecondKeyJoinsLiveEntry(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	res := &config.Resolved{ProviderName: "fake", Model: "m1"}
	store := approvalTestStore(t)
	launch := contextBoundSession(t, res, store, "launch")
	pool := NewSessionPool(launch, res, nil, false)
	t.Cleanup(pool.CloseAll)
	if err := contextBoundSession(t, res, store, "X").Save("X"); err != nil {
		t.Fatal(err)
	}
	live, err := pool.GetOrCreate("X")
	if err != nil {
		t.Fatal(err)
	}
	for name, resume := range map[string]func() (ports.Conversation, error){
		"GetOrCreate":      func() (ports.Conversation, error) { return pool.GetOrCreate(" X ") },
		"GetOrCreateInDir": func() (ports.Conversation, error) { return pool.GetOrCreateInDir(" X ", nil, "") },
	} {
		t.Run(name, func(t *testing.T) {
			got, err := resume()
			if err != nil {
				t.Fatal(err)
			}
			if got != live {
				t.Fatalf("resume published a second copy %p over the live entry %p", got, live)
			}
			if pool.convs["X"] != live {
				t.Fatalf("live entry for X was clobbered")
			}
		})
	}
}
