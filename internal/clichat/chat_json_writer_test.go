package clichat

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/events"
)

// TestNDJSONChunkWriterReassemblesSplitMultiByteRune pins the correctness
// requirement this writer exists for: a multi-byte UTF-8 character split
// across two separate Write() calls - exactly what agent.Loop's FinalWriter
// does with streamed content deltas - must reassemble into one valid chunk
// event with the original text intact, not two mangled halves.
func TestNDJSONChunkWriterReassemblesSplitMultiByteRune(t *testing.T) {
	// "café 🎉" - a 2-byte rune (é, U+00E9 = 0xC3 0xA9) and a 4-byte rune
	// (🎉, U+1F389) both straddle Write boundaries below.
	const want = "café 🎉"
	full := []byte(want)

	// Split mid-é (after the 0xC3 lead byte) and mid-🎉 (after its first
	// byte) by writing one byte at a time across the whole string - the
	// worst case for a naive per-Write json.Marshal approach.
	var buf bytes.Buffer
	w := newNDJSONChunkWriter(&buf)
	for i := range full {
		if _, err := w.Write(full[i : i+1]); err != nil {
			t.Fatalf("Write byte %d: %v", i, err)
		}
	}
	w.Flush()

	lines := splitNonEmptyLines(buf.String())
	if len(lines) == 0 {
		t.Fatal("no chunk lines emitted")
	}
	var reconstructed strings.Builder
	for _, line := range lines {
		var ev ndjsonEvent
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("line %q is not valid JSON: %v", line, err)
		}
		if ev.Type != "chunk" {
			t.Fatalf("line %q: type = %q, want %q", line, ev.Type, "chunk")
		}
		if strings.ContainsRune(ev.Text, '�') {
			t.Fatalf("line %q: chunk text contains U+FFFD (mangled UTF-8): %q", line, ev.Text)
		}
		reconstructed.WriteString(ev.Text)
	}
	if got := reconstructed.String(); got != want {
		t.Fatalf("reconstructed text = %q, want %q", got, want)
	}
}

// TestNDJSONChunkWriterHoldsBackIncompleteRuneUntilComplete verifies the
// buffering behavior directly: writing only the lead byte of a multi-byte
// rune must not emit anything until the rest of the rune arrives.
func TestNDJSONChunkWriterHoldsBackIncompleteRuneUntilComplete(t *testing.T) {
	full := []byte("é") // 0xC3 0xA9
	var buf bytes.Buffer
	w := newNDJSONChunkWriter(&buf)

	if _, err := w.Write(full[:1]); err != nil {
		t.Fatalf("Write lead byte: %v", err)
	}
	if buf.Len() != 0 {
		t.Fatalf("emitted output after only a lead byte: %q", buf.String())
	}

	if _, err := w.Write(full[1:]); err != nil {
		t.Fatalf("Write trailing byte: %v", err)
	}
	lines := splitNonEmptyLines(buf.String())
	if len(lines) != 1 {
		t.Fatalf("got %d lines, want 1: %v", len(lines), lines)
	}
	var ev ndjsonEvent
	if err := json.Unmarshal([]byte(lines[0]), &ev); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if ev.Text != "é" {
		t.Fatalf("text = %q, want %q", ev.Text, "é")
	}
}

// TestNDJSONChunkWriterDiscardDropsBufferedBytes pins the cancellation
// contract: bytes held back as a possibly-incomplete trailing rune must never
// surface as a phantom chunk once the turn that produced them is discarded.
func TestNDJSONChunkWriterDiscardDropsBufferedBytes(t *testing.T) {
	full := []byte("é")
	var buf bytes.Buffer
	w := newNDJSONChunkWriter(&buf)
	if _, err := w.Write(full[:1]); err != nil {
		t.Fatalf("Write lead byte: %v", err)
	}
	w.Discard()
	w.Flush()
	if buf.Len() != 0 {
		t.Fatalf("Discard did not prevent a later Flush from emitting buffered bytes: %q", buf.String())
	}
}

