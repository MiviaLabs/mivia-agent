package cli

import (
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
)

func TestRoutedIdentityUsesOpaqueInstanceID(t *testing.T) {
	definition := agents.ResolvedAgent{Name: "worker", Provenance: agents.Provenance{Source: config.AgentSourceUser}}
	a := routedIdentity(definition, "runtime-a", 1)
	b := routedIdentity(definition, "runtime-b", 1)
	if a == nil || b == nil || a.InstanceID == b.InstanceID {
		t.Fatalf("instances are not distinct: %#v %#v", a, b)
	}
	if a.DefinitionName != "worker" || a.DefinitionSource != "user" || a.ModelGeneration != 1 {
		t.Fatalf("identity = %#v", a)
	}
}

func TestSessionAgentStatusIncludesDefinitionSourceAndGeneration(t *testing.T) {
	res := &config.Resolved{ProviderName: "test", Model: "model", BaseURL: "https://example.test", APIKeyEnv: "TEST_KEY"}
	sess := chat.NewSession(res, nil)
	state := &agentSessionState{Selected: &agents.ResolvedAgent{Name: "worker", Provenance: agents.Provenance{Source: config.AgentSourceUser}}}
	text := formatSessionAgentStatus(state, sess)
	for _, want := range []string{"agent=worker", "source=user", "generation=1"} {
		if !strings.Contains(text, want) {
			t.Fatalf("status %q missing %q", text, want)
		}
	}
}
