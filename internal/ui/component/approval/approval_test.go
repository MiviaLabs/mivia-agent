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
