package cli

import (
	"encoding/json"
	"errors"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
	"github.com/MiviaLabs/mivia-agent/internal/chat"
)

// ndjsonEvent is the wire schema for line-mode --json output. Exactly one
// event type is populated per line:
//
//	{"type":"chunk","text":"..."}                            - one per emitted piece of streamed answer content
//	{"type":"thinking","text":"..."}                         - one per emitted piece of model reasoning (chain of thought), for providers that expose it
//	{"type":"tool_start","tool_call_id":"...","name":"...","input":"..."}  - a tool call began; input is a bounded, redacted preview
//	{"type":"tool_end","tool_call_id":"...","name":"...","output":"...","status":"ok|failed"}   - that tool call finished; output is a bounded, redacted preview, status is its outcome (see toolEndStatus)
//	{"type":"subagent_done","origin_task_id":"..."}  - a delegated subagent run finished; its loop will emit nothing further
//	{"type":"subagent_heartbeat","origin_task_id":"...","message":"..."}  - a delegated subagent is still running; message is a short wall-clock progress note (model thinking, tool batch, ...), best-effort and not guaranteed on every tick
//	{"type":"model_changed","provider":"...","model":"...","discarded_effort":"..."}  - a /model switch succeeded; discarded_effort is set only if the switch dropped an active reasoning effort
//	{"type":"effort_changed","model":"...","effort":"..."}  - an /effort switch succeeded
//	{"type":"slash_info","message":"..."}          - any other slash command's informational output (status query, current model/effort, a soft failure like "model not available", ...)
//	{"type":"slash_error","message":"..."}         - a slash command's hard-error output
//	{"type":"done","session_id":"..."}             - exactly once, turn completed successfully
//	{"type":"cancelled"}                           - exactly once, turn was SIGINT-interrupted
//	{"type":"error","message":"…"}                 - exactly once, turn failed
//
// The "external_*" types (see chat_hub.go) mirror this same vocabulary for a
// turn running in a DIFFERENT mivia process for the same session (a terminal
// TUI, another desktop thread) - relayed live via internal/hub, not read
// from disk. Every one carries "run_id" (that OTHER process's own turn
// identifier - unrelated to any turn_id this process's own turns use) so a
// consumer can tell which external turn each line continues:
//
//	{"type":"external_turn_start","run_id":"...","session_id":"...","role":"user","text":"..."}  - a new external turn began; text is what the other process's user typed
//	{"type":"external_chunk","run_id":"...","text":"..."}
//	{"type":"external_thinking","run_id":"...","text":"..."}
//	{"type":"external_tool_start","run_id":"...","tool_call_id":"...","name":"...","input":"..."}
//	{"type":"external_tool_end","run_id":"...","tool_call_id":"...","name":"...","output":"...","status":"ok|failed"}
//	{"type":"external_done","run_id":"..."}
//	{"type":"external_error","run_id":"...","message":"…"}
//
// "tool_start"/"tool_end" additionally carry "origin_task_id"/"origin_agent"/
// "origin_depth" when the tool call was made by a delegated subagent rather
// than the root loop (agent.EventSubagentStart/End - same field shape as
// agent.EventToolStart/End, just attributed - see agent.EventOrigin's doc
// comment); "tool_start" further carries "origin_task_description" (the
// bounded task text the subagent was given, constant across every one of
// its own tool_start events - a consumer only needs the first one it sees
// per origin_task_id). Root-loop tool calls omit all four origin fields
// entirely (agent.EventOrigin's zero value), so an older consumer that only
// reads tool_call_id/name/input/output still renders a correct, if
// unattributed, transcript - a consumer that wants to group a subagent's
// nested tool
// calls under its own run (rather than flatten them into the parent turn)
// keys off origin_task_id.
//
// "thinking"/"tool_start"/"tool_end"/"subagent_done"/"model_changed"/
// "effort_changed"/"slash_info"/"slash_error" are best-effort progress
// events: an older consumer that only understands chunk/done/cancelled/error
// can ignore unknown types and still render a correct transcript, since the
// final answer text still arrives entirely via "chunk" events. The tool
// events carry the same redacted preview fields the interactive TUI already
// renders (see agentEventBridgeCallback in tui_events.go) - the wire
// representation intentionally does not expose anything the TUI itself
// would not show.
//
// model_changed/effort_changed/slash_info/slash_error exist because, before
// them, every slash command routed through terminalSlashSink, which is a
// silent no-op when wrapped around a nil *Terminal (line-mode's case) - a
// caller like mivia-agent-desktop sending "/model provider/model" over the
// same stdin it already writes chat turns to had no way to learn whether the
// switch succeeded. See jsonSlashSink in chat_json_slash.go.
//
// /model and /effort route their failure branches (unavailable model,
// rejected effort) through sink.Error rather than sink.Info specifically so
// a --json consumer can treat "slash_error" as the authoritative failure
// signal for a switch attempt and ignore "slash_info" entirely for that
// purpose - a successful switch always emits slash_info (the same prose
// confirmation the TUI shows) immediately followed by model_changed/
// effort_changed, so slash_info alone is never a reliable success/failure
// discriminator on its own.
//
// A SIGINT-interrupted turn gets its own "cancelled" type rather than being
// folded into "error": a caller that wants to distinguish "the user stopped
// this on purpose" from "this genuinely failed" (e.g. to decide whether to
// surface an error toast) would otherwise have to string-match a message
// field, which is fragile - a bare type check is not.
//
// "done" carries the session's SessionID so a caller that started a brand
// new conversation (no --session on the invocation) learns the id mivia
// just minted, without which it has no way to look this conversation up
// later via `mivia sessions list`/`show`/`--session <id>` resume.
type ndjsonEvent struct {
	Type            string `json:"type"`
	Text            string `json:"text,omitempty"`
	Message         string `json:"message,omitempty"`
	SessionID       string `json:"session_id,omitempty"`
	ToolCallID      string `json:"tool_call_id,omitempty"`
	Name            string `json:"name,omitempty"`
	Input           string `json:"input,omitempty"`
	Output          string `json:"output,omitempty"`
	Provider        string `json:"provider,omitempty"`
	Model           string `json:"model,omitempty"`
	Effort          string `json:"effort,omitempty"`
	DiscardedEffort string `json:"discarded_effort,omitempty"`
	// Status is "ok" or "failed" on a "tool_end", derived from the same
	// toolEndDetail the TUI renders (see toolEndStatus). Before this field
	// existed the failure signal lived only in Event.Detail, which
	// eventPreview drops whenever a tool produced any output at all - so a
	// --json consumer had no way to tell a failed tool call from a
	// successful one. Absent means "an older bundled CLI that predates this
	// field", which a consumer should read as ok (the prior behavior), not
	// as failure.
	Status string `json:"status,omitempty"`
	// OriginTaskID/OriginAgent/OriginDepth attribute a tool_start/tool_end
	// (or a subagent_done) to the delegated subagent that produced it - see
	// agent.EventOrigin. Omitted entirely for the root loop's own tool
	// calls (the common case).
	OriginTaskID string `json:"origin_task_id,omitempty"`
	OriginAgent  string `json:"origin_agent,omitempty"`
	OriginDepth  int    `json:"origin_depth,omitempty"`
	// OriginTaskDescription carries the same value on every "tool_start" a
	// given subagent run produces (its task doesn't change mid-run) - a
	// consumer only needs to read it off the first one it sees for a given
	// origin_task_id and can ignore it on the rest. Present only alongside
	// the other Origin* fields (a subagent's own nested tool_start), never
	// on the root loop's own tool calls. See agent.EventOrigin.TaskDescription.
	OriginTaskDescription string `json:"origin_task_description,omitempty"`
	// RunID/Role are used only by the "external_*" types (see this file's
	// top doc comment and chat_hub.go): RunID is the other process's own
	// turn identifier, Role marks "external_turn_start"'s synthetic user
	// turn.
	RunID string `json:"run_id,omitempty"`
	Role  string `json:"role,omitempty"`
}

