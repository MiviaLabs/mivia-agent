package cliagents

import (
	"slices"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/config"
)

// TestPlanToolTiersNeverDefersAnAuthorizedMCPTool is the regression test for
// the root cause behind a configured, successfully-connected MCP server
// (e.g. codegraph) never actually being called: an MCP tool's name is a
// runtime hash of the remote tool the server reports
// (internal/mcp.EncodeToolName), so a hand-authored [tools] core list can
// never name it - naming ANY core list used to silently and permanently
// defer every MCP tool, with no way back short of predicting the hash.
func TestPlanToolTiersNeverDefersAnAuthorizedMCPTool(t *testing.T) {
	base := tierRegistry("read_file", "grep", "mcp__repo__x6563686f")
	res := &config.Resolved{Tools: config.ToolsConfig{Core: corePtr("read_file")}}
	selected := &agents.ResolvedAgent{
		Name: "a", EffectiveTools: []string{"read_file", "grep"}, EffectiveMCPServers: []string{"repo"},
	}
	plan := planToolTiers(base, selected, res)
	if !slices.Contains(plan.Tiers.Core, "mcp__repo__x6563686f") {
		t.Fatalf("core = %v, want the MCP tool kept core despite an unrelated configured core list", plan.Tiers.Core)
	}
	if !slices.Equal(plan.Tiers.Deferred, []string{"grep"}) {
		t.Fatalf("deferred = %v, want only [grep] - the MCP tool must not appear here", plan.Tiers.Deferred)
	}
	for _, candidate := range plan.Candidates {
		if candidate.Name == "mcp__repo__x6563686f" {
			t.Fatalf("MCP tool appeared in the deferred-tool index: %+v", plan.Candidates)
		}
	}
}

// TestPlanToolTiersKeepsGlobalMCPToolCoreForTheRootIdentity is the
// no-agents-defined regression: a workspace with no agent named
// config.DefaultAgentName (and no --agent) runs the ROOT identity, whose MCP
// server selection is the config's global server set - the same set
// SetupSessionMCPTools attached. Before the fix, withMCPServerToolsAlwaysCore
// bailed on selected == nil, so the root identity's global MCP tool was
// advertised yet silently deferred behind load_tools, whose publication can
// then defer again (background orchestration, sibling turns). The tier rule
// must consult the same server selection that granted authority.
func TestPlanToolTiersKeepsGlobalMCPToolCoreForTheRootIdentity(t *testing.T) {
	base := tierRegistry("read_file", "grep", "mcp__repo__x6563686f")
	res := &config.Resolved{
		Tools: config.ToolsConfig{Core: corePtr("read_file")},
		MCP: config.MCPConfig{
			Enabled: true,
			Servers: []config.MCPServerConfig{{ID: "repo", Global: true}},
		},
	}
	plan := planToolTiers(base, nil, res)
	if !slices.Contains(plan.Tiers.Core, "mcp__repo__x6563686f") {
		t.Fatalf("core = %v, want the root identity's global MCP tool kept core", plan.Tiers.Core)
	}
	if !slices.Equal(plan.Tiers.Deferred, []string{"grep"}) {
		t.Fatalf("deferred = %v, want only [grep] - the MCP tool must not appear here", plan.Tiers.Deferred)
	}
}

// TestPlanToolTiersRootIdentityDefersNonGlobalMCPTool pins the authority
// boundary of the root-identity exemption: GlobalServerIDs is what the root
// identity attaches, so a non-global server's tool must NOT be pulled into
// the core tier by the same rule.
func TestPlanToolTiersRootIdentityDefersNonGlobalMCPTool(t *testing.T) {
	base := tierRegistry("read_file", "mcp__repo__x6563686f")
	res := &config.Resolved{
		Tools: config.ToolsConfig{Core: corePtr("read_file")},
		MCP: config.MCPConfig{
			Enabled: true,
			Servers: []config.MCPServerConfig{{ID: "repo"}}, // not global: root never attaches it
		},
	}
	plan := planToolTiers(base, nil, res)
	if slices.Contains(plan.Tiers.Core, "mcp__repo__x6563686f") {
		t.Fatalf("core = %v, want a non-global server's tool to stay out of the root identity's core tier", plan.Tiers.Core)
	}
}

// TestWithMCPServerToolsAlwaysCoreDoesNotMutateTheConfiguredCoreSlice guards
// the append-in-place hazard: core aliases *config.Resolved.Tools.Core, so
// growing it in place (when it happens to have spare capacity) would corrupt
// state another binding, or a later call with the same *[]string, still
// reads.
func TestWithMCPServerToolsAlwaysCoreDoesNotMutateTheConfiguredCoreSlice(t *testing.T) {
	backing := make([]string, 1, 4) // spare capacity: append would write through without a copy
	backing[0] = "read_file"
	authorized := []string{"read_file", "mcp__repo__x6563686f"}

	out := withMCPServerToolsAlwaysCore(backing, authorized, []string{"repo"})

	if !slices.Equal(out, []string{"read_file", "mcp__repo__x6563686f"}) {
		t.Fatalf("out = %v, want the MCP tool appended", out)
	}
	if !slices.Equal(backing, []string{"read_file"}) {
		t.Fatalf("backing = %v, the original core slice must not be mutated", backing)
	}
}

// TestIdentityMCPServerScopeWithNoAgentAndNoConfig covers the root-identity,
// nil-config fallback: no test elsewhere calls identityMCPServerScope with
// both selected and res nil.
func TestIdentityMCPServerScopeWithNoAgentAndNoConfig(t *testing.T) {
	if got := identityMCPServerScope(nil, nil); got != nil {
		t.Fatalf("identityMCPServerScope(nil, nil) = %v, want nil", got)
	}
}
