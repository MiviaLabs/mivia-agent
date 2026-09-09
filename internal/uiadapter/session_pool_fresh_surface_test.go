package uiadapter

// Regression coverage for the degraded plain /new session: CreateFresh built
// its entry inline (inherit, approval, widener, binding factory) and never
// published an agent surface, while every other pooled entry path
// (CreateFreshInDir, GetOrCreateInDir) reaches wireEntryLocked and its
// AttachRebuiltSurface call. A /new session therefore carried NO dispatcher
// on its binding, so the agent loop fell back to
// runtime.NewToolDispatcher(registry, runtime.Policy{}) - no approval
// snapshot, no lifecycle hooks, no denylist, no result caps - and the
// dispatcher-owned session tools it inherited from the LAUNCH registry
// executed against the launch session instead of this one.

import (
	"testing"
)

func TestCreateFreshPublishesSessionSurface(t *testing.T) {
	root := t.TempDir()
	pool, launch, _ := newPoolWithAgentState(t, root)
	t.Cleanup(pool.CloseAll)
	// Production parity: the launch attach captures the pre-scope base on the
	// shared agent state (internal/clichat/chat_repl.go), which is what every
	// later surface rebuild re-scopes from.
	pool.agentState.ToolBase = launch.Tools.Clone()

	conv, err := pool.CreateFresh()
	if err != nil {
		t.Fatalf("CreateFresh: %v", err)
	}
	pooled, ok := conv.(*Conversation)
	if !ok || pooled == nil {
		t.Fatalf("CreateFresh returned %T, want *Conversation", conv)
	}
	fresh := pooled.Session()

	if fresh.CurrentBinding().Dispatcher == nil {
		t.Error("fresh /new session has no dispatcher on its binding: its tool calls would run ungoverned")
	}
	if fresh.Tools == nil {
		t.Fatal("fresh /new session has no tool registry")
	}
	for _, name := range sessionToolCatalogNames {
		if _, found := fresh.Tools.Get(name); !found {
			t.Errorf("fresh /new session registry is missing session tool %q", name)
		}
	}
	if fresh.Tools == launch.Tools {
		t.Error("fresh /new session shares the launch registry: its session tools would execute against the launch session")
	}
}
