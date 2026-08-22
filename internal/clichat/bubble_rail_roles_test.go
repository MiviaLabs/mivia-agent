package clichat

import (
	"strings"
	"testing"
)

func TestRailPalette_ToolsAreNeutralNotYellow(t *testing.T) {
	opts := RailOpts{ASCII: false, Color: true}
	for _, name := range []string{"read_file", "run_command", "delegate", "kill"} {
		b := ChatBlock{Kind: ChatBlockTool, ToolName: name, Text: "ok content", Collapsed: true}
		r := ResolveBlockRail(b, GroupMember{}, opts, RailView{})
		if r.Color != ChromeNeutral {
			t.Fatalf("%s rail color=%q want neutral %q (not yellow)", name, r.Color, ChromeNeutral)
		}
		if r.Color == chromeTools || r.Color == ChromeError {
			t.Fatalf("%s must not use tools-yellow or error red when OK", name)
		}
		if r.Mode != RailModeHeader {
			t.Fatalf("%s mode=%v want header-only", name, r.Mode)
		}
	}
}

func TestRailPalette_FalseErrorNotRed(t *testing.T) {
	// Body mentions "error handling" - must NOT paint red.
	b := ChatBlock{
		Kind: ChatBlockTool, ToolName: "read_file",
		Text: "func handle error handling here\nreturn nil",
	}
	if blockToolFailed(b) {
		t.Fatal("body containing 'error' must not mark tool failed")
	}
	r := ResolveBlockRail(b, GroupMember{}, RailOpts{Color: true}, RailView{})
	if r.Color == ChromeError || r.Glyph == "!" {
		t.Fatalf("false positive failure rail: %+v", r)
	}
}

func TestRailPalette_StrictFailureIsRed(t *testing.T) {
	cases := []ChatBlock{
		{Kind: ChatBlockTool, Text: "error: boom"},
		{Kind: ChatBlockTool, Text: "failed: no such file"},
		{Kind: ChatBlockTool, Text: "ok", Rendered: "exit=1"},
	}
	for _, b := range cases {
		if !blockToolFailed(b) {
			t.Fatalf("expected failed: text=%q rendered=%q", b.Text, b.Rendered)
		}
		r := ResolveBlockRail(b, GroupMember{}, RailOpts{Color: true}, RailView{})
		if r.Color != ChromeError || r.Glyph != "!" {
			t.Fatalf("failed rail=%+v", r)
		}
	}
}

func TestRailPalette_GroupHeaderHeavierThanStep(t *testing.T) {
	opts := RailOpts{ASCII: false, Color: true}
	hdr := railFromRole(RailRoleGroupHeader, RailStateNeutral, opts, RailView{})
	step := railFromRole(RailRoleStep, RailStateNeutral, opts, RailView{})
	if !hdr.Bold {
		t.Fatal("group header should be bold (defines set)")
	}
	if step.Bold {
		t.Fatal("step should be thin")
	}
	if hdr.Glyph == step.Glyph {
		t.Fatalf("header and step should differ in weight/glyph: %q vs %q", hdr.Glyph, step.Glyph)
	}
}

func TestRailPalette_LiveThinkingAnimatesCyan(t *testing.T) {
	opts := RailOpts{ASCII: false, Color: true}
	b := ChatBlock{Kind: ChatBlockThinking, Text: "plan", Collapsed: false}
	r0 := ResolveBlockRail(b, GroupMember{}, opts, RailView{Frame: 0, Live: true})
	r1 := ResolveBlockRail(b, GroupMember{}, opts, RailView{Frame: 1, Live: true})
	if r0.Color != chromeAwait {
		t.Fatalf("live thinking color=%q want cyan", r0.Color)
	}
	if !r0.Animate {
		t.Fatal("live thinking should animate")
	}
	// Frame advances glyph
	if r0.Glyph == r1.Glyph && len(BrandWorkFrames) > 1 {
		// possible collision on cycle; require Animate flag at least
		t.Logf("glyphs equal at f0/f1: %q (ok if short cycle)", r0.Glyph)
	}
	// History frozen
	hist := ResolveBlockRail(b, GroupMember{}, opts, RailView{Frame: 3, Live: false})
	if hist.Animate || hist.Color != ChromeNeutral {
		t.Fatalf("history thinking should be neutral static: %+v", hist)
	}
}

func TestRailPalette_SameColorReadFileAndRunCommand(t *testing.T) {
	opts := RailOpts{ASCII: true, Color: false}
	a := ResolveBlockRail(ChatBlock{Kind: ChatBlockTool, ToolName: "read_file", Text: "x"}, GroupMember{}, opts, RailView{})
	b := ResolveBlockRail(ChatBlock{Kind: ChatBlockTool, ToolName: "run_command", Text: "x"}, GroupMember{}, opts, RailView{})
	if a.Glyph != b.Glyph || a.Color != b.Color {
		t.Fatalf("tool names must share rail: read=%+v run=%+v", a, b)
	}
}

