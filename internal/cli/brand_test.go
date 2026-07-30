package cli

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/charmbracelet/lipgloss"
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
	// The header is two physical lines (diamond rows); the injected detail
	// must be sanitized and never add a third line or control characters.
	unsafeDetail := "working\nspoof\r\x1b[2J with a very long progress detail"
	wide := stripANSI(renderWorkChrome(
		0, phaseThinking, "model", 3*time.Second, 0, 0, 0, 0, 100, unsafeDetail,
	))
	if !strings.Contains(wide, "working spoof") {
		t.Fatalf("status bar did not retain sanitized progress detail: %q", wide)
	}
	wideLines := strings.Split(wide, "\n")
	if len(wideLines) != 2 {
		t.Fatalf("status header must be exactly two lines, got %d: %q", len(wideLines), wide)
	}
	for _, ln := range wideLines {
		for _, r := range ln {
			if r < 0x20 || r == 0x7f {
				t.Fatalf("status bar contains control character U+%04X: %q", r, ln)
			}
		}
	}

	out := stripANSI(renderWorkChrome(
		0, phaseThinking, "model", 3*time.Second, 0, 0, 0, 0, 20,
		unsafeDetail,
	))
	outLines := strings.Split(out, "\n")
	if len(outLines) != 2 {
		t.Fatalf("narrow status header must be exactly two lines, got %d: %q", len(outLines), out)
	}
	for _, ln := range outLines {
		for _, r := range ln {
			if r < 0x20 || r == 0x7f {
				t.Fatalf("status bar contains control character U+%04X: %q", r, ln)
			}
		}
		if got := visibleWidth(ln); got > 20 {
			t.Fatalf("status line width = %d, want <= 20: %q", got, ln)
		}
	}
}

// requireMiniDiamondLines asserts a status header is exactly two lines and
// each line opens with the 4-cell mini state diamond (braille canvas with at
// least one lit dot) — the diamond never leaves the screen in any phase.
func requireMiniDiamondLines(t *testing.T, out string, ctx string) [2]string {
	t.Helper()
	lines := strings.Split(stripANSI(out), "\n")
	if len(lines) != 2 {
		t.Fatalf("%s: status must be two lines, got %d: %q", ctx, len(lines), out)
	}
	for i, ln := range lines {
		runes := []rune(ln)
		if len(runes) < miniLogoPxW/2 {
			t.Fatalf("%s: line %d too short for diamond: %q", ctx, i, ln)
		}
		lit := false
		for _, r := range runes[:miniLogoPxW/2] {
			if r < 0x2800 || r > 0x28FF {
				t.Fatalf("%s: line %d does not open with the braille diamond: %q", ctx, i, ln)
			}
			if r > 0x2800 {
				lit = true
			}
		}
		if !lit {
			t.Fatalf("%s: line %d diamond cells all blank: %q", ctx, i, ln)
		}
	}
	return [2]string{lines[0], lines[1]}
}

func TestRenderStatusBarTwoLineDiamond(t *testing.T) {
	// Working chrome: two lines, the state diamond leading both.
	out := renderStatusBar(3, phaseThinking, "model", true, time.Second, 0, 0, 0, 0, 0, 80, "")
	lines := requireMiniDiamondLines(t, out, "working")
	if !strings.Contains(lines[0], "mivia") {
		t.Fatalf("missing brand: %q", lines[0])
	}
	if !strings.Contains(lines[0], "model") {
		t.Fatalf("missing model: %q", lines[0])
	}
	if !strings.Contains(lines[0], "thinking") {
		t.Fatalf("missing phase: %q", lines[0])
	}
	if !strings.Contains(lines[0], "─") {
		t.Fatalf("expected middle rule: %q", lines[0])
	}
	// Diamond (left) precedes the phase label (right).
	if di, th := strings.IndexFunc(lines[0], func(r rune) bool { return r > 0x2800 && r <= 0x28FF }), strings.Index(lines[0], "thinking"); di < 0 || th < 0 || di > th {
		t.Fatalf("expected left diamond before right phase: %q", lines[0])
	}
	// Both lines respect the width budget.
	for i, ln := range strings.Split(out, "\n") {
		if w := lipgloss.Width(ln); w > 80 {
			t.Fatalf("line %d exceeds width: %d > 80", i, w)
		}
	}

	// Idle chrome: same two-line shape, diamond still present.
	idle := renderStatusBar(0, phaseIdle, "model", false, 0, 0, 0, 0, 0, 4, 80, "")
	idleLines := requireMiniDiamondLines(t, idle, "idle")
	if !strings.Contains(idleLines[0], "mivia") {
		t.Fatalf("idle missing brand: %q", idleLines[0])
	}
	if !strings.Contains(idleLines[0], "4 msgs") {
		t.Fatalf("idle missing msgs: %q", idleLines[0])
	}

	// The retired braille MIVIA wordmark stays gone.
	for _, s := range []string{out, idle} {
		if strings.Contains(stripANSI(s), "⣿⠇⣶") {
			t.Fatalf("braille wordmark resurfaced: %q", s)
		}
	}

	// Error phase (idle path after failure): frozen diamond still on screen.
	errBar := renderStatusBar(7, phaseError, "model", false, 0, 0, 0, 0, 0, 0, 80, "")
	requireMiniDiamondLines(t, errBar, "error")
	if errBar != renderStatusBar(13, phaseError, "model", false, 0, 0, 0, 0, 0, 0, 80, "") {
		t.Fatal("error diamond must be frozen across frames")
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