// jsonTurnEventCallback returns an agent.Event handler that translates
// reasoning and tool-lifecycle events into NDJSON lines on w. It is the
// --json counterpart of agentEventBridgeCallback (tui_events.go), which does
// the equivalent translation into TUI bridge pushes - both read the same
// agent.Event fields and the same redacted eventPreview helper, so an
// external consumer sees the same content the TUI renders, just framed as
// NDJSON instead of terminal UI.
// toolEndStatus maps a tool_end event's Detail onto the wire's coarse
// "ok"/"failed" status. Detail is toolEndDetail's vocabulary (see
// internal/agent/loop_tools.go): "completed", "failed", and either with a
// "(truncated)"/"(duplicate)" qualifier - every failure variant starts with
// "failed", so the prefix is the whole test. Truncation is deliberately not
// surfaced here: it describes the preview, not the call's outcome, and a
// truncated-but-successful call must not read as an error.
//
// An empty Detail (an emitter that never set one) is "ok" rather than
// unknown: this field exists to let a consumer mark failures, and inventing
// a third state for a case that means "no signal" would make every such
// call render as a warning for no reason.
func toolEndStatus(detail string) string {
	if strings.HasPrefix(detail, "failed") {
		return "failed"
	}
	return "ok"
}

func jsonTurnEventCallback(w io.Writer) func(event agent.Event) {
	return func(e agent.Event) {
		switch e.Kind {
		case agent.EventThinking:
			if e.Content != "" {
				writeNDJSONEvent(w, ndjsonEvent{Type: "thinking", Text: e.Content})
			}
		case agent.EventToolStart, agent.EventSubagentStart:
			writeNDJSONEvent(w, ndjsonEvent{
				Type:                  "tool_start",
				ToolCallID:            e.ToolCallID,
				Name:                  e.Name,
				Input:                 eventPreview(e.Input, e.Detail),
				OriginTaskID:          e.Origin.TaskID,
				OriginAgent:           e.Origin.Agent,
				OriginDepth:           e.Origin.Depth,
				OriginTaskDescription: e.Origin.TaskDescription,
			})
		case agent.EventToolEnd, agent.EventSubagentEnd:
			writeNDJSONEvent(w, ndjsonEvent{
				Type:         "tool_end",
				ToolCallID:   e.ToolCallID,
				Name:         e.Name,
				Output:       eventPreview(e.Output, e.Detail),
				Status:       toolEndStatus(e.Detail),
				OriginTaskID: e.Origin.TaskID,
				OriginAgent:  e.Origin.Agent,
				OriginDepth:  e.Origin.Depth,
			})
		case agent.EventSubagentDone:
			writeNDJSONEvent(w, ndjsonEvent{
				Type:         "subagent_done",
				OriginTaskID: e.Origin.TaskID,
			})
		case agent.EventSubagentHeartbeat:
			// Origin is required for this event to mean anything (it retires
			// nothing on its own, just refreshes one subagent's progress
			// note) - a heartbeat with no origin (should not happen, see
			// OnEventForMultiStep) is dropped rather than sent as a
			// meaningless line.
			if e.Origin.TaskID == "" {
				return
			}
			writeNDJSONEvent(w, ndjsonEvent{
				Type:         "subagent_heartbeat",
				OriginTaskID: e.Origin.TaskID,
				Message:      e.Detail,
			})
		}
	}
}

