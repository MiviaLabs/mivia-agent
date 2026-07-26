// Package cli — chat rendering for plain (--plain) mode.
package cli

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
)

// TerminalWriter is the minimal interface ChatRenderer needs.
type TerminalWriter interface {
	Write(p []byte) (n int, err error)
	WriteString(s string)
	Size() (width, height int)
}

// ChatRenderer formats conversation messages in a clean chat-app style.
type ChatRenderer struct {
	out   TerminalWriter
	model string

	mu        sync.Mutex
	toolStart map[string][]time.Time // stack per name for parallel support
	toolOrder []string               // call order for matching
}

// NewChatRenderer creates a renderer bound to a terminal writer.
func NewChatRenderer(out TerminalWriter, model string) *ChatRenderer {
	return &ChatRenderer{
		out:       out,
		model:     model,
		toolStart: make(map[string][]time.Time),
	}
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

// PrintUser prints the user's message.
func (r *ChatRenderer) PrintUser(text string) {
	r.out.WriteString("\n")
	r.DimHeader("you")
	r.out.WriteString(strings.TrimRight(text, "\n\r\t "))
	r.out.WriteString("\n")
}

// PrintAssistantHeader prints a divider before assistant output.
func (r *ChatRenderer) PrintAssistantHeader() {
	r.DimHeader(r.model)
}

// PrintDim prints a dim-styled line.
func (r *ChatRenderer) PrintDim(format string, args ...any) {
	r.printDim(format, args...)
}

func (r *ChatRenderer) printDim(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	if msg != "" {
		r.out.WriteString(fmt.Sprintf("  %s%s%s\n", ansiDim, msg, ansiDimEnd))
	}
}

// PrintToolStart prints a tool invocation with spinner glyph.
func (r *ChatRenderer) PrintToolStart(name, detail string) {
	r.mu.Lock()
	r.toolStart[name] = append(r.toolStart[name], time.Now())
	r.mu.Unlock()
	icon := toolIconForName(name)
	r.out.WriteString(fmt.Sprintf("  %s%s%s %s%s%s %s%s%s\n",
		ansiCyan, "◐", ansiReset,
		ansiBold, name, ansiBoldEnd,
		ansiDim, truncateStr(detail, 80), ansiDimEnd,
	))
	_ = icon
}

// PrintToolEnd prints a tool result with elapsed time.
func (r *ChatRenderer) PrintToolEnd(name, detail string) {
	r.mu.Lock()
	starts := r.toolStart[name]
	var elapsed string
	if len(starts) > 0 {
		// Pop the most recent start (LIFO — works for parallel too since
		// tool ends fire in order after all parallel results collected).
		start := starts[len(starts)-1]
		starts = starts[:len(starts)-1]
		if len(starts) == 0 {
			delete(r.toolStart, name)
		} else {
			r.toolStart[name] = starts
		}
		elapsed = " " + formatDuration(time.Since(start))
	}
	r.mu.Unlock()
	failed := strings.HasPrefix(strings.ToLower(detail), "error") ||
		strings.Contains(detail, "exit=1") ||
		strings.Contains(detail, "exit=error")
	icon, color := "✓", ansiGreen
	if failed {
		icon, color = "✗", ansiRed
	}
	r.out.WriteString(fmt.Sprintf("  %s%s%s %s%s%s %s%s%s%s%s%s\n",
		color, icon, ansiReset,
		ansiBold, name, ansiBoldEnd,
		ansiDim, truncateStr(detail, 80), ansiDimEnd,
		ansiYellow, elapsed, ansiReset,
	))
}

// PrintParallel prints a parallel tool execution notice.
func (r *ChatRenderer) PrintParallel(detail string) {
	r.out.WriteString(fmt.Sprintf("  %s⚡%s %s%s%s\n", ansiYellow, ansiReset, ansiDim, detail, ansiDimEnd))
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

// RenderHistory prints session history with markdown for assistant turns.
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
				r.PrintDim("(tool results…)")
				lastWasTool = true
			}
		}
	}
}