// TestNDJSONChunkWriterPlainASCIIEmitsImmediately guards the common-case
// fast path: ordinary ASCII text needs no buffering delay.
func TestNDJSONChunkWriterPlainASCIIEmitsImmediately(t *testing.T) {
	var buf bytes.Buffer
	w := newNDJSONChunkWriter(&buf)
	if _, err := w.Write([]byte("hello")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	lines := splitNonEmptyLines(buf.String())
	if len(lines) != 1 {
		t.Fatalf("got %d lines, want 1", len(lines))
	}
	var ev ndjsonEvent
	if err := json.Unmarshal([]byte(lines[0]), &ev); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if ev.Text != "hello" {
		t.Fatalf("text = %q, want %q", ev.Text, "hello")
	}
}

// TestJSONTurnEventCallbackEmitsThinkingAndToolLifecycle pins the --json
// wire contract for the three new event types: thinking deltas and a
// tool_start/tool_end pair each become their own NDJSON line with the
// expected type tag and fields.
func TestJSONTurnEventCallbackEmitsThinkingAndToolLifecycle(t *testing.T) {
	var buf bytes.Buffer
	onEvent := jsonTurnEventCallback(&buf)

	onEvent(agent.Event{Kind: agent.EventThinking, Content: "considering the options"})
	onEvent(agent.Event{Kind: agent.EventToolStart, ToolCallID: "call_1", Name: "read_file", Input: "path=foo.go"})
	onEvent(agent.Event{Kind: agent.EventToolEnd, ToolCallID: "call_1", Name: "read_file", Output: "12 lines"})

	lines := splitNonEmptyLines(buf.String())
	if len(lines) != 3 {
		t.Fatalf("got %d lines, want 3: %v", len(lines), lines)
	}

	var thinking ndjsonEvent
	if err := json.Unmarshal([]byte(lines[0]), &thinking); err != nil {
		t.Fatalf("line 0 invalid JSON: %v", err)
	}
	if thinking.Type != "thinking" || thinking.Text != "considering the options" {
		t.Fatalf("thinking event = %+v, want type=thinking text=%q", thinking, "considering the options")
	}

	var toolStart ndjsonEvent
	if err := json.Unmarshal([]byte(lines[1]), &toolStart); err != nil {
		t.Fatalf("line 1 invalid JSON: %v", err)
	}
	if toolStart.Type != "tool_start" || toolStart.ToolCallID != "call_1" || toolStart.Name != "read_file" || toolStart.Input != "path=foo.go" {
		t.Fatalf("tool_start event = %+v", toolStart)
	}

	var toolEnd ndjsonEvent
	if err := json.Unmarshal([]byte(lines[2]), &toolEnd); err != nil {
		t.Fatalf("line 2 invalid JSON: %v", err)
	}
	if toolEnd.Type != "tool_end" || toolEnd.ToolCallID != "call_1" || toolEnd.Name != "read_file" || toolEnd.Output != "12 lines" {
		t.Fatalf("tool_end event = %+v", toolEnd)
	}
}

// TestJSONTurnEventCallbackIgnoresOtherKinds ensures event kinds with no
// --json wire representation (e.g. steps, heartbeats) are silently dropped
// rather than emitting a malformed or unexpected line - the --json protocol
// only ever grows explicitly documented types.
func TestJSONTurnEventCallbackIgnoresOtherKinds(t *testing.T) {
	var buf bytes.Buffer
	onEvent := jsonTurnEventCallback(&buf)
	onEvent(agent.Event{Kind: agent.EventStep, Detail: "step 1"})
	onEvent(agent.Event{Kind: agent.EventHeartbeat, Detail: "tick"})
	if buf.Len() != 0 {
		t.Fatalf("expected no output for unhandled event kinds, got: %q", buf.String())
	}
}

// TestJSONTurnEventCallbackReportsToolFailureStatus pins the failure signal
// on the wire. Before "status" existed, a failed tool call was
// indistinguishable from a successful one to a --json consumer: the only
// failure marker was Event.Detail, which eventPreview discards whenever the
// tool produced any output at all (the normal case).
// TestJSONTurnEventCallbackEmitsTokenUsage pins the wire contract for
// provider-reported token accounting: EventTokenUsage must reach the --json
// stream as a "token_usage" line carrying the typed payload's real
// input/output counts plus the estimate/drift fields. Assertions run on the
// raw JSON map, not the ndjsonEvent struct, so this test documents what an
// external consumer actually parses. Before this case existed the numbers
// were computed one layer down and silently dropped at this boundary, so a
// GUI wrapper had no way to show real context usage.
func TestJSONTurnEventCallbackEmitsTokenUsage(t *testing.T) {
	typed, err := events.NewTokenUsageEvent("openai", "gpt-x", 1200, 80, 900, 1.33)
	if err != nil {
		t.Fatalf("NewTokenUsageEvent: %v", err)
	}
	var buf bytes.Buffer
	onEvent := jsonTurnEventCallback(&buf)
	onEvent(agent.Event{Kind: agent.EventTokenUsage, TokenUsage: &typed})

	lines := splitNonEmptyLines(buf.String())
	if len(lines) != 1 {
		t.Fatalf("got %d lines, want 1: %v", len(lines), lines)
	}
	var raw map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &raw); err != nil {
		t.Fatalf("line %q is not valid JSON: %v", lines[0], err)
	}
	if raw["type"] != "token_usage" {
		t.Fatalf("type = %v, want token_usage", raw["type"])
	}
	if raw["provider"] != "openai" || raw["model"] != "gpt-x" {
		t.Fatalf("provider/model = %v/%v, want openai/gpt-x", raw["provider"], raw["model"])
	}
	nested, ok := raw["token_usage"].(map[string]any)
	if !ok {
		t.Fatalf("no nested token_usage record on line %q", lines[0])
	}
	for _, field := range []string{"input_tokens", "output_tokens", "estimated_tokens", "calibration_ratio"} {
		if _, ok := nested[field]; !ok {
			t.Fatalf("token_usage record missing %q on line %q", field, lines[0])
		}
	}
	if nested["input_tokens"] != float64(1200) || nested["output_tokens"] != float64(80) {
		t.Fatalf("token_usage record = %v, want input 1200 / output 80", nested)
	}
}

