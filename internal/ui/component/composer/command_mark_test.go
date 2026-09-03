package composer

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
)

var markCommands = []Command{{Name: "model", Desc: "pick a model"}, {Name: "compact", Desc: "compact"}}

// markRows renders value with the given command set. Rendering the SAME value
// twice, once with the commands registered and once without, is what makes the
// mark testable: the two differ only by the mark, so no assertion has to read
// SGR bookkeeping. ansi.Cut re-emits every preceding style at a cut boundary,
// so scanning a span for a bold code reports the prompt's bold, not the mark's.
func markRows(t *testing.T, value string, cmds []Command) []string {
	t.Helper()
	m := New(theme.Theme{Name: "test"}, theme.TierTrueColor, 40)
	m.SetCommands(cmds)
	m.SetValue(value)
	return strings.Split(m.View(), "\n")
}

func stripAll(rows []string) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = ansi.Strip(r)
	}
	return out
}

// marked reports whether registering the commands changes how value draws.
func marked(t *testing.T, value string) bool {
	t.Helper()
	with := markRows(t, value, markCommands)
	without := markRows(t, value, nil)
	if strings.Join(stripAll(with), "\n") != strings.Join(stripAll(without), "\n") {
		t.Fatalf("the mark changed the TEXT of %q, not only its styling", value)
	}
	return strings.Join(with, "\n") != strings.Join(without, "\n")
}

// TestMatchedCommandWidthTakesOnlyExactCommands is the rule that makes the
// mark worth having: its value is its ABSENCE on a typo, which says the input
// will not run before Enter is pressed rather than after. A prefix rule would
// light up on the way to every command and so say nothing at all.
func TestMatchedCommandWidthTakesOnlyExactCommands(t *testing.T) {
	m := New(theme.Theme{Name: "test"}, theme.TierTrueColor, 40)
	m.SetCommands(markCommands)
	cases := []struct {
		in   string
		want int
	}{
		{"/model", len("/model")},
		{"/model gpt-5", len("/model")},
		{"/model\targ", len("/model")},
		{"/compact", len("/compact")},
		{"/mdel", 0},
		{"/mod", 0},
		{"/modelx", 0},
		{"/", 0},
		{"model", 0},
		{"", 0},
		{"say /model out loud", 0},
	}
	for _, c := range cases {
		if got := m.menu.matchedCommandWidth(c.in); got != c.want {
			t.Errorf("matchedCommandWidth(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

// TestRecognisedCommandIsMarkedAndATypoIsNot: the contrast between the two is
// the whole feature, so both halves are asserted together.
func TestRecognisedCommandIsMarkedAndATypoIsNot(t *testing.T) {
	if !marked(t, "/model") {
		t.Error("a recognised command draws no differently; nothing separates it from a typo")
	}
	if marked(t, "/mdel") {
		t.Error("a typo was marked, which is the one thing the mark must never say")
	}
	if marked(t, "/mod") {
		t.Error("a partial command was marked; it does not run yet")
	}
	if marked(t, "hello there") {
		t.Error("ordinary prose was marked")
	}
}

// TestMarkLeavesTheArgumentsAlone: the composer can vouch for the command name
// and not for the free text after it, so the mark stops at the token.
//
// Asserted as "from the argument onward the two renderings are byte-identical",
// which is what it means for the mark to have closed before the argument. The
// argument's columns cannot be compared with ansi.Cut instead: Cut re-emits
// every style active at the boundary, so the marked row's span differs by that
// carried prefix even when the argument itself is drawn identically.
func TestMarkLeavesTheArgumentsAlone(t *testing.T) {
	const value = "/model gpt-5"
	with := markRows(t, value, markCommands)[1]
	without := markRows(t, value, nil)[1]
	if with == without {
		t.Fatal("precondition: the command token itself was never marked")
	}
	wi, oi := strings.Index(with, "gpt-5"), strings.Index(without, "gpt-5")
	if wi < 0 || oi < 0 {
		t.Fatalf("argument missing from a rendered row:\nwith    %q\nwithout %q", with, without)
	}
	if with[wi:] != without[oi:] {
		t.Errorf("the mark ran past the command token into the argument:\nwith    %q\nwithout %q",
			with[wi:], without[oi:])
	}
}

// TestMarkTouchesOnlyTheFirstRow: a value long enough to wrap must not panic
// and must not mark a later row, where no command token can be.
func TestMarkTouchesOnlyTheFirstRow(t *testing.T) {
	value := "/model " + strings.Repeat("argument ", 20)
	with := markRows(t, value, markCommands)
	without := markRows(t, value, nil)
	if len(with) < 4 {
		t.Fatalf("expected a wrapped body, got %d rows", len(with))
	}
	if len(with) != len(without) {
		t.Fatalf("the mark changed the row count: %d vs %d", len(with), len(without))
	}
	for i := 2; i < len(with); i++ {
		if with[i] != without[i] {
			t.Errorf("row %d differs, but only the first row can hold a command token", i)
		}
	}
}

// TestMarkAtANarrowWidthDoesNotPanic: the token can be wider than the bar.
func TestMarkAtANarrowWidthDoesNotPanic(t *testing.T) {
	for _, w := range []int{0, 1, 4, 8, 12} {
		m := New(theme.Theme{Name: "test"}, theme.TierTrueColor, w)
		m.SetCommands(markCommands)
		m.SetValue("/compact")
		_ = m.View()
	}
}

// TestMarkAppliesOnEveryTier: bold carries the mark where there is no colour
// to spend, so an ASCII or no-TTY terminal still separates a real command from
// a typo.
func TestMarkAppliesOnEveryTier(t *testing.T) {
	for _, tier := range []theme.Tier{theme.TierTrueColor, theme.Tier256, theme.TierASCII, theme.TierNoTTY} {
		draw := func(cmds []Command) string {
			m := New(theme.Theme{Name: "test"}, tier, 40)
			m.SetCommands(cmds)
			m.SetValue("/model")
			return m.View()
		}
		if draw(markCommands) == draw(nil) {
			t.Errorf("tier %v draws a recognised command exactly like an unrecognised one", tier)
		}
	}
}
