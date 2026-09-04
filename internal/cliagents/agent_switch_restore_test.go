package cliagents

import (
	"os"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/composition"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
)

// TestRestoreRootSurfaceRebuildsBaselineSurface covers the candidate!=nil
// branch of restoreRootSurface: after switching to a narrow agent and back,
// the baseline prompt, step budget, and the FULL base toolset are restored.
// Kill mutation: gut restoreRootSurface past the baseline check down to
// Selected=nil - prompt, steps, or the republished surface then diverge and
// one of the assertions fails.
func TestRestoreRootSurfaceRebuildsBaselineSurface(t *testing.T) {
	completer := &scriptedCompleter{turns: []provider.Response{{Content: "done"}}}
	fixture := newDeferredFixture(t, completer, []string{"read_file"}, []string{"read_file", "grep", "glob"})
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
	if fixture.state.Selected == nil || fixture.state.Selected.Name != "narrow" {
		t.Fatalf("precondition: selected = %+v, want narrow", fixture.state.Selected)
	}
	baselinePrompt, baselineSteps := fixture.state.BaselinePrompt, fixture.state.BaselineMaxSteps

	if err := ApplySessionAgent(fixture.sess, fixture.res, fixture.state, config.RootAgentName, false); err != nil {
		t.Fatalf("restore root: %v", err)
	}
	if fixture.state.Selected != nil {
		t.Fatalf("root restore must clear the selection, got %q", fixture.state.Selected.Name)
	}
	gotPrompt, gotSteps := fixture.sess.AgentSettings()
	if gotPrompt != baselinePrompt || gotSteps != baselineSteps {
		t.Fatalf("baseline not restored: prompt %q/%q steps %d/%d",
			gotPrompt, baselinePrompt, gotSteps, baselineSteps)
	}
	if _, ok := fixture.sess.Tools.Get("glob"); !ok {
		t.Fatal("restored root surface lost the base toolset (glob missing)")
	}
}

// TestRestoreRootSurfaceFreshStateNoOp covers the !BaselineCaptured
// early-return: restoring root before ever switching away is a no-op that
// clears nothing but the (already nil) selection.
func TestRestoreRootSurfaceFreshStateNoOp(t *testing.T) {
	completer := &scriptedCompleter{turns: []provider.Response{{Content: "done"}}}
	fixture := newDeferredFixture(t, completer, []string{"read_file"}, []string{"read_file"})
	fixture.state.BaselineCaptured = false

	if err := ApplySessionAgent(fixture.sess, fixture.res, fixture.state, config.RootAgentName, false); err != nil {
		t.Fatalf("fresh-state restore must be a no-op success, got %v", err)
	}
	if fixture.state.Selected != nil {
		t.Fatalf("selection must stay nil, got %q", fixture.state.Selected.Name)
	}
}

// TestRestoreRootSurfaceWrapsSurfaceBuildFailure covers the error-wrap arm:
// a surface build failure (the dispatcher stub errors) surfaces as
// "root surface: ..." and leaves the selection untouched. Kill mutation:
// drop the fmt.Errorf wrap or mutate state before the build.
func TestRestoreRootSurfaceWrapsSurfaceBuildFailure(t *testing.T) {
	completer := &scriptedCompleter{turns: []provider.Response{{Content: "done"}}}
	fixture := newDeferredFixture(t, completer, []string{"read_file"}, []string{"read_file"})

	saved := NewSessionDispatcherVar
	NewSessionDispatcherVar = func(SessionDispatcherOpts) (*runtime.Dispatcher, error) {
		return nil, errNilDispatcherDep
	}
	defer func() { NewSessionDispatcherVar = saved }()

	if err := ApplySessionAgent(fixture.sess, fixture.res, fixture.state, config.RootAgentName, false); err == nil {
		t.Fatal("restore must fail when the surface build fails")
	} else if !strings.Contains(err.Error(), "root surface") {
		t.Fatalf("error = %v, want a root-surface wrap", err)
	}
	if fixture.state.Selected == nil || fixture.state.Selected.Name != "reader" {
		t.Fatalf("failed restore must leave the selection untouched, got %+v", fixture.state.Selected)
	}
}

// TestRestoreRootSurfaceAttachesGlobalMCPServers pins the second half of the
// reported bug's fix: a session that started with a NAMED agent selected
// (one whose own mcp_servers scope did not include a configured global
// server) must still gain that global server's tools when the operator
// switches back to root with `/agent general-orchestrator`. Before the fix,
// restoreRootSurface never attempted an MCP merge at all, so a global
// server absent from the narrow agent's own scope stayed permanently
// unreachable for the rest of the session.
func TestRestoreRootSurfaceAttachesGlobalMCPServers(t *testing.T) {
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
	// The narrow agent, exactly as selected on this session, names no MCP
	// servers of its own - so the global "repo" server was never merged into
	// ToolBase for it.
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
	if _, ok := fixture.state.ToolBase.Get("mcp__repo__x6563686f"); ok {
		t.Fatal("precondition: the narrow agent's own scope must not have the global server's tools yet")
	}

	if err := ApplySessionAgent(fixture.sess, fixture.res, fixture.state, config.RootAgentName, false); err != nil {
		t.Fatalf("restore root: %v", err)
	}
	if _, ok := fixture.state.ToolBase.Get("mcp__repo__x6563686f"); !ok {
		t.Fatal("restoring the root surface did not attach the configured global MCP server's tools")
	}
}
