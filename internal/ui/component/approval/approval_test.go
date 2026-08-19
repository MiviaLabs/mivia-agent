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
	for _, want := range []string{"run_command", "cmd=ls", "[y] once", "[d] deny always"} {
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
	_, cmd := m.Update(keyMsg("y"))
	if cmd != nil {
		t.Error("expected no Cmd when nothing is pending")
	}
}

func TestUpdateDecisions(t *testing.T) {
	cases := []struct {
		key  string
		want ports.Decision
	}{
		{"y", ports.DecisionOnce},
		{"a", ports.DecisionAlways},
		{"n", ports.DecisionDeny},
		{"d", ports.DecisionDenyAlways},
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