// TestJSONTurnEventCallbackDropsTokenUsageWithoutPayload mirrors
// cache_usage's rule: the typed payload is required, because an event
// without it carries no numbers worth a wire record.
func TestJSONTurnEventCallbackDropsTokenUsageWithoutPayload(t *testing.T) {
	var buf bytes.Buffer
	onEvent := jsonTurnEventCallback(&buf)
	onEvent(agent.Event{Kind: agent.EventTokenUsage, TokenUsage: nil})
	if buf.Len() != 0 {
		t.Fatalf("expected no output for a token_usage event with no payload, got: %q", buf.String())
	}
}

func TestJSONTurnEventCallbackReportsToolFailureStatus(t *testing.T) {
	cases := []struct {
		name   string
		detail string
		want   string
	}{
		{name: "completed", detail: "completed", want: "ok"},
		{name: "failed", detail: "failed", want: "failed"},
		{name: "failed truncated", detail: "failed (truncated)", want: "failed"},
		{name: "failed duplicate", detail: "failed (duplicate)", want: "failed"},
		// Truncation describes the preview, not the outcome - a truncated
		// but successful call must not read as an error.
		{name: "completed truncated", detail: "completed (truncated)", want: "ok"},
		// No signal at all is ok, not a third "unknown" state.
		{name: "empty detail", detail: "", want: "ok"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			onEvent := jsonTurnEventCallback(&buf)

			onEvent(agent.Event{
				Kind: agent.EventToolEnd, ToolCallID: "call_1",
				Name: "run_command", Output: "exit status 1", Detail: tc.detail,
			})

			lines := splitNonEmptyLines(buf.String())
			if len(lines) != 1 {
				t.Fatalf("got %d lines, want 1: %v", len(lines), lines)
			}
			var toolEnd ndjsonEvent
			if err := json.Unmarshal([]byte(lines[0]), &toolEnd); err != nil {
				t.Fatalf("invalid JSON: %v", err)
			}
			if toolEnd.Status != tc.want {
				t.Fatalf("status = %q, want %q (detail %q)", toolEnd.Status, tc.want, tc.detail)
			}
			// The output preview must stay untouched by the new field.
			if toolEnd.Output != "exit status 1" {
				t.Fatalf("output = %q, want it unchanged", toolEnd.Output)
			}
		})
	}
}

