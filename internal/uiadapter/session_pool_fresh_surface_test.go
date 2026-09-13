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
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
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

// TestCreateFreshBackgroundInDirPinsAdvertisedTools covers the exact entry
// the TUI's automation spawner calls (internal/newtui's
// automationSessionSpawner.CreateFreshInDir ->
// pool.CreateFreshBackgroundInDir). Such a session is built by the pool, not
// by the launch attach, so it used to carry NO pinned advertised snapshot -
// and a nil snapshot is what the agent loop's Surface rotation turned into
// an empty wire tools[] on every step after the first, which ended a
// TUI-triggered automation run after one tool roundtrip.
func TestCreateFreshBackgroundInDirPinsAdvertisedTools(t *testing.T) {
	root := t.TempDir()
	pool, launch, _ := newPoolWithAgentState(t, root)
	t.Cleanup(pool.CloseAll)
	pool.agentState.ToolBase = launch.Tools.Clone()

	conv, err := pool.CreateFreshBackgroundInDir(nil, "")
	if err != nil {
		t.Fatalf("CreateFreshBackgroundInDir: %v", err)
	}
	pooled, ok := conv.(*Conversation)
	if !ok || pooled == nil {
		t.Fatalf("CreateFreshBackgroundInDir returned %T, want *Conversation", conv)
	}
	specs := pooled.Session().AdvertisedToolSpecs()
	if len(specs) == 0 {
		t.Fatal("a background-spawned pooled session pinned no advertised tools[]: its turn would run with an empty tool array from step 2 on")
	}
	// Built from the PRE-scope base, so every workspace tool the pool
	// adopted is offered - not just whatever tier the published registry
	// happens to execute. The session-tool catalog (dispatch_tasks and
	// friends) is excluded: it joins the union through
	// cliagents.AdvertisedSessionToolSpecsVar, a seam internal/cli wires in
	// the real binary and this test binary does not.
	catalog := make(map[string]bool, len(sessionToolCatalogNames))
	for _, name := range sessionToolCatalogNames {
		catalog[name] = true
	}
	for _, tool := range pool.agentState.ToolBase.List() {
		if catalog[tool.Name()] {
			continue
		}
		if !advertisesTool(specs, tool.Name()) {
			t.Errorf("advertised union is missing %q, which the pre-scope base offers", tool.Name())
		}
	}
}

// advertisesTool reports whether an OpenAI-shaped tools[] array offers name.
func advertisesTool(specs []provider.ToolSpec, name string) bool {
	for _, spec := range specs {
		fn, _ := spec["function"].(map[string]any)
		if got, _ := fn["name"].(string); got == name {
			return true
		}
	}
	return false
}

// TestCreateFreshSurfaceFailureReachesTheToolScopeNotice pins the failure
// half of wireEntryLocked's AttachRebuiltSurface handling. A pool whose
// agent state carries no pre-scope tool base cannot rebuild a surface
// ("tool base is unavailable"), and the operator must hear about it through
// the pool's single-slot notice - the session is still usable, so nothing
// else would say the orchestration surface is missing.
func TestCreateFreshSurfaceFailureReachesTheToolScopeNotice(t *testing.T) {
	root := t.TempDir()
	pool, _, _ := newPoolWithAgentState(t, root)
	t.Cleanup(pool.CloseAll)
	// Deliberately NOT setting pool.agentState.ToolBase: that is what the
	// rebuild re-scopes from, and its absence is the failure under test.

	if _, err := pool.CreateFresh(); err != nil {
		t.Fatalf("CreateFresh: %v", err)
	}
	notice := pool.takeToolScopeNotice()
	if notice == "" {
		t.Fatal("a failed surface rebuild left the tool-scope notice empty: the operator would never learn the session has no orchestration surface")
	}
	if !strings.Contains(notice, "session tools:") {
		t.Fatalf("tool-scope notice = %q, want the surface-rebuild failure", notice)
	}
}

// namedNoopTool is noopTool with a caller-chosen name, so a test can build a
// registry larger than tools.MaxAdvertisedTools.
type namedNoopTool struct{ name string }

func (t namedNoopTool) Name() string             { return t.name }
func (namedNoopTool) Description() string        { return "does nothing" }
func (namedNoopTool) Parameters() map[string]any { return map[string]any{"type": "object"} }
func (namedNoopTool) Execute(context.Context, json.RawMessage) (string, error) {
	return "ok", nil
}

// TestCreateFreshAdvertisingTruncationReachesTheToolScopeNotice pins that a
// union too large for the wire tells the operator. The pin runs inside the
// live TUI, so it must not write to os.Stderr the way the pre-TUI launch
// attach does; the count comes back to the pool and lands in the same
// single-slot notice as every other message on this path.
func TestCreateFreshAdvertisingTruncationReachesTheToolScopeNotice(t *testing.T) {
	root := t.TempDir()
	pool, launch, _ := newPoolWithAgentState(t, root)
	t.Cleanup(pool.CloseAll)
	base := launch.Tools.Clone()
	for i := 0; i < tools.MaxAdvertisedTools+16; i++ {
		base.Register(namedNoopTool{name: fmt.Sprintf("filler_tool_%03d", i)})
	}
	pool.agentState.ToolBase = base

	if _, err := pool.CreateFresh(); err != nil {
		t.Fatalf("CreateFresh: %v", err)
	}
	notice := pool.takeToolScopeNotice()
	if !strings.Contains(notice, "advertising cap") {
		t.Fatalf("tool-scope notice = %q, want the advertising-cap truncation count", notice)
	}
}
