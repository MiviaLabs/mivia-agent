package cli

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestDeriveBrandPhase(t *testing.T) {
	cases := []struct {
		waiting         bool
		open, stream, q int
		err             bool
		elapsed         time.Duration
		want            brandPhase
	}{
		{false, 0, 0, 0, false, 0, phaseIdle},
		{false, 0, 0, 2, false, 0, phaseQueued},
		{true, 0, 0, 0, false, 0, phaseAwaiting},
		{true, 0, 0, 0, false, 3 * time.Second, phaseThinking},
		{true, 0, 100, 0, false, 0, phaseStreaming},
		{true, 1, 0, 0, false, 0, phaseTools},
		{true, 3, 0, 0, false, 0, phaseMulti},
		{false, 0, 0, 0, true, 0, phaseError},
	}
	for _, tc := range cases {
		got := deriveBrandPhase(tc.waiting, tc.open, tc.stream, tc.q, tc.err, tc.elapsed)
		if got != tc.want {
			t.Fatalf("derive=%v want %v (waiting=%v open=%d stream=%d q=%d err=%v elapsed=%v)", got, tc.want, tc.waiting, tc.open, tc.stream, tc.q, tc.err, tc.elapsed)
		}
	}
}

func TestBrandLabel_SemanticStateNames(t *testing.T) {
	cases := map[brandPhase]string{
		phaseIdle: "ready", phaseWelcome: "welcome", phaseAwaiting: "awaiting", phaseThinking: "thinking",
		phaseStreaming: "streaming", phaseTools: "tools", phaseMulti: "parallel",
		phaseQueued: "queued", phaseError: "error", phaseCancel: "cancelled",
	}
	for phase, want := range cases {
		if got := brandLabel(phase); got != want {
			t.Errorf("phase %v label=%q want %q", phase, got, want)
		}
	}
}

func TestRenderWorkChrome_IncludesSemanticStateLabel(t *testing.T) {
	for _, phase := range []brandPhase{phaseThinking, phaseStreaming, phaseTools, phaseMulti, phaseQueued, phaseError, phaseCancel} {
		out := stripANSI(renderWorkChrome(0, phase, "model", time.Second, 0, 0, 0, 0, 80, ""))
		if !strings.Contains(out, brandLabel(phase)) {
			t.Errorf("phase %v missing semantic label %q in %q", phase, brandLabel(phase), out)
		}
	}
}

func TestRenderWorkChrome_ShowsThinkingProgressDetail(t *testing.T) {
	out := stripANSI(renderWorkChrome(
		0, phaseThinking, "model", 3*time.Second, 0, 0, 0, 0, 100, "model thinking (2 s)",
	))
	if !strings.Contains(out, "model thinking (2 s)") {
		t.Fatalf("thinking header omitted progress detail: %q", out)
	}
}

func TestRenderWorkChrome_BoundsAndSanitizesProgressDetail(t *testing.T) {
	unsafeDetail := "working\nspoof\r\x1b[2J with a very long progress detail"
	wide := stripANSI(renderWorkChrome(
		0, phaseThinking, "model", 3*time.Second, 0, 0, 0, 0, 100, unsafeDetail,
	))
	if !strings.Contains(wide, "working spoof") {
		t.Fatalf("status bar did not retain sanitized progress detail: %q", wide)
	}
	if strings.ContainsAny(wide, "\r\n") {
		t.Fatalf("status bar contains a line break: %q", wide)
	}
	for _, r := range wide {
		if r < 0x20 || r == 0x7f {
			t.Fatalf("status bar contains control character U+%04X: %q", r, wide)
		}
	}

	out := stripANSI(renderWorkChrome(
		0, phaseThinking, "model", 3*time.Second, 0, 0, 0, 0, 20,
		unsafeDetail,
	))
	if strings.ContainsAny(out, "\r\n") {
		t.Fatalf("status bar contains a line break: %q", out)
	}
	for _, r := range out {
		if r < 0x20 || r == 0x7f {
			t.Fatalf("status bar contains control character U+%04X: %q", r, out)
		}
	}
	if got := visibleWidth(out); got > 20 {
		t.Fatalf("status bar width = %d, want <= 20: %q", got, out)
	}
}

