package approval

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

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

func TestViewEmptyWhenInactive(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII)
	if got := m.View(); got != "" {
		t.Errorf("got %q, want empty view with no pending request", got)
	}
}

func TestViewShowsPendingRequest(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII)
	m.SetRequest(uievent.ToolPendingBody{ToolCallID: "c1", Name: "run_command", Args: map[string]any{"cmd": "ls"}})
	if !m.Active() {
		t.Fatal("expected Active() after SetRequest")
	}
	got := m.View()
	for _, want := range []string{"run_command", "cmd=ls", "o once", "D deny always"} {
		if !strings.Contains(got, want) {
			t.Errorf("approval view missing %q:\n%s", want, got)
		}
	}
}

func TestClearDismissesPendingRequest(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII)
	m.SetRequest(uievent.ToolPendingBody{ToolCallID: "c1", Name: "run_command"})
	if !m.Active() {
		t.Fatal("expected Active() after SetRequest")
	}
	m.Clear()
	if m.Active() {
		t.Error("expected inactive after Clear()")
	}
	if got := m.View(); got != "" {
		t.Errorf("got %q, want empty view after Clear()", got)
	}
}

func keyMsg(s string) tea.KeyPressMsg {
	return tea.KeyPressMsg{Text: s, Code: rune(s[0])}
}

func TestUpdateIgnoresKeysWithNoRequest(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII)
	_, cmd := m.Update(keyMsg("o"))
	if cmd != nil {
		t.Error("expected no Cmd when nothing is pending")
	}
}

// TestDenyOnceIsDistinctFromDenyAlways pins the highest-severity defect
// this component has had: "d" once meant DecisionDenyAlways, so a user
// pressing d - the documented deny-ONCE key - silently granted a
// session-wide standing denial. d and D must never collapse together.
func TestDenyOnceIsDistinctFromDenyAlways(t *testing.T) {
	resolve := func(t *testing.T, key tea.KeyPressMsg) ports.Decision {
		t.Helper()
		m := New(loadTheme(t), theme.TierASCII)
		m.SetRequest(uievent.ToolPendingBody{ToolCallID: "c1"})
		_, cmd := m.Update(key)
		if cmd == nil {
			t.Fatalf("key %q produced no decision", key.String())
		}
		return cmd().(DecisionMsg).Decision
	}

	if got := resolve(t, keyMsg("d")); got != ports.DecisionDeny {
		t.Errorf("d resolved %v, want DecisionDeny (deny once)", got)
	}
	// Both spellings a terminal may produce for shift+d.
	for _, k := range []tea.KeyPressMsg{
		{Text: "D", Code: 'D'},
		{Code: 'd', Mod: tea.ModShift},
	} {
		if got := resolve(t, k); got != ports.DecisionDenyAlways {
			t.Errorf("%q resolved %v, want DecisionDenyAlways", k.String(), got)
		}
	}
}

func TestUpdateDecisions(t *testing.T) {
	cases := []struct {
		key  string
		want ports.Decision
	}{
		{"o", ports.DecisionOnce},
		{"a", ports.DecisionAlways},
		{"d", ports.DecisionDeny},
		{"D", ports.DecisionDenyAlways},
		// Enter and Esc were unbound by any test. Enter silently meaning
		// deny-always is the same class of defect as "d" once was, and it
		// would have shipped unnoticed.
		{"enter", ports.DecisionOnce},
		{"esc", ports.DecisionDeny},
	}
	for _, c := range cases {
		t.Run(c.key, func(t *testing.T) {
			m := New(loadTheme(t), theme.TierASCII)
			m.SetRequest(uievent.ToolPendingBody{ToolCallID: "c1", Name: "run_command"})
			next, cmd := m.Update(keyMsg(c.key))
			if cmd == nil {
				t.Fatal("expected a DecisionMsg Cmd")
			}
			msg := cmd()
			dm, ok := msg.(DecisionMsg)
			if !ok {
				t.Fatalf("got %T, want DecisionMsg", msg)
			}
			if dm.ToolCallID != "c1" || dm.Decision != c.want {
				t.Errorf("got %+v, want ToolCallID=c1 Decision=%v", dm, c.want)
			}
			if next.Active() {
				t.Error("expected the request to be cleared after a decision")
			}
		})
	}
}

