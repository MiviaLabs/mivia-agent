package cli

import (
	"testing"

	cliorchestrate "github.com/MiviaLabs/mivia-agent/internal/cliorchestrate"
)

func TestToolAndHandlerNameConsts(t *testing.T) {
	// Wire contracts: a typo in a const value must fail here before it reaches the model.
	cases := []struct {
		name string
		got  string
		want string
	}{
		{"cliorchestrate.HandlerMultiStep", cliorchestrate.HandlerMultiStep, "multi_step"},
		{"handlerDelegate", cliorchestrate.HandlerDelegate, "delegate"},
		{"cliorchestrate.HandlerOneshot", cliorchestrate.HandlerOneshot, "oneshot"},
		{"toolDispatchTasks", cliorchestrate.ToolDispatchTasks, "dispatch_tasks"},
		{"cliorchestrate.ToolSpawnAgent", cliorchestrate.ToolSpawnAgent, "spawn_agent"},
		{"cliorchestrate.ToolJoinRun", cliorchestrate.ToolJoinRun, "join_run"},
		{"cliorchestrate.ToolInspectAgents", cliorchestrate.ToolInspectAgents, "inspect_agents"},
		{"cliorchestrate.ToolCancelRun", cliorchestrate.ToolCancelRun, "cancel_run"},
	}
	for _, tc := range cases {
		if tc.got != tc.want {
			t.Errorf("%s = %q, want %q", tc.name, tc.got, tc.want)
		}
	}
}
