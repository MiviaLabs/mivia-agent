package cli

import "github.com/MiviaLabs/mivia-agent/internal/agent"

// agentEventBridgeCallback returns an OnEvent handler that forwards
// agent loop events to the TUI bridge for rendering.
func agentEventBridgeCallback(bridge *streamBridge) func(agent.Event) {
	return func(e agent.Event) {
		switch e.Kind {
		case agent.EventToolStart:
			bridge.PushToolWithID(true, e.ToolCallID, e.Name, eventPreview(e.Input, e.Detail))
		case agent.EventToolEnd:
			bridge.PushToolWithID(false, e.ToolCallID, e.Name, eventPreview(e.Output, e.Detail))
		case agent.EventToolParallel:
			bridge.PushTool(true, "parallel", e.Detail)
		case agent.EventPrune:
			bridge.PushTool(false, "prune", e.Detail)
		case agent.EventAssistant:
			if e.Content != "" {
				bridge.PushThinking(e.Content)
			}
		case agent.EventStep:
			bridge.PushStep(e.Detail)
		case agent.EventSubagentStart:
			bridge.PushToolWithID(true, e.ToolCallID, e.Name, eventPreview(e.Input, e.Detail))
		case agent.EventSubagentEnd:
			bridge.PushToolWithID(false, e.ToolCallID, e.Name, eventPreview(e.Output, e.Detail))
		}
	}
}

func eventPreview(preview, fallback string) string {
	if preview != "" {
		return preview
	}
	return fallback
}