// TestJSONTurnEventCallbackAttributesSubagentToolCalls pins the wire
// contract for a delegated subagent's own nested tool calls: they reuse the
// same tool_start/tool_end types as a root-loop tool call (so an older
// consumer still renders them), but carry origin_task_id/origin_agent/
// origin_depth so a --json consumer can group them under their own run
// instead of flattening them into the parent turn - see
// mivia-agent-desktop's ToolCallList, which does exactly that.
func TestJSONTurnEventCallbackAttributesSubagentToolCalls(t *testing.T) {
	var buf bytes.Buffer
	onEvent := jsonTurnEventCallback(&buf)
	origin := agent.EventOrigin{TaskID: "task-1", Agent: "researcher", Depth: 1}

	onEvent(agent.Event{Kind: agent.EventSubagentStart, ToolCallID: "call_1", Name: "read_file", Input: "path=foo.go", Origin: origin})
	onEvent(agent.Event{Kind: agent.EventSubagentEnd, ToolCallID: "call_1", Name: "read_file", Output: "12 lines", Origin: origin})
	onEvent(agent.Event{Kind: agent.EventSubagentDone, Name: "researcher", Origin: origin})

	lines := splitNonEmptyLines(buf.String())
	if len(lines) != 3 {
		t.Fatalf("got %d lines, want 3: %v", len(lines), lines)
	}

	var toolStart ndjsonEvent
	if err := json.Unmarshal([]byte(lines[0]), &toolStart); err != nil {
		t.Fatalf("line 0 invalid JSON: %v", err)
	}
	if toolStart.Type != "tool_start" || toolStart.OriginTaskID != "task-1" || toolStart.OriginAgent != "researcher" || toolStart.OriginDepth != 1 {
		t.Fatalf("tool_start event = %+v, want origin task-1/researcher/1", toolStart)
	}

	var toolEnd ndjsonEvent
	if err := json.Unmarshal([]byte(lines[1]), &toolEnd); err != nil {
		t.Fatalf("line 1 invalid JSON: %v", err)
	}
	if toolEnd.Type != "tool_end" || toolEnd.OriginTaskID != "task-1" || toolEnd.OriginAgent != "researcher" || toolEnd.OriginDepth != 1 {
		t.Fatalf("tool_end event = %+v, want origin task-1/researcher/1", toolEnd)
	}

	var done ndjsonEvent
	if err := json.Unmarshal([]byte(lines[2]), &done); err != nil {
		t.Fatalf("line 2 invalid JSON: %v", err)
	}
	if done.Type != "subagent_done" || done.OriginTaskID != "task-1" {
		t.Fatalf("subagent_done event = %+v, want origin_task_id=task-1", done)
	}
}

