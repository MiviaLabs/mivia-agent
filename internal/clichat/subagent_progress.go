package clichat

import (
	"sync"
	"sync/atomic"

	"github.com/MiviaLabs/mivia-agent/internal/chat"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
	"github.com/MiviaLabs/mivia-agent/internal/events"
)

// subagentSession is the session whose EventBus receives subagent progress,
// set for the life of a chat surface by SetSubagentSession.
//
// It replaces a bus reference that production never set. SetGlobalBus existed
// and was exported, but nothing outside its own definition called it, so every
// subagent event published into a nil bus and vanished - while the code read as
// if a second surface was being fed. internal/hub lists all four subagent kinds
// as relayable; none of them had ever reached a bus.
//
// Holding the session rather than the bus is what lets the published events
// carry SessionID and TurnID. Without SessionID a hub receiver drops them on
// purpose (externalEventBelongsToSession), so a bare bus reference would have
// fixed the publish and changed nothing a consumer could see.
var subagentSessionState struct {
	sync.RWMutex
	sess *chat.Session
}

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
	publishSubagentEvent(e)
}

// publishSubagentEvent forwards one subagent event to the session bus,
// attributed to the producing agent and to the turn that dispatched it.
func publishSubagentEvent(e agent.Event) {
	subagentSessionState.RLock()
	sess := subagentSessionState.sess
	subagentSessionState.RUnlock()
	if sess != nil && sess.EventBus != nil {
		ev := events.NewEventFromAgentParts(
			events.Kind(e.Kind),
			e.ToolCallID,
			e.Name,
			e.Detail,
			e.Content,
			e.Input,
			e.Output,
		).WithAgentAttribution(e.Origin.TaskID, e.Origin.Agent, e.Origin.Depth)
		if e.Identity != nil {
			identity := *e.Identity
			ev.Identity = &identity
		}
		// A hub receiver rejects an event with no SessionID rather than
		// matching two empty strings, so this attribution is what makes the
		// publish observable at all.
		ev.SessionID = sess.SessionID
		ev.TurnID = sess.CurrentTurnEventID()
		sess.EventBus.Publish(ev)
	}
}

// SetSubagentSession binds subagent progress publishing to sess for the life of
// a chat surface, and returns a function that unbinds it. Every surface routes
// through one choke point (dispatchChatSurface), so binding there covers the
// one-shot, REPL and TUI paths alike.
func SetSubagentSession(sess *chat.Session) func() {
	subagentSessionState.Lock()
	subagentSessionState.sess = sess
	subagentSessionState.Unlock()
	return func() {
		subagentSessionState.Lock()
		subagentSessionState.sess = nil
		subagentSessionState.Unlock()
	}
}
