package mark

import (
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/ui/render"
	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
)

// TestGlyphsMatchWaveTables tests the braille pulse cycle across TrueColor and ASCII tiers.
func TestGlyphsMatchWaveTables(t *testing.T) {
	th := loadTheme(t)
	m := New(th, theme.TierTrueColor, Thinking)
	var got []rune
	for i := 0; i < 8; i++ {
		got = append(got, m.Glyph())
		next, _ := m.Update(TickMsg{})
		m = next
	}
	if string(got) != "⠶⠛⠿⣿⣶⠿⠛⠶" {
		t.Errorf("thinking cycle = %q, want ⠶⠛⠿⣿⣶⠿⠛⠶", string(got))
	}

	m = New(th, theme.TierASCII, Thinking)
	var asc string
	for i := 0; i < 8; i++ {
		asc += string(m.Glyph())
		next, _ := m.Update(TickMsg{})
		m = next
	}
	if asc != ".+**+*+." {
		t.Errorf("ASCII cycle = %q, want .+**+*+.", asc)
	}

	if g := New(th, theme.TierTrueColor, Idle).Glyph(); g != '⬖' {
		t.Errorf("idle glyph = %q, want ⬖", g)
	}
	if g := New(th, theme.TierASCII, Idle).Glyph(); g != '<' {
		t.Errorf("ASCII idle = %q, want <", g)
	}
	if g := New(th, theme.TierASCII, Failed).Glyph(); g != 'X' {
		t.Errorf("ASCII failed = %q, want X", g)
	}
}

// TestWaitingBlinksAtAQuarterRate pins the speed semantics: waiting
// advances one frame per four ticks.
func TestWaitingBlinksAtAQuarterRate(t *testing.T) {
	th := loadTheme(t)
	m := New(th, theme.TierTrueColor, Waiting)
	var seq string
	for i := 0; i < 8; i++ {
		seq += string(m.Glyph())
		next, _ := m.Update(TickMsg{})
		m = next
	}
	if seq != "⠶⠶⠶⠶⠛⠛⠛⠛" {
		t.Errorf("waiting sequence = %q, want ⠶⠶⠶⠶⠛⠛⠛⠛ across 8 ticks", seq)
	}
}

// TestStaticStatesIgnoreTicks pins the load-bearing rule: a static mark
// means the agent is not working, so ticks must not move it.
func TestStaticStatesIgnoreTicks(t *testing.T) {
	th := loadTheme(t)
	for _, st := range []State{Idle, Failed, Done} {
		m := New(th, theme.TierTrueColor, st)
		before := m.Glyph()
		next, cmd := m.Update(TickMsg{})
		if cmd != nil {
			t.Errorf("%s re-armed a ticker; static states run no clock", st.Word())
		}
		if next.Glyph() != before {
			t.Errorf("%s moved on a tick", st.Word())
		}
	}
}

// TestSetStateRestartsTheCycle: a new activity restarts the animation,
// never resumes a stale mid-cycle position.
func TestSetStateRestartsTheCycle(t *testing.T) {
	th := loadTheme(t)
	m := New(th, theme.TierTrueColor, Thinking)
	for i := 0; i < 3; i++ {
		next, _ := m.Update(TickMsg{})
		m = next
	}
	m.SetState(Running)
	if g := m.Glyph(); g != '⠶' {
		t.Errorf("cycle did not restart: first glyph %q, want ⠶", g)
	}
}

func TestAutonomousStatesAreMonochromeAndWaitingIsWarning(t *testing.T) {
	th := loadTheme(t)
	for _, st := range []State{Thinking, Running} {
		m := New(th, theme.TierTrueColor, st)
		view := m.View()
		warnSeq := render.Role(th, theme.TierTrueColor, theme.RoleWarning).Render("X")
		// extract the escape code part
		warnEscape := strings.TrimSuffix(warnSeq, "X\x1b[0m")
		warnEscape = strings.TrimSuffix(warnEscape, "X\x1b[m")
		if strings.Contains(view, warnEscape) {
			t.Errorf("%s view must not contain RoleWarning escape (%q)", st.Word(), warnEscape)
		}
	}

	for _, st := range []State{Waiting, Pending} {
		m := New(th, theme.TierTrueColor, st)
		view := m.View()
		warnSeq := render.Role(th, theme.TierTrueColor, theme.RoleWarning).Render("X")
		warnEscape := strings.TrimSuffix(warnSeq, "X\x1b[0m")
		warnEscape = strings.TrimSuffix(warnEscape, "X\x1b[m")
		if !strings.Contains(view, warnEscape) {
			t.Errorf("%s view must contain RoleWarning escape (%q) in %q", st.Word(), warnEscape, view)
		}
	}
}

func loadTheme(t *testing.T) theme.Theme {
	t.Helper()
	themes, err := theme.Embedded()
	if err != nil {
		t.Fatal(err)
	}
	for _, th := range themes {
		if th.Name == "mivia-dark" {
			return th
		}
	}
	t.Fatal("mivia-dark theme not found")
	return theme.Theme{}
}
