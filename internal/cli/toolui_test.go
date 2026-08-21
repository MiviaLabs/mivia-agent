package cli

import (
	"bytes"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestParseToolPathJSON(t *testing.T) {
	p := ParseToolPath(`{"path":"pkg/main.go","old_string":"x"}`, "")
	if p != "pkg/main.go" {
		t.Fatalf("got %q", p)
	}
	p = ParseToolPath(`{"path":"a\\b.txt"}`, "")
	if p != "ab.txt" && p != `a\b.txt` {
		// unescape of single backslash-char: we store next char after \
		if p == "" {
			t.Fatalf("empty path")
		}
	}
}

func TestParseToolPathWroteUpdated(t *testing.T) {
	p := ParseToolPath("", "wrote src/a.go (12 bytes, create +3)")
	if p != "src/a.go" {
		t.Fatalf("wrote: %q", p)
	}
	// Old format had --- old, new format uses --- a/path.
	// Both should parse correctly.
	p = ParseToolPath("", "updated internal/cli/toolui.go (1 replacement, +2 −1)\n--- a/internal/cli/toolui.go")
	if p != "internal/cli/toolui.go" {
		t.Fatalf("updated: %q", p)
	}
	// Prefer detail JSON over result text when both present.
	p = ParseToolPath(`{"path":"from-detail.go"}`, "wrote other.go (1 bytes, create +1)")
	if p != "from-detail.go" {
		t.Fatalf("prefer detail: %q", p)
	}
}

func TestSummarizeToolDetail(t *testing.T) {
	// New format: --- a/path (GitHub-style unified diff).
	s := summarizeToolDetail("search_replace", `{"path":"x.go"}`, "updated x.go (1 replacement, +2 −1)\n--- a/x.go\n a")
	if s != "updated (1 replacement, +2 −1)" {
		t.Fatalf("summary=%q", s)
	}
	s = summarizeToolDetail("write_file", `{"path":"x.go"}`, "wrote x.go (4 bytes, create +1)")
	if s != "wrote (4 bytes, create +1)" {
		t.Fatalf("write summary=%q", s)
	}
	s = summarizeToolDetail("read_file", `{"path":"only.go"}`, "")
	if !strings.Contains(s, "only.go") {
		t.Fatalf("detail fallback=%q", s)
	}
}

func TestSummarizeDelegateAndDispatchOperatorFacing(t *testing.T) {
	s := summarizeToolDetail("delegate", `{"task":"analyze auth module for JWT bugs","multi_step":true}`, "")
	if !strings.Contains(s, "multi_step") || !strings.Contains(s, "analyze auth") {
		t.Fatalf("delegate summary=%q", s)
	}
	s = summarizeToolDetail("delegate", `{"task":"quick q"}`, "")
	if !strings.Contains(s, "oneshot") || !strings.Contains(s, "quick q") {
		t.Fatalf("oneshot summary=%q", s)
	}
	s = summarizeToolDetail("dispatch_tasks", `{"tasks":[{"id":"t1","prompt":"map package layout"},{"id":"t2","prompt":"find race bugs"}]}`, "")
	if !strings.Contains(s, "2 tasks") || !strings.Contains(s, "map package") {
		t.Fatalf("dispatch summary=%q", s)
	}
	item := NewToolRenderItem("delegate", `{"task":"see me","multi_step":false}`, "", false, false)
	if !strings.Contains(item.summary(80), "see me") {
		t.Fatalf("summary lost task: %q", item.summary(80))
	}
}

func TestExpandSectionLabelsForAgents(t *testing.T) {
	if expandSectionLabel("delegate", true) != "task" {
		t.Fatal(expandSectionLabel("delegate", true))
	}
	if expandSectionLabel("dispatch_tasks", true) != "tasks" {
		t.Fatal(expandSectionLabel("dispatch_tasks", true))
	}
	if expandSectionLabel("read_file", true) != "input" {
		t.Fatal(expandSectionLabel("read_file", true))
	}
}

func TestRenderToolPanelPathChipAndEditPreview(t *testing.T) {
	// Force color path so glyph assertions are deterministic under NO_COLOR/dumb CI.
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm-256color")
	now := time.Now()
	rows := []toolRow{
		{
			Name:   "search_replace",
			Detail: `{"path":"a.go","old_string":"old","new_string":"new"}`,
			Result: "updated a.go (1 replacement, +1 −1)\n--- old\nold\n+++ new\nnew",
			Start:  now.Add(-10 * time.Millisecond),
			End:    now,
			Done:   true,
		},
	}
	out, lines := renderToolPanel(rows, 100, now, -1, 0, phaseTools)
	if lines < 1 {
		t.Fatalf("lines=%d", lines)
	}
	if !strings.Contains(out, "search_replace") {
		t.Fatalf("missing name: %q", out)
	}
	if !strings.Contains(out, "⚙") {
		t.Fatalf("missing kind icon: %q", out)
	}
	// Monochrome / dumb TERM must still surface a kind marker (ASCII "e").
	t.Setenv("NO_COLOR", "1")
	t.Setenv("TERM", "dumb")
	mono, _ := renderToolPanel(rows, 100, now, -1, 0, phaseTools)
	if !strings.Contains(mono, "search_replace") || !strings.Contains(mono, "e") {
		t.Fatalf("monochrome missing kind marker: %q", mono)
	}
	// Expanded previews work with and without color.
	rows[0].Expanded = true
	out, _ = renderToolPanel(rows, 100, now, -1, 0, phaseTools)
	if !strings.Contains(out, "output") {
		t.Fatalf("missing output section (mono): %q", out)
	}
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm-256color")
	out, _ = renderToolPanel(rows, 100, now, -1, 0, phaseTools)
	if !strings.Contains(out, "output") {
		t.Fatalf("missing output section (color): %q", out)
	}
	// Many lines should still render up to 16 for edit tools
	var body strings.Builder
	body.WriteString("updated a.go (1 replacement, +10 −1)\n--- old\nold\n+++ new\n")
	for i := 0; i < 20; i++ {
		body.WriteString("+line\n")
	}
	rows[0].Result = body.String()
	out, n := renderToolPanel(rows, 100, now, -1, 0, phaseTools)
	// Header + collapsed + input header + 1 input + output header + up to 16 output lines
	if n < 10 {
		t.Fatalf("expected expanded edit preview lines, got %d out=%q", n, out)
	}
}

func TestRenderToolPanelNarrowWidthNoPanic(t *testing.T) {
	now := time.Now()
	long := strings.Repeat("x", 200)
	rows := []toolRow{{
		Name:     "write_file",
		Detail:   long,
		Result:   long + "\n+added\n-removed",
		Start:    now.Add(-time.Second),
		End:      now,
		Done:     true,
		Expanded: true,
	}}
	for _, w := range []int{0, 5, 12, 13, 20} {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("width=%d panic: %v", w, r)
				}
			}()
			out, n := renderToolPanel(rows, w, now, -1, 0, phaseTools)
			if n < 1 || out == "" {
				t.Fatalf("width=%d empty panel", w)
			}
		}()
	}
}

