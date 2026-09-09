package cliagents_test

// End-to-end invariant for plans/remaining-gaps-closure.md D2/D3: with a
// session-carried resolver base and a nil shared state.ToolBase, every
// widened site (gate, warn, MCP target) must read the SAME entryBase —
// the published surface carries the resolver base's sentinel and the
// shared launch base stays untouched.

import (
	"context"
	"encoding/json"
	"io"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/cliagents"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

type entrybaseNullCompleter struct{}

func (entrybaseNullCompleter) Name() string { return "null" }
func (entrybaseNullCompleter) Chat(context.Context, provider.Request) (string, error) {
	return "", nil
}
func (entrybaseNullCompleter) ChatStream(context.Context, provider.Request, io.Writer) (string, error) {
	return "", nil
}
func (entrybaseNullCompleter) ChatTurn(context.Context, provider.Request) (*provider.Response, error) {
	return &provider.Response{}, nil
}

type sentinelTool struct{ name string }

func (s sentinelTool) Name() string               { return s.name }
func (s sentinelTool) Description() string        { return "sentinel " + s.name }
func (s sentinelTool) Parameters() map[string]any { return map[string]any{"type": "object"} }
func (s sentinelTool) Execute(_ context.Context, _ json.RawMessage) (string, error) {
	return "ok", nil
}

func TestApplySessionAgentUsesResolverBaseForRebuild(t *testing.T) {
	res := &config.Resolved{ProviderName: "fake", Model: "m1", SystemPrompt: "sys"}
	sess := chat.NewSession(res, entrybaseNullCompleter{})
	sess.SessionID = "wt-entry"

	launchBase := tools.NewRegistry()
	launchBase.Register(sentinelTool{name: "launch-only"})
	sess.Tools = launchBase

	// BaselineCaptured makes restoreRootSurface actually rebuild the
	// surface: without it the root switch returns before entryBase runs
	// and this test proves nothing (an earlier revision did exactly that).
	state := &cliagents.AgentSessionState{}
	state.ToolBase = launchBase
	state.BaselineCaptured = true
	state.BaselinePrompt = "sys"

	// Pool adoption installs a private resolver base carrying an extra
	// sentinel the shared launch base never had.
	resolverBase := tools.NewRegistry()
	for _, tool := range launchBase.List() {
		resolverBase.Register(tool)
	}
	resolverBase.Register(sentinelTool{name: "wt-only"})
	sess.ToolBaseResolver = func() *tools.Registry { return resolverBase }

	if err := cliagents.ApplySessionAgent(sess, res, state, config.RootAgentName, false); err != nil {
		t.Fatalf("ApplySessionAgent: %v", err)
	}

	// The rebuilt surface must derive from entryBase = the resolver base:
	// the published registry carries the resolver-only sentinel. A rebuild
	// from the shared state.ToolBase would not - this line fails if the
	// entryBase preference regresses to the launch-captured base.
	if sess.Tools == nil {
		t.Fatal("session lost its registry after switch")
	}
	if _, ok := sess.Tools.Get("wt-only"); !ok {
		t.Fatalf("published surface lacks the resolver sentinel; rebuilt from the launch base instead: %v",
			toolNames(sess.Tools))
	}
	if _, ok := sess.Tools.Get("launch-only"); !ok {
		t.Fatal("published surface lost the shared base tool")
	}
	if got := len(launchBase.List()); got != 1 {
		t.Errorf("shared launch base mutated during switch: %d tools, want 1", got)
	}
}

func toolNames(reg *tools.Registry) []string {
	var names []string
	for _, tool := range reg.List() {
		names = append(names, tool.Name())
	}
	return names
}
