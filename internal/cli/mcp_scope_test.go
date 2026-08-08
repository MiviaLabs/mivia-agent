package cli

import (
	"reflect"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
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
