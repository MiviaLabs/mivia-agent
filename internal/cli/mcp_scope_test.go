package cli

import (
	"reflect"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
)

func TestAuthorizedAgentToolsIncludesOnlySelectedMCPServers(t *testing.T) {
	registry := tools.NewRegistry()
	registry.Register(namedTool{name: "read_file"})
	registry.Register(namedTool{name: "mcp__alpha__x6c697374"})
	registry.Register(namedTool{name: "mcp__beta__x736563726574"})
	agent := &agents.ResolvedAgent{
		EffectiveTools:      []string{"read_file"},
		EffectiveMCPServers: []string{"alpha"},
	}

	got := authorizedAgentTools(agent, registry)
	want := []string{"read_file", "mcp__alpha__x6c697374"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("authorizedAgentTools() = %v, want %v", got, want)
	}
}

func TestWorkflowMCPServersUsesReferencedAgentsOnly(t *testing.T) {
	registry := agents.NewRegistry()
	if err := registry.Publish(agents.ResolvedAgent{Name: "worker", EffectiveMCPServers: []string{"alpha"}}); err != nil {
		t.Fatal(err)
	}
	if err := registry.Publish(agents.ResolvedAgent{Name: "unused", EffectiveMCPServers: []string{"beta"}}); err != nil {
		t.Fatal(err)
	}
	wf := &definition.CompiledWorkflow{Steps: []definition.Step{{ID: "work", Agent: "worker"}}}
	if got := workflowMCPServers(wf, registry); !reflect.DeepEqual(got, []string{"alpha"}) {
		t.Fatalf("workflowMCPServers() = %v, want [alpha]", got)
	}
}

func TestWorkflowMCPServersIncludesPanelMembers(t *testing.T) {
	registry := agents.NewRegistry()
	if err := registry.Publish(agents.ResolvedAgent{Name: "panelist", EffectiveMCPServers: []string{"alpha"}}); err != nil {
		t.Fatal(err)
	}
	wf := &definition.CompiledWorkflow{Steps: []definition.Step{{
		ID: "review", Kind: "agent_panel", Panel: &definition.AgentPanel{Members: []definition.PanelMember{{ID: "one", Agent: "panelist"}}},
	}}}
	if got := workflowMCPServers(wf, registry); !reflect.DeepEqual(got, []string{"alpha"}) {
		t.Fatalf("workflowMCPServers() = %v, want [alpha]", got)
	}
}
