package clichat

import (
	"sync"
	"sync/atomic"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
	"github.com/MiviaLabs/mivia-agent/internal/events"
)

// sessionBuses is the session-keyed bus registry that replaced the dead
// global bus singleton. Subagent lifecycle events are attributed to a
// SESSION (agent.EventOrigin.SessionID, stamped on every producer path -
// INV-HUB-5), not to the process, so the registry is keyed the same way:
// a session with no registered bus (never dispatched, or already torn
// down) gets no publish, and a session that IS registered gets only ITS
// OWN events, never another session's running in the same process.
var sessionBuses struct {
	sync.RWMutex
	m map[string]*events.Bus
}

// RegisterSessionBus binds sessionID to bus so emitSubagentProgress can
// publish that session's subagent lifecycle events onto it. A re-register
// under the same sessionID replaces whatever was bound before.
//
// The returned release func unbinds the registration, but only if the
// stored bus still equals the one this call registered (match-before-
// delete): a later re-register (a new turn, a new dispatch) followed by
// an earlier turn's stale release must not unbind the replacement. release
// is idempotent and safe to call from any goroutine, including
// concurrently with another release for the same sessionID.
func RegisterSessionBus(sessionID string, bus *events.Bus) (release func()) {
	sessionBuses.Lock()
	if sessionBuses.m == nil {
		sessionBuses.m = make(map[string]*events.Bus)
	}
	sessionBuses.m[sessionID] = bus
	sessionBuses.Unlock()

	var released atomic.Bool
	return func() {
		if !released.CompareAndSwap(false, true) {
			return
		}
		sessionBuses.Lock()
		if sessionBuses.m[sessionID] == bus {
			delete(sessionBuses.m, sessionID)
		}
		sessionBuses.Unlock()
	}
}

// LookupSessionBus returns the bus registered for sessionID, or nil if none
// is bound.
func LookupSessionBus(sessionID string) *events.Bus {
	sessionBuses.RLock()
	bus := sessionBuses.m[sessionID]
	sessionBuses.RUnlock()
	return bus
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

// busPublishableKind is the fail-closed allowlist of subagent EventKinds
// that may reach a session's chatsync bus. Only the four LIFECYCLE kinds
// (start/end of a nested tool call, a periodic heartbeat, and the run-level
// terminal signal) are structural progress - never model output. A
// subagent's EventThinking/EventAssistant content stays LOCAL to this
// process's UI sink (the fn(e) call above, unconditional for every kind)
// and is deliberately never published here, so a remote chat-sync viewer
// never sees a subagent's reasoning or prose, only that it is working.
func busPublishableKind(kind agent.EventKind) bool {
	switch kind {
	case agent.EventSubagentStart, agent.EventSubagentEnd, agent.EventSubagentHeartbeat, agent.EventSubagentDone:
		return true
	default:
		return false
	}
}

func emitSubagentProgress(e agent.Event) {
	subagentProgress.mu.RLock()
	fn := subagentProgress.fn
	subagentProgress.mu.RUnlock()
	if fn != nil {
		fn(e)
	}
	if !busPublishableKind(e.Kind) {
		return
	}
	if e.Origin.SessionID == "" {
		return
	}
	bus := LookupSessionBus(e.Origin.SessionID)
	if bus == nil {
		return
	}
	ev := events.NewEventFromAgentParts(
		events.Kind(e.Kind),
		e.ToolCallID,
		e.Name,
		e.Detail,
		e.Content,
		e.Input,
		e.Output,
	).WithAgentAttribution(e.Origin.TaskID, e.Origin.Agent, e.Origin.Depth)
	// The session and turn come off the ORIGIN, because this sink is
	// package-level and has none of its own. Without them the event is
	// published with an empty SessionID and internal/hub's receiver drops
	// it (externalEventBelongsToSession), so a second live surface saw the
	// root loop's tool calls and none of its subagents'.
	ev.SessionID = e.Origin.SessionID
	ev.TurnID = e.Origin.TurnID
	if e.Identity != nil {
		identity := *e.Identity
		ev.Identity = &identity
	}
	bus.Publish(ev)
}