// writeNDJSONEvent marshals ev as one NDJSON line and writes it to w.
func writeNDJSONEvent(w io.Writer, ev ndjsonEvent) {
	line, err := json.Marshal(ev)
	if err != nil {
		// ev is our own control struct (a type tag plus plain strings), so
		// this should never happen; fall back to a minimal event that itself
		// cannot fail to marshal rather than dropping the line silently.
		line, _ = json.Marshal(ndjsonEvent{Type: "error", Message: "internal: failed to encode ndjson event"})
	}
	line = append(line, '\n')
	_, _ = w.Write(line)
}

// ndjsonChunkWriter reframes a stream of raw content-delta Write() calls (the
// FinalWriter contract agent.Loop uses - see agent/loop.go, "content deltas go
// to FinalWriter") as NDJSON chunk events.
//
// The deltas arrive as arbitrary byte slices with no guarantee that a
// multi-byte UTF-8 rune is not split across two consecutive Write calls.
// Marshaling each raw Write independently would let json.Marshal silently
// replace a split rune's dangling bytes with U+FFFD on each side, corrupting
// otherwise-valid text. This writer buffers any incomplete trailing UTF-8
// sequence across calls and only emits a chunk once the buffered bytes are
// confirmed to end on a complete rune boundary.
type ndjsonChunkWriter struct {
	w       io.Writer
	pending []byte
}

