package settings

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/MiviaLabs/mivia-agent/internal/ui/component/topbar"
	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
)

// automationsSectionOf reaches the Automations section (nav index 2).
func automationsSectionOf(s Screen) *automationsSection { return s.sections[2].(*automationsSection) }

func focusAutomations(t *testing.T, s Screen) Screen {
	t.Helper()
	for i := 0; i < 2; i++ {
		next, _ := s.Update(tea.KeyPressMsg{Code: tea.KeyDown})
		s = next.(Screen)
	}
	if got := s.sections[s.nav].Title(); got != "Automations" {
		t.Fatalf("nav landed on %q, want Automations", got)
	}
	next, _ := s.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	return next.(Screen)
}

func awaitAutomationsSaveTest(t *testing.T, s Screen, cmd tea.Cmd) Screen {
	t.Helper()
	if cmd == nil {
		t.Fatal("expected a Cmd from an Automations action")
	}
	next, _ := s.Update(cmd())
	return next.(Screen)
}

func TestAutomationsSectionListsEveryAutomation(t *testing.T) {
	s, _ := newHarnessScreen(t, 100, 30)
	plain := ansi.Strip(automationsSectionOf(s).View())
	for _, want := range []string{"Nightly bug audit", "Release checklist", "manual", "scheduled"} {
		if !strings.Contains(plain, want) {
			t.Errorf("Automations view is missing %q:\n%s", want, plain)
		}
	}
}

func TestAutomationsDetailShowsScheduleAndNoRunsYet(t *testing.T) {
	s, _ := newHarnessScreen(t, 100, 30)
	plain := ansi.Strip(automationsSectionOf(s).View())
	if !strings.Contains(plain, "trigger:") {
		t.Errorf("detail is missing the trigger line:\n%s", plain)
	}
	if !strings.Contains(plain, "no runs yet") {
		t.Errorf("expected a fresh automation to show \"no runs yet\":\n%s", plain)
	}
}

func TestTogglingAutomationEnabledPersists(t *testing.T) {
	s, h := newHarnessScreen(t, 100, 30)
	s = focusAutomations(t, s)
	before := automationsSectionOf(s).rows[0]

	next, cmd := s.Update(tea.KeyPressMsg{Text: " ", Code: ' '})
	s = awaitAutomationsSaveTest(t, next.(Screen), cmd)

	var after ports.Automation
	for _, a := range h.SettingsAdapters().Automations.Automations() {
		if a.ID == before.ID {
			after = a
		}
	}
	if after.Enabled == before.Enabled {
		t.Errorf("automation %q enabled flag did not flip: still %v", before.ID, after.Enabled)
	}
}

func TestRemovingAnAutomationUpdatesTheStore(t *testing.T) {
	s, h := newHarnessScreen(t, 100, 30)
	s = focusAutomations(t, s)
	target := automationsSectionOf(s).rows[0].ID

	next, cmd := s.Update(tea.KeyPressMsg{Text: "x", Code: 'x'})
	s = awaitAutomationsSaveTest(t, next.(Screen), cmd)

	for _, a := range h.SettingsAdapters().Automations.Automations() {
		if a.ID == target {
			t.Errorf("automation %q still present after removal", target)
		}
	}
}

