package cliagents

// handler_names.go mirrors the built-in handler wire names from
// internal/cli/tool_names.go. The constants are needed by loadSessionSkills
// (model_binding.go) to build the reserved skill-name set. They are
// intentionally untyped string consts matching the cli originals.
const (
	handlerMultiStep = "multi_step"
	handlerDelegate  = "delegate"
	handlerOneshot   = "oneshot"
)
