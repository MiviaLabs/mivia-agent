package legacytui

// agent_dialog_coverage_test.go drives the agent-dialog helpers in
// agent_dialog.go that the legacytui runtime tests do not exercise
// individually. Each helper is a pure function on agents.ResolvedAgent
// and the registry.

import (
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
)

func TestAgentListRowsEmpty(t *testing.T) {
	if rows := agentListRows(nil, ""); rows != nil {
		t.Errorf("agentListRows(nil) = %v, want nil", rows)
	}
}

func TestAgentListRowsWithCurrent(t *testing.T) {
	reg := agents.NewRegistry()
	_ = reg.Publish(agents.ResolvedAgent{Name: "alpha"})
	_ = reg.Publish(agents.ResolvedAgent{Name: "beta"})
	rows := agentListRows(reg, "beta")
	if len(rows) != 2 {
		t.Fatalf("agentListRows returned %d rows, want 2", len(rows))
	}
	found := false
	for _, r := range rows {
		if r.Name == "beta" && r.Current {
			found = true
		}
	}
	if !found {
		t.Errorf("expected beta to be marked current; rows=%+v", rows)
	}
}
