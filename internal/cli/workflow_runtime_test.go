package cli

import (
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

func TestWorkflowRuntimeBindingRejectsRemovedPinnedPolicy(t *testing.T) {
	agent := agents.ResolvedAgent{Name: "worker"}
	pinned := workflowledger.AgentSnapshot{ProviderName: "openrouter", Model: "old/model"}
	opts := SessionDispatcherOpts{
		ProviderName: "openrouter", Model: "new/model",
		ModelCatalog: []config.ProviderModelGroup{{
			Provider: "openrouter", Selectable: true,
			Models: []config.ModelSpec{{Name: "new/model", ContextWindowTokens: 1000}},
		}},
	}
	_, err := workflowRuntimeBinding(agent, pinned, true, opts)
	if err == nil || !strings.Contains(err.Error(), "not selectable") {
		t.Fatalf("pinned binding error = %v, want current-policy rejection", err)
	}
}
