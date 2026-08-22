// Package cli - chat rendering for plain (--plain) mode.
package clichat

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
)

// isMemoryFrameMessage reports whether a message is the session-owned
// core-memory context frame (by sentinel Name, or by frame shape for legacy
// un-named frames). The frame is host surface, not conversation: rendering it
// as the opening user card would show the memory block as the user's words.
func isMemoryFrameMessage(m provider.Message) bool {
	if m.Role != provider.RoleUser {
		return false
	}
	return m.Name == chat.MemoryContextMessageName || chat.IsMemoryContextFrameContent(m.Content)
}

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
	padding := w - RuneWidth(prefix) - 1
	if padding < 1 {
		padding = 1
	}
	r.out.WriteString(fmt.Sprintf("%s%s%s%s\n", AnsiDim, prefix, strings.Repeat("─", padding), AnsiDimEnd))
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

// PrintInterim prints intermediate assistant speech before tools (classic REPL).
func (r *ChatRenderer) PrintInterim(text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	r.out.WriteString("\n")
	r.printDim("%s", text)
}

// PrintStatusLine prints a Phase-A style empty-speech tool status ("→ Reading…").
func (r *ChatRenderer) PrintStatusLine(line string) {
	line = strings.TrimSpace(line)
	if line == "" {
		return
	}
	if !strings.HasPrefix(line, "→") {
		line = "→ " + line
	}
	r.printDim("%s", line)
}

func (r *ChatRenderer) printDim(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	if msg != "" {
		r.out.WriteString(fmt.Sprintf("  %s%s%s\n", AnsiDim, msg, AnsiDimEnd))
	}
}

// PrintToolStart prints a tool invocation with spinner glyph.
func (r *ChatRenderer) PrintToolStart(name, detail string) {
	r.mu.Lock()
	r.toolStart[name] = append(r.toolStart[name], time.Now())
	r.mu.Unlock()
	icon := ToolIconForName(name)
	r.out.WriteString(fmt.Sprintf("  %s%s%s %s%s%s %s%s%s\n",
		AnsiCyan, NewToolRenderItem(name, detail, "", false, false).StatusIcon(false), AnsiReset,
		AnsiBold, name, AnsiBoldEnd,
		AnsiDim, BoundedToolText(detail, 80), AnsiDimEnd,
	))
	_ = icon
}

// PrintToolEnd prints a tool result with elapsed time.
func (r *ChatRenderer) PrintToolEnd(name, detail string) {
	r.mu.Lock()
	starts := r.toolStart[name]
	var elapsed string
	if len(starts) > 0 {
		// Pop the most recent start (LIFO - works for parallel too since
		// tool ends fire in order after all parallel results collected).
		start := starts[len(starts)-1]
		starts = starts[:len(starts)-1]
		if len(starts) == 0 {
			delete(r.toolStart, name)
		} else {
			r.toolStart[name] = starts
		}
		elapsed = " " + FormatDuration(time.Since(start))
	}
	r.mu.Unlock()
	failed := strings.HasPrefix(strings.ToLower(detail), "error") ||
		strings.Contains(detail, "exit=1") ||
		strings.Contains(detail, "exit=error")
	item := NewToolRenderItem(name, detail, detail, true, failed)
	icon, color := item.StatusIcon(false), AnsiGreen
	if failed {
		color = AnsiRed
	}
	r.out.WriteString(fmt.Sprintf("  %s%s%s %s%s%s %s%s%s%s%s%s\n",
		color, icon, AnsiReset,
		AnsiBold, name, AnsiBoldEnd,
		AnsiDim, item.Summary(80), AnsiDimEnd,
		AnsiYellow, elapsed, AnsiReset,
	))
}

// PrintParallel prints a parallel tool execution notice.
func (r *ChatRenderer) PrintParallel(detail string) {
	r.out.WriteString(fmt.Sprintf("  %s⚡%s %s%s%s\n", AnsiYellow, AnsiReset, AnsiDim, detail, AnsiDimEnd))
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
	r.out.WriteString(fmt.Sprintf("  %serror: %s%s\n", AnsiRed, err, AnsiReset))
}

// PrintInfo prints an informational message.
func (r *ChatRenderer) PrintInfo(msg string) {
	r.out.WriteString(fmt.Sprintf("  %s\n", msg))
}

