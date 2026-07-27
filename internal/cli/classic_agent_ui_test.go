package cli

import (
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
)

func TestClassicUI_InterimPrintedWhenNoStreamBytes(t *testing.T) {
	t.Parallel()
	mt := newMockTerminal()
	r := NewChatRenderer(mt, "m")
	ui, h := newClassicAgentHandler(r)
	_ = wrapClassicFinalWriter(ui, mt) // no stream writes
	h(agent.Event{Kind: agent.EventAssistant, Content: "I'll inspect the project layout first.", Detail: "interim"})
	out := stripANSI(mt.String())
	if !strings.Contains(out, "inspect the project") {
		t.Fatalf("expected interim speech, got %q", out)
	}
}

func TestClassicUI_InterimSkippedWhenAlreadyStreamed(t *testing.T) {
	t.Parallel()
	mt := newMockTerminal()
	r := NewChatRenderer(mt, "m")
	ui, h := newClassicAgentHandler(r)
	w := wrapClassicFinalWriter(ui, mt)
	_, _ = w.Write([]byte("I'll inspect the project layout first."))
	h(agent.Event{Kind: agent.EventAssistant, Content: "I'll inspect the project layout first.", Detail: "interim"})
	out := stripANSI(mt.String())
	// One copy only (stream write), not double via interim reprint.
	if strings.Count(out, "inspect the project") != 1 {
		t.Fatalf("expected single speech copy, got %q", out)
	}
}

func TestClassicUI_FinalEventNotPrinted(t *testing.T) {
	t.Parallel()
	mt := newMockTerminal()
	r := NewChatRenderer(mt, "m")
	_, h := newClassicAgentHandler(r)
	h(agent.Event{Kind: agent.EventAssistant, Content: "final answer only via stream"})
	if strings.Contains(stripANSI(mt.String()), "final answer") {
		t.Fatal("final EventAssistant must not print")
	}
}

func TestClassicUI_EmptyContentStatusThenTool(t *testing.T) {
	t.Parallel()
	mt := newMockTerminal()
	r := NewChatRenderer(mt, "m")
	_, h := newClassicAgentHandler(r)
	h(agent.Event{Kind: agent.EventToolStart, Name: "list_dir", Detail: `{"path":"."}`})
	out := stripANSI(mt.String())
	if !strings.Contains(out, "→") && !strings.Contains(out, "Listing") {
		t.Fatalf("expected status line, got %q", out)
	}
	if !strings.Contains(out, "list_dir") {
		t.Fatalf("expected tool start, got %q", out)
	}
}

func TestClassicUI_RealInterimSkipsStatus(t *testing.T) {
	t.Parallel()
	mt := newMockTerminal()
	r := NewChatRenderer(mt, "m")
	_, h := newClassicAgentHandler(r)
	h(agent.Event{Kind: agent.EventAssistant, Content: "I'll search the codebase first.", Detail: "interim"})
	h(agent.Event{Kind: agent.EventToolStart, Name: "grep", Detail: `{"pattern":"x"}`})
	out := stripANSI(mt.String())
	if strings.Contains(out, "→") {
		t.Fatalf("status must not accompany real interim: %q", out)
	}
	if !strings.Contains(out, "search the codebase") {
		t.Fatalf("expected interim, got %q", out)
	}
}

func TestClassicUI_ShouldCommitInterimGate(t *testing.T) {
	t.Parallel()
	mt := newMockTerminal()
	r := NewChatRenderer(mt, "m")
	_, h := newClassicAgentHandler(r)
	h(agent.Event{Kind: agent.EventAssistant, Content: "OK.", Detail: "interim"})
	h(agent.Event{Kind: agent.EventToolStart, Name: "grep", Detail: `{"pattern":"auth"}`})
	out := stripANSI(mt.String())
	if strings.Contains(out, "OK.") {
		t.Fatalf("ghost interim must not print: %q", out)
	}
	if !strings.Contains(out, "→") && !strings.Contains(out, "Searching") {
		t.Fatalf("expected status after rejected interim: %q", out)
	}
}

func TestClassicStreamWriter_RevokeStream(t *testing.T) {
	t.Parallel()
	mt := newMockTerminal()
	r := NewChatRenderer(mt, "m")
	ui, _ := newClassicAgentHandler(r)
	w := wrapClassicFinalWriter(ui, mt)
	rev, ok := w.(interface{ RevokeStream() string })
	if !ok {
		t.Fatal("classic FinalWriter must implement RevokeStream")
	}
	_ = rev.RevokeStream()
}
