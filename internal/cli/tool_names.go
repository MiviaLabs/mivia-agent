package cli

// tool_names.go - the wire names of built-in handlers and agent-control tools.
// These strings cross the model/tool-schema JSON boundary as plain values; they are
// intentionally untyped string consts, not a typed enum, to avoid marshal conversions.

// Built-in handler names registered with runtime.Subagent.
const (
	handlerMultiStep = "multi_step"
	// HandlerDelegate is the wire name of the delegation handler.
	HandlerDelegate = "delegate"
	handlerOneshot  = "oneshot"
)

// Agent-control tool names (the surfaces that launch/control another agent).
// agentControlTools (action.go) is the membership source of truth for this set.
const (
	// ToolDispatchTasks is the wire name of the multi-task dispatch tool.
	ToolDispatchTasks = "dispatch_tasks"
	toolSpawnAgent    = "spawn_agent"
	toolJoinRun       = "join_run"
	toolInspectAgents = "inspect_agents"
	toolCancelRun     = "cancel_run"
)

// The dispatch_tasks / spawn_agent task enums are not built here: the live
// schemas build each tasks[].items property in taskItemSchema (task_routing.go)
// from registered agent + skill names, and a model-facing "handler" field is
// strictly rejected (decodeStrictTaskJSON), so no built-in-handler enum exists.
