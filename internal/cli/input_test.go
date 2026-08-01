package cli

import (
	"fmt"
	"strings"
	"testing"
)

// stripANSICtrl removes ANSI escape sequences and control characters
// (\r, \b, etc.) from a string for test assertions.
func stripANSICtrl(s string) string {
	var out strings.Builder
	in := 0
	for _, r := range s {
		if r == '\033' {
			in = 2 // expect '[' next
			continue
		}
		if in > 0 {
			if in == 2 && r == '[' {
				in = 3
				continue
			}
			if in >= 3 {
				if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') {
					in = 0 // end of sequence
				}
				continue
			}
		}
		// Skip common control characters from rendering.
		if r == '\r' || r == '\b' {
			continue
		}
		out.WriteRune(r)
	}
	return out.String()
}

func TestInputBufferNew(t *testing.T) {
	ib := NewInputBuffer("> ")
	if ib.Prompt() != "> " {
		t.Fatalf("expected prompt '> ', got %q", ib.Prompt())
	}
	if ib.Len() != 0 {
		t.Fatalf("expected empty buffer, got len %d", ib.Len())
	}
	if ib.Pos() != 0 {
		t.Fatalf("expected pos 0, got %d", ib.Pos())
	}
}

func TestInputBufferInsertAndMove(t *testing.T) {
	ib := NewInputBuffer("> ")
	ib.Insert('a')
	ib.Insert('b')
	ib.Insert('c')
	if ib.String() != "abc" {
		t.Fatalf("expected abc, got %q", ib.String())
	}
	if ib.Pos() != 3 {
		t.Fatalf("expected pos 3, got %d", ib.Pos())
	}

	ib.MoveLeft()
	ib.MoveLeft()
	ib.Insert('X')
	if ib.String() != "aXbc" {
		t.Fatalf("expected aXbc, got %q", ib.String())
	}
	if ib.Pos() != 2 {
		t.Fatalf("expected pos 2, got %d", ib.Pos())
	}
}

func TestInputBufferBackspace(t *testing.T) {
	ib := NewInputBuffer("> ")
	ib.SetString("hello")
	ib.Backspace() // remove 'o'
	if ib.String() != "hell" {
		t.Fatalf("expected hell, got %q", ib.String())
	}
	ib.MoveHome()
	ib.Backspace() // no-op at start
	if ib.String() != "hell" {
		t.Fatalf("expected still hell, got %q", ib.String())
	}
}

func TestInputBufferDelete(t *testing.T) {
	ib := NewInputBuffer("> ")
	ib.SetString("hello")
	ib.MoveHome()
	ib.Delete() // remove 'h'
	if ib.String() != "ello" {
		t.Fatalf("expected ello, got %q", ib.String())
	}
	ib.MoveEnd()
	ib.Delete() // no-op at end
	if ib.String() != "ello" {
		t.Fatalf("expected still ello, got %q", ib.String())
	}
}

func TestInputBufferHomeEnd(t *testing.T) {
	ib := NewInputBuffer("> ")
	ib.SetString("hello world")
	ib.MoveHome()
	if ib.Pos() != 0 {
		t.Fatalf("expected pos 0, got %d", ib.Pos())
	}
	ib.MoveEnd()
	if ib.Pos() != 11 {
		t.Fatalf("expected pos 11, got %d", ib.Pos())
	}
}

func TestInputBufferKillLine(t *testing.T) {
	ib := NewInputBuffer("> ")
	ib.SetString("hello")
	ib.KillLine()
	if ib.String() != "" {
		t.Fatalf("expected empty, got %q", ib.String())
	}
	if ib.Pos() != 0 {
		t.Fatalf("expected pos 0, got %d", ib.Pos())
	}
}

func TestInputBufferKillWord(t *testing.T) {
	tests := []struct {
		input    string
		startPos int // -1 means end
		expected string
		expPos   int
	}{
		{"hello world", -1, "hello ", 6},     // at end, kills "world"
		{"hello world", 0, "hello world", 0}, // at home, no-op
		{"abc   def", -1, "abc   ", 6},       // skips spaces then kills "def"
	}
	for _, tt := range tests {
		ib := NewInputBuffer("> ")
		ib.SetString(tt.input)
		if tt.startPos >= 0 {
			ib.pos = tt.startPos
		}
		ib.KillWord()
		if ib.String() != tt.expected || ib.Pos() != tt.expPos {
			t.Errorf("KillWord(%q, %d): got %q (pos %d), want %q (pos %d)",
				tt.input, tt.startPos, ib.String(), ib.Pos(), tt.expected, tt.expPos)
		}
	}
}

