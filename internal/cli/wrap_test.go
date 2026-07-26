package cli

import (
	"strings"
	"testing"
)

// --- wrapANSI tests ---

func TestWrapANSIPlainText(t *testing.T) {
	input := "hello world foo bar"
	got := wrapANSIv2(input, 10)
	for _, line := range strings.Split(got, "\n") {
		vis := visibleWidth(line)
		if vis > 10 {
			t.Fatalf("line %q has visible width %d > 10", line, vis)
		}
	}
	if !strings.Contains(got, "\n") {
		t.Fatalf("expected wrapping, got %q", got)
	}
}

func TestWrapANSIShortLine(t *testing.T) {
	input := "short"
	got := wrapANSIv2(input, 80)
	if got != "short" {
		t.Fatalf("expected 'short', got %q", got)
	}
}

func TestWrapANSIWithANSICodes(t *testing.T) {
	// Bold "hello world" with ANSI wrapping
	input := "\033[1mhello world foo bar\033[0m"
	got := wrapANSIv2(input, 10)
	for _, line := range strings.Split(got, "\n") {
		vis := visibleWidth(line)
		if vis > 10 {
			t.Fatalf("line %q has visible width %d > 10", line, vis)
		}
	}
	// Should contain ANSI bold codes
	if !strings.Contains(got, "\033[1m") {
		t.Fatalf("expected bold ANSI in output, got %q", got)
	}
}

func TestWrapANSIWithMultibyteChars(t *testing.T) {
	input := "│ a │ b │ c │ d │"
	got := wrapANSIv2(input, 8)
	for _, line := range strings.Split(got, "\n") {
		vis := visibleWidth(line)
		if vis > 8 {
			t.Fatalf("line %q has visible width %d > 8", line, vis)
		}
	}
	if !strings.Contains(got, "\n") {
		t.Fatalf("expected wrapping, got %q", got)
	}
}

func TestWrapANSIWithTableSeparators(t *testing.T) {
	input := "│ Key │ Behavior │ Notes │"
	got := wrapANSIv2(input, 10)
	for _, line := range strings.Split(got, "\n") {
		vis := visibleWidth(line)
		if vis > 10 {
			t.Fatalf("line %q has visible width %d > 10", line, vis)
		}
	}
}

func TestWrapANSIColorReset(t *testing.T) {
	// Each cell has a color, reset at end
	input := "\033[32m✓\033[0m read_file 123ms \033[31m✗\033[0m failed 456ms"
	got := wrapANSIv2(input, 20)
	for _, line := range strings.Split(got, "\n") {
		vis := visibleWidth(line)
		if vis > 20 {
			t.Fatalf("line %q has visible width %d > 20", line, vis)
		}
	}
	if !strings.Contains(got, "\033[32m") || !strings.Contains(got, "\033[31m") {
		t.Fatalf("expected color codes in output, got %q", got)
	}
}

func TestWrapANSINoSpaceLongWord(t *testing.T) {
	// A single word longer than maxWidth should still be output (hard-wrap)
	input := "superlongwordthatdoesnotfit"
	got := wrapANSIv2(input, 10)
	if !strings.Contains(got, "superlongwordthatdoesnotfit") {
		t.Fatalf("expected long word preserved, got %q", got)
	}
}

func TestWrapANSIDoubleWidthCharacters(t *testing.T) {
	// CJK characters are double-width — no space to break, so output as-is
	input := "你好世界test"
	got := wrapANSIv2(input, 8)
	// No space = no break point, so it should be unchanged
	if got != input {
		t.Fatalf("expected no wrapping without spaces, got %q", got)
	}
}

func TestWrapANSICJKWithSpaces(t *testing.T) {
	input := "你好 世界 test"
	got := wrapANSIv2(input, 8)
	if got == input {
		t.Fatalf("expected wrapping with spaces, got %q", got)
	}
	for _, line := range strings.Split(got, "\n") {
		vis := visibleWidth(line)
		if vis > 8 {
			t.Fatalf("line %q has visible width %d > 8", line, vis)
		}
	}
}

