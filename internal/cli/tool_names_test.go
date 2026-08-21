package cli

import "testing"

func TestToolAndHandlerNameConsts(t *testing.T) {
	// Wire contracts: a typo in a const value must fail here before it reaches the model.
	cases := []struct {
		name string
		got  string
		want string
	}{
		{"HandlerMultiStep", HandlerMultiStep, "multi_step"},
		{"handlerDelegate", HandlerDelegate, "delegate"},
		{"HandlerOneshot", HandlerOneshot, "oneshot"},
		{"toolDispatchTasks", ToolDispatchTasks, "dispatch_tasks"},
		{"toolSpawnAgent", toolSpawnAgent, "spawn_agent"},
		{"toolJoinRun", toolJoinRun, "join_run"},
		{"toolInspectAgents", toolInspectAgents, "inspect_agents"},
		{"toolCancelRun", toolCancelRun, "cancel_run"},
	}
	for _, tc := range cases {
		if tc.got != tc.want {
			t.Errorf("%s = %q, want %q", tc.name, tc.got, tc.want)
		}
	}
}