// TestJSONTurnEventCallbackCarriesStatusOnSubagentDone pins the additive
// status field on the "subagent_done" NDJSON line: the run's terminal
// status (agent.Event.Status) travels on the same line. Empty Status keeps
// today's wire shape (omitempty drops the field), so an older consumer
// reading the line sees no change.
func TestJSONTurnEventCallbackCarriesStatusOnSubagentDone(t *testing.T) {
	var buf bytes.Buffer
	onEvent := jsonTurnEventCallback(&buf)
	origin := agent.EventOrigin{TaskID: "task-s", Agent: "auditor", Depth: 1}

	onEvent(agent.Event{Kind: agent.EventSubagentDone, Name: "auditor", Status: "timed_out", Origin: origin})
	onEvent(agent.Event{Kind: agent.EventSubagentDone, Name: "auditor", Origin: origin})

	lines := splitNonEmptyLines(buf.String())
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want 2: %v", len(lines), lines)
	}

	var classified ndjsonEvent
	if err := json.Unmarshal([]byte(lines[0]), &classified); err != nil {
		t.Fatalf("line 0 invalid JSON: %v", err)
	}
	if classified.Type != "subagent_done" || classified.Status != "timed_out" {
		t.Fatalf("subagent_done event = %+v, want status=timed_out", classified)
	}

	var legacy ndjsonEvent
	if err := json.Unmarshal([]byte(lines[1]), &legacy); err != nil {
		t.Fatalf("line 1 invalid JSON: %v", err)
	}
	if legacy.Type != "subagent_done" {
		t.Fatalf("subagent_done event = %+v", legacy)
	}
	if strings.Contains(lines[1], `"status"`) {
		t.Fatalf("empty Status must keep today's wire shape (no status key), got: %s", lines[1])
	}
}

// TestJSONTurnEventCallbackCarriesTaskDescriptionOnSubagentStartOnly pins the
// wire contract for agent.EventOrigin.TaskDescription: present on
// "tool_start" for a subagent's own nested calls (so a consumer can show
// what the subagent was asked to do without separately correlating the
// initiating delegate/dispatch_tasks/spawn_agent call), but never on
// "tool_end" - the task doesn't change over the run, so repeating it there
// would be pure duplication.
func TestJSONTurnEventCallbackCarriesTaskDescriptionOnSubagentStartOnly(t *testing.T) {
	var buf bytes.Buffer
	onEvent := jsonTurnEventCallback(&buf)
	origin := agent.EventOrigin{
		TaskID:          "task-1",
		Agent:           "researcher",
		Depth:           1,
		TaskDescription: "investigate the auth module",
	}

	onEvent(agent.Event{Kind: agent.EventSubagentStart, ToolCallID: "call_1", Name: "read_file", Input: "path=foo.go", Origin: origin})
	onEvent(agent.Event{Kind: agent.EventSubagentEnd, ToolCallID: "call_1", Name: "read_file", Output: "12 lines", Origin: origin})

	lines := splitNonEmptyLines(buf.String())
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want 2: %v", len(lines), lines)
	}

	var toolStart ndjsonEvent
	if err := json.Unmarshal([]byte(lines[0]), &toolStart); err != nil {
		t.Fatalf("line 0 invalid JSON: %v", err)
	}
	if toolStart.OriginTaskDescription != "investigate the auth module" {
		t.Fatalf("tool_start OriginTaskDescription = %q, want %q", toolStart.OriginTaskDescription, "investigate the auth module")
	}

	var toolEnd ndjsonEvent
	if err := json.Unmarshal([]byte(lines[1]), &toolEnd); err != nil {
		t.Fatalf("line 1 invalid JSON: %v", err)
	}
	if toolEnd.OriginTaskDescription != "" {
		t.Fatalf("tool_end OriginTaskDescription = %q, want empty (task_end never carries it)", toolEnd.OriginTaskDescription)
	}
}

