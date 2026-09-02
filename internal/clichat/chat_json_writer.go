package clichat

import (
	"encoding/json"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/events"
)

// ndjsonEvent is the wire schema for line-mode --json output; exactly one
// type is populated per line. See docs/product/wire-schema.md for the
// full type vocabulary and field list.
//
// "external_*" types carry a turn running in a DIFFERENT mivia process for
// the same session, relayed via internal/hub. They are TWO families, not one:
// the plain "external_*" types are that process's ROOT agent, and the
// "external_subagent_*" types are one of its subagents. The prefix is the
// whole distinction, because a consumer keyed on type is the only kind of
// consumer a new field cannot reach.
//
// Older consumers that only understand chunk/done/cancelled/error can
// safely ignore unknown types, since the final answer always arrives via
// "chunk" events.
//
// model_changed/effort_changed/slash_info/slash_error exist because slash
// commands used to route through terminalSlashSink, a silent no-op with a
// nil *Terminal (line-mode) — a --json consumer had no way to learn if a
// switch succeeded. Failure branches use sink.Error (not sink.Info) so
// "slash_error" is the sole authoritative failure signal.
//
// "cancelled" is its own type (not folded into "error") so a consumer can
// tell "user stopped this" from "this failed" without string-matching.
//
// "done" carries SessionID so a caller with no --session on invocation
// learns the id mivia just minted, for later `mivia sessions show/--session`.
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
	// Status has one vocabulary per event type. On a "tool_end" - and on the
	// relayed "external_tool_end" and "external_subagent_tool_end" - it is "ok"
	// or "failed", derived from the same toolEndDetail the TUI renders (see
	// toolEndStatus). On a "subagent_done" it is the run's terminal status
	// ("completed", "canceled", "timed_out", or "error"), sourced from
	// agent.Event.Status. Before these fields existed the failure signal
	// lived only in Event.Detail, which eventPreview drops whenever a tool
	// produced any output at all - so a --json consumer had no way to tell
	// a failed tool call from a successful one. Absent means "an older
	// bundled CLI that predates this field", which a consumer should read
	// as ok (the prior behavior), not as failure.
	Status string `json:"status,omitempty"`
	// HookEvent, Program and Tool describe one lifecycle hook run: which
	// phase fired (PreToolUse, PostToolUse, Stop), which script ran, and which
	// tool it ran for. They are separate from Name deliberately - on every
	// other line type Name is the TOOL's name, and overloading it here would
	// make a consumer's "which tool" lookup wrong exactly when a hook blocked
	// the call.
	HookEvent string `json:"hook_event,omitempty"`
	Program   string `json:"program,omitempty"`
	Tool      string `json:"tool,omitempty"`
	// OriginTaskID/OriginAgent/OriginDepth attribute an event to the
	// delegated subagent that produced it - see agent.EventOrigin.
	//
	// On the LOCAL types they appear on tool_start/tool_end and subagent_done,
	// and are omitted for the root loop's own events. On the RELAYED types
	// they appear only on the external_subagent_* family, where the type
	// already says a subagent produced the line and these say which RUN did:
	// two runs of one agent share a name but not a task id.
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
	// RunID/Role are used only by the "external_*" and "external_subagent_*"
	// types (see this file's
	// top doc comment and chat_hub.go): RunID is the other process's own
	// turn identifier, Role marks "external_turn_start"'s synthetic user
	// turn.
	RunID string `json:"run_id,omitempty"`
	Role  string `json:"role,omitempty"`
	// Dropped and TotalDropped appear only on "external_dropped": how many
	// relayed events were lost since the previous report, and the hub's
	// cumulative total. The cross-process relay is deliberately lossy (bounded
	// drop-oldest at every hop), and these are the only signal that says so -
	// see docs/product/wire-schema.md.
	Dropped      uint64 `json:"dropped,omitempty"`
	TotalDropped uint64 `json:"total_dropped,omitempty"`
	// CacheUsage is present only on "cache_usage" events. It is a nested
	// record rather than flat fields so its legitimate zero values (an
	// all-miss step) survive serialization without forcing zero-valued
	// token fields onto every other event type.
	CacheUsage *ndjsonCacheUsage `json:"cache_usage,omitempty"`
	// TokenUsage is present only on "token_usage" events, nested for the
	// same zero-value reason as CacheUsage. Real provider-reported counts,
	// not estimates - see agent.EmitTokenUsage.
	TokenUsage *ndjsonTokenUsage `json:"token_usage,omitempty"`
	// ContextUsage is present only on the turn-final "context_usage" event
	// written by sendLineMode, nested for the same zero-value reason. See
	// chat.ContextUsage for what each field means.
	ContextUsage *ndjsonContextUsage `json:"context_usage,omitempty"`
	// Compaction is present only on "compaction" events, nested for the same
	// zero-value reason as CacheUsage. SourceRange is deliberately omitted -
	// it carries session-internal SourceID values, not useful to a consumer.
	Compaction *ndjsonCompaction `json:"compaction,omitempty"`
}

