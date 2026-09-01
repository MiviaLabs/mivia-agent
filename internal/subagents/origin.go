package subagents

import (
	"context"
	"encoding/json"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
	"github.com/MiviaLabs/mivia-agent/internal/textutil"
)

// maxTaskDescriptionBytes bounds EventOrigin.TaskDescription the same way
// every other UI-facing preview in this codebase is bounded (see
// eventPreview in internal/cli/tui_events.go) - a consumer attributing a
// subagent card to its task shouldn't have to handle an arbitrarily long
// description any more than a tool call's redacted input/output preview.
const maxTaskDescriptionBytes = 200

// taskDescriptionFromInput derives a bounded description from a task's raw
// Input for EventOrigin.TaskDescription. delegate's Input is always a bare
// JSON string (the task text); dispatch_tasks/spawn_agent's per-task Input
// can be arbitrary JSON shaped by that task's own input schema, so this
// falls back to the raw JSON text when it isn't a bare string - still a
// useful-enough preview, just not natural-language.
func taskDescriptionFromInput(input json.RawMessage) string {
	if len(input) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(input, &s) == nil {
		return boundTaskDescription(s)
	}
	return boundTaskDescription(string(input))
}

// boundTaskDescription truncates on a UTF-8 rune boundary
// (textutil.TruncateRuneSafe) rather than a raw byte offset - a task
// description is free text and can carry any script, so a naive
// s[:maxTaskDescriptionBytes] cut can split a multi-byte rune and hand the
// caller invalid UTF-8 (DC-6). The ellipsis is appended after the bound, not
// carved out of it (unlike textutil.TruncateEllipsis) - matches this
// function's existing contract of an up-to-maxTaskDescriptionBytes-byte
// content prefix plus a trailing marker.
func boundTaskDescription(s string) string {
	if len(s) <= maxTaskDescriptionBytes {
		return s
	}
	return textutil.TruncateRuneSafe(s, maxTaskDescriptionBytes) + "…"
}

type originContextKey struct{}

// ContextWithOrigin carries a running subagent's own origin down into
// everything it dispatches, so a nested run can name the run that started it.
//
// The parent origin cannot be derived from the child's runtime.Request:
// Request.ParentID is the parent INVOCATION id, while the attribution key is
// the TaskID, and the two differ whenever a coordinator supplies a workflow
// task id. The only place the parent's TaskID is unambiguous is the parent
// itself, which is why it travels on the context - the same reasoning that
// puts SessionID and TurnID on the origin.
func ContextWithOrigin(ctx context.Context, origin agent.EventOrigin) context.Context {
	return context.WithValue(ctx, originContextKey{}, origin)
}

// OriginFrom returns the origin of the subagent whose execution ctx belongs
// to. The second result is false for the root loop, which has none.
func OriginFrom(ctx context.Context) (agent.EventOrigin, bool) {
	origin, ok := ctx.Value(originContextKey{}).(agent.EventOrigin)
	if !ok || origin.TaskID == "" {
		return agent.EventOrigin{}, false
	}
	return origin, true
}

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