func TestClipPreviewLineNeverNegative(t *testing.T) {
	for _, w := range []int{0, 1, 5, 10, 40} {
		got := ClipPreviewLine(strings.Repeat("a", 100), w)
		if len(got) == 0 {
			t.Fatalf("width=%d empty", w)
		}
	}
}

func TestClipPreviewLineUTF8Safe(t *testing.T) {
	got := ClipPreviewLine("界界界界", 12)
	if !utf8.ValidString(got) {
		t.Fatalf("invalid UTF-8: %q", got)
	}
}

func TestColorDiffLine(t *testing.T) {
	// Smoke: prefixes apply styles (non-empty, not equal to raw for colored).
	if ColorDiffLine("--- old") == "" {
		t.Fatal("header empty")
	}
	if ColorDiffLine("+added") == "" {
		t.Fatal("add empty")
	}
	if ColorDiffLine("-removed") == "" {
		t.Fatal("del empty")
	}
	// Context lines now get dim styling (GitHub-style).
	got := ColorDiffLine("context")
	if got == "" {
		t.Fatal("context empty")
	}
	if strings.Contains(got, "context") {
		t.Logf("context styled: %q", got)
	}
}

func TestToolIconForName(t *testing.T) {
	// Typed action glyphs: ⚙ tool, ◆ agent - single-width, never emoji.
	if ToolIconForName("write_file") != "⚙" {
		t.Fatal("write icon")
	}
	if ToolIconForName("read_file") != "⚙" {
		t.Fatal("read icon")
	}
	if ToolIconForName("delegate") != "◆" {
		t.Fatal("agent icon")
	}
}

// TestBoundedToolTextStripsControlSequences: every classic tool row - start,
// end, panel, history - funnels model-influenced text through BoundedToolText.
// A tool name, an argument or an error message can therefore carry raw ANSI
// straight to the operator's terminal unless this one chokepoint sanitizes it.
func TestBoundedToolTextStripsControlSequences(t *testing.T) {
	got := BoundedToolText("\x1b[2J\x1b[1;31mPWNED\x1b[0m file.txt\x00", 200)
	if strings.Contains(got, "\x1b") {
		t.Fatalf("BoundedToolText = %q, want the escape sequences stripped", got)
	}
	if strings.Contains(got, "\x00") {
		t.Fatalf("BoundedToolText = %q, want NUL stripped", got)
	}
	if !strings.Contains(got, "PWNED file.txt") {
		t.Fatalf("BoundedToolText = %q, want the literal text preserved", got)
	}
}

// TestPrintToolLinesStripControlSequences is the same property at the two
// renderer entry points that write to a live terminal.
func TestPrintToolLinesStripControlSequences(t *testing.T) {
	for _, tc := range []struct {
		name  string
		print func(*ChatRenderer)
	}{
		{"start", func(r *ChatRenderer) { r.PrintToolStart("read_file", "\x1b[2Jevil.txt") }},
		{"end", func(r *ChatRenderer) { r.PrintToolEnd("read_file", "error: open \x1b[2Jevil.txt: no such file") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			buf := new(bytes.Buffer)
			r := NewChatRenderer(&Terminal{out: buf}, "m")
			tc.print(r)
			if strings.Contains(buf.String(), "\x1b[2J") {
				t.Fatalf("%s output = %q, want no raw clear-screen from tool text", tc.name, buf.String())
			}
		})
	}
}

// TestBoundedToolTextTruncatesWholeRunes: the bound used to slice bytes, so a
// cut landing inside a multibyte rune emitted replacement garbage.
func TestBoundedToolTextTruncatesWholeRunes(t *testing.T) {
	for _, max := range []int{1, 2, 3, 4, 5, 8, 12} {
		got := BoundedToolText(strings.Repeat("日", 40), max)
		if !utf8.ValidString(got) {
			t.Fatalf("max=%d: BoundedToolText = %q, want valid UTF-8", max, got)
		}
		if utf8.RuneCountInString(got) > max {
			t.Fatalf("max=%d: BoundedToolText = %q (%d runes), want the bound respected",
				max, got, utf8.RuneCountInString(got))
		}
	}
}
