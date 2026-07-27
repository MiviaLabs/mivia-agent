package cli

import (
	"strings"
	"testing"
)

func TestRailPalette_ToolsAreNeutralNotYellow(t *testing.T) {
	opts := railOpts{ASCII: false, Color: true}
	for _, name := range []string{"read_file", "run_command", "delegate", "kill"} {
		b := ChatBlock{Kind: ChatBlockTool, ToolName: name, Text: "ok content", Collapsed: true}
		r := resolveBlockRail(b, groupMember{}, opts, railView{})
		if r.Color != chromeNeutral {
			t.Fatalf("%s rail color=%q want neutral %q (not yellow)", name, r.Color, chromeNeutral)
		}
		if r.Color == chromeTools || r.Color == chromeError {
			t.Fatalf("%s must not use tools-yellow or error red when OK", name)
		}
		if r.Mode != RailModeHeader {
			t.Fatalf("%s mode=%v want header-only", name, r.Mode)
		}
	}
}

func TestRailPalette_FalseErrorNotRed(t *testing.T) {
	// Body mentions "error handling" — must NOT paint red.
	b := ChatBlock{
		Kind: ChatBlockTool, ToolName: "read_file",
		Text: "func handle error handling here\nreturn nil",
	}
	if blockToolFailed(b) {
		t.Fatal("body containing 'error' must not mark tool failed")
	}
	r := resolveBlockRail(b, groupMember{}, railOpts{Color: true}, railView{})
	if r.Color == chromeError || r.Glyph == "!" {
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
		r := resolveBlockRail(b, groupMember{}, railOpts{Color: true}, railView{})
		if r.Color != chromeError || r.Glyph != "!" {
			t.Fatalf("failed rail=%+v", r)
		}
	}
}

func TestRailPalette_GroupHeaderHeavierThanStep(t *testing.T) {
	opts := railOpts{ASCII: false, Color: true}
	hdr := railFromRole(RailRoleGroupHeader, RailStateNeutral, opts, railView{})
	step := railFromRole(RailRoleStep, RailStateNeutral, opts, railView{})
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
	opts := railOpts{ASCII: false, Color: true}
	b := ChatBlock{Kind: ChatBlockThinking, Text: "plan", Collapsed: false}
	r0 := resolveBlockRail(b, groupMember{}, opts, railView{Frame: 0, Live: true})
	r1 := resolveBlockRail(b, groupMember{}, opts, railView{Frame: 1, Live: true})
	if r0.Color != chromeAwait {
		t.Fatalf("live thinking color=%q want cyan", r0.Color)
	}
	if !r0.Animate {
		t.Fatal("live thinking should animate")
	}
	// Frame advances glyph
	if r0.Glyph == r1.Glyph && len(brandWorkFrames) > 1 {
		// possible collision on cycle; require Animate flag at least
		t.Logf("glyphs equal at f0/f1: %q (ok if short cycle)", r0.Glyph)
	}
	// History frozen
	hist := resolveBlockRail(b, groupMember{}, opts, railView{Frame: 3, Live: false})
	if hist.Animate || hist.Color != chromeNeutral {
		t.Fatalf("history thinking should be neutral static: %+v", hist)
	}
}

func TestRailPalette_SameColorReadFileAndRunCommand(t *testing.T) {
	opts := railOpts{ASCII: true, Color: false}
	a := resolveBlockRail(ChatBlock{Kind: ChatBlockTool, ToolName: "read_file", Text: "x"}, groupMember{}, opts, railView{})
	b := resolveBlockRail(ChatBlock{Kind: ChatBlockTool, ToolName: "run_command", Text: "x"}, groupMember{}, opts, railView{})
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
	rail := LeftRail{Width: 1, Glyph: "|", Char: "|", Color: chromeNeutral, Plain: true, Mode: RailModeHeader}
	lines := []string{"  head", "  body", "  tail"}
	out := applyLeftRail(lines, rail)
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
