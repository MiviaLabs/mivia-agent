package cli

import (
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
)

func testAgentRegistry(t *testing.T, names ...string) *agents.AgentRegistry {
	t.Helper()
	reg := agents.NewRegistry()
	for _, name := range names {
		if err := reg.Publish(agents.ResolvedAgent{Name: name}); err != nil {
			t.Fatal(err)
		}
	}
	return reg
}
