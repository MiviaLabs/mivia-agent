package agent

import (
	"encoding/json"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// ScrubEphemeralToolMessages runs after the final provider step, before a
// session adopts the turn. It preserves assistant/tool pairing while removing
// resource bodies from all subsequent history and persistence.
func ScrubEphemeralToolMessages(messages []provider.Message, reg *tools.Registry) {
	if reg == nil {
		return
	}
	argsByCallID := make(map[string]json.RawMessage)
	for i := range messages {
		if messages[i].Role != provider.RoleAssistant {
			continue
		}
		for _, call := range messages[i].ToolCalls {
			argsByCallID[call.ID] = json.RawMessage(call.Function.Arguments)
		}
	}
	for i := range messages {
		message := &messages[i]
		if message.Role != provider.RoleTool {
			continue
		}
		tool, ok := reg.Get(message.Name)
		if !ok {
			continue
		}
		if ephemeral, ok := tool.(tools.EphemeralResultTool); ok {
			message.Content = ephemeral.EphemeralResultMarker(argsByCallID[message.ToolCallID])
		}
	}
}
