// Package cli implements mivia command handlers.
package cli

import (
	"fmt"
	"strings"
	"unicode"
)

// MaxHistorySize is the maximum number of history entries kept per session.
const MaxHistorySize = 500

// InputBuffer is a line editor with cursor movement and history.
// It manages a buffer of runes and renders itself to a terminal line,
// supporting multi-line wrapping when content exceeds terminal width.
type InputBuffer struct {
	buf     []rune
	pos     int // cursor position in runes
	history []string
	histIdx int // -1 = editing fresh, 0..n-1 = browsing history
	prompt  string
	// prevLines tracks how many visual terminal lines the previous
	// render occupied, so we can correctly clear and redraw.
	prevLines int
}

// NewInputBuffer creates a new input buffer with a given prompt string.
func NewInputBuffer(prompt string) *InputBuffer {
	return &InputBuffer{
		prompt:  prompt,
		histIdx: -1,
	}
}

// SetPrompt updates the prompt string.
func (ib *InputBuffer) SetPrompt(p string) {
	ib.prompt = p
}

// Prompt returns the current prompt string.
func (ib *InputBuffer) Prompt() string {
	return ib.prompt
}

// Len returns the length of the buffer in runes.
func (ib *InputBuffer) Len() int {
	return len(ib.buf)
}

// Pos returns the current cursor position.
func (ib *InputBuffer) Pos() int {
	return ib.pos
}

// Insert adds a rune at the cursor position.
func (ib *InputBuffer) Insert(r rune) {
	ib.buf = append(ib.buf, 0)
	copy(ib.buf[ib.pos+1:], ib.buf[ib.pos:])
	ib.buf[ib.pos] = r
	ib.pos++
}

// Backspace removes the rune before the cursor.
func (ib *InputBuffer) Backspace() {
	if ib.pos <= 0 {
		return
	}
	copy(ib.buf[ib.pos-1:], ib.buf[ib.pos:])
	ib.buf = ib.buf[:len(ib.buf)-1]
	ib.pos--
}

// Delete removes the rune at the cursor.
func (ib *InputBuffer) Delete() {
	if ib.pos >= len(ib.buf) {
		return
	}
	copy(ib.buf[ib.pos:], ib.buf[ib.pos+1:])
	ib.buf = ib.buf[:len(ib.buf)-1]
}

// MoveLeft moves cursor left by one rune.
func (ib *InputBuffer) MoveLeft() {
	if ib.pos > 0 {
		ib.pos--
	}
}

// MoveRight moves cursor right by one rune.
func (ib *InputBuffer) MoveRight() {
	if ib.pos < len(ib.buf) {
		ib.pos++
	}
}

// MoveHome moves cursor to the beginning.
func (ib *InputBuffer) MoveHome() {
	ib.pos = 0
}

// MoveEnd moves cursor to the end.
func (ib *InputBuffer) MoveEnd() {
	ib.pos = len(ib.buf)
}

// KillLine clears the entire buffer.
func (ib *InputBuffer) KillLine() {
	ib.buf = ib.buf[:0]
	ib.pos = 0
}

// KillWord removes the word before the cursor.
func (ib *InputBuffer) KillWord() {
	if ib.pos <= 0 {
		return
	}
	end := ib.pos
	start := end
	// Skip spaces.
	for start > 0 && ib.buf[start-1] == ' ' {
		start--
	}
	// Skip non-spaces.
	for start > 0 && ib.buf[start-1] != ' ' {
		start--
	}
	copy(ib.buf[start:], ib.buf[end:])
	ib.buf = ib.buf[:len(ib.buf)-(end-start)]
	ib.pos = start
}

// KillToEnd removes from cursor to end of buffer.
func (ib *InputBuffer) KillToEnd() {
	if ib.pos >= len(ib.buf) {
		return
	}
	ib.buf = ib.buf[:ib.pos]
}

// String returns the current buffer content.
func (ib *InputBuffer) String() string {
	return string(ib.buf)
}

// SetString replaces the buffer content and moves cursor to end.
func (ib *InputBuffer) SetString(s string) {
	ib.buf = []rune(s)
	ib.pos = len(ib.buf)
}

// PrevHistory loads the previous history entry.
func (ib *InputBuffer) PrevHistory() {
	if len(ib.history) == 0 {
		return
	}
	if ib.histIdx == -1 {
		ib.histIdx = len(ib.history) - 1
	} else if ib.histIdx > 0 {
		ib.histIdx--
	}
	ib.SetString(ib.history[ib.histIdx])
}

// NextHistory loads the next history entry.
func (ib *InputBuffer) NextHistory() {
	if ib.histIdx == -1 {
		return
	}
	ib.histIdx++
	if ib.histIdx >= len(ib.history) {
		ib.histIdx = -1
		ib.KillLine()
		return
	}
	ib.SetString(ib.history[ib.histIdx])
}

// ClearHistory removes all history entries.
func (ib *InputBuffer) ClearHistory() {
	ib.history = nil
	ib.histIdx = -1
}