func TestInputBufferKillToEnd(t *testing.T) {
	ib := NewInputBuffer("> ")
	ib.SetString("hello world")
	ib.MoveTo(6) // after "hello "
	ib.KillToEnd()
	if ib.String() != "hello " {
		t.Fatalf("expected 'hello ', got %q", ib.String())
	}
	if ib.Pos() != 6 {
		t.Fatalf("expected pos 6, got %d", ib.Pos())
	}
}

// MoveTo sets cursor to rune position.
func (ib *InputBuffer) MoveTo(pos int) {
	if pos < 0 {
		pos = 0
	}
	if pos > len(ib.buf) {
		pos = len(ib.buf)
	}
	ib.pos = pos
}

func TestInputBufferHistory(t *testing.T) {
	ib := NewInputBuffer("> ")

	// Commit some entries.
	c1 := ib.Commit() // empty, not saved
	if c1 != "" {
		t.Fatalf("expected empty from empty commit")
	}
	ib.SetString("first")
	c2 := ib.Commit()
	if c2 != "first" {
		t.Fatalf("expected 'first', got %q", c2)
	}
	ib.SetString("second")
	ib.Commit()
	ib.SetString("third")
	ib.Commit()

	// Browse history: prev, prev, prev, next, next.
	ib.PrevHistory() // "third"
	if ib.String() != "third" {
		t.Fatalf("expected 'third', got %q", ib.String())
	}
	ib.PrevHistory() // "second"
	if ib.String() != "second" {
		t.Fatalf("expected 'second', got %q", ib.String())
	}
	ib.PrevHistory() // "first"
	if ib.String() != "first" {
		t.Fatalf("expected 'first', got %q", ib.String())
	}
	// Should stay at first (no more history).
	ib.PrevHistory()
	if ib.String() != "first" {
		t.Fatalf("expected 'first' (no wrap), got %q", ib.String())
	}

	ib.NextHistory() // "second"
	if ib.String() != "second" {
		t.Fatalf("expected 'second', got %q", ib.String())
	}
	ib.NextHistory() // "third"
	ib.NextHistory() // past end -> fresh line
	if ib.String() != "" {
		t.Fatalf("expected empty (fresh line), got %q", ib.String())
	}
}

func TestInputBufferCursorCol(t *testing.T) {
	ib := NewInputBuffer("$ ")
	ib.SetString("hi")
	// prompt "$ " = width 2
	// cursor at end: width of "$ hi" = 4
	if col := ib.CursorCol(); col != 4 {
		t.Fatalf("expected cursor col 4, got %d", col)
	}
	ib.MoveHome()
	if col := ib.CursorCol(); col != 2 {
		t.Fatalf("expected cursor col 2 (just prompt), got %d", col)
	}
}

func TestInputBufferCursorColWide(t *testing.T) {
	ib := NewInputBuffer("> ")
	ib.SetString("你好") // 2 CJK chars, each width 2
	// prompt "> " = width 2, buf "你好" = width 4
	// cursor col = 2 + 4 = 6
	if col := ib.CursorCol(); col != 6 {
		t.Fatalf("expected cursor col 6, got %d", col)
	}
	ib.MoveHome()
	if col := ib.CursorCol(); col != 2 {
		t.Fatalf("expected cursor col 2, got %d", col)
	}
}

func TestInputBufferVisibleLine(t *testing.T) {
	ib := NewInputBuffer("> ")
	ib.SetString("hello")
	if vl := ib.VisibleLine(); vl != "> hello" {
		t.Fatalf("expected '> hello', got %q", vl)
	}
}

// --- Render tests (multi-line wrapping) ---

func TestRenderEmpty(t *testing.T) {
	ib := NewInputBuffer("> ")
	rendered := ib.Render(80)
	// Should contain the prompt
	content := stripANSICtrl(rendered)
	if !strings.Contains(content, "> ") {
		t.Fatalf("render should contain prompt '> ', got %q", content)
	}
}

func TestRenderSingleLine(t *testing.T) {
	ib := NewInputBuffer("$ ")
	ib.SetString("hello")
	rendered := ib.Render(80)
	content := stripANSICtrl(rendered)
	if content != "$ hello" {
		t.Fatalf("expected '$ hello', got %q", content)
	}
	if ib.prevLines != 1 {
		t.Fatalf("expected prevLines=1, got %d", ib.prevLines)
	}
}

