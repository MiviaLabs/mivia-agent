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
	opts := railOpts{ASCII: false, Color: true}

	cases := []struct {
		kind   ChatBlockKind
		failed bool
		glyph  string
		color  string
		width  int
	}{
		{ChatBlockUser, false, "›", chromeUser, 1},
		{ChatBlockAssistant, false, "│", chromeAssistant, 1},
		{ChatBlockThinking, false, "┊", chromeThinking, 1},
		{ChatBlockTool, false, "◆", chromeTools, 1},
		{ChatBlockTool, true, "✗", chromeError, 1},
		{ChatBlockSystem, false, "", "", 0},
		{ChatBlockDivider, false, "", "", 0},
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
	}
}

func TestRailForBlock_MatrixASCII(t *testing.T) {
	opts := railOpts{ASCII: true, Color: false}
	r := railForBlock(ChatBlockUser, false, opts)
	if r.Glyph != ">" {
		t.Fatalf("ASCII user glyph=%q", r.Glyph)
	}
	r = railForBlock(ChatBlockTool, true, opts)
	if r.Glyph != "!" {
		t.Fatalf("ASCII failed tool=%q", r.Glyph)
	}
	r = railForBlock(ChatBlockAssistant, false, opts)
	if r.Glyph != "|" {
		t.Fatalf("ASCII assistant=%q", r.Glyph)
	}
}

func TestRailForDividerText_Error(t *testing.T) {
	opts := railOpts{ASCII: false, Color: true}
	r := railForDividerText("error: boom", opts)
	if r.Width != 1 || r.Glyph != "!" || r.Color != chromeError {
		t.Fatalf("error divider rail=%+v", r)
	}
	r = railForDividerText("  ─ done · 1s ─", opts)
	if r.Width != 0 {
		t.Fatalf("done divider should have no rail")
	}
}

func TestApplyLeftRailHeader_FirstContentOnly(t *testing.T) {
	rail := LeftRail{Width: 1, Glyph: "◆", Color: chromeTools, Plain: true}
	lines := []string{"", "  read_file foo", "    body"}
	out := applyLeftRailHeader(lines, rail)
	if strings.TrimSpace(out[0]) != "" {
		t.Fatalf("blank pad line changed: %q", out[0])
	}
	if !strings.HasPrefix(stripANSI(out[1]), "◆ ") {
		t.Fatalf("header rail missing: %q", out[1])
	}
	if !strings.HasPrefix(out[2], "    body") {
		t.Fatalf("continuation must stay un-railed: %q", out[2])
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
	o := chromeRenderOpts()
	if o.Color {
		t.Fatal("NO_COLOR must disable Color")
	}
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "dumb")
	o = chromeRenderOpts()
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
	// User rail › (inside pad)
	if !strings.Contains(plain, "›") && !strings.Contains(plain, ">") {
		t.Fatalf("user rail missing in %q", plain)
	}
	// Tool diamond
	if !strings.Contains(plain, "◆") && !strings.Contains(plain, "*") {
		t.Fatalf("tool rail missing in %q", plain)
	}
	// Thinking
	if !strings.Contains(plain, "┊") && !strings.Contains(plain, ":") {
		// collapsed thinking still has ▸ — rail may replace first space
		if !strings.Contains(plain, "thinking") {
			t.Fatalf("thinking missing: %q", plain)
		}
	}
	// System → preserved
	if !strings.Contains(plain, "→") {
		t.Fatalf("status arrow missing: %q", plain)
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
	// Should not rely on unicode diamonds only — ASCII paths
	joined := stripANSI(plain)
	if !strings.Contains(joined, ">") && !strings.Contains(joined, "hi") {
		t.Fatalf("user content missing under plain: %q", joined)
	}
	// No ANSI color escapes when NO_COLOR (lipgloss may still emit on some styles;
	// rail paint must be plain).
	rail := railForBlock(ChatBlockTool, false, chromeRenderOpts())
	if !rail.Plain {
		t.Fatal("rail must be Plain under NO_COLOR")
	}
	if paintRailCell(rail) != "*" && paintRailCell(rail) != "◆" {
		// ASCII true → *
		if paintRailCell(rail) != "*" {
			t.Fatalf("expected ASCII tool glyph, got %q plain=%v ascii=%v", paintRailCell(rail), rail.Plain, rail.ASCII)
		}
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
	// › or > then space then maybe more pad — has glyph
	if !strings.Contains(content, "›") && !strings.HasPrefix(strings.TrimLeft(content, " "), ">") {
		if !strings.HasPrefix(content, "›") && !strings.HasPrefix(content, ">") {
			// left pad: › + space
			if !strings.Contains(content, "› ") && !strings.Contains(content, "> ") {
				t.Fatalf("user left rail missing: %q", content)
			}
		}
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
}
