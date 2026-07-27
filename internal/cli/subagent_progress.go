package cli

import (
	"sync"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
	"github.com/MiviaLabs/mivia-agent/internal/events"
)

// globalBus is set once by runTUI and used by emitSubagentProgress to
// publish subagent events onto the EventBus. This replaces the global
// callback pattern with persistent subscription.
var globalBus *events.Bus

// subagentProgress sinks nested multi_step tool/heartbeat events into the
// parent TUI. Set from startAI so MultiStepHandler.OnEvent is never nil-wired
// only at dispatcher construction (when the bridge does not exist yet).
var subagentProgress struct {
	mu sync.RWMutex
	fn func(agent.Event)
}

// SetSubagentProgress registers the parent progress handler for multi_step
// subagent events (tool start/end, heartbeats). Pass nil to clear.
func SetSubagentProgress(fn func(agent.Event)) {
	subagentProgress.mu.Lock()
	subagentProgress.fn = fn
	subagentProgress.mu.Unlock()
}

func emitSubagentProgress(e agent.Event) {
	subagentProgress.mu.RLock()
	fn := subagentProgress.fn
	subagentProgress.mu.RUnlock()
	if fn != nil {
		fn(e)
	}
	// Also publish to EventBus (if set).
	if globalBus != nil {
		globalBus.Publish(events.NewEventFromAgentParts(
			events.Kind(e.Kind),
			e.ToolCallID,
			e.Name,
			e.Detail,
			e.Content,
			e.Input,
			e.Output,
		))
	}
}

// SetGlobalBus sets the global EventBus reference used by emitSubagentProgress.
// Called once from runTUI.
func SetGlobalBus(bus *events.Bus) {
	globalBus = bus
}
