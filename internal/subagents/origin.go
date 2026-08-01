package subagents

import "github.com/MiviaLabs/mivia-agent/internal/agent"

// StampEventOrigin decorates onEvent so every event it receives carries the
// given origin. An event that already has an origin keeps it - the stamp
// closest to the producing loop wins, so deeper nesting is never rewritten
// by an outer handler.
//
// A nil onEvent stays nil so callers keep their existing nil checks.
func StampEventOrigin(onEvent func(agent.Event), origin agent.EventOrigin) func(agent.Event) {
	if onEvent == nil {
		return nil
	}
	return func(e agent.Event) {
		if e.Origin.IsZero() {
			e.Origin = origin
		}
		onEvent(e)
	}
}
