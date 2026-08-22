package cli

// workflow_mcp.go — workflowMCPServers moved to internal/cliagents.
// workflowMCPServers is kept as a package-local alias so existing cli callers
// compile without changes.

import (
	"github.com/MiviaLabs/mivia-agent/internal/agents"
	cliagents "github.com/MiviaLabs/mivia-agent/internal/cliagents"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
)

// workflowMCPServers delegates to cliagents.WorkflowMCPServers.
// See cliagents/mcp_scope.go for the implementation.
func workflowMCPServers(wf *definition.CompiledWorkflow, registry *agents.AgentRegistry) []string {
	return cliagents.WorkflowMCPServers(wf, registry)
}