// ---------------------------------------------------------------------------
// History rendering - turn-aware and per-message formatters
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
//	user   → background bar with optional local time + body (no border)
//	assistant with ToolCalls → tool_call_line* + content* (no model border)
//	assistant without ToolCalls → rendered_markdown (no model border)
//	tool   → ["icon name truncated_result"]
func RenderMessageForHistory(msg provider.Message, modelName string, width int) []string {
	w := Max(20, width)
	switch msg.Role {
	case provider.RoleSystem:
		return nil

	case provider.RoleUser:
		// History reload has no per-message SentAt; body still shows with bg.
		return formatUserMessageCard(msg.Content, w, time.Time{})

	case provider.RoleAssistant:
		var lines []string
		// Compact tool-call lines for any ToolCalls in this message.
		for _, tc := range msg.ToolCalls {
			args := NewToolRenderItem(tc.Function.Name, tc.Function.Arguments, "", false, false).Summary(80)
			icon := ToolIconForName(tc.Function.Name)
			line := fmt.Sprintf("  %s %s %s",
				icon,
				ToolNameStyle.Render(tc.Function.Name),
				TUIDimStyle.Render(args),
			)
			lines = append(lines, line)
		}
		// If there is textual content, render as markdown.
		if msg.Content != "" {
			md := RenderMarkdown(msg.Content, Max(20, w-2))
			if md != "" {
				lines = append(lines, WrapANSIv2(md, Max(20, w-2)))
			}
		}
		if len(lines) == 0 {
			return nil
		}
		// No bordered model chrome - content only.
		return lines

	case provider.RoleTool:
		item := NewToolRenderItem(msg.Name, "", msg.Content, true, strings.HasPrefix(strings.ToLower(msg.Content), "error"))
		truncated := item.Summary(maxToolResultPreview)
		icon := ToolOkStyle.Render(item.StatusIcon(false))
		if item.Failed {
			icon = ToolErrStyle.Render(item.StatusIcon(false))
		}
		line := fmt.Sprintf("  %s %s %s",
			icon,
			ToolNameStyle.Render(msg.Name),
			TUIDimStyle.Render(truncated),
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

	// Find the start: first user message (or skip leading system). The
	// memory-context frame is user-role host surface, never the opening card.
	startIdx := -1
	for i, m := range msgs {
		if m.Role == provider.RoleUser && !isMemoryFrameMessage(m) {
			startIdx = i
			break
		}
	}
	if startIdx < 0 {
		return nil // No user message in this group
	}

	var result []string
	w := Max(20, width)

	// User card (first user message in the group) - no border, keep bg.
	userMsg := msgs[startIdx]
	result = append(result, formatUserMessageCard(userMsg.Content, w, time.Time{})...)

	toolCallLines, toolResultLines, finalAnswer, hasModelContent := renderTurnBody(msgs[startIdx+1:])

	if !hasModelContent {
		return result
	}

	// Model content without bordered chrome.
	_ = modelName
	result = append(result, toolCallLines...)
	result = append(result, toolResultLines...)

	if finalAnswer != "" {
		md := RenderMarkdown(finalAnswer, Max(20, w-2))
		if md != "" {
			result = append(result, WrapANSIv2(md, Max(20, w-2)))
		}
	}

	return result
}

func renderTurnBody(msgs []provider.Message) ([]string, []string, string, bool) {
	var calls, results []string
	var answer string
	hasContent := false
	for _, msg := range msgs {
		switch msg.Role {
		case provider.RoleAssistant:
			calls = append(calls, renderToolCalls(msg.ToolCalls)...)
			if len(msg.ToolCalls) > 0 || msg.Content != "" {
				hasContent = true
			}
			if msg.Content != "" {
				answer = msg.Content
			}
		case provider.RoleTool:
			results = append(results, renderToolResult(msg))
			hasContent = true
		}
	}
	return calls, results, answer, hasContent
}

func renderToolCalls(calls []provider.ToolCall) []string {
	lines := make([]string, 0, len(calls))
	for _, call := range calls {
		args := NewToolRenderItem(call.Function.Name, call.Function.Arguments, "", false, false).Summary(80)
		lines = append(lines, fmt.Sprintf("  %s %s %s", ToolIconForName(call.Function.Name), ToolNameStyle.Render(call.Function.Name), TUIDimStyle.Render(args)))
	}
	return lines
}

func renderToolResult(msg provider.Message) string {
	item := NewToolRenderItem(msg.Name, "", msg.Content, true, strings.HasPrefix(strings.ToLower(msg.Content), "error"))
	icon := ToolOkStyle.Render(item.StatusIcon(false))
	if item.Failed {
		icon = ToolErrStyle.Render(item.StatusIcon(false))
	}
	return fmt.Sprintf("  %s %s %s", icon, ToolNameStyle.Render(msg.Name), TUIDimStyle.Render(item.Summary(maxToolResultPreview)))
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
		if m.Role == provider.RoleUser && !isMemoryFrameMessage(m) {
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
	for i, tr := range turns {
		lines := RenderTurn(msgs[tr.start:tr.end], modelName, width)
		if len(lines) > 0 {
			if i > 0 {
				// Insert dim divider between turns.
				// Bare turn rule dropped - see renderBlockBody.
			}
			result = append(result, lines...)
		}
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
