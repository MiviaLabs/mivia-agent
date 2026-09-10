package transcript

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

// TestLiveToolRowSaysWhatItWaitsFor pins C5: a tool block's detail
// column carries a suffix that says what a live row is waiting for -
// "waiting to run" while KindToolPending, "waiting for result" once
// KindToolStart lands - and neither survives once the call settles.
// "requesting approval" must never appear: a policy-auto-approved
// pending call never enters the approval queue (events.go), so the
// phrase would misdescribe it.
func TestLiveToolRowSaysWhatItWaitsFor(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII)
	m.SetSize(80, 24)

	m, _ = m.HandleEvent(uievent.Event{
		Kind: uievent.KindToolPending,
		Body: uievent.ToolPendingBody{ToolCallID: "c1", Name: "run_command"},
	})
	if got := ansi.Strip(m.Dump()); !strings.Contains(got, "waiting to run") {
		t.Errorf("pending row = %q, want it to contain %q", got, "waiting to run")
	}

	m, _ = m.HandleEvent(uievent.Event{
		Kind: uievent.KindToolStart,
		Body: uievent.ToolStartBody{ToolCallID: "c1", Name: "run_command"},
	})
	if got := ansi.Strip(m.Dump()); !strings.Contains(got, "waiting for result") {
		t.Errorf("running row = %q, want it to contain %q", got, "waiting for result")
	} else if strings.Contains(got, "waiting to run") {
		t.Errorf("running row = %q, must not still say %q", got, "waiting to run")
	}

	m, _ = m.HandleEvent(uievent.Event{
		Kind: uievent.KindToolEnd,
		Body: uievent.ToolEndBody{ToolCallID: "c1", Name: "run_command", OK: true, Result: "ok"},
	})
	got := ansi.Strip(m.Dump())
	if strings.Contains(got, "waiting to run") || strings.Contains(got, "waiting for result") {
		t.Errorf("settled row = %q, must drop both wait phrases", got)
	}
	if strings.Contains(got, "requesting approval") {
		t.Errorf("settled row = %q, must never say %q", got, "requesting approval")
	}
}
