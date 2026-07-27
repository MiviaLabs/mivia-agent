package cli

import (
	"sync"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
)

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
}
