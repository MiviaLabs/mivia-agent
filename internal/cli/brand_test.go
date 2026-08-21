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
		out := stripANSI(renderWorkChrome(0, phase, time.Second, 0, 0, 0, 0, 80, "", "", ""))
		if !strings.Contains(out, brandLabel(phase)) {
			t.Errorf("phase %v missing semantic label %q in %q", phase, brandLabel(phase), out)
		}
	}
}

func TestRenderWorkChrome_ShowsThinkingProgressDetail(t *testing.T) {
	out := stripANSI(renderWorkChrome(
		0, phaseThinking, 3*time.Second, 0, 0, 0, 0, 100, "model thinking (2 s)", "", "",
	))
	if !strings.Contains(out, "model thinking (2 s)") {
		t.Fatalf("thinking header omitted progress detail: %q", out)
	}
}

func TestRenderWorkChrome_BoundsAndSanitizesProgressDetail(t *testing.T) {
	unsafeDetail := "working\nspoof\r\x1b[2J with a very long progress detail"
	wide := stripANSI(renderWorkChrome(
		0, phaseThinking, 3*time.Second, 0, 0, 0, 0, 100, unsafeDetail, "", "",
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
		0, phaseThinking, 3*time.Second, 0, 0, 0, 0, 20,
		unsafeDetail, "", "",
	))
	if strings.ContainsAny(out, "\r\n") {
		t.Fatalf("status bar contains a line break: %q", out)
	}
	for _, r := range out {
		if r < 0x20 || r == 0x7f {
			t.Fatalf("status bar contains control character U+%04X: %q", r, out)
		}
	}
	if got := VisibleWidth(out); got > 20 {
		t.Fatalf("status bar width = %d, want <= 20: %q", got, out)
	}
}

func TestRenderStatusBarSimpleDiamond(t *testing.T) {
	// One physical line; the simple state diamond leads it in every phase:
	// ◆ (filled) while working, ◇ (outline) at rest - never a braille mark.
	out := stripANSI(renderStatusBar(3, phaseThinking, true, time.Second, 0, 0, 0, 0, 0, 80, "", "", ""))
	if strings.Count(out, "\n") > 0 {
		t.Fatalf("status must be one line: %q", out)
	}
	if !strings.HasPrefix(out, "◆ ") {
		t.Fatalf("working status must lead with ◆: %q", out)
	}
	for _, want := range []string{"mivia", "thinking", "─"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q: %q", want, out)
		}
	}
	if strings.Contains(out, "model") {
		t.Fatalf("status bar must not show the model name: %q", out)
	}
	if w := lipgloss.Width(out); w > 80 {
		t.Fatalf("status exceeds width: %d > 80", w)
	}

	idle := stripANSI(renderStatusBar(0, phaseIdle, false, 0, 0, 0, 0, 0, 4, 80, "", "", ""))
	if strings.Count(idle, "\n") > 0 {
		t.Fatalf("idle status must be one line: %q", idle)
	}
	if !strings.HasPrefix(idle, "◇ ") {
		t.Fatalf("idle status must lead with ◇: %q", idle)
	}
	if !strings.Contains(idle, "4 msgs") {
		t.Fatalf("idle missing msgs: %q", idle)
	}

	// Error after a turn (idle path): still a diamond, never blank.
	errBar := stripANSI(renderStatusBar(7, phaseError, false, 0, 0, 0, 0, 0, 0, 80, "", "", ""))
	if !strings.HasPrefix(errBar, "◆ ") {
		t.Fatalf("error status must lead with ◆: %q", errBar)
	}

	// No braille marks in the header - they read as noise at one cell.
	for _, s := range []string{out, idle, errBar} {
		for _, r := range s {
			if r >= 0x2800 && r <= 0x28FF {
				t.Fatalf("braille glyph in header: %q", s)
			}
		}
	}
}

func TestBrandWorkFramesCompleteCells(t *testing.T) {
	// Pulse must be dense enough for a full 1-cell mark, not a raster tip slice.
	if len(BrandWorkFrames) < 8 {
		t.Fatalf("want >= 8 frames, got %d", len(BrandWorkFrames))
	}
	seen := map[string]bool{}
	for i, f := range BrandWorkFrames {
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
			t.Fatalf("frame %d too sparse (%d dots): %q - tip/slice look", i, dots, f)
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
	// Compatibility alias for tool rows - must not return multi-line diamond tip.
	s := nanoFirstLine(3, brandColorMulti)
	if s == "" || strings.Contains(s, "\n") {
		t.Fatalf("nanoFirstLine must be single-cell: %q", s)
	}
}
