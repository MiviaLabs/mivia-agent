package chatsync

import (
	"strings"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/events"
)

// The wire promised (docs/product/chat-sync-wire.md, "Truncation Budgets")
// that a tool result is cut at 16 KiB with the cut recorded in
// `trunc.fields`. It never was: the projector read `Event.Output`, which is
// the operator PREVIEW the agent loop had already cut to 512 bytes with no
// marker (internal/agent/loop_tool_preview.go). Every `read_file` reached the
// web as 512 bytes reporting `output_bytes: 512`, so a viewer could not tell
// a 512-byte file from a 200 KB one. The projector now reads the unbounded
// body the emitter carries alongside the preview and applies its own budget.
func TestToolEndShipsTheBodyNotThePreview(t *testing.T) {
	for _, tc := range []struct {
		name string
		kind events.Kind
		task string
		want string
	}{
		{name: "root", kind: events.KindToolEnd, want: TypeToolEnded},
		{name: "subagent", kind: events.KindSubagentEnd, task: "task-1", want: TypeSubagentToolEnded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := NewProjector("s", 0, ProjectorOptions{IncludeToolIO: true})
			body := strings.Repeat("x", 200*1024)
			ev := events.Event{
				Kind:       tc.kind,
				SessionID:  "s",
				TurnID:     "turn:1",
				Timestamp:  time.Now(),
				ToolCallID: "call_1",
				Name:       "read_file",
				Output:     body[:512],
				OutputBody: body,
				AgentTask:  tc.task,
			}
			wes := p.Project(ev)
			if len(wes) != 1 || wes[0].Type != tc.want {
				t.Fatalf("got %d wire events (%v), want one %s", len(wes), wes, tc.want)
			}
			var out string
			var outBytes int
			var env Envelope
			switch payload := wes[0].Payload.(type) {
			case *ToolEndedPayload:
				out, outBytes, env = payload.Output, payload.OutputBytes, payload.Envelope
			case *SubagentToolEndedPayload:
				out, outBytes, env = payload.Output, payload.OutputBytes, payload.Envelope
			default:
				t.Fatalf("payload type %T", wes[0].Payload)
			}
			if outBytes != len(body) {
				t.Fatalf("output_bytes = %d, want the body's %d, not the preview's", outBytes, len(body))
			}
			if len(out) != BudgetToolOutput {
				t.Fatalf("output shipped %d bytes, want the %d-byte wire budget", len(out), BudgetToolOutput)
			}
			if env.Trunc == nil || env.Trunc.Fields["output"].Total != len(body) || env.Trunc.Fields["output"].Kept != BudgetToolOutput {
				t.Fatalf("trunc = %+v, want output kept %d of %d", env.Trunc, BudgetToolOutput, len(body))
			}
		})
	}
}

// The same for the call's arguments: `search_replace`'s old/new strings were
// cut at 256 bytes before the projector saw them.
func TestToolStartShipsTheInputBodyNotThePreview(t *testing.T) {
	for _, tc := range []struct {
		name string
		kind events.Kind
		task string
	}{
		{name: "root", kind: events.KindToolStart},
		{name: "subagent", kind: events.KindSubagentStart, task: "task-1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := NewProjector("s", 0, ProjectorOptions{IncludeToolIO: true})
			body := `{"path":"a.go","old_string":"` + strings.Repeat("o", 3000) + `","new_string":"n"}`
			ev := events.Event{
				Kind:       tc.kind,
				SessionID:  "s",
				TurnID:     "turn:1",
				Timestamp:  time.Now(),
				ToolCallID: "call_1",
				Name:       "search_replace",
				Input:      body[:256],
				InputBody:  body,
				AgentTask:  tc.task,
			}
			wes := p.Project(ev)
			if len(wes) != 1 {
				t.Fatalf("got %d wire events, want 1", len(wes))
			}
			var in string
			var inBytes int
			switch payload := wes[0].Payload.(type) {
			case *ToolStartedPayload:
				in, inBytes = payload.Input, payload.InputBytes
			case *SubagentToolStartedPayload:
				in, inBytes = payload.Input, payload.InputBytes
			default:
				t.Fatalf("payload type %T", wes[0].Payload)
			}
			if in != body || inBytes != len(body) {
				t.Fatalf("input shipped %d bytes (input_bytes %d), want the whole %d-byte body", len(in), inBytes, len(body))
			}
		})
	}
}

// An emitter that predates the body fields still works: the preview is what
// there is, and its length is what is reported. Nothing regresses to empty.
func TestToolEndFallsBackToThePreviewWhenNoBodyWasCarried(t *testing.T) {
	p := NewProjector("s", 0, ProjectorOptions{IncludeToolIO: true})
	ev := events.Event{
		Kind: events.KindToolEnd, SessionID: "s", TurnID: "turn:1", Timestamp: time.Now(),
		ToolCallID: "call_1", Name: "grep", Output: "a.go:1:x",
	}
	wes := p.Project(ev)
	payload := wes[0].Payload.(*ToolEndedPayload)
	if payload.Output != "a.go:1:x" || payload.OutputBytes != len("a.go:1:x") {
		t.Fatalf("payload = %+v, want the preview and its length", payload)
	}
}

// The privacy gate still withholds the body: a wider field must not become a
// wider leak.
func TestToolBodyRespectsTheIncludeToolIOGate(t *testing.T) {
	p := NewProjector("s", 0, ProjectorOptions{IncludeToolIO: false})
	ev := events.Event{
		Kind: events.KindToolEnd, SessionID: "s", TurnID: "turn:1", Timestamp: time.Now(),
		ToolCallID: "call_1", Name: "read_file", Output: "secret", OutputBody: "secret and more",
	}
	payload := p.Project(ev)[0].Payload.(*ToolEndedPayload)
	if payload.Output != "" {
		t.Fatalf("output = %q, want withheld", payload.Output)
	}
	if payload.OutputBytes != len("secret and more") {
		t.Fatalf("output_bytes = %d, want the withheld body's length %d", payload.OutputBytes, len("secret and more"))
	}
	if len(payload.Envelope.Redacted) != 1 || payload.Envelope.Redacted[0] != "output" {
		t.Fatalf("redacted = %v, want [output]", payload.Envelope.Redacted)
	}
}