// ndjsonCacheUsage carries one completion turn's provider-reported
// prompt-cache accounting (see agent.EmitCacheUsage). HitPercent repeats the
// same guarded percent the TUI status line shows: 0 when InputTokens is 0.
type ndjsonCacheUsage struct {
	InputTokens       int `json:"input_tokens"`
	CachedInputTokens int `json:"cached_input_tokens"`
	CacheWriteTokens  int `json:"cache_write_tokens"`
	HitPercent        int `json:"hit_percent"`
}

// ndjsonTokenUsage carries one completion turn's provider-reported
// input/output token counts plus estimate-vs-actual drift (see
// agent.EmitTokenUsage). A --json consumer that wants real context usage
// reads input_tokens here: it is the actual prompt size the provider
// charged for, so it already includes the whole conversation.
type ndjsonTokenUsage struct {
	InputTokens      int     `json:"input_tokens"`
	OutputTokens     int     `json:"output_tokens"`
	EstimatedTokens  int     `json:"estimated_tokens"`
	CalibrationRatio float64 `json:"calibration_ratio"`
}

// ndjsonContextUsage carries the session-level context accounting the TUI
// status dialog renders (chat.Session.ContextUsage): used/budget/window/
// reserve in tokens and the prompt-side percent. UsedTokens is the len(s)/4
// estimate, unlike ndjsonTokenUsage's provider-reported counts; consumers
// that see both prefer the real numbers and keep this as the fallback that
// also states the window and budget.
type ndjsonContextUsage struct {
	UsedTokens          int `json:"used_tokens"`
	BudgetTokens        int `json:"budget_tokens"`
	ContextWindowTokens int `json:"context_window_tokens"`
	OutputReserveTokens int `json:"output_reserve_tokens"`
	Percent             int `json:"percent"`
}

// ndjsonCompaction carries one context-compaction record (see
// agent.EmitCompaction / events.CompactionEvent), nested for the same
// zero-value reason as ndjsonCacheUsage.
type ndjsonCompaction struct {
	Trigger        string `json:"trigger"`
	BeforeTokens   int    `json:"before_tokens"`
	AfterTokens    int    `json:"after_tokens"`
	ElidedMessages int    `json:"elided_messages"`
	ElidedBytes    int    `json:"elided_bytes"`
	SummaryVersion uint32 `json:"summary_version"`
	// Summarized is whether an LLM summary of the dropped messages was really
	// produced. summary_version is always 1 (its validator refuses 0), so it
	// cannot carry this and a consumer had no way to tell a summarized
	// compaction from a purely structural one.
	Summarized bool `json:"summarized"`
	// Reason is the classified, content-free cause when Summarized is false
	// (see events.CompactionEvent.Reason). Empty when Summarized is true.
	Reason string `json:"reason,omitempty"`
}

