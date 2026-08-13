package cli

import (
	"io"

	"github.com/MiviaLabs/mivia-agent/internal/reasoning"
)

// jsonSlashSink adapts slashSink to --json line-mode: /model and /effort
// results (and every other slash command's plain informational output)
// become structured NDJSON events instead of vanishing, which is what
// happens today when terminalSlashSink wraps a nil *Terminal - see that
// type's Info/Error methods, which are a no-op body guarded on `s.t != nil`.
type jsonSlashSink struct {
	w io.Writer
}

// Info emits any slash command's informational output as a generic
// "slash_info" event - the fallback for every command that has no typed
// shape below (a status query, "current model=...", a soft failure like
// "model not available", ...). Still visible on the wire, even without a
// dedicated field for a caller to key off.
func (s *jsonSlashSink) Info(msg string) {
	writeNDJSONEvent(s.w, ndjsonEvent{Type: "slash_info", Message: msg})
}

// Error emits a slash command's hard-error output as "slash_error".
func (s *jsonSlashSink) Error(msg string) {
	writeNDJSONEvent(s.w, ndjsonEvent{Type: "slash_error", Message: msg})
}

// ModelChanged reports a successful /model switch. discarded is the
// previously-active reasoning effort the switch dropped, if any (mirrors
// effortDiscardedSuffix's prose equivalent) - the zero value means none.
func (s *jsonSlashSink) ModelChanged(provider, model string, discarded reasoning.Level) {
	ev := ndjsonEvent{Type: "model_changed", Provider: provider, Model: model}
	if discarded.Active() {
		ev.DiscardedEffort = string(discarded)
	}
	writeNDJSONEvent(s.w, ev)
}

// EffortChanged reports a successful /effort switch.
func (s *jsonSlashSink) EffortChanged(model string, level reasoning.Level) {
	writeNDJSONEvent(s.w, ndjsonEvent{Type: "effort_changed", Model: model, Effort: string(level)})
}

// activeJSONSlashSink, when non-nil, is where handleSlashInfo's "/model" case
// and handleSlashEffort route feedback in --json line-mode - see
// replLineMode, the only place this is ever set. nil (TUI, classic REPL, and
// every process not started with --json) keeps every slash command on the
// normal prose sink, unchanged.
//
// Safe as a package-level variable rather than a parameter threaded through
// handleSlash and its ~20 call sites (most in tests unrelated to --json):
// exactly one `mivia chat --json` process drives exactly one single-threaded
// stdin scan loop (replLineMode), so there is never a second goroutine that
// could see a stale or concurrently-written value within that process's
// lifetime.
var activeJSONSlashSink *jsonSlashSink

// slashSinkFor returns the active --json sink if one is set, else the normal
// terminal-writing sink (nil-safe - see terminalSlashSink).
func slashSinkFor(term *Terminal) slashSink {
	if activeJSONSlashSink != nil {
		return activeJSONSlashSink
	}
	return terminalSlashSink{t: term}
}