func TestWrapANSICodeBlockWithAnsi(t *testing.T) {
	input := "\033[33m  func main() {\n    fmt.Println(\"hello\")\n  }\033[0m"
	got := wrapANSIv2(input, 30)
	for _, line := range strings.Split(got, "\n") {
		vis := visibleWidth(line)
		if vis > 30 {
			t.Fatalf("line %q has visible width %d > 30", line, vis)
		}
	}
	if !strings.Contains(got, "\033[33m") {
		t.Fatalf("expected ANSI codes preserved")
	}
}

func TestWrapANSIRendersAllContent(t *testing.T) {
	input := "one two three four five six seven eight nine ten"
	got := wrapANSIv2(input, 10)
	plain := stripAnsiOut(got)
	plain = strings.ReplaceAll(plain, "\n", " ")
	plain = strings.Join(strings.Fields(plain), " ")
	if !strings.Contains(plain, "one two three four five six seven eight nine ten") {
		t.Fatalf("lost content: %q", plain)
	}
}

func TestWrapANSIEmpty(t *testing.T) {
	got := wrapANSIv2("", 80)
	if got != "" {
		t.Fatalf("expected empty, got %q", got)
	}
}

func TestWrapANSINoWrapNeeded(t *testing.T) {
	input := "short line"
	got := wrapANSIv2(input, 80)
	if got != "short line" {
		t.Fatalf("expected 'short line', got %q", got)
	}
}

func TestWrapANSIMultipleNewlines(t *testing.T) {
	input := "line one\nline two\nline three"
	got := wrapANSIv2(input, 80)
	lines := strings.Split(got, "\n")
	if len(lines) != 3 {
		t.Fatalf("expected 3 lines, got %d: %q", len(lines), got)
	}
}

func TestWrapANSIMixedContent(t *testing.T) {
	input := "\033[1mBold text\033[0m and \033[32mgreen\033[0m and more text here for wrapping"
	got := wrapANSIv2(input, 20)
	for _, line := range strings.Split(got, "\n") {
		vis := visibleWidth(line)
		if vis > 20 {
			t.Fatalf("line %q has visible width %d > 20", line, vis)
		}
	}
}

// --- visibleWidth tests ---

func TestVisibleWidthPlain(t *testing.T) {
	if w := visibleWidth("hello"); w != 5 {
		t.Fatalf("expected 5, got %d", w)
	}
}

func TestVisibleWidthANSI(t *testing.T) {
	if w := visibleWidth("\033[1mhello\033[0m"); w != 5 {
		t.Fatalf("expected 5, got %d", w)
	}
}

func TestVisibleWidthCJK(t *testing.T) {
	if w := visibleWidth("你好"); w != 4 {
		t.Fatalf("expected 4, got %d", w)
	}
}

func TestVisibleWidthTableChars(t *testing.T) {
	if w := visibleWidth("│ a │"); w != 5 {
		t.Fatalf("expected 5, got %d", w)
	}
}

func TestVisibleWidthEmpty(t *testing.T) {
	if w := visibleWidth(""); w != 0 {
		t.Fatalf("expected 0, got %d", w)
	}
}

// --- stripAnsiOut tests ---

func TestStripAnsiOut(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"\033[1mhello\033[0m", "hello"},
		{"\033[32mgreen\033[0m", "green"},
		{"plain", "plain"},
		{"\033[48;5;236mbg\033[49m", "bg"},
		{"\033[1m\033[32mbold green\033[0m\033[0m", "bold green"},
		{"", ""},
	}
	for _, c := range cases {
		got := stripAnsiOut(c.input)
		if got != c.want {
			t.Errorf("stripAnsiOut(%q) = %q, want %q", c.input, got, c.want)
		}
	}
}
