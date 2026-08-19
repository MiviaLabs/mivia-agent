package render

import (
	"slices"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

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
		if ansi.StringWidth(line) > 15 {
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

// TestWrapMeasuresDisplayColumnsNotBytes pins the measure against
// multi-byte text. Counting bytes wraps accented prose at half the
// measure and CJK at a third of it.
func TestWrapMeasuresDisplayColumnsNotBytes(t *testing.T) {
	cases := []struct {
		name  string
		in    string
		meas  int
		wantN int
	}{
		{"accented fits on one row", strings.Repeat("é ", 10), 30, 1},
		{"ascii control", strings.Repeat("e ", 10), 30, 1},
		{"wide runes cost two columns", strings.Repeat("漢 ", 10), 30, 1},
	}
	for _, c := range cases {
		got := Wrap(strings.TrimSpace(c.in), c.meas)
		if len(got) != c.wantN {
			t.Errorf("%s: Wrap(...) produced %d rows, want %d: %q", c.name, len(got), c.wantN, got)
		}
		for _, line := range got {
			if w := ansi.StringWidth(line); w > c.meas {
				t.Errorf("%s: row %q is %d columns, over the measure %d", c.name, line, w, c.meas)
			}
		}
	}
}

// TestWrapKeepsTabIndent pins the other indent character. Trimming only
// spaces let strings.Fields eat the tab, and a tab-indented code line
// lost its indent on every continuation row.
func TestWrapKeepsTabIndent(t *testing.T) {
	got := Wrap("\talpha beta gamma delta epsilon zeta", 20)
	if len(got) < 2 {
		t.Fatalf("expected a wrap, got %q", got)
	}
	for i, line := range got {
		if !strings.HasPrefix(line, "\t") {
			t.Errorf("row %d %q lost the tab indent", i, line)
		}
	}
}

func TestHardWrap(t *testing.T) {
	cases := []struct {
		name  string
		in    string
		width int
		want  []string
	}{
		{"fits", "abc", 10, []string{"abc"}},
		{"unknown width is passthrough", "abcdef", 0, []string{"abcdef"}},
		{"cuts mid-token", "abcdefgh", 3, []string{"abc", "def", "gh"}},
		{"wide runes cut on the column", "漢漢漢", 4, []string{"漢漢", "漢"}},
	}
	for _, c := range cases {
		got := HardWrap(c.in, c.width)
		if len(got) != len(c.want) {
			t.Errorf("%s: HardWrap(%q,%d) = %q, want %q", c.name, c.in, c.width, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("%s: row %d = %q, want %q", c.name, i, got[i], c.want[i])
			}
		}
	}
}

// FuzzWrap asserts the two properties the transcript relies on: no row
// exceeds the measure unless it is a single unbreakable token, and no
// word is lost or reordered.
func FuzzWrap(f *testing.F) {
	f.Add("the quick brown fox", 15)
	f.Add(strings.Repeat("漢 ", 20), 10)
	f.Add("\tindented code line here", 12)
	f.Add("", 0)
	f.Fuzz(func(t *testing.T, text string, measure int) {
		if measure < -4 || measure > 200 || len(text) > 4096 {
			t.Skip()
		}
		got := Wrap(text, measure)
		if measure > 0 {
			for _, line := range got {
				if ansi.StringWidth(line) <= measure {
					continue
				}
				if len(strings.Fields(line)) > 1 {
					t.Fatalf("row %q is %d columns over the measure %d and is breakable",
						line, ansi.StringWidth(line), measure)
				}
			}
		}
		if want, out := strings.Fields(text), strings.Fields(strings.Join(got, "\n")); !slices.Equal(want, out) {
			t.Fatalf("words changed: got %q, want %q", out, want)
		}
	})
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