func TestRenderStatusBarSingleLine(t *testing.T) {
	// Working chrome: one physical line, identity left, phase right.
	out := renderStatusBar(3, phaseThinking, "model", true, time.Second, 0, 0, 0, 0, 0, 80, "")
	if strings.Count(out, "\n") > 0 {
		t.Fatalf("status must be one line: %q", out)
	}
	if !strings.Contains(out, "thinking") {
		t.Fatalf("missing phase: %q", out)
	}
	if !strings.ContainsAny(out, "⣿⠇⣶⣀⡀⠂⠄⠈⠐") {
		t.Fatalf("missing brand braille: %q", out)
	}
	if !strings.Contains(out, "model") {
		t.Fatalf("missing model: %q", out)
	}
	// Left identity before right phase — find first braille glyph.
	firstBraille := strings.IndexAny(out, "⣿⠇⣶⣀⡀⠂⠄⠈⠐")
	if mi, th := firstBraille, strings.Index(out, "thinking"); mi < 0 || th < 0 || mi > th {
		t.Fatalf("expected left brand before right phase: %q", out)
	}
	if !strings.Contains(out, "─") {
		t.Fatalf("expected middle rule: %q", out)
	}
	// Working glyph is braille (not a multi-line diamond crop).
	hasBraille := false
	for _, r := range out {
		if r >= 0x2800 && r <= 0x28FF {
			hasBraille = true
			break
		}
	}
	if !hasBraille {
		t.Fatal("working status must include braille pulse glyph")
	}
	// Not a multi-line welcome diamond: plain text of status is one line.
	if strings.Count(stripANSI(out), "\n") > 0 {
		t.Fatalf("ANSI-stripped status multi-line (diamond crop?): %q", stripANSI(out))
	}

	// Idle chrome: single line with static diamond identity.
	idle := renderStatusBar(0, phaseIdle, "model", false, 0, 0, 0, 0, 0, 4, 80, "")
	if strings.Count(idle, "\n") > 0 {
		t.Fatal("idle status multi-line")
	}
	if !strings.Contains(idle, "⣿") {
		t.Fatalf("idle must use braille brand: %q", idle)
	}
	if !strings.Contains(idle, "model") {
		t.Fatalf("idle identity: %q", idle)
	}
	if strings.Count(stripANSI(idle), "\n") > 0 {
		t.Fatalf("idle stripped multi-line: %q", stripANSI(idle))
	}
}

func TestBrandWorkFramesCompleteCells(t *testing.T) {
	// Pulse must be dense enough for a full 1-cell mark, not a raster tip slice.
	if len(brandWorkFrames) < 8 {
		t.Fatalf("want >= 8 frames, got %d", len(brandWorkFrames))
	}
	seen := map[string]bool{}
	for i, f := range brandWorkFrames {
		plain := stripANSI(f)
		if utf8.RuneCountInString(plain) != 1 {
			t.Fatalf("frame %d must be one rune after ANSI strip, got %q (runes=%d)", i, plain, utf8.RuneCountInString(plain))
		}
		if strings.Contains(plain, "\n") {
			t.Fatalf("frame %d multi-line (welcome diamond crop?): %q", i, plain)
		}
		r, _ := utf8.DecodeRuneInString(plain)
		if r < 0x2800 || r > 0x28FF {
			t.Fatalf("frame %d not braille: U+%04X", i, r)
		}
		if r == 0x2800 {
			t.Fatalf("frame %d empty braille", i)
		}
		bits := int(r - 0x2800)
		dots := 0
		for b := bits; b > 0; b >>= 1 {
			if b&1 != 0 {
				dots++
			}
		}
		// Raster tip crops look sparse (1–2 edge dots). Require a full small mark.
		if dots < 4 {
			t.Fatalf("frame %d too sparse (%d dots): %q — tip/slice look", i, dots, f)
		}
		seen[plain] = true
	}
	if len(seen) < 4 {
		t.Fatalf("expected variety in pulse, got %d unique", len(seen))
	}
}

func TestStatusGlyphSingleCell(t *testing.T) {
	g := statusGlyph(0, phaseThinking)
	if g == "" {
		t.Fatal("empty glyph")
	}
	if strings.Contains(g, "\n") {
		t.Fatal("glyph multi-line")
	}
	plain := stripANSI(g)
	if utf8.RuneCountInString(plain) != 1 {
		t.Fatalf("working glyph must be one cell after ANSI strip: %q", plain)
	}
	r, _ := utf8.DecodeRuneInString(plain)
	if r < 0x2800 || r > 0x28FF {
		t.Fatalf("working glyph not braille: U+%04X", r)
	}
	// Density: not a multi-line diamond tip crop.
	bits := int(r - 0x2800)
	dots := 0
	for b := bits; b > 0; b >>= 1 {
		if b&1 != 0 {
			dots++
		}
	}
	if dots < 4 {
		t.Fatalf("glyph too sparse (%d dots): multi-line diamond crop?", dots)
	}

	idle := statusGlyph(0, phaseIdle)
	if !strings.Contains(idle, brandIdleGlyph) {
		t.Fatalf("idle glyph want %q got %q", brandIdleGlyph, idle)
	}
	if utf8.RuneCountInString(stripANSI(idle)) != 1 {
		t.Fatalf("idle glyph multi-cell: %q", stripANSI(idle))
	}
	if statusGlyph(0, phaseError) == "" {
		t.Fatal("error empty")
	}
}

func TestTryLoadHistoryNearTop(t *testing.T) {
	if !tryLoadHistoryNearTop(10, 0) {
		t.Fatal("should load at top")
	}
	if !tryLoadHistoryNearTop(10, 2) {
		t.Fatal("should load near top")
	}
	if tryLoadHistoryNearTop(0, 0) {
		t.Fatal("no more history")
	}
	if tryLoadHistoryNearTop(10, 20) {
		t.Fatal("not near top")
	}
}

func TestBrandColorsDistinct(t *testing.T) {
	if brandColor(phaseThinking) == brandColor(phaseTools) {
		t.Fatal("phase colors must differ")
	}
}

func TestNanoFirstLineIsSingleCell(t *testing.T) {
	// Compatibility alias for tool rows — must not return multi-line diamond tip.
	s := nanoFirstLine(3, brandColorMulti)
	if s == "" || strings.Contains(s, "\n") {
		t.Fatalf("nanoFirstLine must be single-cell: %q", s)
	}
}
