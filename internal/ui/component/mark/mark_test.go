package mark

import (
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
)

// TestGlyphsMatchTheMockTables pins the mock's UNI/ASC tables: the
// logo rotates U+2B16..U+2B19 without becoming another object, streaming
// runs the diamond fill cycle, and the ASCII tier falls back to the
// mock's ASC rotation.
func TestGlyphsMatchTheMockTables(t *testing.T) {
	th := loadTheme(t)
	m := New(th, theme.TierTrueColor, Thinking)
	var got []rune
	for i := 0; i < 4; i++ {
		got = append(got, m.Glyph())
		next, _ := m.Update(TickMsg{})
		m = next
	}
	if string(got) != "⬖⬘⬗⬙" {
		t.Errorf("thinking cycle = %q, want ⬖⬘⬗⬙", string(got))
	}

	m = New(th, theme.TierASCII, Thinking)
	var asc string
	for i := 0; i < 4; i++ {
		asc += string(m.Glyph())
		next, _ := m.Update(TickMsg{})
		m = next
	}
	if asc != "<^>v" {
		t.Errorf("ASCII cycle = %q, want <^>v", asc)
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
// advances one frame per four ticks, so a slow mark reads as blocked on
// someone else.
func TestWaitingBlinksAtAQuarterRate(t *testing.T) {
	th := loadTheme(t)
	m := New(th, theme.TierTrueColor, Waiting)
	var seq string
	for i := 0; i < 8; i++ {
		seq += string(m.Glyph())
		next, _ := m.Update(TickMsg{})
		m = next
	}
	if seq != "⬖⬖⬖⬖◇◇◇◇" {
		t.Errorf("waiting sequence = %q, want four ⬖ then four ◇", seq)
	}
}

// TestStaticStatesIgnoreTicks pins the load-bearing rule: a static mark
// means the agent is not working, so ticks must not move it.
func TestStaticStatesIgnoreTicks(t *testing.T) {
	th := loadTheme(t)
	for _, st := range []State{Idle, Pending, Failed, Done} {
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
	if g := m.Glyph(); g != '⬖' {
		t.Errorf("cycle did not restart: first glyph %q, want ⬖", g)
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
