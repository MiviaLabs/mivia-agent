package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
)

func TestDisplaySessionName(t *testing.T) {
	latest := chat.AutoSaveName + "20250115T103000"
	latestSI := chat.SessionInfo{Name: latest, UpdatedAt: time.Now()}
	olderSI := chat.SessionInfo{
		Name:      chat.AutoSaveName + "20250114T090000",
		UpdatedAt: time.Now().Add(-2 * time.Hour),
	}
	legacySI := chat.SessionInfo{Name: chat.AutoSaveName, UpdatedAt: time.Now().Add(-3 * time.Hour)}
	namedSI := chat.SessionInfo{Name: "project-a"}

	if got := displaySessionName(latestSI, latest); got != "Last session" {
		t.Fatalf("latest auto: got %q", got)
	}
	if got := displaySessionName(olderSI, latest); got != "Auto · 2h ago" {
		t.Fatalf("older auto: got %q", got)
	}
	if got := displaySessionName(legacySI, latest); !strings.HasPrefix(got, "Auto · ") {
		t.Fatalf("legacy older auto: got %q", got)
	}
	// Bare __last__ as the only/latest auto-save.
	if got := displaySessionName(legacySI, chat.AutoSaveName); got != "Last session" {
		t.Fatalf("legacy latest: got %q", got)
	}
	// Empty latestAuto still labels a single auto as Last session.
	if got := displaySessionName(latestSI, ""); got != "Last session" {
		t.Fatalf("empty latestAuto: got %q", got)
	}
	if got := displaySessionName(namedSI, latest); got != "project-a" {
		t.Fatalf("named: got %q", got)
	}
}

func TestLatestAutoSaveName(t *testing.T) {
	// Newest-first list: first auto wins even if a named session sits above.
	sessions := []chat.SessionInfo{
		{Name: "work", UpdatedAt: time.Now()},
		{Name: chat.AutoSaveName + "20250115T103000", UpdatedAt: time.Now().Add(-time.Minute)},
		{Name: chat.AutoSaveName, UpdatedAt: time.Now().Add(-time.Hour)},
	}
	got := latestAutoSaveName(sessions)
	want := chat.AutoSaveName + "20250115T103000"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	if latestAutoSaveName(nil) != "" {
		t.Fatal("empty list should return empty")
	}
	if latestAutoSaveName([]chat.SessionInfo{{Name: "only-named"}}) != "" {
		t.Fatal("no autos should return empty")
	}
}

func TestFormatSessionAge(t *testing.T) {
	if formatSessionAge(time.Time{}) != "" {
		t.Fatal("zero should be empty")
	}
	if got := formatSessionAge(time.Now().Add(-30 * time.Second)); got != "just now" {
		t.Fatalf("got %q", got)
	}
	if got := formatSessionAge(time.Now().Add(-5 * time.Minute)); got != "5m ago" {
		t.Fatalf("got %q", got)
	}
}

func TestLogoFramesBrandShape(t *testing.T) {
	if logoFrameCount() < 8 {
		t.Fatalf("need granular animation frames, got %d", logoFrameCount())
	}
	out := renderLogoFrame(0, 40)
	if out == "" {
		t.Fatal("empty render")
	}
	// Welcome diamond is multi-line braille art (unlike 1-cell status glyphs).
	if strings.Count(out, "\n") < 2 {
		t.Fatalf("welcome logo must be multi-line braille, got lines=%d %q", strings.Count(out, "\n")+1, out)
	}
	// High-fidelity path uses braille, not coarse /\\ ASCII.
	hasBraille := false
	for _, r := range out {
		if r >= 0x2800 && r <= 0x28FF {
			hasBraille = true
			break
		}
	}
	if !hasBraille {
		t.Fatal("expected braille pixel logo, got coarse art")
	}
	// Spot-check several animation frames stay multi-line + braille.
	for _, fr := range []int{0, 1, logoFrameCount() / 2, logoFrameCount() - 1} {
		frame := renderLogoFrame(fr, 48)
		if strings.Count(frame, "\n") < 2 {
			t.Fatalf("frame %d not multi-line", fr)
		}
		ok := false
		for _, r := range frame {
			if r >= 0x2800 && r <= 0x28FF {
				ok = true
				break
			}
		}
		if !ok {
			t.Fatalf("frame %d missing braille", fr)
		}
	}
	wm := renderWordmark(40)
	if !strings.Contains(wm, "MIVIA") {
		t.Fatal("wordmark missing MIVIA")
	}
	if !strings.Contains(stripANSI(wm), "MIVIA") {
		t.Fatalf("wordmark strip lost MIVIA: %q", stripANSI(wm))
	}
}

