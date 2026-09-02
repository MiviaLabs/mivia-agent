package cliagents

import (
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/mcp"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// An operator's mandatory tool denylist must reach MCP tools.
//
// AuthorizedAgentTools grants authority to every registry tool belonging to a
// selected MCP server - "the server selection is the authority", so an agent
// file need not repeat volatile remote names. That loop runs AFTER
// applyToolPolicy has already removed denied names from EffectiveTools, and
// consults no denylist of its own, so it puts them back.
//
// At ScopeSpawned that is caught: scopeAdmits refuses a denied name whatever
// the allowlist says. At ScopeRoot it is not - a denied name the allowlist
// carries is KEPT, and the comment explaining why gives the reason this
// fails: "the agent's effective set already excludes these names at resolve
// time". For MCP tools it does not.
func TestAnOperatorDenylistReachesAnMCPTool(t *testing.T) {
	denied := encodeMCPName(t, "gh", "delete_repo")
	allowed := encodeMCPName(t, "gh", "list_issues")

	registry := tools.NewRegistry()
	registry.Register(namedTool{name: "read_file"})
	registry.Register(namedTool{name: denied})
	registry.Register(namedTool{name: allowed})

	// The operator's addition reaches the agent as EffectiveDenylist, which
	// resolve computes from disallowed_tools plus the operator's additions.
	agent := &agents.ResolvedAgent{
		EffectiveTools:      []string{"read_file"},
		EffectiveMCPServers: []string{"gh"},
		EffectiveDenylist:   []string{denied},
	}

	got := AuthorizedAgentTools(agent, registry)
	if contains(got, denied) {
		t.Errorf("the operator denied %q and it is still authorized: %v", denied, got)
	}
	if !contains(got, allowed) {
		t.Errorf("a non-denied MCP tool lost its authority; server selection is "+
			"what grants it and only the denied name may be removed: %v", got)
	}

	// The whole point: what the ROOT registry will execute.
	scoped, _ := ScopedRootRegistry(registry, agent, []string{denied})
	if _, ok := scoped.Get(denied); ok {
		t.Errorf("the denied MCP tool is in the executable root registry; the " +
			"operator's guardrail does not apply to any tool an agent's MCP " +
			"server happens to expose")
	}
	if _, ok := scoped.Get(allowed); !ok {
		t.Error("a non-denied MCP tool was dropped from the root registry")
	}
}

// The agent's own disallowed_tools must reach its MCP tools too, and this one
// has no second line of defence at all: the operator denylist is passed to
// ScopedRegistry as ExtraDenylist, but disallowed_tools is not - the ONLY
// thing that ever excluded it was EffectiveTools, which this loop bypasses.
func TestAnAgentsOwnDisallowedToolsReachItsMCPTools(t *testing.T) {
	denied := encodeMCPName(t, "gh", "delete_repo")

	registry := tools.NewRegistry()
	registry.Register(namedTool{name: denied})

	agent := &agents.ResolvedAgent{
		EffectiveMCPServers: []string{"gh"},
		DisallowedTools:     []string{denied},
		EffectiveDenylist:   []string{denied},
	}

	if got := AuthorizedAgentTools(agent, registry); contains(got, denied) {
		t.Errorf("the agent file denied %q and it is still authorized: %v", denied, got)
	}
	scoped, _ := ScopedRootRegistry(registry, agent, nil)
	if _, ok := scoped.Get(denied); ok {
		t.Error("a tool the agent's own disallowed_tools names is executable at " +
			"root, and nothing else in the chain would have caught it")
	}
}

// encodeMCPName builds the real wire name rather than a hand-written literal,
// so the test cannot drift from the encoding.
//
// It also demonstrates the point: EncodeToolName is a deterministic hex
// encoding of the remote name, not a hash, so an operator CAN write one of
// these in a denylist - which is what makes the bug above reachable.
func encodeMCPName(t *testing.T, server, remote string) string {
	t.Helper()
	name, err := mcp.EncodeToolName(server, remote)
	if err != nil {
		t.Fatalf("encode %s/%s: %v", server, remote, err)
	}
	return name
}

func contains(names []string, want string) bool {
	for _, n := range names {
		if n == want {
			return true
		}
	}
	return false
}

// The DELEGATED path is where this bites hardest, and where a fix that only
// covered the root registry would have missed it entirely.
//
// A subagent's own MCP servers are merged into the authority registry when the
// task starts (EnsureMCPTools -> MergeMCPTools), which is AFTER root scope has
// run. So the root agent need not select the server at all: nothing upstream
// has ever seen these tools. The spawned surface is built with no
// ExtraDenylist, so ScopeSpawned's own check sees only the COMPILED denylist -
// an operator's additions are not in it - and the allowlist is the only place
// their guardrail can still apply.
func TestADelegatedAgentCannotUseADeniedMCPToolItBringsItself(t *testing.T) {
	denied := encodeMCPName(t, "gh", "delete_repo")
	allowed := encodeMCPName(t, "gh", "list_issues")

	// The authority registry AFTER the subagent's own servers were merged in.
	authority := tools.NewRegistry()
	authority.Register(namedTool{name: denied})
	authority.Register(namedTool{name: allowed})

	worker := &agents.ResolvedAgent{
		EffectiveMCPServers: []string{"gh"},
		EffectiveDenylist:   []string{denied},
	}

	// Exactly how agent_task_handler builds the subagent's surface: spawned
	// mode, allowlist from AuthorizedAgentTools, and no ExtraDenylist.
	surface := tools.ScopedRegistry(authority, tools.ScopeOptions{
		Mode:      tools.ScopeSpawned,
		Allowlist: agents.AllowlistSet(AuthorizedAgentTools(worker, authority)),
	})

	if _, ok := surface.Get(denied); ok {
		t.Error("a delegated agent can invoke an MCP tool the operator denied, " +
			"by bringing the server itself: the merge happens after root scope, " +
			"and spawned mode was given no operator denylist to check against")
	}
	if _, ok := surface.Get(allowed); !ok {
		t.Error("the subagent lost a non-denied tool from its own server")
	}
}