// writeTokenUsageLine frames one provider-reported token accounting record
// as a "token_usage" NDJSON line. Extracted from jsonTurnEventCallback to
// keep that switch under the structure-check function-size limit. The
// counts are provider-reported (real), unlike the session-level estimate a
// turn-final "context_usage" event carries - a consumer reading both
// prefers these.
func writeTokenUsageLine(w io.Writer, typed events.TokenUsageEvent) {
	writeNDJSONEvent(w, ndjsonEvent{
		Type:     "token_usage",
		Provider: typed.Provider,
		Model:    typed.Model,
		TokenUsage: &ndjsonTokenUsage{
			InputTokens:      typed.InputTokens,
			OutputTokens:     typed.OutputTokens,
			EstimatedTokens:  typed.EstimatedTokens,
			CalibrationRatio: typed.CalibrationRatio,
		},
	})
}

// writeCompactionLine frames one context-compaction record as a
// "compaction" NDJSON line. Extracted from jsonTurnEventCallback for the
// same reason as writeTokenUsageLine: keeps that switch under the
// structure-check function-size limit.
func writeCompactionLine(w io.Writer, detail string, typed events.CompactionEvent) {
	writeNDJSONEvent(w, ndjsonEvent{
		Type:       "compaction",
		Message:    detail,
		Compaction: compactionPayload(typed),
	})
}

// compactionPayload maps the typed compaction record onto the wire struct -
// the ONE mapping, shared by the local turn path (writeCompactionLine) and
// the cross-process relay path (chat_hub.go's renderExternalCompaction), so
// the two surfaces cannot drift apart.
func compactionPayload(typed events.CompactionEvent) *ndjsonCompaction {
	return &ndjsonCompaction{
		Trigger:        typed.Trigger,
		BeforeTokens:   typed.BeforeTokens,
		AfterTokens:    typed.AfterTokens,
		ElidedMessages: typed.ElidedMessages,
		ElidedBytes:    typed.ElidedBytes,
		SummaryVersion: typed.SummaryVersion,
		Summarized:     typed.Summarized,
		Reason:         typed.Reason,
	}
}

// writeCacheUsageLine frames one provider-reported prompt-cache accounting
// record as a "cache_usage" NDJSON line. Extracted from
// jsonTurnEventCallback for the same reason as writeTokenUsageLine.
func writeCacheUsageLine(w io.Writer, typed events.CacheUsageEvent) {
	writeNDJSONEvent(w, ndjsonEvent{
		Type:     "cache_usage",
		Provider: typed.Provider,
		Model:    typed.Model,
		CacheUsage: &ndjsonCacheUsage{
			InputTokens:       typed.InputTokens,
			CachedInputTokens: typed.CachedInputTokens,
			CacheWriteTokens:  typed.CacheWriteTokens,
			HitPercent:        typed.HitPercent(),
		},
	})
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

// jsonTurnEventCallback routes one agent event onto the local NDJSON
// surface. The run-level subagent lines live in writeJSONSubagentLine, so
// this function stays a readable routing table rather than one long body.
func jsonTurnEventCallback(w io.Writer) func(event agent.Event) {
	return func(e agent.Event) {
		if writeJSONSubagentLine(w, e) {
			return
		}
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
				Input:                 EventPreview(e.Input, e.Detail),
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
				Output:       EventPreview(e.Output, e.Detail),
				Status:       toolEndStatus(e.Detail),
				OriginTaskID: e.Origin.TaskID,
				OriginAgent:  e.Origin.Agent,
				OriginDepth:  e.Origin.Depth,
			})
		case agent.EventCacheUsage:
			// The typed payload is required: a cache_usage event without it
			// carries no numbers worth a wire record.
			if e.CacheUsage == nil {
				return
			}
			writeCacheUsageLine(w, *e.CacheUsage)
		case agent.EventTokenUsage:
			// Same payload-required rule as cache_usage: without the typed
			// record there are no numbers worth a wire line.
			if e.TokenUsage == nil {
				return
			}
			writeTokenUsageLine(w, *e.TokenUsage)
		case agent.EventCompaction:
			// The typed payload is required, same rule as cache_usage.
			if e.Compaction == nil {
				return
			}
			writeCompactionLine(w, e.Detail, *e.Compaction)
		}
	}
}

