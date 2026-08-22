package clichat

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// --- wrapANSI tests ---

func TestWrapANSIPlainText(t *testing.T) {
	input := "hello world foo bar"
	got := WrapANSIv2(input, 10)
	for _, line := range strings.Split(got, "\n") {
		vis := VisibleWidth(line)
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
	got := WrapANSIv2(input, 80)
	if got != "short" {
		t.Fatalf("expected 'short', got %q", got)
	}
}

func TestWrapANSIWithANSICodes(t *testing.T) {
	// Bold "hello world" with ANSI wrapping
	input := "\033[1mhello world foo bar\033[0m"
	got := WrapANSIv2(input, 10)
	for _, line := range strings.Split(got, "\n") {
		vis := VisibleWidth(line)
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
	// CJK wide runes (width 2) with spaces: wrap at word boundaries within maxWidth.
	// (Rendered table rows with │ are hard-truncated elsewhere; this covers normal wrap.)
	input := "你好 世界 测试 宽度 对齐"
	got := WrapANSIv2(input, 8)
	for _, line := range strings.Split(got, "\n") {
		vis := VisibleWidth(line)
		if vis > 8 {
			t.Fatalf("line %q has visible width %d > 8", line, vis)
		}
	}
	if !strings.Contains(got, "\n") {
		t.Fatalf("expected wrapping for spaced CJK, got %q", got)
	}
}

func TestWrapANSIWithTableSeparators(t *testing.T) {
	input := "│ Key │ Behavior │ Notes │"
	got := WrapANSIv2(input, 10)
	for _, line := range strings.Split(got, "\n") {
		vis := VisibleWidth(line)
		if vis > 10 {
			t.Fatalf("line %q has visible width %d > 10", line, vis)
		}
	}
}

func TestWrapANSIColorReset(t *testing.T) {
	// Each cell has a color, reset at end
	input := "\033[32m✓\033[0m read_file 123ms \033[31m✗\033[0m failed 456ms"
	got := WrapANSIv2(input, 20)
	for _, line := range strings.Split(got, "\n") {
		vis := VisibleWidth(line)
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
	got := WrapANSIv2(input, 10)
	if !strings.Contains(got, "superlongwordthatdoesnotfit") {
		t.Fatalf("expected long word preserved, got %q", got)
	}
}

func TestWrapANSIDoubleWidthCharacters(t *testing.T) {
	// CJK characters are double-width - no space to break, so output as-is
	input := "你好世界test"
	got := WrapANSIv2(input, 8)
	// No space = no break point, so it should be unchanged
	if got != input {
		t.Fatalf("expected no wrapping without spaces, got %q", got)
	}
}

func TestWrapANSICJKWithSpaces(t *testing.T) {
	input := "你好 世界 test"
	got := WrapANSIv2(input, 8)
	if got == input {
		t.Fatalf("expected wrapping with spaces, got %q", got)
	}
	for _, line := range strings.Split(got, "\n") {
		vis := VisibleWidth(line)
		if vis > 8 {
			t.Fatalf("line %q has visible width %d > 8", line, vis)
		}
	}
}

func TestWrapANSICodeBlockWithAnsi(t *testing.T) {
	input := "\033[33m  func main() {\n    fmt.Println(\"hello\")\n  }\033[0m"
	got := WrapANSIv2(input, 30)
	for _, line := range strings.Split(got, "\n") {
		vis := VisibleWidth(line)
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
	got := WrapANSIv2(input, 10)
	plain := stripAnsiOut(got)
	plain = strings.ReplaceAll(plain, "\n", " ")
	plain = strings.Join(strings.Fields(plain), " ")
	if !strings.Contains(plain, "one two three four five six seven eight nine ten") {
		t.Fatalf("lost content: %q", plain)
	}
}

func TestWrapANSIEmpty(t *testing.T) {
	got := WrapANSIv2("", 80)
	if got != "" {
		t.Fatalf("expected empty, got %q", got)
	}
}

func TestWrapANSINoWrapNeeded(t *testing.T) {
	input := "short line"
	got := WrapANSIv2(input, 80)
	if got != "short line" {
		t.Fatalf("expected 'short line', got %q", got)
	}
}

func TestWrapANSIMultipleNewlines(t *testing.T) {
	input := "line one\nline two\nline three"
	got := WrapANSIv2(input, 80)
	lines := strings.Split(got, "\n")
	if len(lines) != 3 {
		t.Fatalf("expected 3 lines, got %d: %q", len(lines), got)
	}
}

func TestWrapANSIMixedContent(t *testing.T) {
	input := "\033[1mBold text\033[0m and \033[32mgreen\033[0m and more text here for wrapping"
	got := WrapANSIv2(input, 20)
	for _, line := range strings.Split(got, "\n") {
		vis := VisibleWidth(line)
		if vis > 20 {
			t.Fatalf("line %q has visible width %d > 20", line, vis)
		}
	}
}

// --- visibleWidth tests ---

func TestVisibleWidthPlain(t *testing.T) {
	if w := VisibleWidth("hello"); w != 5 {
		t.Fatalf("expected 5, got %d", w)
	}
}

func TestVisibleWidthANSI(t *testing.T) {
	if w := VisibleWidth("\033[1mhello\033[0m"); w != 5 {
		t.Fatalf("expected 5, got %d", w)
	}
}

func TestVisibleWidthCJK(t *testing.T) {
	if w := VisibleWidth("你好"); w != 4 {
		t.Fatalf("expected 4, got %d", w)
	}
}

func TestVisibleWidthTableChars(t *testing.T) {
	if w := VisibleWidth("│ a │"); w != 5 {
		t.Fatalf("expected 5, got %d", w)
	}
}

func TestVisibleWidthEmpty(t *testing.T) {
	if w := VisibleWidth(""); w != 0 {
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

// --- wrapANSIv2 regression tests (cli-wrap-v2-quadratic-remeasure fix) ---

// TestWrapANSIv2LongLineWithinBudget is the regression guard for the O(n^2)
// re-measurement in wrapLineV2. The input is one ~200 KiB line made of a long
// unbroken run (the pre-fix quadratic trigger: without a space no flush ever
// resets currentLine, so every byte re-measured the whole accumulated line)
// followed by spaced tokens that wrap. Pre-fix that costs ~runLen^2/2 ~ 1.6e10
// byte-steps (seconds to tens of seconds); post-fix the wrap is O(n) and
// finishes in well under a millisecond. The generous 3s absolute budget
// follows the repo's INV-TUI-22 timing-budget pattern and leaves >1000x
// headroom for the linear form.
func TestWrapANSIv2LongLineWithinBudget(t *testing.T) {
	const maxWidth = 40
	// 180000-byte unbreakable run + 20000 bytes of spaced tokens (~195 KiB single line).
	run := strings.Repeat("a", 180000)
	words := strings.Repeat("word ", 4000)
	input := run + " " + words

	start := time.Now()
	got := WrapANSIv2(input, maxWidth)
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("wrapANSIv2 took %v on a 200 KiB line; wrapping must be O(n)", elapsed)
	}

	lines := strings.Split(got, "\n")
	// The unbreakable run is preserved whole as the first line.
	if lines[0] != run {
		t.Fatalf("unbreakable run not preserved whole: first line len %d, want %d", len(lines[0]), len(run))
	}
	// Every wrapped (breakable) line stays within maxWidth.
	for i := 1; i < len(lines); i++ {
		if VisibleWidth(lines[i]) > maxWidth {
			t.Fatalf("wrapped line %d has visible width %d > %d", i, VisibleWidth(lines[i]), maxWidth)
		}
	}
	// All input tokens survive in order.
	wantTokens := strings.Fields(input)
	gotTokens := strings.Fields(stripAnsiOut(got))
	if len(gotTokens) != len(wantTokens) {
		t.Fatalf("token count: got %d want %d", len(gotTokens), len(wantTokens))
	}
	for i := range wantTokens {
		if gotTokens[i] != wantTokens[i] {
			t.Fatalf("token %d: got %q want %q", i, gotTokens[i], wantTokens[i])
		}
	}
}

// TestWrapANSIv2ExactOutputMixed pins the byte-exact output of the rewrite
// for input mixing ANSI SGR codes, CJK wide runes, tabs, and repeated spaces
// across wrap boundaries. Case 1 is produced identically by the old and new
// implementations (3-byte wide runes never diverge: a partial per-byte
// measurement never exceeds the completed-rune width). Case 2 is the one
// reachable divergence: a 4-byte wide rune (CJK Ext B) that completes to
// exactly maxWidth. The old per-byte re-measurement read the rune's third
// partial byte as width maxWidth+1 (each partial byte measured 1) and broke
// the line at the last space; the rewrite measures the rune once, keeps the
// line intact, and stays within width. The golden value pins the rewrite's
// output.
func TestWrapANSIv2ExactOutputMixed(t *testing.T) {
	cases := []struct {
		name     string
		input    string
		maxWidth int
		want     string
	}{
		{
			name:     "ansi cjk tab repeated spaces",
			input:    "\033[1m你 好\tcd  末尾",
			maxWidth: 10,
			want:     "\033[1m你 好\tcd \n末尾",
		},
		{
			name:     "four byte wide rune at exact boundary",
			input:    "ab  \U00020000",
			maxWidth: 6,
			want:     "ab  \U00020000",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := WrapANSIv2(c.input, c.maxWidth)
			if got != c.want {
				t.Fatalf("wrapANSIv2(%q, %d) = %q, want %q", c.input, c.maxWidth, got, c.want)
			}
			for _, line := range strings.Split(got, "\n") {
				if VisibleWidth(line) > c.maxWidth {
					t.Fatalf("line %q has visible width %d > %d", line, VisibleWidth(line), c.maxWidth)
				}
			}
		})
	}
}

// TestWrapANSIv2WideRuneAtWrapBoundary places wide runes straddling the
// maxWidth boundary and asserts the rune is never split across lines (every
// output line is valid UTF-8), every line stays within maxWidth, and the
// whitespace-separated token stream survives in order. The exact output is
// pinned per case so the ANSI-preserving wrap and the space-byte cut points
// cannot regress (the reported failure compared stripped output tokens against
// raw input tokens; both sides are now stripped, and the golden bytes pin the
// corrected output for the ANSI-prefixed wide-rune case).
func TestWrapANSIv2WideRuneAtWrapBoundary(t *testing.T) {
	cases := []struct {
		input    string
		maxWidth int
		want     string // exact expected output
	}{
		{"ab 你好 cd", 6, "ab\n你好\ncd"},
		{"你 世界 测试", 7, "你 世界\n测试"},
		{"ab  \U00020000 cd", 6, "ab  \U00020000\ncd"},
		{"\033[32m你 好\033[0m world", 6, "\033[32m你 好\033[0m\nworld"},
	}
	for _, c := range cases {
		got := WrapANSIv2(c.input, c.maxWidth)
		if got != c.want {
			t.Fatalf("input %q: wrapANSIv2(_, %d) = %q, want %q", c.input, c.maxWidth, got, c.want)
		}
		lines := strings.Split(got, "\n")
		for _, line := range lines {
			if !utf8.ValidString(line) {
				t.Fatalf("input %q: line %q is not valid UTF-8 (a wide rune was split)", c.input, line)
			}
			if VisibleWidth(line) > c.maxWidth {
				t.Fatalf("input %q: line %q has visible width %d > %d", c.input, line, VisibleWidth(line), c.maxWidth)
			}
		}
		var gotTokens []string
		for _, line := range lines {
			gotTokens = append(gotTokens, strings.Fields(stripAnsiOut(line))...)
		}
		// Strip ANSI from the input too: the gotTokens side is stripped, so the
		// streams are only comparable when both sides drop the zero-width codes.
		wantTokens := strings.Fields(stripAnsiOut(c.input))
		if len(gotTokens) != len(wantTokens) {
			t.Fatalf("input %q: token count got %d want %d", c.input, len(gotTokens), len(wantTokens))
		}
		for i := range wantTokens {
			if gotTokens[i] != wantTokens[i] {
				t.Fatalf("input %q: token %d got %q want %q", c.input, i, gotTokens[i], wantTokens[i])
			}
		}
	}
}
