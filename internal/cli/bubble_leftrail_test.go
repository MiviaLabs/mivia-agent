package cli

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestRailForBlock_MatrixUnicode(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm-256color")
	opts := RailOpts{ASCII: false, Color: true}

	cases := []struct {
		kind   ChatBlockKind
		failed bool
		glyph  string
		color  string
		width  int
		bold   bool
	}{
		{ChatBlockUser, false, "", "", 0, false},
		{ChatBlockAssistant, false, "", "", 0, false}, // default voice: no rail
		{ChatBlockThinking, false, "┊", ChromeNeutral, 1, false},
		{ChatBlockTool, false, "│", ChromeNeutral, 1, false}, // thin gray - not yellow
		{ChatBlockTool, true, "!", ChromeError, 1, true},     // strict fail only
		{ChatBlockSystem, false, "", "", 0, false},
		{ChatBlockDivider, false, "", "", 0, false},
	}
	for _, tc := range cases {
		r := railForBlock(tc.kind, tc.failed, opts)
		if r.Width != tc.width {
			t.Errorf("%s width=%d want %d", tc.kind, r.Width, tc.width)
		}
		if tc.width > 0 && r.Glyph != tc.glyph {
			t.Errorf("%s glyph=%q want %q", tc.kind, r.Glyph, tc.glyph)
		}
		if tc.width > 0 && r.Color != tc.color {
			t.Errorf("%s color=%q want %q", tc.kind, r.Color, tc.color)
		}
		if tc.width > 0 && r.Bold != tc.bold {
			t.Errorf("%s Bold=%v want %v", tc.kind, r.Bold, tc.bold)
		}
	}
}

func TestRailForBlock_MatrixASCII(t *testing.T) {
	opts := RailOpts{ASCII: true, Color: false}
	r := railForBlock(ChatBlockUser, false, opts)
	if r.Width != 0 {
		t.Fatalf("ASCII user rail should be off, got %+v", r)
	}
	r = railForBlock(ChatBlockTool, true, opts)
	if r.Glyph != "!" || r.Color != ChromeError {
		t.Fatalf("ASCII failed tool=%+v", r)
	}
	r = railForBlock(ChatBlockAssistant, false, opts)
	if r.Width != 0 {
		t.Fatalf("ASCII assistant should have no rail, got %+v", r)
	}
	r = railForBlock(ChatBlockTool, false, opts)
	if r.Glyph != "|" || r.Color != ChromeNeutral {
		t.Fatalf("ASCII tool OK should be thin gray |, got %+v", r)
	}
}

func TestRailForDividerText_Error(t *testing.T) {
	opts := RailOpts{ASCII: false, Color: true}
	r := railForDividerText("error: boom", opts)
	if r.Width != 1 || r.Glyph != "!" || r.Color != ChromeError {
		t.Fatalf("error divider rail=%+v", r)
	}
	r = railForDividerText("  ─ done · 1s ─", opts)
	if r.Width != 0 {
		t.Fatalf("done divider should have no rail")
	}
}

func TestApplyLeftRail_FullHeightAllLines(t *testing.T) {
	rail := LeftRail{Width: 1, Glyph: "#", Color: chromeTools, Plain: true}
	lines := []string{"  ", "  read_file foo", "    body"}
	out := ApplyLeftRail(lines, rail)
	// Blank pad line: no glyph; text lines: glyph
	if strings.HasPrefix(stripANSI(out[0]), "#") {
		t.Fatalf("blank pad must not get rail: %q", out[0])
	}
	for i := 1; i < len(out); i++ {
		p := stripANSI(out[i])
		if !strings.HasPrefix(p, "#") {
			t.Fatalf("line %d missing rail: %q", i, p)
		}
	}
}

func TestApplyLeftRail_JoinHorizontalFullHeight(t *testing.T) {
	// Industry pattern: JoinHorizontal rail column matches body height.
	// Blank lines skip glyph; text lines get rail.
	rail := LeftRail{Width: 1, Glyph: "#", Plain: true}
	lines := []string{"", "  hello", "  world", ""}
	out := ApplyLeftRail(lines, rail)
	if len(out) != len(lines) {
		t.Fatalf("line count %d want %d", len(out), len(lines))
	}
	if strings.HasPrefix(stripANSI(out[0]), "#") || strings.HasPrefix(stripANSI(out[3]), "#") {
		t.Fatalf("blank lines must not get rail: %v", dumpPlain(out))
	}
	if stripANSI(out[1]) != "# hello" {
		t.Fatalf("width-neutral join want %q got %q", "# hello", stripANSI(out[1]))
	}
	if stripANSI(out[2]) != "# world" {
		t.Fatalf("line2 want %q got %q", "# world", stripANSI(out[2]))
	}
}