// TestEnterAndEscAreNotStandingDenials states the dangerous mistake
// directly. A standing session-wide denial the user never asked for is
// the failure this file exists to prevent.
func TestEnterAndEscAreNotStandingDenials(t *testing.T) {
	for _, k := range []string{"enter", "esc"} {
		m := New(loadTheme(t), theme.TierASCII)
		m.SetRequest(uievent.ToolPendingBody{ToolCallID: "c1"})
		_, cmd := m.Update(keyMsg(k))
		if cmd == nil {
			t.Fatalf("%q produced no decision", k)
		}
		if got := cmd().(DecisionMsg).Decision; got == ports.DecisionDenyAlways {
			t.Errorf("%q resolved to a standing denial", k)
		}
	}
}

// TestStructuralEnterAndEscape covers the other spelling a terminal may
// send: a key event carrying a Code rather than Text.
func TestStructuralEnterAndEscape(t *testing.T) {
	cases := []struct {
		msg  tea.KeyPressMsg
		want ports.Decision
	}{
		{tea.KeyPressMsg{Code: tea.KeyEnter}, ports.DecisionOnce},
		{tea.KeyPressMsg{Code: tea.KeyEscape}, ports.DecisionDeny},
	}
	for _, c := range cases {
		m := New(loadTheme(t), theme.TierASCII)
		m.SetRequest(uievent.ToolPendingBody{ToolCallID: "c1"})
		_, cmd := m.Update(c.msg)
		if cmd == nil {
			t.Fatalf("%v produced no decision", c.msg)
		}
		if got := cmd().(DecisionMsg).Decision; got != c.want {
			t.Errorf("%v resolved %v, want %v", c.msg, got, c.want)
		}
	}
}

// TestUpdateIgnoresNonKeyMsgWithARequestArmed covers the guard's other
// arm: a request IS armed, but the Msg is not a key press.
func TestUpdateIgnoresNonKeyMsgWithARequestArmed(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII)
	m.SetRequest(uievent.ToolPendingBody{ToolCallID: "c1"})
	next, cmd := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	if cmd != nil {
		t.Error("a resize resolved an approval")
	}
	if !next.Active() {
		t.Error("a resize cleared the pending request")
	}
}

func TestUpdateIgnoresUnknownKey(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII)
	m.SetRequest(uievent.ToolPendingBody{ToolCallID: "c1"})
	next, cmd := m.Update(keyMsg("x"))
	if cmd != nil {
		t.Error("expected no Cmd for an unrecognised key")
	}
	if !next.Active() {
		t.Error("expected the request to remain pending after an unrecognised key")
	}
}

// TestViewDrawsABorderAtEveryTier pins the box: the prompt is framed by
// a RoleBorderFocus border at every colour tier, and the frame degrades
// to plain glyphs (no escape sequences) when the tier carries no colour.
func TestViewDrawsABorderAtEveryTier(t *testing.T) {
	th := loadTheme(t)
	m := New(th, theme.TierTrueColor)
	m.SetRequest(uievent.ToolPendingBody{ToolCallID: "c1", Name: "run_command"})

	colored := m.View()
	for _, glyph := range []string{"╭", "╰", "│"} {
		if !strings.Contains(colored, glyph) {
			t.Errorf("true-color view missing border glyph %q:\n%s", glyph, colored)
		}
	}
	if !strings.Contains(colored, "\x1b[") {
		t.Error("true-color view has no colour escape; the border role must colour it")
	}

	plain := New(th, theme.TierASCII)
	plain.SetRequest(uievent.ToolPendingBody{ToolCallID: "c1", Name: "run_command"})
	// Structural emphasis (the bold title) survives NO_COLOR by design, so
	// the assertion targets colour escapes, not all escapes.
	plainView := plain.View()
	for _, colour := range []string{"38;2", "38;5", "\x1b[3"} {
		if strings.Contains(plainView, colour) {
			t.Errorf("ASCII view carries colour %q:\n%s", colour, plainView)
		}
	}
	if !strings.Contains(plainView, "╭") {
		t.Errorf("ASCII view lost the border:\n%s", plainView)
	}
}