// writeJSONSubagentLine writes the RUN-level lines - the ones that describe a
// subagent run itself rather than a nested tool call - and the turn reset.
// It reports whether it handled the event.
func writeJSONSubagentLine(w io.Writer, e agent.Event) bool {
	switch e.Kind {
	case agent.EventAssistantReset:
		// The answer streams as "chunk" lines, so a retry sends the whole
		// answer twice with nothing between the two. This line is that
		// something: a consumer drops the chunks it has already accumulated
		// for this turn and starts the answer again.
		writeNDJSONEvent(w, ndjsonEvent{
			Type:         "assistant_reset",
			Message:      e.Detail,
			OriginTaskID: e.Origin.TaskID,
			OriginAgent:  e.Origin.Agent,
			OriginDepth:  e.Origin.Depth,
		})
	case agent.EventHook:
		// A hook is a program the runtime runs on the operator's machine for
		// every matching call, and one of them can BLOCK the call. Without
		// this line a --json consumer saw the tool never run and was told
		// nothing about why - the single most important thing a hook has to
		// say. Every run produces a line, including a silent one: a mis-typed
		// matcher that selects nothing is indistinguishable from a working
		// hook until the silent runs are visible too.
		writeNDJSONEvent(w, ndjsonEvent{
			Type:       "hook",
			HookEvent:  e.Name,
			Program:    e.Program,
			Tool:       e.Tool,
			ToolCallID: e.ToolCallID,
			Message:    e.Detail,
			// Input is redacted at the producer (emitHookRuns), so this is the
			// same bounded text the operator's own TUI row shows.
			Input:  e.Input,
			Output: e.Output,
			Status: hookStatus(e.Denied),
		})
	case agent.EventSubagentBegin:
		// The run's opening signal. Without it a --json consumer first hears
		// of a subagent when it calls a tool, and a run that only thinks and
		// answers is never announced at all.
		writeNDJSONEvent(w, ndjsonEvent{
			Type:                  "subagent_begin",
			Name:                  e.Name,
			Input:                 e.Detail,
			OriginTaskID:          e.Origin.TaskID,
			OriginAgent:           e.Origin.Agent,
			OriginDepth:           e.Origin.Depth,
			OriginTaskDescription: e.Origin.TaskDescription,
		})
	case agent.EventSubagentDone:
		writeNDJSONEvent(w, ndjsonEvent{
			Type:         "subagent_done",
			OriginTaskID: e.Origin.TaskID,
			Status:       e.Status,
		})
	case agent.EventSubagentHeartbeat:
		// Origin is required for this event to mean anything (it retires
		// nothing on its own, just refreshes one subagent's progress note) -
		// a heartbeat with no origin (should not happen, see
		// OnEventForMultiStep) is dropped rather than sent as a meaningless
		// line.
		if e.Origin.TaskID != "" {
			writeNDJSONEvent(w, ndjsonEvent{
				Type:         "subagent_heartbeat",
				OriginTaskID: e.Origin.TaskID,
				Message:      e.Detail,
			})
		}
	default:
		return false
	}
	return true
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

// jsonTurnErrorMessage returns the redaction-safe description of a failed
// --json turn for the "error" event's message field. The classification lives
// in chat.TurnErrorMessage because this is not the only boundary that must not
// leak raw error text: internal/hub relays across processes and needs exactly
// the same answer. This wrapper stays so the NDJSON writer keeps naming its own
// concern, and so the two boundaries cannot drift apart.
func jsonTurnErrorMessage(err error) string {
	return chat.TurnErrorMessage(err)
}

// hookStatus names the one thing a consumer must not have to infer: whether
// this hook run stopped the tool call. "ok" for a hook that merely reported,
// and a distinct word for one that refused - not the tool vocabulary's
// "failed", because the hook did not fail. It did its job.
func hookStatus(denied bool) string {
	if denied {
		return "blocked"
	}
	return "ok"
}