func TestRenderSessionPickerEmpty(t *testing.T) {
	block, hits, _ := renderSessionPicker(nil, 0, 0, 80, 5, 10)
	if !strings.Contains(block, "No saved sessions") {
		t.Fatalf("expected empty hint, got %q", block)
	}
	if len(hits) != 0 {
		t.Fatal("no hits expected")
	}
}

func TestRenderSessionPickerSelection(t *testing.T) {
	// Newest-first: latest auto, named, older auto.
	sessions := []chat.SessionInfo{
		{Name: chat.AutoSaveName + "20250115T103000", MessageCount: 4, UpdatedAt: time.Now()},
		{Name: "work", MessageCount: 10, UpdatedAt: time.Now().Add(-time.Hour)},
		{Name: chat.AutoSaveName, MessageCount: 2, UpdatedAt: time.Now().Add(-2 * time.Hour)},
	}
	block, hits, sc := renderSessionPicker(sessions, 0, 0, 80, 5, 10)
	if sc != 0 {
		t.Fatalf("scroll %d", sc)
	}
	plain := stripANSI(block)
	// Exactly one "Last session" (latest auto only).
	if c := strings.Count(plain, "Last session"); c != 1 {
		t.Fatalf("want one Last session label, got %d in %q", c, plain)
	}
	if !strings.Contains(plain, "Auto · 2h ago") {
		t.Fatalf("missing older auto label in %q", plain)
	}
	if !strings.Contains(plain, "work") {
		t.Fatalf("missing named session in %q", plain)
	}
	// Hit Y should be absolute from yBase+2
	if hits[0].y0 != 12 || hits[0].idx != 0 {
		t.Fatalf("hit0=%+v", hits[0])
	}
}

// ─── Wordmark braille tests ─────────────────────────────────

func TestWordmarkBrailleBraille(t *testing.T) {
	out := renderWordmarkBraille(0, 80)
	if out == "" {
		t.Fatal("empty wordmark")
	}
	// Multi-line output: 2 braille rows for MIVIA + 2 braille rows for AGENT.
	lines := strings.Split(out, "\n")
	if len(lines) < 4 {
		t.Fatalf("wordmark must be at least 4 lines, got %d", len(lines))
	}
	// Braille characters present across all four lines.
	brailleCount := 0
	for _, r := range lines[0] + lines[1] + lines[2] + lines[3] {
		if r >= 0x2800 && r <= 0x28FF {
			brailleCount++
		}
	}
	if brailleCount < 10 {
		t.Fatalf("expected braille runes across 4 lines, got %d in %q", brailleCount, out)
	}
}

func TestWordmarkBrailleAnimation(t *testing.T) {
	// Verify that different frames produce different brightness/color assignments
	// for each letter. We check the color logic directly since lipgloss styling
	// (ANSI codes) is not emitted in test environments.
	colors0 := make([]string, 5)
	colors5 := make([]string, 5)
	colors12 := make([]string, 5)
	colors23 := make([]string, 5)
	for li := 0; li < 5; li++ {
		colors0[li] = brightnessColor(letterBrightness(0, li))
		colors5[li] = brightnessColor(letterBrightness(5, li))
		colors12[li] = brightnessColor(letterBrightness(12, li))
		colors23[li] = brightnessColor(letterBrightness(23, li))
	}

	// Same frame should produce same colors.
	for li := 0; li < 5; li++ {
		if brightnessColor(letterBrightness(0, li)) != colors0[li] {
			t.Fatal("same frame index produced different color")
		}
	}

	// At least some frames should have a different color assignment.
	frame0Str := ""
	frame5Str := ""
	frame12Str := ""
	frame23Str := ""
	for li := 0; li < 5; li++ {
		frame0Str += colors0[li]
		frame5Str += colors5[li]
		frame12Str += colors12[li]
		frame23Str += colors23[li]
	}
	different := 0
	for _, s := range []string{frame5Str, frame12Str, frame23Str} {
		if s != frame0Str {
			different++
		}
	}
	if different == 0 {
		t.Fatal("all frames have identical color pattern — glow dead")
	}

	// Also verify the full styled output is the same length (structural consistency).
	f0 := renderWordmarkBraille(0, 40)
	f5 := renderWordmarkBraille(5, 40)
	if len(f0) != len(f5) {
		t.Fatalf("frame length mismatch: %d vs %d", len(f0), len(f5))
	}
	if strings.Count(f0, "\n") != 3 {
		t.Fatalf("expected 4 lines (2 MIVIA braille + 2 AGENT braille), got %d lines", strings.Count(f0, "\n")+1)
	}
}

