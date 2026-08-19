package statusline

import (
	"strings"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
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
	if got := m.View(time.Now()); got != "" {
		t.Errorf("got %q, want empty view when inactive", got)
	}
}

func TestStartArmsAndReturnsTickCmd(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII)
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	cmd := m.Start("thinking", start)
	if cmd == nil {
		t.Fatal("expected Start to return a tick Cmd")
	}
	if _, ok := cmd().(TickMsg); !ok {
		t.Errorf("got %T, want the scheduled Cmd to yield TickMsg", cmd())
	}
	if !m.Active() {
		t.Fatal("expected Active() after Start")
	}
	got := m.View(start.Add(3 * time.Second))
	for _, want := range []string{"thinking", "3s"} {
		if !strings.Contains(got, want) {
			t.Errorf("statusline view missing %q: %q", want, got)
		}
	}
}

func TestStopClearsView(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII)
	m.Start("thinking", time.Now())
	m.Stop()
	if m.Active() {
		t.Error("expected Active() false after Stop")
	}
	if got := m.View(time.Now()); got != "" {
		t.Errorf("got %q, want empty view after Stop", got)
	}
}

func TestUpdateTickAdvancesFrameAndInactiveIgnoresTick(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII)
	// Inactive: a stray tick must be a no-op with no rescheduling.
	next, cmd := m.Update(TickMsg{})
	if cmd != nil {
		t.Error("expected no Cmd from a tick while inactive")
	}
	m = next

	m.Start("running", time.Now())
	before := m.frame
	next, cmd = m.Update(TickMsg{})
	if cmd == nil {
		t.Error("expected the tick to reschedule while active")
	}
	if next.frame == before {
		t.Error("expected the spinner frame to advance on tick")
	}
}

func TestSetLabelDoesNotResetElapsed(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII)
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	m.Start("thinking", start)
	m.SetLabel("running tool")
	got := m.View(start.Add(5 * time.Second))
	if !strings.Contains(got, "running tool") || !strings.Contains(got, "5s") {
		t.Errorf("got %q, want label updated and elapsed clock preserved", got)
	}
}