func newNDJSONChunkWriter(w io.Writer) *ndjsonChunkWriter {
	return &ndjsonChunkWriter{w: w}
}

// Write buffers p, emits a chunk event for whatever prefix is confirmed
// complete, and holds back any trailing partial rune for the next call. It
// always reports the full length of p as written (and never returns a
// non-nil error from the buffering step itself) so callers that only check
// (n, err) against len(p) - like io.Copy or io.WriteString - see success.
func (n *ndjsonChunkWriter) Write(p []byte) (int, error) {
	written := len(p)
	if len(p) == 0 {
		return written, nil
	}
	n.pending = append(n.pending, p...)
	complete, incomplete := splitTrailingIncompleteRune(n.pending)
	n.pending = incomplete
	if len(complete) > 0 {
		writeNDJSONEvent(n.w, ndjsonEvent{Type: "chunk", Text: string(complete)})
	}
	return written, nil
}

// Flush emits whatever is left in the buffer, complete or not, as a final
// chunk. Called at the end of a successful turn so trailing bytes are never
// silently dropped. Must NOT be called after a cancelled turn - see Discard.
func (n *ndjsonChunkWriter) Flush() {
	if len(n.pending) == 0 {
		return
	}
	pending := n.pending
	n.pending = nil
	writeNDJSONEvent(n.w, ndjsonEvent{Type: "chunk", Text: string(pending)})
}

// Discard drops any buffered, not-yet-emitted bytes without writing them.
// Used on the cancelled/errored-turn path: bytes held back by Write because
// they might have been the start of a split rune were never a complete,
// confirmed chunk, so surfacing them now would fabricate a phantom chunk for
// content the turn never actually finished producing.
func (n *ndjsonChunkWriter) Discard() {
	n.pending = nil
}

// splitTrailingIncompleteRune splits b into a leading portion that is safe to
// emit now and a trailing portion that may be an incomplete UTF-8 sequence
// waiting on more bytes. It scans back at most utf8.UTFMax bytes for the
// start byte of the last rune; if that rune is already complete (or no
// multi-byte start byte is found in range), the whole slice is safe to emit.
func splitTrailingIncompleteRune(b []byte) (complete, incomplete []byte) {
	n := len(b)
	if n == 0 {
		return b, nil
	}
	limit := n - utf8.UTFMax
	start := n - 1
	for start >= 0 && start >= limit && !utf8.RuneStart(b[start]) {
		start--
	}
	if start < 0 || start < limit {
		// No rune-start byte within the lookback window: either the tail is
		// all ASCII (handled above via RuneStart on the very last byte in the
		// common case) or the bytes are not valid UTF-8 continuation data at
		// all. Either way there is nothing identifiable to hold back.
		return b, nil
	}
	if utf8.FullRune(b[start:]) {
		return b, nil
	}
	return b[:start], b[start:]
}

// jsonTurnErrorMessage returns a redaction-safe, plain-text description of a
// failed --json turn for the wire ("error" event's message field). Provider
// and tool error text can carry request content verbatim (DC-14: external
// error text may carry request content; see .mivia/quality/defect-taxonomy.md),
// so err.Error() is never put on the wire as-is. Only a couple of recognized
// internal sentinel failures get a slightly more specific, still content-free
// message; everything else collapses to one generic message, with the real
// error still available to an operator via stderr (sendLineMode's caller
// prints it there).
func jsonTurnErrorMessage(err error) string {
	switch {
	case errors.Is(err, chat.ErrPersistence):
		return "chat turn failed: could not persist session state"
	case errors.Is(err, chat.ErrStaleOperation), errors.Is(err, chat.ErrStaleAutosave):
		return "chat turn failed: superseded by a newer turn"
	default:
		return "chat turn failed"
	}
}
