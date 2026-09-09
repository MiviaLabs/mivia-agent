package chatsync

import (
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/events"
)

// TestProjectorSetSyncOptsFlipsIncludeToolIO pins the live re-arm seam
// for the tool-IO gate: a Projector built with IncludeToolIO=false
// must, after SetSyncOpts flips it to true, project a tool_start
// event with Input populated instead of redacted to empty. Flipping
// back to false must redact it again (SetSyncOpts is symmetric).
//
// The gate does NOT suppress the wire event itself - tool_start still
// ships with IncludeToolIO=false, it only omits the input payload
// (confirmed against gates_test.go's TestPrivacyGatesDefaultOff,
// which pins the same redact-not-drop behaviour).
func TestProjectorSetSyncOptsFlipsIncludeToolIO(t *testing.T) {
	p := NewProjector("sess-1", 0, ProjectorOptions{IncludeToolIO: false})
	ev := events.Event{
		Kind: events.KindToolStart, SessionID: "sess-1", TurnID: "t1",
		ToolCallID: "c1", Name: "write_file", Input: `{"x":1}`,
		Timestamp: time.Now(),
	}

	we := p.Project(ev)
	if len(we) != 1 {
		t.Fatalf("pre-flip: got %d wire events, want 1", len(we))
	}
	if got := we[0].Payload.(*ToolStartedPayload).Input; got != "" {
		t.Errorf("pre-flip input = %q, want '' when IncludeToolIO is false", got)
	}

	p.SetSyncOpts(false, true, false)
	we = p.Project(ev)
	if len(we) != 1 {
		t.Fatalf("post-flip-on: got %d wire events, want 1", len(we))
	}
	if got := we[0].Payload.(*ToolStartedPayload).Input; got == "" {
		t.Error("post-flip-on input is empty; want the gate open")
	}

	p.SetSyncOpts(false, false, false)
	we = p.Project(ev)
	if len(we) != 1 {
		t.Fatalf("post-flip-off: got %d wire events, want 1", len(we))
	}
	if got := we[0].Payload.(*ToolStartedPayload).Input; got != "" {
		t.Errorf("post-flip-off input = %q, want '' (gate re-closed)", got)
	}
}

// TestProjectorSetSyncOptsFlipsIncludeThinking mirrors
// TestProjectorSetSyncOptsFlipsIncludeToolIO for the thinking gate.
func TestProjectorSetSyncOptsFlipsIncludeThinking(t *testing.T) {
	p := NewProjector("sess-1", 0, ProjectorOptions{IncludeThinking: false})
	ev := events.Event{
		Kind: events.KindThinking, SessionID: "sess-1", TurnID: "t1",
		Content: "thought", Timestamp: time.Now(),
	}

	we := p.Project(ev)
	if len(we) != 1 {
		t.Fatalf("pre-flip: got %d wire events, want 1", len(we))
	}
	if got := we[0].Payload.(*ThinkingDeltaPayload).Text; got != "" {
		t.Errorf("pre-flip text = %q, want '' when IncludeThinking is false", got)
	}

	p.SetSyncOpts(true, false, false)
	we = p.Project(ev)
	if len(we) != 1 {
		t.Fatalf("post-flip-on: got %d wire events, want 1", len(we))
	}
	if got := we[0].Payload.(*ThinkingDeltaPayload).Text; got == "" {
		t.Error("post-flip-on text is empty; want the gate open")
	}

	p.SetSyncOpts(false, false, false)
	we = p.Project(ev)
	if len(we) != 1 {
		t.Fatalf("post-flip-off: got %d wire events, want 1", len(we))
	}
	if got := we[0].Payload.(*ThinkingDeltaPayload).Text; got != "" {
		t.Errorf("post-flip-off text = %q, want '' (gate re-closed)", got)
	}
}

// TestProjectorSetSyncOptsFlipsStreamAssistant pins the StreamAssistant
// gate. Unlike the other two, this gate controls ONLY the per-fragment
// DELTA path (projectAssistantDelta, projector.go): the final settled
// aggregate always ships full text regardless of StreamAssistant (its
// own doc comment: "the settled message still ships either way"). The
// test therefore drives a delta event (Detail: "delta"), not the
// aggregate, to observe the gate at all.
func TestProjectorSetSyncOptsFlipsStreamAssistant(t *testing.T) {
	p := NewProjector("sess-1", 0, ProjectorOptions{StreamAssistant: false})
	delta := events.Event{
		Kind: events.KindAssistant, SessionID: "sess-1", TurnID: "t1",
		Detail: "delta", Content: "hi", Timestamp: time.Now(),
	}

	if we := p.Project(delta); len(we) != 0 {
		t.Fatalf("pre-flip: delta produced %d wire events, want 0 (StreamAssistant off ships no deltas)", len(we))
	}

	p.SetSyncOpts(false, false, true)
	if we := p.Project(delta); len(we) == 0 {
		t.Error("post-flip-on: delta produced 0 wire events, want at least 1")
	}

	p.SetSyncOpts(false, false, false)
	if we := p.Project(delta); len(we) != 0 {
		t.Errorf("post-flip-off: delta produced %d wire events, want 0 (gate re-closed)", len(we))
	}
}

// TestProjectorSetSyncOptsPreservesNonFlagFields pins that the re-arm
// seam only flips the three sync gates - everything else (WriterID,
// RedactToolArgs, ErrorMessage) is the contract that decides what
// counts as a session's identity, and a re-arm must not silently
// rewrite it. If a future setter is added (for example, a runtime
// redact toggle), this test must be extended, not deleted.
func TestProjectorSetSyncOptsPreservesNonFlagFields(t *testing.T) {
	customErr := func(error) string { return "custom" }
	p := NewProjector("sess-1", 0, ProjectorOptions{
		IncludeThinking: false,
		WriterID:        "writer-X",
		RedactToolArgs:  true,
		ErrorMessage:    customErr,
	})

	p.SetSyncOpts(true, false, true)

	if p.opts.WriterID != "writer-X" {
		t.Errorf("WriterID changed to %q after SetSyncOpts; must be preserved", p.opts.WriterID)
	}
	if !p.opts.RedactToolArgs {
		t.Errorf("RedactToolArgs changed to false after SetSyncOpts; must be preserved")
	}
	if p.opts.ErrorMessage == nil {
		t.Errorf("ErrorMessage changed to nil after SetSyncOpts; must be preserved")
	}
	if p.opts.IncludeToolIO {
		t.Errorf("IncludeToolIO flipped to true; SetSyncOpts(true, false, true) was meant to keep it false")
	}
}
