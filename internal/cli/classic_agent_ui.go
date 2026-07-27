package cli

import (
	"io"
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
type classicStreamWriter struct {
	ui *classicAgentUI
	w  io.Writer
}

func (w *classicStreamWriter) Write(p []byte) (int, error) {
	if len(p) > 0 {
		w.ui.noteStream(len(p))
	}
	if w.w == nil {
		return len(p), nil
	}
	return w.w.Write(p)
}

// RevokeStream marks that tool_calls arrived after stream bytes. Classic terminals
// cannot erase already-written text; interim print is skipped when streamBytes > 0.
func (w *classicStreamWriter) RevokeStream() string {
	w.ui.noteRevoke()
	return ""
}

func (ui *classicAgentUI) noteStream(n int) {
	ui.mu.Lock()
	ui.streamBytes += n
	ui.mu.Unlock()
}

func (ui *classicAgentUI) noteRevoke() {
	// No buffer erase; streamBytes remains for interim skip decision.
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

// wrapClassicFinalWriter pairs a classicAgentUI with a FinalWriter.
func wrapClassicFinalWriter(ui *classicAgentUI, base io.Writer) io.Writer {
	return &classicStreamWriter{ui: ui, w: base}
}

// makeAgentUIWithRenderer is the legacy entry used by tests/one-off tool prints.
// Prefer newClassicAgentHandler + wrapClassicFinalWriter for full interim support.
func makeAgentUIWithRenderer(r *ChatRenderer) func(agent.Event) {
	_, h := newClassicAgentHandler(r)
	return h
}