func TestRenderMultiLineWrapping(t *testing.T) {
	// Terminal width of 10 columns. Prompt "$ " = 2 cols.
	// Long string: "1234567890abcde" = 15 cols. Total width = 2+15 = 17.
	// Lines: ceil(17/10) = 2.
	ib := NewInputBuffer("$ ")
	ib.SetString("1234567890abcde")
	rendered := ib.Render(10)

	// Verify content is in the output
	content := stripANSICtrl(rendered)
	if !strings.Contains(content, "$ 1234567890abcde") {
		t.Fatalf("render should contain full content, got %q", content)
	}
	if ib.prevLines != 2 {
		t.Fatalf("expected prevLines=2 (17 cols / 10 term), got %d", ib.prevLines)
	}
}

func TestRenderWideCharsWrap(t *testing.T) {
	// Terminal width of 8 cols. Prompt "> " = 2 cols.
	// CJK chars: "你好世界" = 4 chars × 2 width = 8 cols.
	// Total: 2+8=10. Lines: ceil(10/8) = 2.
	ib := NewInputBuffer("> ")
	ib.SetString("你好世界") // 8 cols wide
	rendered := ib.Render(8)
	content := stripANSICtrl(rendered)
	if !strings.Contains(content, "> 你好世界") {
		t.Fatalf("render should contain CJK content, got %q", content)
	}
	if ib.prevLines != 2 {
		t.Fatalf("expected prevLines=2 (10 cols / 8 term), got %d", ib.prevLines)
	}
}

func TestRenderExactLineWidth(t *testing.T) {
	ib := NewInputBuffer("> ")
	ib.SetString("1234")
	rendered := ib.Render(6)
	content := stripANSICtrl(rendered)
	if !strings.Contains(content, "> 1234") {
		t.Fatalf("render should contain content, got %q", content)
	}
	if ib.prevLines != 1 {
		t.Fatalf("expected prevLines=1 (6 cols / 6 term), got %d", ib.prevLines)
	}
}

func TestRenderZeroTermWidth(t *testing.T) {
	// Zero/wrong terminal width should fall back to 80.
	ib := NewInputBuffer("> ")
	ib.SetString("hello")
	rendered := ib.Render(0)
	content := stripANSICtrl(rendered)
	if !strings.Contains(content, "> hello") {
		t.Fatalf("render with termWidth=0 should still work, got %q", content)
	}
	if ib.prevLines != 1 {
		t.Fatalf("expected prevLines=1, got %d", ib.prevLines)
	}
}

func TestRenderNegativeTermWidth(t *testing.T) {
	ib := NewInputBuffer("> ")
	ib.SetString("hello")
	rendered := ib.Render(-1)
	content := stripANSICtrl(rendered)
	if !strings.Contains(content, "> hello") {
		t.Fatalf("render with negative termWidth should still work, got %q", content)
	}
}

func TestRenderCursorAtStart(t *testing.T) {
	// Cursor at start (pos 0) with long content wrapping.
	ib := NewInputBuffer("$ ")
	ib.SetString("abcdefghijklmno") // 15 cols + 2 prompt = 17
	ib.MoveHome()                   // cursor at prompt end (col 2)
	rendered := ib.Render(10)

	// Cursor should be at (0, 2) - first line, column 2
	if ib.prevLines != 2 {
		t.Fatalf("expected prevLines=2, got %d", ib.prevLines)
	}
	// Check that ANSI sequence positions cursor at col 2
	if !strings.Contains(rendered, "\033[2C") && !strings.Contains(rendered, "\033[2C") {
		// ANSI: after writing, we need to check cursor column positioning.
		// The render moves from end position back to (0,2).
		// endLine=1 (17/10), endCol=7 (17%10).
		// curLine=0 (2/10), curCol=2 (2%10).
		// Move up 1, then right 2.
		if !strings.Contains(rendered, "\033[1A") || !strings.Contains(rendered, "\033[2C") {
			t.Fatalf("expected cursor repositioning (up 1, right 2) in render, got %q", rendered)
		}
	}
}

func TestRenderCursorAtEnd(t *testing.T) {
	// Cursor at end with long wrapping content.
	ib := NewInputBuffer("$ ")
	ib.SetString("abcdefghijklmno") // 15 cols + 2 prompt = 17
	// Cursor already at end
	rendered := ib.Render(10)

	// endLine=1 (17/10), endCol=7 (17%10)
	// curLine=1 (17/10), curCol=7 (17%10) - same, no repositioning needed
	content := stripANSICtrl(rendered)
	if !strings.Contains(content, "$ abcdefghijklmno") {
		t.Fatalf("missing content in render")
	}
}

