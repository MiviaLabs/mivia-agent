package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/compiler"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
)

// TestWorkflowMCPServersOrderFollowsRegistryOrder pins the P1.4 ordering rule.
// The union of MCP servers follows the agent registry publication order.
// It does not follow the workflow step order.
func TestWorkflowMCPServersOrderFollowsRegistryOrder(t *testing.T) {
	registry := agents.NewRegistry()
	publishOrder := []agents.ResolvedAgent{
		{Name: "zebra", EffectiveMCPServers: []string{"gamma"}},
		{Name: "alpha", EffectiveMCPServers: []string{"beta"}},
		{Name: "mule", EffectiveMCPServers: []string{"alpha"}},
	}
	for _, agent := range publishOrder {
		if err := registry.Publish(agent); err != nil {
			t.Fatal(err)
		}
	}
	wf := &compiler.CompiledWorkflow{Steps: []definition.Step{
		{ID: "s1", Agent: "mule"},
		{ID: "s2", Agent: "zebra"},
		{ID: "s3", Agent: "alpha"},
	}}
	// The registry order is gamma (zebra), beta (alpha), alpha (mule).
	// The workflow step order would give alpha (mule), gamma (zebra), beta (alpha).
	want := []string{"gamma", "beta", "alpha"}
	if got := workflowMCPServers(wf, registry); !reflect.DeepEqual(got, want) {
		t.Fatalf("workflowMCPServers() = %v, want %v", got, want)
	}
}

// TestPrepareWorkflowBuildRegistersMCPToolsBeforeControllerStart pins P1.4:
// the workflow build registers the MCP tools of the referenced agents into
// the authority registry before the controller starts.
func TestPrepareWorkflowBuildRegistersMCPToolsBeforeControllerStart(t *testing.T) {
	t.Setenv("MIVIA_CLI_MCP_HELPER", "1")
	root, res, _, _, wf := newWorkflowBuildFixture(t)

	// The workspace agent selects the global MCP server "repo".
	// A workspace agent can select only a global server.
	agentOne := `name = "one"
description = "workflow test agent with MCP scope"
tools = ["read_file"]
mcp_servers = ["repo"]
max_turns = 1
`
	if err := os.WriteFile(filepath.Join(root, ".mivia", "agents", "one.toml"), []byte(agentOne), 0o600); err != nil {
		t.Fatal(err)
	}
	// The workspace MCP table makes the server "repo" known to agent resolution.
	// The command is this test binary. It never runs from this table; the build
	// starts the server from res.MCP.
	mcpTable := fmt.Sprintf(`[mcp]
enabled = true

[[mcp.servers]]
id = "repo"
transport = "stdio"
command = '%s'
args = ["-test.run=^TestMCPStdioHelper$"]
env = ["MIVIA_CLI_MCP_HELPER"]
global = true
timeout_seconds = 10
`, os.Args[0])
	if err := os.WriteFile(filepath.Join(root, ".mivia", "mivia.toml"), []byte(mcpTable), 0o600); err != nil {
		t.Fatal(err)
	}
	// prepareWorkflowBuild starts the MCP server from res.MCP.
	// The server definition comes from the shared test helper.
	res.MCP = serverConfig().MCP

	setup, err := prepareWorkflowBuild(root, res, wf, "wfrun-p1-4", nil, "", nil)
	if err != nil {
		t.Fatalf("prepareWorkflowBuild() error = %v", err)
	}
	defer setup.cleanup()
	if _, ok := setup.authority.Get("mcp__repo__x6563686f"); !ok {
		t.Fatalf("authority registry has no MCP tool %q; workflow MCP servers = %v",
			"mcp__repo__x6563686f", workflowMCPServers(wf, setup.loaded.Registry))
	}
}
