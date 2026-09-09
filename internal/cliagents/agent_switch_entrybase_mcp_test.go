package cliagents

import (
	"os"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/composition"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// installResolverBase mimics SessionPool.adoptRegistry for a worktree entry: a
// private resolver base, distinct from the shared launch-captured
// state.ToolBase, that every surface rebuild must derive from (entryBase).
func installResolverBase(fixture *deferredFixture, extra ...string) *tools.Registry {
	resolverBase := fixture.state.ToolBase.Clone()
	for _, name := range extra {
		resolverBase.Register(namedTool{name: name})
	}
	fixture.sess.ToolBaseResolver = func() *tools.Registry { return resolverBase }
	return resolverBase
}

// Restoring the root surface on a worktree entry must merge the configured
// global MCP servers into the registry the rebuilt surface is actually built
// from - the entry's resolver base - not into the shared launch base that a
// pool-adopted session no longer rebuilds from. Otherwise the restore
// reports success while the worktree session's root surface silently lacks
// the global server's tools.
func TestRestoreRootSurfaceMergesGlobalMCPIntoEntryBase(t *testing.T) {
	t.Setenv("MIVIA_CLI_MCP_HELPER", "1")
	completer := &scriptedCompleter{turns: []provider.Response{{Content: "done"}}}
	fixture := newDeferredFixture(t, completer, []string{"read_file"}, []string{"read_file", "grep", "glob"})
	fixture.res.MCP = config.MCPConfig{Enabled: true, Servers: []config.MCPServerConfig{{
		ID: "repo", Transport: "stdio", Command: os.Args[0],
		Args: []string{"-test.run=^TestMCPStdioHelper$"}, Env: []string{"MIVIA_CLI_MCP_HELPER"},
		TimeoutSeconds: 10, Global: true,
	}}}
	manager, mcpCleanup, err := composition.AttachMCPServers(fixture.state.ToolBase, fixture.res.MCP, nil, nil)
	if err != nil {
		t.Fatalf("attach MCP manager: %v", err)
	}
	defer mcpCleanup()
	fixture.state.MCPManager = manager
	resolverBase := installResolverBase(fixture)

	narrow := agents.ResolvedAgent{
		Name: "narrow", SystemPrompt: "N",
		EffectiveTools: []string{"read_file", "grep"},
		CoreTools:      corePtr("read_file"),
	}
	if err := fixture.state.Registry.Publish(narrow); err != nil {
		t.Fatal(err)
	}
	if err := ApplySessionAgent(fixture.sess, fixture.res, fixture.state, "narrow", false); err != nil {
		t.Fatalf("switch to narrow: %v", err)
	}
	if err := ApplySessionAgent(fixture.sess, fixture.res, fixture.state, config.RootAgentName, false); err != nil {
		t.Fatalf("restore root: %v", err)
	}
	const mcpTool = "mcp__repo__x6563686f"
	if _, ok := resolverBase.Get(mcpTool); !ok {
		t.Fatal("restoring root merged the global MCP server into the shared launch base, not this entry's resolver base")
	}
	if _, ok := fixture.sess.Tools.Get(mcpTool); !ok {
		t.Fatal("published root surface lacks the global MCP server's tools")
	}
}

// Restoring the root surface must wrap and surface a failure merging the
// configured global MCP servers, not swallow it: the manager here was built
// before "ghost" was added to the resolved config's global server set, so
// ensureRootMCPTools's merge cannot know about it and EnsureServers refuses
// the unknown ID. ApplySessionAgent must fail with that error wrapped as
// "MCP tools: ...", the restoreRootSurface branch a healthy merge never
// exercises.
func TestRestoreRootSurfaceWrapsMCPMergeFailure(t *testing.T) {
	completer := &scriptedCompleter{turns: []provider.Response{{Content: "done"}}}
	fixture := newDeferredFixture(t, completer, []string{"read_file"}, []string{"read_file", "grep", "glob"})
	manager, mcpCleanup, err := composition.AttachMCPServers(fixture.state.ToolBase, config.MCPConfig{Enabled: true}, nil, nil)
	if err != nil {
		t.Fatalf("attach empty MCP manager: %v", err)
	}
	defer mcpCleanup()
	fixture.state.MCPManager = manager

	narrow := agents.ResolvedAgent{
		Name: "narrow", SystemPrompt: "N",
		EffectiveTools: []string{"read_file", "grep"},
		CoreTools:      corePtr("read_file"),
	}
	if err := fixture.state.Registry.Publish(narrow); err != nil {
		t.Fatal(err)
	}
	if err := ApplySessionAgent(fixture.sess, fixture.res, fixture.state, "narrow", false); err != nil {
		t.Fatalf("switch to narrow: %v", err)
	}
	// Only now does the resolved config gain a global server the
	// already-built manager was never told about.
	fixture.res.MCP = config.MCPConfig{Enabled: true, Servers: []config.MCPServerConfig{{
		ID: "ghost", Transport: "stdio", Command: "true", Global: true,
	}}}

	err = ApplySessionAgent(fixture.sess, fixture.res, fixture.state, config.RootAgentName, false)
	if err == nil || !strings.Contains(err.Error(), "MCP tools:") {
		t.Fatalf("restore root = %v, want a wrapped \"MCP tools:\" error", err)
	}
}

// The /agent "disabled tools omitted" warning must be judged against the
// registry the switch actually builds from (entryBase), not the shared launch
// base: a tool the worktree entry offers is not disabled there.
func TestAgentSwitchDisabledWarningReadsEntryBase(t *testing.T) {
	completer := &scriptedCompleter{turns: []provider.Response{{Content: "done"}}}
	fixture := newDeferredFixture(t, completer, []string{"read_file"}, []string{"read_file", "grep"})
	// "glob" is a KNOWN compiled tool (IntersectWithRegistry only reports
	// known names as disabled) that the launch base lacks and the entry's
	// resolver base offers.
	installResolverBase(fixture, "glob")
	wt := agents.ResolvedAgent{
		Name: "wt", SystemPrompt: "W",
		EffectiveTools: []string{"read_file", "glob"},
		CoreTools:      corePtr("read_file"),
	}
	if err := fixture.state.Registry.Publish(wt); err != nil {
		t.Fatal(err)
	}
	stderr := captureStderr(t)
	if err := ApplySessionAgent(fixture.sess, fixture.res, fixture.state, "wt", false); err != nil {
		t.Fatalf("switch: %v", err)
	}
	if out := stderr(); strings.Contains(out, "glob") {
		t.Fatalf("warned about a tool the entry's live base offers:\n%s", out)
	}
}
