package render

import (
	"strings"
	"testing"

	uikitconfig "github.com/MiviaLabs/mivia-agent/internal/uikit/config"
)

func TestProseMeasure(t *testing.T) {
	cases := []struct {
		width, want int
		why         string
	}{
		{0, uikitconfig.ProseMeasureNarrow, "unmeasured falls back to the narrow measure"},
		{-5, uikitconfig.ProseMeasureNarrow, "negative is treated as unmeasured"},
		{40, 40, "a terminal narrower than the measure wraps to the terminal"},
		{80, uikitconfig.ProseMeasureNarrow, "the narrow breakpoint uses the narrow measure"},
		{119, uikitconfig.ProseMeasureNarrow, "just under wide stays narrow"},
		{120, uikitconfig.ProseMeasureWide, "the wide breakpoint widens the measure"},
		{200, uikitconfig.ProseMeasureWide, "wider than wide does not widen further"},
	}
	for _, c := range cases {
		if got := ProseMeasure(c.width); got != c.want {
			t.Errorf("ProseMeasure(%d) = %d, want %d (%s)", c.width, got, c.want, c.why)
		}
	}
}

func TestWrapBreaksOnWordBoundaries(t *testing.T) {
	got := Wrap("the quick brown fox jumps over the lazy dog", 15)
	for _, line := range got {
		if len(line) > 15 {
			t.Errorf("line %q exceeds the measure", line)
		}
	}
	if strings.Join(got, " ") != "the quick brown fox jumps over the lazy dog" {
		t.Errorf("wrapping lost or reordered words: %q", got)
	}
}

func TestWrapPreservesExistingNewlines(t *testing.T) {
	got := Wrap("one\ntwo", 40)
	if len(got) != 2 || got[0] != "one" || got[1] != "two" {
		t.Errorf("got %q, want the two lines preserved", got)
	}
}

// TestWrapDoesNotSplitALongWord pins the deliberate choice: a token
// longer than the measure is left whole. Splitting an identifier or a
// URL mid-token hurts more than one long line does.
func TestWrapDoesNotSplitALongWord(t *testing.T) {
	long := strings.Repeat("x", 40)
	got := Wrap(long, 10)
	if len(got) != 1 || got[0] != long {
		t.Errorf("got %q, want the long token kept whole", got)
	}
}

func TestWrapKeepsLeadingIndentOnContinuations(t *testing.T) {
	got := Wrap("    alpha beta gamma delta epsilon", 20)
	if len(got) < 2 {
		t.Fatalf("expected a wrap, got %q", got)
	}
	for i, line := range got {
		if !strings.HasPrefix(line, "    ") {
			t.Errorf("line %d %q lost the leading indent", i, line)
		}
	}
}

func TestWrapShortInputIsUnchanged(t *testing.T) {
	got := Wrap("short", 40)
	if len(got) != 1 || got[0] != "short" {
		t.Errorf("got %q, want the input unchanged", got)
	}
}

func TestWrapNonPositiveMeasureIsPassthrough(t *testing.T) {
	got := Wrap("a b c", 0)
	if len(got) != 1 || got[0] != "a b c" {
		t.Errorf("got %q, want passthrough at a zero measure", got)
	}
}

func TestWrapBlankLine(t *testing.T) {
	// A whitespace-only line longer than the measure has no words to
	// wrap and must survive rather than vanish.
	got := Wrap(strings.Repeat(" ", 30), 10)
	if len(got) != 1 {
		t.Errorf("got %q, want the blank line preserved as one row", got)
	}
}

func TestProgressBar(t *testing.T) {
	cases := []struct {
		width, step, total int
		want               string
	}{
		{10, 0, 10, "[..........]   0%"},
		{10, 5, 10, "[#####.....]  50%"},
		{10, 10, 10, "[##########] 100%"},
		{10, 15, 10, "[##########] 100%"}, // clamped: step past total
		{10, -3, 10, "[..........]   0%"}, // clamped: negative step
	}
	for _, c := range cases {
		if got := ProgressBar(c.width, c.step, c.total); got != c.want {
			t.Errorf("ProgressBar(%d,%d,%d) = %q, want %q", c.width, c.step, c.total, got, c.want)
		}
	}
}

func TestProgressBarDegenerateInputs(t *testing.T) {
	// No total and no width mean there is nothing honest to draw.
	if got := ProgressBar(10, 1, 0); got != "" {
		t.Errorf("got %q, want empty for a zero total", got)
	}
	if got := ProgressBar(0, 1, 10); got != "" {
		t.Errorf("got %q, want empty for a zero width", got)
	}
}

// TestProgressBarIsPureASCII pins the section 3 glyph table: the bar
// uses "#" and ".", so it is identical at every colour tier.
func TestProgressBarIsPureASCII(t *testing.T) {
	got := ProgressBar(20, 7, 20)
	for _, r := range got {
		if r > 127 {
			t.Fatalf("non-ASCII rune %q in %q", r, got)
		}
	}
}