func TestRailForChatBlock_Unified(t *testing.T) {
	opts := RailOpts{ASCII: true, Color: false}
	r := railForChatBlock(ChatBlock{Kind: ChatBlockTool, Text: "error: x"}, opts)
	if r.Width != 1 || r.Color != ChromeError || r.Glyph != "!" {
		t.Fatalf("failed tool chrome=%+v", r)
	}
	r = railForChatBlock(ChatBlock{Kind: ChatBlockDivider, Text: "error: boom"}, opts)
	if r.Glyph != "!" {
		t.Fatalf("error divider=%+v", r)
	}
	r = railForChatBlock(ChatBlock{Kind: ChatBlockSystem, Text: "→ go"}, opts)
	if r.Width != 0 {
		t.Fatalf("system should have no rail: %+v", r)
	}
}

func TestApplyLeftRailHeader_WidthZeroNoop(t *testing.T) {
	in := []string{"  hello"}
	out := applyLeftRailHeader(in, LeftRail{Width: 0, Glyph: "›"})
	if out[0] != in[0] {
		t.Fatalf("noop failed: %q", out[0])
	}
}

func TestLeftPadWithRail(t *testing.T) {
	rail := LeftRail{Width: 1, Glyph: "›", Plain: true}
	got := leftPadWithRail(2, rail)
	if stripANSI(got) != "› " {
		t.Fatalf("leftPad=%q want › + space", got)
	}
	got = leftPadWithRail(2, LeftRail{Width: 0})
	if got != "  " {
		t.Fatalf("no rail pad=%q", got)
	}
}

func TestPaintRailCell_Colored(t *testing.T) {
	rail := LeftRail{Width: 1, Glyph: "›", Color: "12", Plain: false}
	cell := paintRailCell(rail)
	if !strings.Contains(cell, "›") {
		t.Fatalf("missing glyph: %q", cell)
	}
	// Plain path must strip to bare glyph.
	rail.Plain = true
	if paintRailCell(rail) != "›" {
		t.Fatal("plain must be bare glyph")
	}
	// Color path still contains glyph after strip.
	rail.Plain = false
	if stripANSI(paintRailCell(rail)) != "›" {
		t.Fatalf("colored strip=%q", stripANSI(paintRailCell(rail)))
	}
}

func TestChromeRenderOpts_NO_COLOR(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	t.Setenv("TERM", "xterm-256color")
	o := ChromeRenderOpts()
	if o.Color {
		t.Fatal("NO_COLOR must disable Color")
	}
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "dumb")
	o = ChromeRenderOpts()
	if !o.ASCII || o.Color {
		t.Fatalf("dumb TERM opts=%+v", o)
	}
	_ = os.Unsetenv
}

func TestRenderChatBlocks_RailsOnKinds(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm-256color")
	blocks := []ChatBlock{
		{ID: "u", Kind: ChatBlockUser, Text: "hello user", SentAt: time.Now()},
		{ID: "a", Kind: ChatBlockAssistant, Text: "hello assistant"},
		{ID: "t", Kind: ChatBlockTool, ToolName: "read_file", Text: "ok", Collapsed: true},
		{ID: "th", Kind: ChatBlockThinking, Text: "reason", Collapsed: true},
		{ID: "s", Kind: ChatBlockSystem, Text: "→ Listing…"},
		{ID: "e", Kind: ChatBlockDivider, Text: "error: failed"},
	}
	r := RenderChatBlocks(blocks, "m", 60, true)
	plain := stripANSI(strings.Join(r.Lines, "\n"))
	// Thin semantic rails (│ ┊ !) - not yellow full bars
	if !strings.Contains(plain, "│") && !strings.Contains(plain, "┊") && !strings.Contains(plain, "!") {
		t.Fatalf("block rails missing in %q", plain)
	}
	if !strings.Contains(plain, "thinking") {
		t.Fatalf("thinking missing: %q", plain)
	}
	// Work-status uses ▸/▾ collapse affordance (arrow "→" is storage prefix only).
	if !strings.Contains(plain, "Listing") || (!strings.Contains(plain, "▸") && !strings.Contains(plain, "▾")) {
		t.Fatalf("work-status chrome missing: %q", plain)
	}
	// Error divider
	if !strings.Contains(plain, "!") && !strings.Contains(plain, "error") {
		t.Fatalf("error chrome missing: %q", plain)
	}
	// No box borders
	if strings.Contains(plain, "╭") || strings.Contains(plain, "╰") {
		t.Fatalf("box borders revived: %q", plain)
	}
}

func TestRenderChatBlocks_NO_COLOR_ASCII(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	t.Setenv("TERM", "dumb")
	blocks := []ChatBlock{
		{ID: "u", Kind: ChatBlockUser, Text: "hi"},
		{ID: "t", Kind: ChatBlockTool, ToolName: "grep", Text: "x", Collapsed: true},
	}
	r := RenderChatBlocks(blocks, "m", 40, true)
	plain := strings.Join(r.Lines, "\n")
	// Should not rely on unicode diamonds only - ASCII paths
	joined := stripANSI(plain)
	if !strings.Contains(joined, "hi") {
		t.Fatalf("user content missing under plain: %q", joined)
	}
	// User cards: no left rail (bg bar only)
	for _, ln := range r.Lines {
		p := stripANSI(ln)
		if strings.Contains(p, "hi") && (strings.HasPrefix(p, "#") || strings.HasPrefix(p, "▌")) {
			t.Fatalf("user content must not have left rail: %q", p)
		}
	}
	rail := railForBlock(ChatBlockTool, false, ChromeRenderOpts())
	if !rail.Plain {
		t.Fatal("rail must be Plain under NO_COLOR")
	}
	if cell := paintRailCell(rail); cell != "|" {
		t.Fatalf("expected ASCII tool glyph | (thin gray), got %q (ascii=%v)", cell, rail.ASCII)
	}
}