func TestRenderConsecutiveSameLength(t *testing.T) {
	// Two renders with same content length should correctly clear and redraw.
	ib := NewInputBuffer("> ")
	ib.SetString("short")
	r1 := ib.Render(10)
	_ = r1
	if ib.prevLines != 1 {
		t.Fatalf("after first render, expected prevLines=1, got %d", ib.prevLines)
	}

	// Same content length again
	ib.SetString("world")
	r2 := ib.Render(10)
	content := stripANSICtrl(r2)
	if !strings.Contains(content, "> world") {
		t.Fatalf("second render should contain new content, got %q", content)
	}
}

func TestRenderShrinkLines(t *testing.T) {
	// First render spans 3 lines, second spans 1 line (shorter).
	ib := NewInputBuffer("> ")
	ib.SetString("1234567890123456789012345") // 25 + 2 = 27, termWidth=10 -> 3 lines
	ib.Render(10)
	if ib.prevLines != 3 {
		t.Fatalf("expected prevLines=3, got %d", ib.prevLines)
	}

	// Shrink to a single line
	ib.SetString("hi")
	r2 := ib.Render(10)
	content := stripANSICtrl(r2)
	if !strings.Contains(content, "> hi") {
		t.Fatalf("shorter render should show new content, got %q", content)
	}
	if ib.prevLines != 1 {
		t.Fatalf("after shrink, expected prevLines=1, got %d", ib.prevLines)
	}
}

func TestRenderGrowLines(t *testing.T) {
	// First render spans 1 line, second spans 3 lines (longer).
	ib := NewInputBuffer("> ")
	ib.SetString("hi")
	ib.Render(10)
	if ib.prevLines != 1 {
		t.Fatalf("expected prevLines=1, got %d", ib.prevLines)
	}

	// Grow to 3 lines
	ib.SetString("1234567890123456789012345") // 25 + 2 = 27, termWidth=10 -> 3 lines
	r2 := ib.Render(10)
	content := stripANSICtrl(r2)
	if !strings.Contains(content, "1234567890123456789012345") {
		t.Fatalf("grown render should show full content, got %q", content)
	}
	if ib.prevLines != 3 {
		t.Fatalf("after grow, expected prevLines=3, got %d", ib.prevLines)
	}
}

func TestRenderLargeWidth(t *testing.T) {
	// Very wide terminal - no wrapping.
	ib := NewInputBuffer("> ")
	ib.SetString("a very long but not wrapping line of text")
	r := ib.Render(200)
	if ib.prevLines != 1 {
		t.Fatalf("expected prevLines=1 with wide terminal, got %d", ib.prevLines)
	}
	content := stripANSICtrl(r)
	if !strings.Contains(content, ib.VisibleLine()) {
		t.Fatalf("render should match visible line")
	}
}

func TestContentWidth(t *testing.T) {
	ib := NewInputBuffer("> ")
	ib.SetString("hello")
	// prompt "> " = 2, "hello" = 5, total = 7
	if cw := ib.ContentWidth(); cw != 7 {
		t.Fatalf("expected ContentWidth=7, got %d", cw)
	}
	ib.SetString("你好")
	// prompt "> " = 2, "你好" = 4, total = 6
	if cw := ib.ContentWidth(); cw != 6 {
		t.Fatalf("expected ContentWidth=6 for CJK, got %d", cw)
	}
	ib.SetString("")
	// prompt "> " = 2
	if cw := ib.ContentWidth(); cw != 2 {
		t.Fatalf("expected ContentWidth=2 for empty buf, got %d", cw)
	}
}

func TestClearHistory(t *testing.T) {
	ib := NewInputBuffer("> ")
	ib.SetString("first")
	ib.Commit()
	ib.SetString("second")
	ib.Commit()
	if len(ib.history) != 2 {
		t.Fatalf("expected 2 history entries, got %d", len(ib.history))
	}
	ib.ClearHistory()
	if len(ib.history) != 0 {
		t.Fatalf("expected 0 history entries after ClearHistory, got %d", len(ib.history))
	}
	if ib.histIdx != -1 {
		t.Fatalf("expected histIdx=-1 after ClearHistory, got %d", ib.histIdx)
	}
}