// TestJSONTurnEventCallbackEmitsSubagentHeartbeat ensures a heartbeat with an
// origin becomes a "subagent_heartbeat" line carrying the origin task id and
// the detail text as message.
func TestJSONTurnEventCallbackEmitsSubagentHeartbeat(t *testing.T) {
	var buf bytes.Buffer
	onEvent := jsonTurnEventCallback(&buf)
	origin := agent.EventOrigin{TaskID: "task-1", Agent: "researcher", Depth: 1}

	onEvent(agent.Event{Kind: agent.EventSubagentHeartbeat, Detail: "reading files", Origin: origin})

	lines := splitNonEmptyLines(buf.String())
	if len(lines) != 1 {
		t.Fatalf("got %d lines, want 1: %v", len(lines), lines)
	}

	var heartbeat ndjsonEvent
	if err := json.Unmarshal([]byte(lines[0]), &heartbeat); err != nil {
		t.Fatalf("line 0 invalid JSON: %v", err)
	}
	if heartbeat.Type != "subagent_heartbeat" || heartbeat.OriginTaskID != "task-1" || heartbeat.Message != "reading files" {
		t.Fatalf("subagent_heartbeat event = %+v, want origin_task_id=task-1 message=%q", heartbeat, "reading files")
	}
}

// TestJSONTurnEventCallbackDropsHeartbeatWithNoOrigin ensures a root-loop
// heartbeat (agent.EventOrigin's zero value) - which should not occur per
// OnEventForMultiStep, but is defensively guarded - is dropped rather than
// emitted as a meaningless line with no origin_task_id.
func TestJSONTurnEventCallbackDropsHeartbeatWithNoOrigin(t *testing.T) {
	var buf bytes.Buffer
	onEvent := jsonTurnEventCallback(&buf)

	onEvent(agent.Event{Kind: agent.EventSubagentHeartbeat, Detail: "thinking"})

	if buf.Len() != 0 {
		t.Fatalf("expected no output, got %q", buf.String())
	}
}

// TestJSONTurnEventCallbackOmitsOriginForRootLoopToolCalls ensures a
// root-loop tool call (agent.EventOrigin's zero value) does not leak empty
// origin_task_id/origin_agent fields onto the wire - the common case stays
// byte-identical to before origin attribution was added.
func TestJSONTurnEventCallbackOmitsOriginForRootLoopToolCalls(t *testing.T) {
	var buf bytes.Buffer
	onEvent := jsonTurnEventCallback(&buf)
	onEvent(agent.Event{Kind: agent.EventToolStart, ToolCallID: "call_1", Name: "read_file", Input: "path=foo.go"})

	line := strings.TrimSpace(buf.String())
	if strings.Contains(line, "origin_task_id") || strings.Contains(line, "origin_agent") || strings.Contains(line, "origin_depth") {
		t.Fatalf("root-loop tool_start leaked origin fields: %s", line)
	}
}

// TestJSONTurnEventCallbackEmitsCompaction pins the wire contract for
// EventCompaction: it must reach --json consumers as a "compaction" line
// carrying the human-readable detail as message plus a nested record with
// the typed payload's numbers, following the same nested-struct pattern as
// cache_usage/token_usage so a no-elision compaction's legitimate zero
// values don't vanish.
func TestJSONTurnEventCallbackEmitsCompaction(t *testing.T) {
	start := contextstate.SourceID{SessionID: "session-1", Sequence: 1}
	end := contextstate.SourceID{SessionID: "session-1", Sequence: 5}
	rng, err := contextstate.NewSourceRange(start, end)
	if err != nil {
		t.Fatalf("NewSourceRange: %v", err)
	}
	typed, err := events.NewCompactionEvent(events.CompactionEventParams{
		Trigger:        "threshold",
		BeforeTokens:   10000,
		AfterTokens:    3000,
		ElidedMessages: 5,
		ElidedBytes:    4200,
		SourceRange:    rng,
		SummaryVersion: 1,
	})
	if err != nil {
		t.Fatalf("NewCompactionEvent: %v", err)
	}

	var buf bytes.Buffer
	onEvent := jsonTurnEventCallback(&buf)
	onEvent(agent.Event{
		Kind:       agent.EventCompaction,
		Detail:     "context compacted: 10000 -> 3000 tokens (5 tool results elided, 4200 bytes)",
		Compaction: &typed,
	})

	lines := splitNonEmptyLines(buf.String())
	if len(lines) != 1 {
		t.Fatalf("got %d lines, want 1: %v", len(lines), lines)
	}
	var got ndjsonEvent
	if err := json.Unmarshal([]byte(lines[0]), &got); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if got.Type != "compaction" {
		t.Fatalf("type = %q, want %q", got.Type, "compaction")
	}
	if got.Message != "context compacted: 10000 -> 3000 tokens (5 tool results elided, 4200 bytes)" {
		t.Fatalf("message = %q", got.Message)
	}
	if got.Compaction == nil {
		t.Fatal("compaction record missing")
	}
	want := ndjsonCompaction{
		Trigger:        "threshold",
		BeforeTokens:   10000,
		AfterTokens:    3000,
		ElidedMessages: 5,
		ElidedBytes:    4200,
		SummaryVersion: 1,
	}
	if *got.Compaction != want {
		t.Fatalf("compaction record = %+v, want %+v", *got.Compaction, want)
	}

	// The typed payload is required - a bare event emits nothing.
	buf.Reset()
	onEvent = jsonTurnEventCallback(&buf)
	onEvent(agent.Event{Kind: agent.EventCompaction, Detail: "context compacted"})
	if buf.Len() != 0 {
		t.Fatalf("expected no output without typed payload, got %q", buf.String())
	}
}

