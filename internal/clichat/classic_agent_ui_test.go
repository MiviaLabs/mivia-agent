package clichat

import (
	"io"
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

// TestClassicUI_BufferedKeepsInterimOutOfFinalOutput locks the one-shot surface.
// stdout is the answer and stderr is the progress log, but every streamed byte -
// including narration before each tool call - landed in the stdout buffer. The
// result was one blob printed after all tool output, with consecutive messages
// glued together because MarkdownWriter only emits on a newline.
func TestClassicUI_BufferedKeepsInterimOutOfFinalOutput(t *testing.T) {
	var out strings.Builder
	mw := NewMarkdownWriter(&out)
	ui, _ := newClassicAgentHandler(NewChatRenderer(newMockTerminal(), "m"))
	w := wrapClassicBufferedFinalWriter(ui, mw)

	// Step 1: speech, then tool_calls arrive -> that speech was interim.
	_, _ = io.WriteString(w, "First I will list the directory.")
	revoked := w.(*classicStreamWriter).RevokeStream()
	if revoked != "First I will list the directory." {
		t.Fatalf("revoked text = %q", revoked)
	}

	// Step 2: the real answer, no revocation.
	_, _ = io.WriteString(w, "Done: two files written.")
	w.(*classicStreamWriter).commit()
	_ = mw.Flush()

	got := stripANSI(out.String())
	if strings.Contains(got, "First I will list") {
		t.Errorf("interim narration leaked into the final answer output:\n%s", got)
	}
	if !strings.Contains(got, "Done: two files written.") {
		t.Errorf("final answer missing from output:\n%s", got)
	}
}

// TestClassicUI_BufferedRevokeLetsInterimPrint - with nothing yet written to the
// answer sink, the revoked speech must reach the progress stream instead of being
// suppressed as an already-streamed duplicate.
func TestClassicUI_BufferedRevokeLetsInterimPrint(t *testing.T) {
	var out strings.Builder
	mt := newMockTerminal()
	ui, h := newClassicAgentHandler(NewChatRenderer(mt, "m"))
	w := wrapClassicBufferedFinalWriter(ui, NewMarkdownWriter(&out))

	_, _ = io.WriteString(w, "Now I will read the file.")
	w.(*classicStreamWriter).RevokeStream()
	h(agent.Event{Kind: agent.EventAssistant, Content: "Now I will read the file.", Detail: "interim"})

	if !strings.Contains(stripANSI(mt.String()), "Now I will read the file.") {
		t.Errorf("interim speech never reached the progress stream:\n%s", mt.String())
	}
}

// TestClassicUI_LiveMessagesDoNotGlue - the REPL writes straight to the terminal,
// so revoked speech cannot be unprinted, but the message must still be terminated
// or the next one continues its unterminated last line.
func TestClassicUI_LiveMessagesDoNotGlue(t *testing.T) {
	var out strings.Builder
	mw := NewMarkdownWriter(&out)
	ui, _ := newClassicAgentHandler(NewChatRenderer(newMockTerminal(), "m"))
	w := wrapClassicFinalWriter(ui, mw)

	_, _ = io.WriteString(w, "sentence one ends here.")
	w.(*classicStreamWriter).RevokeStream()
	_, _ = io.WriteString(w, "sentence two starts here.")
	w.(*classicStreamWriter).commit()
	_ = mw.Flush()

	got := stripANSI(out.String())
	if strings.Contains(got, "here.sentence two") {
		t.Errorf("consecutive messages glued together:\n%s", got)
	}
}

func TestClassicUI_ThinkingPrints(t *testing.T) {
	t.Parallel()
	mt := newMockTerminal()
	r := NewChatRenderer(mt, "m")
	_, h := newClassicAgentHandler(r)
	h(agent.Event{Kind: agent.EventThinking, Content: "Analyzing requirements...\nStep 1: check files"})
	out := stripANSI(mt.String())
	if !strings.Contains(out, "thinking:") || !strings.Contains(out, "Analyzing requirements") {
		t.Fatalf("expected thinking printed, got %q", out)
	}
}
