package cli

import "github.com/MiviaLabs/mivia-agent/internal/agent"

func agentEventBridgeCallback(bridge *streamBridge) func(agent.Event) {
	return func(e agent.Event) {
		switch e.Kind {
		case agent.EventToolStart:
			bridge.PushTool(true, e.Name, e.Detail)
		case agent.EventToolEnd:
			bridge.PushTool(false, e.Name, e.Detail)
		case agent.EventToolParallel:
			bridge.PushTool(true, "parallel", e.Detail)
		case agent.EventPrune:
			bridge.PushTool(false, "prune", e.Detail)
		case agent.EventAssistant:
			if e.Content != "" {
				bridge.PushThinking(e.Content)
			}
		}
	}
}
