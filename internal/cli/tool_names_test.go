package cli

import (
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/cli/orchestrate"
)

func TestToolAndHandlerNameConsts(t *testing.T) {
	// Wire contracts: a typo in a const value must fail here before it reaches the model.
	cases := []struct {
		name string
		got  string
		want string
	}{
		{"orchestrate.HandlerMultiStep", orchestrate.HandlerMultiStep, "multi_step"},
		{"handlerDelegate", orchestrate.HandlerDelegate, "delegate"},
		{"orchestrate.HandlerOneshot", orchestrate.HandlerOneshot, "oneshot"},
		{"toolDispatchTasks", orchestrate.ToolDispatchTasks, "dispatch_tasks"},
		{"orchestrate.ToolSpawnAgent", orchestrate.ToolSpawnAgent, "spawn_agent"},
		{"orchestrate.ToolJoinRun", orchestrate.ToolJoinRun, "join_run"},
		{"orchestrate.ToolInspectAgents", orchestrate.ToolInspectAgents, "inspect_agents"},
		{"orchestrate.ToolCancelRun", orchestrate.ToolCancelRun, "cancel_run"},
	}
	for _, tc := range cases {
		if tc.got != tc.want {
			t.Errorf("%s = %q, want %q", tc.name, tc.got, tc.want)
		}
	}
}