func splitNonEmptyLines(s string) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}

// TestJSONTurnEventCallbackEmitsCacheUsage pins the cache_usage wire record:
// the numeric fields ride a nested record so an all-miss step's legitimate
// zero values survive serialization, and HitPercent repeats the same guarded
// percent the TUI status line shows (0 when InputTokens is 0).
func TestJSONTurnEventCallbackEmitsCacheUsage(t *testing.T) {
	typed, err := events.NewCacheUsageEvent("deepseek", "deepseek-v4-pro", "implicit", 100, 80, 5)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	onEvent := jsonTurnEventCallback(&buf)
	onEvent(agent.Event{Kind: agent.EventCacheUsage, Detail: "prompt cache: 80/100 tokens cached (80%)", CacheUsage: &typed})

	lines := splitNonEmptyLines(buf.String())
	if len(lines) != 1 {
		t.Fatalf("expected 1 line, got %d: %q", len(lines), buf.String())
	}
	var got ndjsonEvent
	if err := json.Unmarshal([]byte(lines[0]), &got); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if got.Type != "cache_usage" || got.Provider != "deepseek" || got.Model != "deepseek-v4-pro" {
		t.Fatalf("cache_usage event = %+v", got)
	}
	if got.CacheUsage == nil {
		t.Fatal("cache_usage record missing")
	}
	if got.CacheUsage.InputTokens != 100 || got.CacheUsage.CachedInputTokens != 80 || got.CacheUsage.CacheWriteTokens != 5 || got.CacheUsage.HitPercent != 80 {
		t.Fatalf("cache_usage record = %+v", got.CacheUsage)
	}

	// Zero input tokens must serialize as a 0 percent record, not vanish.
	zero, err := events.NewCacheUsageEvent("deepseek", "deepseek-v4-pro", "implicit", 0, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	buf.Reset()
	onEvent = jsonTurnEventCallback(&buf)
	onEvent(agent.Event{Kind: agent.EventCacheUsage, CacheUsage: &zero})
	var gotZero ndjsonEvent
	if err := json.Unmarshal([]byte(splitNonEmptyLines(buf.String())[0]), &gotZero); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if gotZero.CacheUsage == nil || gotZero.CacheUsage.HitPercent != 0 {
		t.Fatalf("zero-input record = %+v", gotZero.CacheUsage)
	}

	// The typed payload is required - a bare event emits nothing.
	buf.Reset()
	onEvent = jsonTurnEventCallback(&buf)
	onEvent(agent.Event{Kind: agent.EventCacheUsage, Detail: "prompt cache: 0/0 tokens cached (0%)"})
	if buf.Len() != 0 {
		t.Fatalf("expected no output without typed payload, got %q", buf.String())
	}
}
