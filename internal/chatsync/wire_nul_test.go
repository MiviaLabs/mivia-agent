package chatsync

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/events"
)

// Postgres cannot store U+0000 in a json or jsonb column - it rejects the
// value with "unsupported Unicode escape sequence". The receiving API inserts
// a whole batch as ONE multi-row statement inside a transaction, so one NUL
// rejects up to a hundred events, not one.
//
// A tool that reads a binary file produces one without trying: the report that
// led here was a build cache's .sst file reaching a tool output preview.

// toolIOOpts is the prose options plus tool text, which an operator turns on
// to see what a tool actually read.
func toolIOOpts() ProjectorOptions {
	opts := proseOpts()
	opts.IncludeToolIO = true
	return opts
}

// wireJSON marshals every projected payload the way the outbox does.
func wireJSON(t *testing.T, evs []WireEvent) string {
	t.Helper()
	var sb strings.Builder
	for _, we := range evs {
		data, err := json.Marshal(we.Payload)
		if err != nil {
			t.Fatalf("marshal %s: %v", we.Type, err)
		}
		sb.Write(data)
	}
	return sb.String()
}

// TestNoWireFieldCarriesANulByte drives every field-bearing kind rather than
// the one that was reported, because the store rejects a NUL wherever it
// appears and any of these can carry text a tool read off disk.
func TestNoWireFieldCarriesANulByte(t *testing.T) {
	dirty := "before" + string(rune(0)) + "after"

	// Tool text travels only when the operator enabled it, and that is the
	// path the reported failure came down: a search result carrying a build
	// cache's .sst bytes. A test with the option off proves nothing about it.
	cases := []struct {
		name string
		opts ProjectorOptions
		ev   events.Event
	}{
		{"tool output", toolIOOpts(), events.Event{
			Kind: events.KindToolEnd, SessionID: "sess-1", TurnID: "turn:1",
			ToolCallID: "c1", Name: "read_file", Output: dirty,
		}},
		// The status is derived from the detail, and used to be built from the
		// RAW event while the same string was sanitised into `detail` two
		// lines away - the one free-text field that skipped the choke point.
		{"tool end detail", toolIOOpts(), events.Event{
			Kind: events.KindToolEnd, SessionID: "sess-1", TurnID: "turn:1",
			ToolCallID: "c1", Name: "read_file", Detail: dirty,
		}},
		{"subagent tool end detail", toolIOOpts(), events.Event{
			Kind: events.KindSubagentEnd, SessionID: "sess-1", TurnID: "turn:1",
			ToolCallID: "c1", Name: "read_file", Detail: dirty,
		}.WithAgentAttribution("task-1", "builder", 1)},
		{"tool input", toolIOOpts(), events.Event{
			Kind: events.KindToolStart, SessionID: "sess-1", TurnID: "turn:1",
			ToolCallID: "c1", Name: "read_file", Input: dirty,
		}},
		{"assistant delta", proseOpts(), events.Event{
			Kind: events.KindAssistant, SessionID: "sess-1", TurnID: "turn:1",
			Content: dirty, Detail: "delta",
		}},
		{"assistant aggregate", proseOpts(), events.Event{
			Kind: events.KindAssistant, SessionID: "sess-1", TurnID: "turn:1",
			Content: dirty,
		}},
		{"thinking", proseOpts(), events.Event{
			Kind: events.KindThinking, SessionID: "sess-1", TurnID: "turn:1",
			Content: dirty,
		}},
		{"error message", proseOpts(), events.Event{
			Kind: events.KindError, SessionID: "sess-1", TurnID: "turn:1",
			Content: dirty,
		}},
		{"subagent tool output", toolIOOpts(), events.Event{
			Kind: events.KindSubagentEnd, SessionID: "sess-1", TurnID: "turn:1",
			ToolCallID: "c1", Name: "read_file", Output: dirty,
		}.WithAgentAttribution("task-1", "builder", 1)},
		{"subagent prose", proseOpts(), events.Event{
			Kind: events.KindAssistant, SessionID: "sess-1", TurnID: "turn:1",
			Content: dirty, Detail: "delta",
		}.WithAgentAttribution("task-1", "builder", 1)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := NewProjector("sess-1", 0, tc.opts)
			p.Project(events.Event{
				Kind: events.KindTurnStart, SessionID: "sess-1", TurnID: "turn:1",
				Detail: "the prompt",
			})
			got := wireJSON(t, p.Project(tc.ev))
			if got == "" {
				t.Fatalf("%s produced no wire payload; this case proves nothing", tc.name)
			}
			// Both spellings: the raw rune, and the escape json.Marshal emits
			// for it - the escape is what Postgres actually rejects.
			if strings.ContainsRune(got, 0) || strings.Contains(got, `\u0000`) {
				t.Errorf("a NUL reached the wire in %s; Postgres rejects the whole "+
					"batch it travels in, losing every other event with it: %s",
					tc.name, got)
			}
		})
	}
}

// TestSanitizingDoesNotDisturbOrdinaryText holds the removal to NUL alone. The
// rest of the payload - including other control characters JSON escapes
// legitimately - has to survive untouched.
func TestSanitizingDoesNotDisturbOrdinaryText(t *testing.T) {
	const text = "line one\nline two\ttabbed \"quoted\" \\ backslash Uber 100%"
	if got := sanitizeWireText(text); got != text {
		t.Errorf("sanitizeWireText altered ordinary text:\n got %q\nwant %q", got, text)
	}
	if got := sanitizeWireText("a" + string(rune(0)) + "b"); got != "ab" {
		t.Errorf("sanitizeWireText did not remove the NUL: %q", got)
	}
}

// TestTruncationCountsDescribeWhatWasSent: the sanitize happens before the
// measure, so a field's reported total is the size of what actually travelled.
func TestTruncationCountsDescribeWhatWasSent(t *testing.T) {
	env := Envelope{}
	got := applyTruncation(&env, "text", strings.Repeat("a", 10)+string(rune(0)), BudgetShortField)
	if strings.ContainsRune(got, 0) {
		t.Fatalf("applyTruncation returned a NUL: %q", got)
	}
	if env.Trunc != nil {
		t.Errorf("a field well inside its budget was recorded as truncated: %+v", env.Trunc)
	}
}
