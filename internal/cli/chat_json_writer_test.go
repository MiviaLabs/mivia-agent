package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
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

func splitNonEmptyLines(s string) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}
