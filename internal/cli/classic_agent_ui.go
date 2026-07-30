package cli

import (
	"io"
	"strings"
	"sync"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
)

// classicAgentUI prints interim speech + empty-content tool status for classic
// (--plain) REPL without double-printing the final answer (FinalWriter owns finals).
type classicAgentUI struct {
	r *ChatRenderer

	mu             sync.Mutex
	streamBytes    int
	interimPrinted bool
	statusPrinted  bool
	openTools      int
}

// classicStreamWriter wraps the final answer writer and implements streamRevoker
// so content-then-tools can mark optimistic stream without TUI bridge.
//
// Content is held per message so a revocation — tool_calls arrived, so that speech
// was narration and not the answer — can be acted on. `live` distinguishes the two
// classic surfaces: the REPL writes straight to the terminal, where bytes cannot be
// unprinted and a revocation can only terminate the message, while one-shot
// collects into a buffer printed once at the end, where a revocation genuinely
// keeps narration out of stdout. That split is what makes stdout the answer and
// stderr the progress log for `mivia chat -p`.
type classicStreamWriter struct {
	ui      *classicAgentUI
	w       io.Writer
	live    bool
	pending strings.Builder
}

func (w *classicStreamWriter) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	w.ui.noteStream(len(p))
	if !w.live {
		w.pending.Write(p)
		return len(p), nil
	}
	if w.w == nil {
		return len(p), nil
	}
	return w.w.Write(p)
}

// RevokeStream marks that tool_calls arrived after stream bytes, so what was
// streamed is interim speech rather than the answer. The agent re-emits it as an
// EventAssistant with Detail "interim", which classicAgentUI prints to the
// progress stream.
func (w *classicStreamWriter) RevokeStream() string {
	revoked := w.pending.String()
	w.pending.Reset()
	if w.live {
		// Already on screen; it cannot be withdrawn. Terminate the message so the
		// next one does not continue its unterminated last line — MarkdownWriter is
		// line buffered and emits only on a newline, so without this two separate
		// messages render as one run-on paragraph.
		w.endMessage()
		return revoked
	}
	// Nothing has reached the answer sink, so the interim print is not a duplicate.
	w.ui.clearStreamBytes()
	return revoked
}

// commit writes the held answer to the real sink and closes the message. Called
// once per turn, after the agent loop returns.
func (w *classicStreamWriter) commit() {
	if !w.live && w.pending.Len() > 0 && w.w != nil {
		_, _ = io.WriteString(w.w, w.pending.String())
	}
	w.pending.Reset()
	w.endMessage()
}

// endMessage completes the current line and flushes any partial block, so a
// message that does not end in a newline still renders.
func (w *classicStreamWriter) endMessage() {
	f, ok := w.w.(interface{ Flush() error })
	if !ok {
		return
	}
	_, _ = io.WriteString(w.w, "\n")
	_ = f.Flush()
}

func (ui *classicAgentUI) noteStream(n int) {
	ui.mu.Lock()
	ui.streamBytes += n
	ui.mu.Unlock()
}

// clearStreamBytes forgets streamed bytes that turned out not to be the answer,
// so onAssistant prints the interim instead of suppressing it as a duplicate.
func (ui *classicAgentUI) clearStreamBytes() {
	ui.mu.Lock()
	ui.streamBytes = 0
	ui.mu.Unlock()
}

func (ui *classicAgentUI) handle(e agent.Event) {
	switch e.Kind {
	case agent.EventStep:
		if e.Detail != "" {
			ui.r.PrintStep(e.Detail)
		}
	case agent.EventToolParallel:
		if e.Detail != "" {
			ui.r.PrintParallel(e.Detail)
		}
	case agent.EventPrune:
		if e.Detail != "" {
			ui.r.PrintPrune(e.Detail)
		}
	case agent.EventAssistant:
		ui.onAssistant(e)
	case agent.EventToolStart:
		ui.onToolStart(e)
	case agent.EventToolEnd:
		ui.onToolEnd(e)
	}
}

func (ui *classicAgentUI) onAssistant(e agent.Event) {
	// Final answers (no Detail) stream via FinalWriter — never reprint.
	if e.Detail != "interim" {
		return
	}
	if !shouldCommitInterim(e.Content) {
		return
	}
	ui.mu.Lock()
	already := ui.streamBytes > 0 || ui.interimPrinted
	if !already {
		ui.interimPrinted = true
	} else {
		ui.interimPrinted = true
	}
	ui.mu.Unlock()
	if already {
		// Speech already on screen (streamed deltas) — do not double-print.
		return
	}
	ui.r.PrintInterim(e.Content)
}

func (ui *classicAgentUI) onToolStart(e agent.Event) {
	ui.mu.Lock()
	first := ui.openTools == 0
	needStatus := first && !ui.interimPrinted && !ui.statusPrinted
	if needStatus {
		ui.statusPrinted = true
	}
	ui.openTools++
	ui.mu.Unlock()

	if needStatus {
		line := toolStatusLine(e.Name, eventPreview(e.Input, e.Detail))
		if line != "" {
			ui.r.PrintStatusLine(line)
		}
	}
	ui.r.PrintToolStart(e.Name, eventPreview(e.Input, e.Detail))
}

func (ui *classicAgentUI) onToolEnd(e agent.Event) {
	ui.r.PrintToolEnd(e.Name, eventPreview(e.Output, e.Detail))
	ui.mu.Lock()
	if ui.openTools > 0 {
		ui.openTools--
	}
	if ui.openTools == 0 {
		// Next tool wave can show status/interim again.
		ui.interimPrinted = false
		ui.statusPrinted = false
		ui.streamBytes = 0
	}
	ui.mu.Unlock()
}

// newClassicAgentHandler returns an OnEvent handler for classic REPL.
func newClassicAgentHandler(r *ChatRenderer) (*classicAgentUI, func(agent.Event)) {
	ui := &classicAgentUI{r: r}
	return ui, ui.handle
}

// wrapClassicFinalWriter pairs a classicAgentUI with a FinalWriter that streams
// live to a terminal (the REPL). Revoked speech stays on screen.
func wrapClassicFinalWriter(ui *classicAgentUI, base io.Writer) io.Writer {
	return &classicStreamWriter{ui: ui, w: base, live: true}
}

// wrapClassicBufferedFinalWriter pairs a classicAgentUI with a FinalWriter whose
// sink is collected and printed once (one-shot `-p`). Revoked speech is withheld,
// so stdout carries the answer while narration goes to the progress stream.
func wrapClassicBufferedFinalWriter(ui *classicAgentUI, base io.Writer) io.Writer {
	return &classicStreamWriter{ui: ui, w: base}
}

// makeAgentUIWithRenderer is the legacy entry used by tests/one-off tool prints.
// Prefer newClassicAgentHandler + wrapClassicFinalWriter for full interim support.
func makeAgentUIWithRenderer(r *ChatRenderer) func(agent.Event) {
	_, h := newClassicAgentHandler(r)
	return h
}
