// Package cli implements mivia command handlers.
package cli

import (
	"fmt"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
)

// TerminalWriter is the minimal interface ChatRenderer needs.
type TerminalWriter interface {
	Write(p []byte) (n int, err error)
	WriteString(s string)
	Size() (width, height int)
}

// ChatRenderer formats conversation messages in a clean chat-app style.
// All output goes to the provided TerminalWriter (stderr in REPL mode).
type ChatRenderer struct {
	out   TerminalWriter
	model string
}

// NewChatRenderer creates a renderer bound to a terminal writer.
func NewChatRenderer(out TerminalWriter, model string) *ChatRenderer {
	return &ChatRenderer{out: out, model: model}
}

// DimHeader prints a dim divider with a label.
func (r *ChatRenderer) DimHeader(label string) {
	w, _ := r.out.Size()
	if w <= 0 {
		w = 80
	}
	prefix := fmt.Sprintf("── %s ──", label)
	padding := w - runeWidth(prefix) - 1
	if padding < 1 {
		padding = 1
	}
	r.out.WriteString(fmt.Sprintf("%s%s%s%s\n", ansiDim, prefix, strings.Repeat("─", padding), ansiDimEnd))
}

// PrintUser prints the user's message with a dim "you" header, trimming trailing whitespace.
func (r *ChatRenderer) PrintUser(text string) {
	r.out.WriteString("\n")
	r.DimHeader("you")
	r.out.WriteString(strings.TrimRight(text, "\n\r\t "))
	r.out.WriteString("\n")
}

// PrintAssistantHeader prints a divider before assistant output with the model name.
func (r *ChatRenderer) PrintAssistantHeader() {
	r.DimHeader(r.model)
}

// PrintDim prints a dim-styled line.
func (r *ChatRenderer) PrintDim(format string, args ...any) {
	r.printDim(format, args...)
}

// printDim is a helper for dim-styled output lines.
func (r *ChatRenderer) printDim(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	if msg != "" {
		r.out.WriteString(fmt.Sprintf("  %s%s%s\n", ansiDim, msg, ansiDimEnd))
	}
}

// PrintToolStart prints a tool invocation.
func (r *ChatRenderer) PrintToolStart(name, detail string) {
	r.printDim("→ %s %s", name, detail)
}

// PrintToolEnd prints a tool result summary.
func (r *ChatRenderer) PrintToolEnd(name, detail string) {
	r.printDim("← %s %s", name, detail)
}

// PrintParallel prints a parallel tool execution notice.
func (r *ChatRenderer) PrintParallel(detail string) {
	r.printDim("⚡ %s", detail)
}

// PrintPrune prints a context pruning notice.
func (r *ChatRenderer) PrintPrune(detail string) {
	r.printDim("📐 %s", detail)
}

// PrintStep prints a step counter.
func (r *ChatRenderer) PrintStep(detail string) {
	r.printDim("%s", detail)
}

// PrintTokenEstimate prints the token estimate before a turn.
func (r *ChatRenderer) PrintTokenEstimate(count int) {
	r.printDim("(~%d tokens in history)", count)
}

// PrintError prints an error message in red.
func (r *ChatRenderer) PrintError(err string) {
	r.out.WriteString(fmt.Sprintf("  %serror: %s%s\n", ansiRed, err, ansiReset))
}

// PrintInfo prints an informational message.
func (r *ChatRenderer) PrintInfo(msg string) {
	r.out.WriteString(fmt.Sprintf("  %s\n", msg))
}

// RenderHistory prints all messages from a session history, excluding system prompt.
// User messages get the "you" header, assistant messages get the model header
// with markdown rendering. Tool messages are shown dimmed inline.
func (r *ChatRenderer) RenderHistory(messages []provider.Message) {
	mw := NewMarkdownWriter(r.out)
	lastWasTool := false
	for _, msg := range messages {
		switch msg.Role {
		case provider.RoleSystem:
			continue
		case provider.RoleUser:
			r.out.WriteString("\n")
			r.DimHeader("you")
			r.out.WriteString(strings.TrimRight(msg.Content, "\n\r\t "))
			r.out.WriteString("\n")
			lastWasTool = false
		case provider.RoleAssistant:
			r.DimHeader(r.model)
			mw.Write([]byte(msg.Content))
			mw.Flush()
			r.out.WriteString("\n")
			lastWasTool = false
		case provider.RoleTool:
			if !lastWasTool {
				r.PrintDim("(tool results follow...)")
				lastWasTool = true
			}
		}
	}
}
