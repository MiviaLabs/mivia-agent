package cli

import (
	"strings"
	"testing"
	"time"
)

func TestDeriveBrandPhase(t *testing.T) {
	cases := []struct {
		waiting         bool
		open, stream, q int
		err             bool
		want            brandPhase
	}{
		{false, 0, 0, 0, false, phaseIdle},
		{false, 0, 0, 2, false, phaseQueued},
		{true, 0, 0, 0, false, phaseThinking},
		{true, 0, 100, 0, false, phaseStreaming},
		{true, 1, 0, 0, false, phaseTools},
		{true, 3, 0, 0, false, phaseMulti},
		{false, 0, 0, 0, true, phaseError},
	}
	for _, tc := range cases {
		got := deriveBrandPhase(tc.waiting, tc.open, tc.stream, tc.q, tc.err)
		if got != tc.want {
			t.Fatalf("derive(%v,%d,%d,%d,%v)=%v want %v",
				tc.waiting, tc.open, tc.stream, tc.q, tc.err, got, tc.want)
		}
	}
}

func TestRenderWorkChromeHasMark(t *testing.T) {
	out := renderWorkChrome(0, phaseThinking, "model", time.Second, 0, 0, 0, 0, 80)
	if out == "" {
		t.Fatal("empty chrome")
	}
	if !strings.Contains(out, "thinking") {
		t.Fatalf("missing label: %q", out)
	}
	// Work chrome is single-line (not a multi-row hero diamond).
	if strings.Count(out, "\n") > 0 {
		t.Fatalf("work chrome should be one line, got %q", out)
	}
	hasBraille := false
	for _, r := range out {
		if r >= 0x2800 && r <= 0x28FF {
			hasBraille = true
			break
		}
	}
	if !hasBraille {
		t.Fatal("work chrome must include tiny braille brand glyph")
	}
}

func TestMiniMarkColors(t *testing.T) {
	a := renderMiniMark(0, brandColorThinking)
	b := renderMiniMark(0, brandColorTools)
	if a == "" || b == "" {
		t.Fatal("empty marks")
	}
	// Same geometry; colors may collapse under NO_COLOR / ascii profile.
	// At least braille content must exist in both.
	for _, s := range []string{a, b} {
		ok := false
		for _, r := range s {
			if r >= 0x2800 && r <= 0x28FF {
				ok = true
				break
			}
		}
		if !ok {
			t.Fatal("expected braille in mark")
		}
	}
	if brandColor(phaseThinking) == brandColor(phaseTools) {
		t.Fatal("phase colors must differ")
	}
}

func TestNanoFirstLine(t *testing.T) {
	s := nanoFirstLine(3, brandColorMulti)
	if s == "" {
		t.Fatal("empty nano line")
	}
}
