package cliagents

import (
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/mcp"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
)

// Every dispatcher rebuild - /agent, /model, a load_tools admission - replaces
// the dispatcher a delegated task agent runs through. EnsureMCPTools is what
// merges that agent's own MCP servers into the authority registry before it is
// scoped, and it fails OPEN when nil: the subagent simply runs without those
// tools, with no error and no notice. Wired only at attach, it vanished on the
// first rebuild and every later delegation silently lost its MCP capability.
func TestRebuiltSurfaceKeepsEnsureMCPTools(t *testing.T) {
	completer := &scriptedCompleter{turns: []provider.Response{{Content: "done"}}}
	fixture := newDeferredFixture(t, completer, []string{"read_file"}, []string{"read_file", "grep"})
	fixture.state.MCPManager = &mcp.Manager{}
	if err := fixture.state.Registry.Publish(agents.ResolvedAgent{
		Name: "narrow", SystemPrompt: "N", EffectiveTools: []string{"read_file"}, CoreTools: corePtr("read_file"),
	}); err != nil {
		t.Fatal(err)
	}
	previous := NewSessionDispatcherVar
	t.Cleanup(func() { NewSessionDispatcherVar = previous })
	var captured SessionDispatcherOpts
	NewSessionDispatcherVar = func(opts SessionDispatcherOpts) (*runtime.Dispatcher, error) {
		captured = opts
		return previous(opts)
	}
	for _, name := range []string{"narrow", config.RootAgentName} {
		if err := ApplySessionAgent(fixture.sess, fixture.res, fixture.state, name, false); err != nil {
			t.Fatalf("switch to %s: %v", name, err)
		}
		if captured.EnsureMCPTools == nil {
			t.Fatalf("rebuild for %q dropped EnsureMCPTools: a delegated agent's MCP servers are silently never merged", name)
		}
	}
}
