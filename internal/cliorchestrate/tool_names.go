package cliorchestrate

// Tool name constants used by orchestration tools registered via
// RegisterOrchestrationTools and NewDispatchTasksTool.
const (
	// ToolDispatchTasks is the model-visible name of the dispatch_tasks tool.
	ToolDispatchTasks = "dispatch_tasks"
	// toolSpawnAgent is the internal name of the spawn_agent tool.
	toolSpawnAgent = "spawn_agent"
	// toolJoinRun is the internal name of the join_run tool.
	toolJoinRun = "join_run"
	// toolInspectAgents is the internal name of the inspect_agents tool.
	toolInspectAgents = "inspect_agents"
	// toolCancelRun is the internal name of the cancel_run tool.
	toolCancelRun = "cancel_run"
)