func TestHistoryCap(t *testing.T) {
	ib := NewInputBuffer("> ")
	// Fill beyond MaxHistorySize.
	for i := range MaxHistorySize + 10 {
		ib.SetString(fmt.Sprintf("entry-%d", i))
		ib.Commit()
	}
	if len(ib.history) > MaxHistorySize {
		t.Fatalf("history exceeded cap: %d entries (max %d)", len(ib.history), MaxHistorySize)
	}
	if len(ib.history) != MaxHistorySize {
		t.Fatalf("expected exactly %d history entries (dropping oldest), got %d", MaxHistorySize, len(ib.history))
	}
	// Should have dropped the oldest 10.
	if ib.history[0] != "entry-10" {
		t.Fatalf("expected oldest entry 'entry-10', got %q", ib.history[0])
	}
	if ib.history[len(ib.history)-1] != fmt.Sprintf("entry-%d", MaxHistorySize+9) {
		t.Fatalf("expected newest entry 'entry-%d', got %q", MaxHistorySize+9, ib.history[len(ib.history)-1])
	}
}

func TestPrevNextHistoryEdgeCases(t *testing.T) {
	ib := NewInputBuffer("> ")
	// No history - should be no-ops.
	ib.PrevHistory() // no-op
	if ib.String() != "" {
		t.Fatalf("expected empty buffer after PrevHistory on empty history")
	}
	ib.NextHistory() // no-op
	if ib.String() != "" {
		t.Fatalf("expected empty buffer after NextHistory on empty history")
	}

	// One entry.
	ib.SetString("only")
	ib.Commit()
	ib.PrevHistory()
	if ib.String() != "only" {
		t.Fatalf("expected 'only', got %q", ib.String())
	}
	ib.NextHistory()
	if ib.String() != "" {
		t.Fatalf("expected empty after NextHistory past newest, got %q", ib.String())
	}
	// Should be able to go back again.
	ib.PrevHistory()
	if ib.String() != "only" {
		t.Fatalf("expected 'only' again, got %q", ib.String())
	}
}

func TestRuneWidth(t *testing.T) {
	tests := []struct {
		input string
		want  int
	}{
		{"", 0},
		{"a", 1},
		{"abc", 3},
		{"你好", 4},        // 2 CJK × 2
		{"a你b好c", 7},     // 3 ASCII + 4 CJK
		{"Hello 世界", 10}, // 7 ASCII + 4 CJK (space = 1) -> wait: H(1)e(1)l(1)l(1)o(1) (1)世(2)界(2) = 10
	}
	for _, tt := range tests {
		got := runeWidth(tt.input)
		if got != tt.want {
			t.Errorf("runeWidth(%q) = %d, want %d", tt.input, got, tt.want)
		}
	}
}

func TestIsWideRune(t *testing.T) {
	if !isWideRune('中') {
		t.Fatal("expected CJK char '中' to be wide")
	}
	if !isWideRune('한') {
		t.Fatal("expected Hangul '한' to be wide")
	}
	if !isWideRune('ア') {
		t.Fatal("expected Katakana 'ア' to be wide")
	}
	if !isWideRune('あ') {
		t.Fatal("expected Hiragana 'あ' to be wide")
	}
	if isWideRune('a') {
		t.Fatal("expected ASCII 'a' to not be wide")
	}
	if isWideRune(' ') {
		t.Fatal("expected space to not be wide")
	}
}

func TestSetString(t *testing.T) {
	ib := NewInputBuffer("> ")
	ib.SetString("hello world")
	if ib.String() != "hello world" {
		t.Fatalf("expected 'hello world', got %q", ib.String())
	}
	if ib.Pos() != 11 {
		t.Fatalf("expected pos 11, got %d", ib.Pos())
	}
}

func TestSetPrompt(t *testing.T) {
	ib := NewInputBuffer("> ")
	ib.SetPrompt("$ ")
	if ib.Prompt() != "$ " {
		t.Fatalf("expected '$ ', got %q", ib.Prompt())
	}
}

func TestNoDuplicateHistory(t *testing.T) {
	ib := NewInputBuffer("> ")
	ib.SetString("hello")
	ib.Commit()
	ib.SetString("hello")
	c2 := ib.Commit()
	if c2 != "hello" {
		t.Fatalf("expected 'hello', got %q", c2)
	}
	// History should only have one entry.
	if len(ib.history) != 1 {
		t.Fatalf("expected 1 history entry, got %d", len(ib.history))
	}
}

// Benchmark the render method for performance.
func BenchmarkRenderShort(b *testing.B) {
	ib := NewInputBuffer("> ")
	ib.SetString("hello world")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ib.Render(80)
	}
}

func BenchmarkRenderLong(b *testing.B) {
	ib := NewInputBuffer("> ")
	ib.SetString("hello world " + strings.Repeat("x", 500))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ib.Render(80)
	}
}

func BenchmarkRenderCJK(b *testing.B) {
	ib := NewInputBuffer("> ")
	ib.SetString(strings.Repeat("你好", 100)) // 200 CJK chars = 400 width
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ib.Render(80)
	}
}