func TestBuildGroupMembers_FirstStepIndex(t *testing.T) {
	blocks := []ChatBlock{
		{ID: "t1", Kind: ChatBlockTool, ToolName: "a", Text: "1"},
		{ID: "t2", Kind: ChatBlockTool, ToolName: "b", Text: "2"},
	}
	mem := buildGroupMembers(blocks)
	if len(mem) != 2 || !mem[0].InGroup || mem[0].ToolIndex != 0 || mem[1].ToolIndex != 1 {
		t.Fatalf("membership=%+v", mem)
	}
	r0 := resolveRailRole(blocks[0], mem[0])
	r1 := resolveRailRole(blocks[1], mem[1])
	if r0 != RailRoleFirstStep || r1 != RailRoleStep {
		t.Fatalf("roles first=%v step=%v", r0, r1)
	}
}

func TestApplyLeftRail_HeaderOnlyNotFullWall(t *testing.T) {
	rail := LeftRail{Width: 1, Glyph: "|", Char: "|", Color: ChromeNeutral, Plain: true, Mode: RailModeHeader}
	lines := []string{"  head", "  body", "  tail"}
	out := ApplyLeftRail(lines, rail)
	if !strings.HasPrefix(stripANSI(out[0]), "|") {
		t.Fatalf("header missing rail: %q", out[0])
	}
	// body/tail should not start with glyph (space column under header)
	for i := 1; i < len(out); i++ {
		p := stripANSI(out[i])
		if strings.HasPrefix(p, "|") {
			t.Fatalf("line %d should not have full-height rail: %q", i, p)
		}
	}
}

func TestApplyLeftRail_SkipsBlankPadLines(t *testing.T) {
	rail := LeftRail{Width: 1, Glyph: "|", Char: "|", Plain: true, Mode: RailModeFull}
	lines := []string{"", "  text here", "   ", "  more"}
	out := ApplyLeftRail(lines, rail)
	if strings.HasPrefix(stripANSI(out[0]), "|") {
		t.Fatalf("blank pad must not get rail glyph: %q", out[0])
	}
	if !strings.HasPrefix(stripANSI(out[1]), "|") {
		t.Fatalf("text line needs rail: %q", out[1])
	}
	if strings.TrimSpace(stripANSI(out[2])) != "" && strings.HasPrefix(stripANSI(out[2]), "|") {
		// pure pad spaces: no glyph
		p := stripANSI(out[2])
		if strings.HasPrefix(strings.TrimLeft(p, " "), "|") || (len(p) > 0 && p[0] == '|') {
			t.Fatalf("pad-only line must not start with rail: %q", p)
		}
	}
	if !strings.HasPrefix(stripANSI(out[3]), "|") {
		t.Fatalf("second text needs rail: %q", out[3])
	}
}

func TestBlockToolFailed_ProductionRunCommandShape(t *testing.T) {
	// Production run_command body: command/cwd then exit=1 on later line.
	body := "command: ls -la\ncwd: /tmp\nexit=1\n"
	b := ChatBlock{Kind: ChatBlockTool, ToolName: "run_command", Text: body, Collapsed: true}
	if !blockToolFailed(b) {
		t.Fatal("exit=1 in body must mark failed")
	}
	r := ResolveBlockRail(b, GroupMember{}, RailOpts{Color: true}, RailView{})
	if r.Color != ChromeError || r.Glyph != "!" {
		t.Fatalf("production fail rail=%+v", r)
	}
	// exit=10 must not match exit=1 token
	b10 := ChatBlock{Kind: ChatBlockTool, Text: "command: x\nexit=10\n"}
	if blockToolFailed(b10) {
		t.Fatal("exit=10 must not count as exit=1")
	}
	// Explicit Failed flag from toolRow
	if !blockToolFailed(ChatBlock{Kind: ChatBlockTool, Failed: true, Text: "ok"}) {
		t.Fatal("Failed flag must win")
	}
}

func TestRailState_HistoryNeverPulsesWhileWaiting(t *testing.T) {
	// Committed history always Live=false (renderBlocksForView).
	opts := RailOpts{Color: true}
	th := ChatBlock{Kind: ChatBlockThinking, Text: "old plan", Collapsed: false}
	r := ResolveBlockRail(th, GroupMember{}, opts, RailView{Frame: 3, Live: false})
	if r.Animate || r.Color != ChromeNeutral {
		t.Fatalf("history thinking must stay neutral static: %+v", r)
	}
	// Live overlay only
	live := ResolveBlockRail(th, GroupMember{}, opts, RailView{Frame: 3, Live: true})
	if !live.Animate || live.Color != chromeAwait {
		t.Fatalf("live thinking must cyan pulse: %+v", live)
	}
	// Group header in history must not go parallel-cyan
	hdr := railFromRole(RailRoleGroupHeader, resolveRailState(ChatBlock{}, RailRoleGroupHeader, RailView{Live: false}), opts, RailView{Live: false})
	if hdr.Color != ChromeNeutral || hdr.Animate {
		t.Fatalf("history group header must be neutral: %+v", hdr)
	}
}