// Commit saves the current buffer to history and returns the string.
// Returns empty string for empty input (not saved to history).
// Resets the visual line tracking.
func (ib *InputBuffer) Commit() string {
	s := strings.TrimSpace(string(ib.buf))
	ib.KillLine()
	ib.histIdx = -1
	ib.prevLines = 0
	if s == "" {
		return ""
	}
	// Don't duplicate last history entry.
	if len(ib.history) > 0 && ib.history[len(ib.history)-1] == s {
		return s
	}
	ib.history = append(ib.history, s)
	// Cap history to prevent unbounded growth.
	if len(ib.history) > MaxHistorySize {
		excess := len(ib.history) - MaxHistorySize
		ib.history = ib.history[excess:]
	}
	return s
}

// VisibleLine returns the full visible content (prompt + buffer).
func (ib *InputBuffer) VisibleLine() string {
	return ib.prompt + string(ib.buf)
}

// CursorCol returns the 0-based column where the cursor should be
// within the visible line, accounting for wide characters.
func (ib *InputBuffer) CursorCol() int {
	return runeWidth(ib.prompt) + runeWidth(string(ib.buf[:ib.pos]))
}

// ContentWidth returns the total visual column width of the visible line.
func (ib *InputBuffer) ContentWidth() int {
	return runeWidth(ib.VisibleLine())
}

// Render produces ANSI escape sequences to render the input line
// correctly, supporting multi-line wrapping when content exceeds
// terminal width. It:
//  1. Moves cursor to the first visual line of the input area
//  2. Clears all previously occupied lines
//  3. Writes the prompt + buffer content (letting terminal wrap)
//  4. Repositions cursor to the correct visual line and column
//
// termWidth is the terminal width in columns. If termWidth <= 0,
// it defaults to 80 (standard terminal fallback).
func (ib *InputBuffer) Render(termWidth int) string {
	if termWidth <= 0 {
		termWidth = 80
	}

	contentWidth := ib.ContentWidth()
	cursorCol := ib.CursorCol()

	// Calculate visual line count (ceil division of width by terminal width).
	newLines := 1
	if contentWidth > 0 {
		newLines = (contentWidth + termWidth - 1) / termWidth
		if newLines < 1 {
			newLines = 1
		}
	}

	// Must clear enough lines to handle both growing and shrinking.
	// When newLines > prevLines, the terminal would have scrolled,
	// so we need to account for that by clearing the larger count.
	clearLines := ib.prevLines
	if newLines > clearLines {
		clearLines = newLines
	}

	var sb strings.Builder

	// 1. Move cursor to the first visual line of the previous input area.
	if clearLines > 1 {
		fmt.Fprintf(&sb, "\033[%dA", clearLines-1)
	}
	sb.WriteString("\r")

	// 2. Clear all previously occupied lines (entire width, moving down).
	for i := 0; i < clearLines; i++ {
		sb.WriteString("\033[2K") // clear entire line
		if i < clearLines-1 {
			sb.WriteString("\033[B") // cursor down (no scroll)
		}
	}
	// Move back to the first line.
	if clearLines > 1 {
		fmt.Fprintf(&sb, "\033[%dA", clearLines-1)
	}
	sb.WriteString("\r")

	// 3. Write the full visible content. The terminal will wrap it naturally.
	sb.WriteString(ib.VisibleLine())

	// 4. Reposition cursor to the correct visual line and column.
	curLine := cursorCol / termWidth
	curCol := cursorCol % termWidth

	// After writing, cursor is at the end of the content.
	endLine := contentWidth / termWidth
	endCol := contentWidth % termWidth

	// Move from end position to cursor position if different.
	if curLine != endLine || curCol != endCol {
		// Vertical adjustment.
		if endLine > curLine {
			fmt.Fprintf(&sb, "\033[%dA", endLine-curLine)
		} else if endLine < curLine {
			fmt.Fprintf(&sb, "\033[%dB", curLine-endLine)
		}
		// Horizontal adjustment: go to start of line, then advance.
		if curCol > 0 {
			sb.WriteString("\r")
			fmt.Fprintf(&sb, "\033[%dC", curCol)
		} else {
			sb.WriteString("\r")
		}
	}

	ib.prevLines = newLines
	return sb.String()
}

// RenderInPlace is a convenience wrapper that renders to a terminal's stderr.
// It handles the common case of "render and write" in one call.
func (ib *InputBuffer) RenderInPlace(t *Terminal) {
	w, _ := t.Size()
	t.WriteString(ib.Render(w))
}

// runeWidth returns the visible column width of a string.
// ASCII = 1 per rune, CJK = 2 per rune.
func runeWidth(s string) int {
	w := 0
	for _, r := range s {
		if isWideRune(r) {
			w += 2
		} else {
			w++
		}
	}
	return w
}

// isWideRune returns true for CJK wide characters.
func isWideRune(r rune) bool {
	return unicode.Is(unicode.Han, r) ||
		unicode.Is(unicode.Hangul, r) ||
		unicode.Is(unicode.Katakana, r) ||
		unicode.Is(unicode.Hiragana, r)
}
