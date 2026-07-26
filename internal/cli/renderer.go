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

// ---------------------------------------------------------------------------
// History rendering — turn-aware and per-message formatters
// Used by TUI history loading (hydrateHistory, /load, loadMoreMessages)
// and by RenderHistory in plain mode.
// ---------------------------------------------------------------------------

// maxToolResultPreview is the max chars of a tool result shown inline in history.
const maxToolResultPreview = 200

// RenderMessageForHistory formats a single provider.Message into display-ready lines.
// Returns nil for system prompts. Each returned string may contain ANSI codes and
// newlines. Callers append these strings directly to the viewport message list.
//
// Roles:
//
//	system → nil (skip)
//	user   → bordered "you" card (formatUserMessageCard)
//	assistant with ToolCalls → [model header, tool_call_line*, content*]
//	assistant without ToolCalls → [model header, rendered_markdown]
//	tool   → ["icon name truncated_result"]
func RenderMessageForHistory(msg provider.Message, modelName string, width int) []string {
	w := max(20, width)
	switch msg.Role {
	case provider.RoleSystem:
		return nil

	case provider.RoleUser:
		return formatUserMessageCard(msg.Content, w)

	case provider.RoleAssistant:
		var lines []string
		// Compact tool-call lines for any ToolCalls in this message.
		for _, tc := range msg.ToolCalls {
			args := truncateStr(tc.Function.Arguments, 80)
			icon := toolIconForName(tc.Function.Name)
			line := fmt.Sprintf("  %s %s %s",
				icon,
				toolNameStyle.Render(tc.Function.Name),
				tuiDimStyle.Render(args),
			)
			lines = append(lines, line)
		}
		// If there is textual content, render as markdown.
		if msg.Content != "" {
			md := RenderMarkdown(msg.Content, max(20, w-2))
			if md != "" {
				lines = append(lines, wrapANSIv2(md, max(20, w-2)))
			}
		}
		if len(lines) == 0 {
			return nil
		}
		header := formatModelHeader(modelName, w)
		result := make([]string, 0, len(lines)+1)
		result = append(result, header)
		result = append(result, lines...)
		return result

	case provider.RoleTool:
		truncated := truncateStr(msg.Content, maxToolResultPreview)
		icon := toolOkStyle.Render("✓")
		if strings.HasPrefix(strings.ToLower(truncated), "error") {
			icon = toolErrStyle.Render("✗")
		}
		line := fmt.Sprintf("  %s %s %s",
			icon,
			toolNameStyle.Render(msg.Name),
			tuiDimStyle.Render(truncated),
		)
		return []string{line}

	default:
		return nil
	}
}

// RenderTurn renders a group of messages forming one conversational turn.
// A turn starts with a user message and includes the assistant reply
// (possibly with tool calls and results), ending at the next user message
// or end of slice.
//
// Returns nil if the group is empty or has no user message.
// The output uses turn-aware grouping: one header for the user,
// one header for the model, inline tool call/result lines, and
// the final assistant answer rendered as markdown.
func RenderTurn(msgs []provider.Message, modelName string, width int) []string {
	if len(msgs) == 0 {
		return nil
	}

	// Find the start: first user message (or skip leading system).
	startIdx := -1
	for i, m := range msgs {
		if m.Role == provider.RoleUser {
			startIdx = i
			break
		}
	}
	if startIdx < 0 {
		return nil // No user message in this group
	}

	var result []string
	w := max(20, width)

	// User card (first user message in the group).
	userMsg := msgs[startIdx]
	result = append(result, formatUserMessageCard(userMsg.Content, w)...)

	// Model header (shown once per turn, before any tools or answer).
	modelHeader := formatModelHeader(modelName, w)

	// Process the rest of the turn.
	var toolCallLines []string   // accumulated tool-call request lines
	var toolResultLines []string // accumulated tool result lines
	var finalAnswer string       // the last assistant content (no tool calls)
	hasModelContent := false     // whether we have anything to show under model header

	for i := startIdx + 1; i < len(msgs); i++ {
		m := msgs[i]
		switch m.Role {
		case provider.RoleAssistant:
			// Capture tool calls (compact lines).
			for _, tc := range m.ToolCalls {
				args := truncateStr(tc.Function.Arguments, 80)
				icon := toolIconForName(tc.Function.Name)
				line := fmt.Sprintf("  %s %s %s",
					icon,
					toolNameStyle.Render(tc.Function.Name),
					tuiDimStyle.Render(args),
				)
				toolCallLines = append(toolCallLines, line)
				hasModelContent = true
			}
			// If this assistant message has content and no tool calls,
			// it is the final answer. Accumulate in case there are more
			// tool-call assistant messages before the final one.
			if m.Content != "" && len(m.ToolCalls) == 0 {
				finalAnswer = m.Content
				hasModelContent = true
			}
			// If it has both tool calls and content, keep the content too.
			if m.Content != "" && len(m.ToolCalls) > 0 {
				finalAnswer = m.Content
				hasModelContent = true
			}

		case provider.RoleTool:
			truncated := truncateStr(m.Content, maxToolResultPreview)
			icon := toolOkStyle.Render("✓")
			if strings.HasPrefix(strings.ToLower(truncated), "error") {
				icon = toolErrStyle.Render("✗")
			}
			line := fmt.Sprintf("  %s %s %s",
				icon,
				toolNameStyle.Render(m.Name),
				tuiDimStyle.Render(truncated),
			)
			toolResultLines = append(toolResultLines, line)
			hasModelContent = true
		}
	}

	if !hasModelContent {
		// No model content at all — just return user portion.
		return result
	}

	// Emit: model header, then tool calls, then results, then final answer.
	result = append(result, modelHeader)
	result = append(result, toolCallLines...)
	result = append(result, toolResultLines...)

	if finalAnswer != "" {
		md := RenderMarkdown(finalAnswer, max(20, w-2))
		if md != "" {
			result = append(result, wrapANSIv2(md, max(20, w-2)))
		}
	}

	return result
}

// RenderHistoryMessages groups messages by user-message boundaries and
// renders each turn with turn-aware formatting. System messages are skipped.
// This is the primary entry point for loading full session history into the TUI
// viewport or the plain-mode renderer.
func RenderHistoryMessages(msgs []provider.Message, modelName string, width int) []string {
	if len(msgs) == 0 {
		return nil
	}

	// Split into turns at user message boundaries.
	type turnRange struct {
		start, end int
	}
	var turns []turnRange

	start := -1
	for i, m := range msgs {
		if m.Role == provider.RoleUser {
			if start >= 0 {
				turns = append(turns, turnRange{start: start, end: i})
			}
			start = i
		}
	}
	// Last turn.
	if start >= 0 {
		turns = append(turns, turnRange{start: start, end: len(msgs)})
	}

	if len(turns) == 0 {
		return nil
	}

	var result []string
	for _, tr := range turns {
		lines := RenderTurn(msgs[tr.start:tr.end], modelName, width)
		result = append(result, lines...)
	}
	return result
}

// RenderHistory prints session history with turn-aware formatting.
// Tool calls and results are shown compactly inline.
func (r *ChatRenderer) RenderHistory(messages []provider.Message) {
	w, _ := r.out.Size()
	if w <= 0 {
		w = 80
	}
	lines := RenderHistoryMessages(messages, r.model, w)
	for _, l := range lines {
		r.out.WriteString(l)
		r.out.WriteString("\n")
	}
}