// TestTriggerStreamsALiveRunToSuccess drives the full manual-trigger
// path end to end: "t" returns a tea.Batch of the trigger's SaveHandle
// wait and the first watch read. Real bubbletea unpacks a BatchMsg and
// runs each Cmd concurrently, feeding results back independently; this
// test does the same unpacking by hand, then follows only the
// watch-derived chain (the section re-arms watchNext itself after each
// Pending/Running delivery) until the run reaches a terminal state.
func TestTriggerStreamsALiveRunToSuccess(t *testing.T) {
	s, _ := newHarnessScreen(t, 100, 30)
	s = focusAutomations(t, s)

	next, cmd := s.Update(tea.KeyPressMsg{Text: "t", Code: 't'})
	s = next.(Screen)
	if cmd == nil {
		t.Fatal("expected \"t\" to return a Cmd (save + watch)")
	}
	batch, ok := cmd().(tea.BatchMsg)
	if !ok || len(batch) != 2 {
		t.Fatalf("expected a 2-Cmd tea.BatchMsg from \"t\", got %#v", cmd())
	}

	// One leaf is the trigger's own SaveHandle result (Saved almost
	// immediately, per the fake); apply it so s.rebuild() runs, but it
	// carries no run state.
	saveMsg := batch[0]()
	next, _ = s.Update(saveMsg)
	s = next.(Screen)

	// The other leaf is the first watch read; follow its self-re-armed
	// chain to a terminal state, bounded so a regression that stops
	// re-arming (or never terminates) fails fast instead of hanging.
	watchCmd := batch[1]
	deadline := time.After(2 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatalf("run never reached a terminal state; last liveRun=%+v", automationsSectionOf(s).liveRun)
		default:
		}
		msg := watchCmd()
		next, nextCmd := s.Update(msg)
		s = next.(Screen)
		if r := automationsSectionOf(s).liveRun; r != nil && r.State != ports.RunPending && r.State != ports.RunRunning {
			break
		}
		if nextCmd == nil {
			t.Fatal("watch chain stopped re-arming before reaching a terminal state")
		}
		watchCmd = nextCmd
	}

	got := automationsSectionOf(s)
	if got.liveRun == nil || got.liveRun.State != ports.RunSucceeded {
		t.Fatalf("expected the live run to reach RunSucceeded, got %+v", got.liveRun)
	}
	plain := ansi.Strip(got.View())
	if !strings.Contains(plain, "succeeded") {
		t.Errorf("history did not pick up the completed run:\n%s", plain)
	}
}

func TestCursorMoveRefreshesHistoryAndDropsTheLiveRun(t *testing.T) {
	s, _ := newHarnessScreen(t, 100, 30)
	s = focusAutomations(t, s)
	sec := automationsSectionOf(s)
	sec.liveRun = &ports.Run{State: ports.RunRunning}

	next, _ := s.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	s = next.(Screen)
	if automationsSectionOf(s).liveRun != nil {
		t.Error("expected moving the cursor to drop the previous row's live run")
	}
}

func TestUnavailableAutomationsSectionSaysSo(t *testing.T) {
	th := loadTheme(t)
	tb := topbar.New(th, theme.TierTrueColor, ports.ModelInfo{}, ports.Usage{}, 80)
	s := New(th, theme.TierTrueColor, tb, ports.Settings{}, 0)
	next, _ := s.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	s = next.(Screen)
	if got := ansi.Strip(automationsSectionOf(s).View()); !strings.Contains(got, "unavailable") {
		t.Errorf("expected the nil-store Automations section to say unavailable, got %q", got)
	}
}

// TestAutomationsRowsAlignColumns pins settings-screen.md section 1's
// aligned layout: every automation row's enabled/disabled column must
// start at the same screen position regardless of its own name length.
func TestAutomationsRowsAlignColumns(t *testing.T) {
	s, _ := newHarnessScreen(t, 100, 30)
	rows := strings.Split(ansi.Strip(automationsSectionOf(s).View()), "\n")
	var withStatus []string
	for _, r := range rows {
		if strings.Contains(r, "enabled") || strings.Contains(r, "disabled") {
			withStatus = append(withStatus, r)
		}
	}
	if len(withStatus) < 2 {
		t.Fatalf("fixture has fewer than 2 automation rows: %v", withStatus)
	}
	col := func(r string) int {
		if i := strings.Index(r, "disabled"); i >= 0 {
			return i
		}
		return strings.Index(r, "enabled")
	}
	first := col(withStatus[0])
	for i, r := range withStatus[1:] {
		if got := col(r); got != first {
			t.Errorf("row %d: status column at %d, want %d (same as row 0):\n%q\n%q",
				i+1, got, first, withStatus[0], r)
		}
	}
}
