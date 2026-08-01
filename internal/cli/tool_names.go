package cli

import "github.com/MiviaLabs/mivia-agent/internal/skills"

// tool_names.go - the wire names of built-in handlers and agent-control tools.
// These strings cross the model/tool-schema JSON boundary as plain values; they are
// intentionally untyped string consts, not a typed enum, to avoid marshal conversions.

// Built-in handler names registered with runtime.Subagent.
const (
	handlerMultiStep = "multi_step"
	handlerDelegate  = "delegate"
	handlerOneshot   = "oneshot"
)

// Agent-control tool names (the surfaces that launch/control another agent).
// agentControlTools (action.go) is the membership source of truth for this set.
const (
	toolDispatchTasks = "dispatch_tasks"
	toolSpawnAgent    = "spawn_agent"
	toolJoinRun       = "join_run"
	toolInspectAgents = "inspect_agents"
	toolCancelRun     = "cancel_run"
)

// builtinHandlerNames is the ordered enum advertised in the dispatch_tasks /
// orchestrate schemas before registered skill names are appended. Order is part of
// the model-facing contract - do not reorder.
var builtinHandlerNames = []string{
	handlerMultiStep, handlerDelegate, handlerOneshot,
}

// injectHandlerEnum writes the built-in-handler + registered-skill name enum into the
// <prop> property of the tasks[].items schema map. It is the single implementation of
// the logic previously duplicated in orchestrate.go (prop "name") and dispatch.go
// (prop "handler").
//
// The returned enum is always a fresh slice so callers (or schema consumers) cannot
// mutate builtinHandlerNames via append or index assignment.
func injectHandlerEnum(result map[string]any, prop string, skillReg *skills.Registry) {
	enumValues := make([]string, len(builtinHandlerNames))
	copy(enumValues, builtinHandlerNames)
	if skillReg != nil {
		for _, info := range skillReg.ListModelFacing(nil) {
			enumValues = append(enumValues, info.Name)
		}
	}
	props := result["properties"].(map[string]any)
	tasks := props["tasks"].(map[string]any)
	items := tasks["items"].(map[string]any)
	itemProps := items["properties"].(map[string]any)
	target := itemProps[prop].(map[string]any)
	target["enum"] = enumValues
}