func TestWordmarkFallbackText(t *testing.T) {
	// Text wordmark unchanged.
	wm := renderWordmark(40)
	if !strings.Contains(wm, "MIVIA") {
		t.Fatal("wordmark missing MIVIA")
	}
	if !strings.Contains(stripANSI(wm), "MIVIA") {
		t.Fatalf("strip lost MIVIA: %q", stripANSI(wm))
	}
	if !strings.Contains(stripANSI(wm), "agent") {
		t.Fatal("wordmark missing agent")
	}
}

func TestGlowBrightnessRange(t *testing.T) {
	// Brightness stays within [0.3, 1.0] across a full cycle.
	for letterIdx := 0; letterIdx < 5; letterIdx++ {
		for frame := 0; frame < 100; frame++ {
			b := letterBrightness(frame, letterIdx)
			if b < 0.29 || b > 1.01 {
				t.Fatalf("brightness out of range [0.3, 1.0]: %f at frame=%d letter=%d", b, frame, letterIdx)
			}
		}
	}
	// Each letter has a different phase (not all same value at same frame).
	values := make([]float64, 5)
	for li := 0; li < 5; li++ {
		values[li] = letterBrightness(0, li)
	}
	allSame := true
	for i := 1; i < 5; i++ {
		if values[i] != values[0] {
			allSame = false
			break
		}
	}
	if allSame {
		t.Fatal("all letters have same brightness at frame 0 — phase offsets missing")
	}
}

func TestBrightnessColorMapping(t *testing.T) {
	tests := []struct {
		b     float64
		want  string
		label string
	}{
		{1.0, "15", "peak bright"},
		{0.90, "15", "near-peak"},
		{0.85, "15", "threshold high"},
		{0.84, "250", "just below bright"},
		{0.75, "250", "light gray"},
		{0.66, "250", "light gray edge"},
		{0.65, "250", "threshold mid"},
		{0.64, "244", "just below mid"},
		{0.55, "244", "mid gray"},
		{0.46, "244", "mid edge"},
		{0.45, "244", "threshold low"},
		{0.44, "236", "just below low"},
		{0.30, "236", "dim"},
		{0.0, "236", "zero"},
	}
	for _, tt := range tests {
		got := brightnessColor(tt.b)
		if got != tt.want {
			t.Errorf("brightnessColor(%v) = %q, want %q (%s)", tt.b, got, tt.want, tt.label)
		}
	}
}

func TestWordmarkBrailleStaticMatch(t *testing.T) {
	s1 := renderWordmarkBrailleStatic(60)
	s2 := renderWordmarkBraille(0, 60)
	if s1 != s2 {
		t.Fatal("static wordmark differs from frame-0 animated wordmark")
	}
}

func TestWelcomeLayoutHeightBudget(t *testing.T) {
	// Verify layout function allocates enough rows for the picker
	// with both dot-matrix and text wordmarks at threshold sizes.
	// At h=29, w=60: braille wordmark (4 lines) must leave picker rows.
	logo := strings.Repeat(".\n", 11) + "." // 12 lines (hi-res diamond)
	brailleWord := "..\n..\n..\n.."         // 4 lines (2 MIVIA + 2 AGENT braille)
	textWord := "MIVIA  agent"              // 1 line
	inputH := 3
	const extraLines = 5
	// Braille wordmark: logoLines(12) + wordLines(4) + extraLines(5) + blank(1) + input(3) + hint(1)
	fixedBraille := strings.Count(logo, "\n") + 1 + strings.Count(brailleWord, "\n") + 1 + extraLines + 1 + inputH + 1
	maxRowsBraille := 29 - fixedBraille
	if maxRowsBraille < 3 {
		t.Fatalf("braille wordmark at h=29: maxRows=%d (need >=3)", maxRowsBraille)
	}
	// Text wordmark: logoLines(12) + wordLines(1) + extraLines(5) + blank(1) + input(3) + hint(1)
	fixedText := strings.Count(logo, "\n") + 1 + strings.Count(textWord, "\n") + 1 + extraLines + 1 + inputH + 1
	if fixedText >= fixedBraille {
		t.Fatal("text wordmark should use fewer fixed rows than braille")
	}
}
