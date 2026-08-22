package cliorchestrate

// Tool name constants used by orchestration tools registered via
// RegisterOrchestrationTools and NewDispatchTasksTool.
const (
	// HandlerMultiStep is the wire name of the multi_step handler.
	HandlerMultiStep = "multi_step"
	// HandlerDelegate is the wire name of the delegate handler.
	HandlerDelegate = "delegate"
	// HandlerOneshot is the wire name of the oneshot handler.
	HandlerOneshot = "oneshot"
)

const (
	// ToolDispatchTasks is the model-visible name of the dispatch_tasks tool.
	ToolDispatchTasks = "dispatch_tasks"
	// ToolSpawnAgent is the internal name of the spawn_agent tool.
	ToolSpawnAgent = "spawn_agent"
	// ToolJoinRun is the internal name of the join_run tool.
	ToolJoinRun = "join_run"
	// ToolInspectAgents is the internal name of the inspect_agents tool.
	ToolInspectAgents = "inspect_agents"
	// ToolCancelRun is the internal name of the cancel_run tool.
	ToolCancelRun = "cancel_run"
)
