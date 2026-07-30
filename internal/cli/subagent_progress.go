package cli

import (
	"sync"
	"sync/atomic"

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
	mu  sync.RWMutex
	fn  func(agent.Event)
	gen uint64 // incremented on every SetSubagentProgress call
}

// subagentGen is incremented on each SetSubagentProgress call to produce
// unique generation tokens for conditional clear.
var subagentGen atomic.Uint64

// SetSubagentProgress registers the parent progress handler for multi_step
// subagent events (tool start/end, heartbeats). Returns a generation token
// that must be passed to ClearSubagentProgress for safe conditional removal.
func SetSubagentProgress(fn func(agent.Event)) uint64 {
	token := subagentGen.Add(1)
	subagentProgress.mu.Lock()
	subagentProgress.fn = fn
	subagentProgress.gen = token
	subagentProgress.mu.Unlock()
	return token
}

// ClearSubagentProgress conditionally clears the registered progress handler
// only if the generation token still matches. This prevents a stale goroutine
// (from a cancelled turn) from clearing a newer turn's callback:
//
//	genA := TurnA: SetSubagentProgress(fnA)
//	genB := TurnB: SetSubagentProgress(fnB)   → gen now matches genB
//	TurnA exits: ClearSubagentProgress(genA)   → no-op (gen != genA), fnB preserved
func ClearSubagentProgress(token uint64) {
	subagentProgress.mu.Lock()
	if subagentProgress.gen == token {
		subagentProgress.fn = nil
	}
	subagentProgress.mu.Unlock()
}

func emitSubagentProgress(e agent.Event) {
	subagentProgress.mu.RLock()
	fn := subagentProgress.fn
	subagentProgress.mu.RUnlock()
	if fn != nil {
		fn(e)
	}
	// Also publish to EventBus (if set), attributed to the producing agent.
	if globalBus != nil {
		globalBus.Publish(events.NewEventFromAgentParts(
			events.Kind(e.Kind),
			e.ToolCallID,
			e.Name,
			e.Detail,
			e.Content,
			e.Input,
			e.Output,
		).WithAgentAttribution(e.Origin.TaskID, e.Origin.Agent, e.Origin.Depth))
	}
}

// SetGlobalBus sets the global EventBus reference used by emitSubagentProgress.
// Called once from runTUI.
func SetGlobalBus(bus *events.Bus) {
	globalBus = bus
}