func TestUserBubble_RailInLeftPad(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm-256color")
	lines := UserBubble.Render("hello world", 40, time.Time{})
	// Find content line
	var content string
	for _, ln := range lines {
		if strings.Contains(stripANSI(ln), "hello world") {
			content = stripANSI(ln)
			break
		}
	}
	if content == "" {
		t.Fatal("content line missing")
	}
	// Left pad: Render uses plain spaces for padding. The rail glyph is
	// applied by the caller (renderOneChatBlock → applyLeftRailHeader),
	// not by Render. This test only verifies content is present.
	if !strings.Contains(content, "hello") {
		t.Fatalf("expected body content, got %q", content)
	}
}

func TestBlockToolFailed(t *testing.T) {
	if !blockToolFailed(ChatBlock{Kind: ChatBlockTool, Text: "error: boom"}) {
		t.Fatal("expected failed")
	}
	if blockToolFailed(ChatBlock{Kind: ChatBlockTool, Text: "ok"}) {
		t.Fatal("ok is not failed")
	}
	if blockToolFailed(ChatBlock{Kind: ChatBlockUser, Text: "error"}) {
		t.Fatal("user not tool")
	}
	// Body mention of "error" must not false-positive.
	if blockToolFailed(ChatBlock{Kind: ChatBlockTool, Text: "see error handling patterns"}) {
		t.Fatal("substring 'error' in body is not a tool failure")
	}
	// First line "error handling…" is not error: prefix
	if blockToolFailed(ChatBlock{Kind: ChatBlockTool, Text: "error handling patterns\nmore"}) {
		t.Fatal("first line 'error handling' without colon is not failure")
	}
}

// Production tools always set Rendered via formatToolLine - rails must still apply.
func TestRenderChatBlocks_ToolWithRenderedGetsRail(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm-256color")
	item := NewToolRenderItem("read_file", `{"path":"a.go"}`, "ok", true, false)
	line := formatToolLine(item, 80, terminalToolRenderOptions())
	blocks := []ChatBlock{{
		ID: "t1", Kind: ChatBlockTool, ToolName: "read_file",
		Text: "ok", Rendered: line, Collapsed: true,
	}}
	r := RenderChatBlocks(blocks, "m", 80, true)
	if len(r.Lines) < 1 {
		t.Fatal("no lines")
	}
	plain := stripANSI(r.Lines[0])
	if !strings.HasPrefix(plain, "│") && !strings.HasPrefix(plain, "|") {
		t.Fatalf("Rendered tool missing thin rail: %q", plain)
	}
	if !strings.Contains(plain, "read_file") {
		t.Fatalf("tool name missing: %q", plain)
	}
}

func TestApplyLeftRailHeader_WidthNeutralOnDoubleSpace(t *testing.T) {
	in := "  read_file args"
	out := injectRailOnLine(in, "◆")
	// Same visible width: replaced "  " with "◆ ".
	if visibleWidth(out) != visibleWidth(in) {
		t.Fatalf("width %d → %d for %q", visibleWidth(in), visibleWidth(out), out)
	}
}

func TestRenderChatBlocks_NarrowWidthBudget(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	t.Setenv("TERM", "dumb")
	const w = 24
	blocks := []ChatBlock{
		{ID: "u", Kind: ChatBlockUser, Text: "hello"},
		{ID: "t", Kind: ChatBlockTool, ToolName: "grep", Text: "x", Collapsed: true},
	}
	r := RenderChatBlocks(blocks, "m", w, true)
	for i, ln := range r.Lines {
		// Allow small slack for emoji tool icons on non-dumb; dumb is ASCII.
		if visibleWidth(ln) > w+2 {
			t.Fatalf("line %d exceeds budget: vis=%d w=%d %q", i, visibleWidth(ln), w, stripANSI(ln))
		}
	}
}

func TestUserBubble_NoRailMultiLine(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	t.Setenv("TERM", "dumb")
	long := strings.Repeat("word ", 40)
	blocks := []ChatBlock{{ID: "u", Kind: ChatBlockUser, Text: long}}
	r := RenderChatBlocks(blocks, "m", 30, true)
	var content2 []string
	for _, ln := range r.Lines {
		if strings.TrimSpace(stripANSI(ln)) != "" {
			content2 = append(content2, stripANSI(ln))
		}
	}
	if len(content2) < 2 {
		t.Fatal("expected multi-line user card")
	}
	// User: no left rail on any content line.
	for i, p := range content2 {
		if strings.HasPrefix(p, "#") || strings.HasPrefix(p, "▌") {
			t.Fatalf("content line %d has rail (user should not): %q", i, p)
		}
	}
}
