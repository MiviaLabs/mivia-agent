package settings

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/keymap"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
)

func newTestAutomationsSection(t *testing.T, store ports.AutomationSettings) *automationsSection {
	t.Helper()
	th := loadTheme(t)
	sec := newAutomationsSection(store)
	sec.SetTheme(th, theme.TierTrueColor)
	sec.SetSize(80, 24)
	return sec
}

func awaitAutomationsSaveTest(t *testing.T, sec *automationsSection, cmd tea.Cmd) *automationsSection {
	t.Helper()
	if cmd == nil {
		t.Fatal("expected a Cmd from an Automations action")
	}
	next, _ := sec.Update(cmd())
	return next.(*automationsSection)
}

func TestAutomationsSectionListsEveryAutomation(t *testing.T) {
	h := newMockSettings()
	sec := newTestAutomationsSection(t, h.SettingsAdapters().Automations)
	plain := ansi.Strip(sec.View())
	for _, want := range []string{"Nightly bug audit", "Release checklist", "manual", "scheduled"} {
		if !strings.Contains(plain, want) {
			t.Errorf("Automations view is missing %q:\n%s", want, plain)
		}
	}
}

func TestAutomationsDetailShowsScheduleAndNoRunsYet(t *testing.T) {
	h := newMockSettings()
	sec := newTestAutomationsSection(t, h.SettingsAdapters().Automations)
	plain := ansi.Strip(sec.View())
	if !strings.Contains(plain, "trigger:") {
		t.Errorf("detail is missing the trigger line:\n%s", plain)
	}
	if !strings.Contains(plain, "no runs yet") {
		t.Errorf("expected a fresh automation to show \"no runs yet\":\n%s", plain)
	}
}

func TestTogglingAutomationEnabledPersists(t *testing.T) {
	h := newMockSettings()
	sec := newTestAutomationsSection(t, h.SettingsAdapters().Automations)
	before := sec.rows[0]

	next, cmd := sec.Update(tea.KeyPressMsg{Text: " ", Code: ' '})
	sec = awaitAutomationsSaveTest(t, next.(*automationsSection), cmd)

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
	h := newMockSettings()
	sec := newTestAutomationsSection(t, h.SettingsAdapters().Automations)
	target := sec.rows[0].ID

	next, cmd := sec.Update(tea.KeyPressMsg{Text: "x", Code: 'x'})
	sec = awaitAutomationsSaveTest(t, next.(*automationsSection), cmd)

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
	h := newMockSettings()
	sec := newTestAutomationsSection(t, h.SettingsAdapters().Automations)

	next, cmd := sec.Update(tea.KeyPressMsg{Text: "t", Code: 't'})
	sec = next.(*automationsSection)
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
	next, _ = sec.Update(saveMsg)
	sec = next.(*automationsSection)

	// The other leaf is the first watch read; follow its self-re-armed
	// chain to a terminal state, bounded so a regression that stops
	// re-arming (or never terminates) fails fast instead of hanging.
	watchCmd := batch[1]
	deadline := time.After(2 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatalf("run never reached a terminal state; last liveRun=%+v", sec.liveRun)
		default:
		}
		msg := watchCmd()
		next, nextCmd := sec.Update(msg)
		sec = next.(*automationsSection)
		if r := sec.liveRun; r != nil && r.State != ports.RunPending && r.State != ports.RunRunning {
			break
		}
		if nextCmd == nil {
			t.Fatal("watch chain stopped re-arming before reaching a terminal state")
		}
		watchCmd = nextCmd
	}

	if sec.liveRun == nil || sec.liveRun.State != ports.RunSucceeded {
		t.Fatalf("expected the live run to reach RunSucceeded, got %+v", sec.liveRun)
	}
	plain := ansi.Strip(sec.View())
	if !strings.Contains(plain, "succeeded") {
		t.Errorf("history did not pick up the completed run:\n%s", plain)
	}
}

func TestCursorMoveRefreshesHistoryAndDropsTheLiveRun(t *testing.T) {
	h := newMockSettings()
	sec := newTestAutomationsSection(t, h.SettingsAdapters().Automations)
	sec.liveRun = &ports.Run{State: ports.RunRunning}

	next, _ := sec.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	sec = next.(*automationsSection)
	if sec.liveRun != nil {
		t.Error("expected moving the cursor to drop the previous row's live run")
	}
}

func TestUnavailableAutomationsSectionSaysSo(t *testing.T) {
	sec := newTestAutomationsSection(t, nil)
	if got := ansi.Strip(sec.View()); !strings.Contains(got, "unavailable") {
		t.Errorf("expected the nil-store Automations section to say unavailable, got %q", got)
	}
}

// TestAutomationsHintsAdvertiseTriggerAndNew pins a real gap found
// after the create/edit editor landed: "t" (trigger a manual run) and
// "n" (open the new-automation form) both already worked as key
// bindings, but Hints() never listed them, so the on-screen hint bar
// never told a user either affordance existed - the exact "how do I
// even run this" question a user would hit with no visible CTA.
func TestAutomationsHintsAdvertiseTriggerAndNew(t *testing.T) {
	h := newMockSettings()
	sec := newTestAutomationsSection(t, h.SettingsAdapters().Automations)
	hints := sec.Hints()
	has := func(id keymap.ID) bool {
		for _, h := range hints {
			if h == id {
				return true
			}
		}
		return false
	}
	if !has(keymap.IDSettingsTrigger) {
		t.Error("expected Hints() to advertise IDSettingsTrigger (t: trigger a manual run)")
	}
	if !has(keymap.IDSettingsNew) {
		t.Error("expected Hints() to advertise IDSettingsNew (n: add a new automation)")
	}
}

// TestAutomationsRowsAlignColumns pins the settings screen's aligned
// layout: every automation row's enabled/disabled column must start at
// the same screen position regardless of its own name length.
func TestAutomationsRowsAlignColumns(t *testing.T) {
	h := newMockSettings()
	sec := newTestAutomationsSection(t, h.SettingsAdapters().Automations)
	rows := strings.Split(ansi.Strip(sec.View()), "\n")
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

// TestStaleWatchEndedMsgDoesNotClearCurrentWatch pins the D7-style
// fenced-handle discipline for watch teardown: a superseded watch's own
// automationsWatchEndedMsg (e.g. from Cancel() closing its channel)
// must not clobber a watch armed AFTER it. Only a message carrying the
// CURRENT handle may clear s.watch/s.watchID.
func TestStaleWatchEndedMsgDoesNotClearCurrentWatch(t *testing.T) {
	h := newMockSettings()
	sec := newTestAutomationsSection(t, h.SettingsAdapters().Automations)

	staleHandle, err := sec.store.Watch(context.Background(), sec.rows[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	currentHandle, err := sec.store.Watch(context.Background(), sec.rows[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	sec.watch, sec.watchID = currentHandle, sec.rows[0].ID

	next, _ := sec.Update(automationsWatchEndedMsg{handle: staleHandle})
	sec = next.(*automationsSection)
	if sec.watch == nil || sec.watchID == "" {
		t.Fatal("a stale watch's ended message must not clear the current watch")
	}

	next, _ = sec.Update(automationsWatchEndedMsg{handle: currentHandle})
	sec = next.(*automationsSection)
	if sec.watch != nil || sec.watchID != "" {
		t.Error("the current watch's own ended message must clear s.watch/s.watchID")
	}
}

// TestFailedMsgCancelsTheArmedWatch pins the cleanup a save failure
// must trigger: a failed Apply (e.g. cancelling a run) leaves the
// section's watch subscription open unless automationsFailedMsg itself
// cancels it, leaking the underlying channel/goroutine.
func TestFailedMsgCancelsTheArmedWatch(t *testing.T) {
	h := newMockSettings()
	sec := newTestAutomationsSection(t, h.SettingsAdapters().Automations)

	watch, err := sec.store.Watch(context.Background(), sec.rows[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	sec.watch, sec.watchID = watch, sec.rows[0].ID

	next, _ := sec.Update(automationsFailedMsg{message: "boom", cancelsWatch: true})
	sec = next.(*automationsSection)
	if sec.notice != "boom" {
		t.Errorf("expected notice to be set to the failure message, got %q", sec.notice)
	}
	if sec.watch != nil || sec.watchID != "" {
		t.Error("expected a cancelsWatch automationsFailedMsg to cancel and clear the armed watch")
	}

	// Cancel() closes the channel; reading past that is the signal the
	// watch was actually torn down, not just forgotten by the section.
	if _, ok := <-watch.Events(); ok {
		t.Error("expected the watch's channel to be closed by the cancellation")
	}
}

// TestUnrelatedFailedSaveLeavesTheArmedWatchOpen pins the D-series fix:
// awaitAutomationsSave is shared by toggleEnabled, remove, trigger,
// cancelRun and the editor's saveEditor, but only a failed trigger or
// cancelRun legitimately targets the currently armed watch. A failed
// toggle/remove on an unrelated automation (cursor can move to
// automation B while automation A's run is still being watched - only
// s.liveRun is cleared on cursor move, never s.watch) must not tear
// down automation A's live-progress stream.
func TestUnrelatedFailedSaveLeavesTheArmedWatchOpen(t *testing.T) {
	h := newMockSettings()
	sec := newTestAutomationsSection(t, h.SettingsAdapters().Automations)

	watchedID := sec.rows[0].ID
	watch, err := sec.store.Watch(context.Background(), watchedID)
	if err != nil {
		t.Fatal(err)
	}
	sec.watch, sec.watchID = watch, watchedID

	// Force toggleEnabled's Apply to resolve with a SaveFailed event by
	// pointing it at a row ID the store does not recognize - this drives
	// the real call site, not a hand-built message.
	sec.rows[sec.cursor].ID = "does-not-exist"
	next, cmd := sec.toggleEnabled()
	sec = next.(*automationsSection)
	sec = awaitAutomationsSaveTest(t, sec, cmd)

	if sec.notice == "" {
		t.Fatal("expected the bogus toggle to fail and set a notice")
	}
	if sec.watch != watch || sec.watchID != watchedID {
		t.Error("an unrelated failed save must not clear the armed watch")
	}

	// The watch must still be alive: cancel it ourselves and confirm the
	// close is what ends it (it was never cancelled by the failure).
	watch.Cancel()
	if _, ok := <-watch.Events(); ok {
		t.Error("expected the watch to still be open right before this explicit cancel")
	}
}

// TestFailedCancelRunClearsTheArmedWatch is the counterpart to
// TestUnrelatedFailedSaveLeavesTheArmedWatchOpen: cancelRun legitimately
// targets the currently armed watch, so a failed cancel must still tear
// it down.
func TestFailedCancelRunClearsTheArmedWatch(t *testing.T) {
	h := newMockSettings()
	sec := newTestAutomationsSection(t, h.SettingsAdapters().Automations)

	watch, err := sec.store.Watch(context.Background(), sec.rows[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	sec.watch, sec.watchID = watch, sec.rows[0].ID
	sec.liveRun = &ports.Run{ID: "does-not-exist", State: ports.RunRunning}

	next, cmd := sec.cancelRun()
	sec = next.(*automationsSection)
	if cmd == nil {
		t.Fatal("expected cancelRun on a live run to return a Cmd")
	}
	sec = awaitAutomationsSaveTest(t, sec, cmd)

	if sec.notice == "" {
		t.Fatal("expected the bogus cancel to fail and set a notice")
	}
	if sec.watch != nil || sec.watchID != "" {
		t.Error("expected a failed cancelRun to cancel and clear the armed watch")
	}
	if sec.liveRun != nil {
		t.Error("expected a failed cancelRun to clear the stale liveRun snapshot so the panel does not show a Pending/Running run that no longer is")
	}
}

// TestCancelRunKeySendsCancelAutomationRun pins "s": pressing it while
// a run is live (Pending/Running) must apply CancelAutomationRun for
// that run's ID and return a Cmd; with no live pending/running run it
// is a no-op.
func TestCancelRunKeySendsCancelAutomationRun(t *testing.T) {
	h := newMockSettings()
	sec := newTestAutomationsSection(t, h.SettingsAdapters().Automations)

	// No live run: "s" is a no-op.
	next, cmd := sec.Update(tea.KeyPressMsg{Text: "s", Code: 's'})
	sec = next.(*automationsSection)
	if cmd != nil {
		t.Error("expected \"s\" with no live run to be a no-op (nil Cmd)")
	}

	// Insert a real run into the store first: cancelRun looks the run
	// ID up across the store's run lists, so a liveRun set with no
	// matching stored run would fail with "not found" rather than
	// exercising the cancel path.
	automationID := sec.rows[0].ID
	h.mu.Lock()
	h.runs[automationID] = append(h.runs[automationID], ports.Run{
		ID: "run-live", AutomationID: automationID, State: ports.RunRunning, StartedAt: timeNow(),
	})
	h.mu.Unlock()

	sec.liveRun = &ports.Run{ID: "run-live", State: ports.RunRunning}
	next, cmd = sec.Update(tea.KeyPressMsg{Text: "s", Code: 's'})
	sec = next.(*automationsSection)
	if cmd == nil {
		t.Fatal("expected \"s\" with a live pending/running run to return a Cmd")
	}
	sec = awaitAutomationsSaveTest(t, sec, cmd)
	if sec.notice != "" {
		t.Errorf("expected cancelling a live run to succeed with no notice, got %q", sec.notice)
	}

	got, ok := h.SettingsAdapters().Automations.Run("run-live")
	if !ok {
		t.Fatal("expected the store to still know about run-live")
	}
	if got.State != ports.RunCancelled {
		t.Errorf("expected run-live's state to be RunCancelled after \"s\", got %v", got.State)
	}
}

func TestAutomationsDetailRendersSkippedRun(t *testing.T) {
	h := newMockSettings()
	now := timeNow()
	h.runs["nightly-audit"] = []ports.Run{
		{
			ID:           "run-skipped",
			AutomationID: "nightly-audit",
			Trigger:      ports.TriggerScheduled,
			State:        ports.RunSkipped,
			StartedAt:    now,
		},
	}
	h.automations[0].LastRun = &ports.RunSummary{
		ID:        "run-skipped",
		State:     ports.RunSkipped,
		StartedAt: now,
	}
	sec := newTestAutomationsSection(t, h.SettingsAdapters().Automations)
	plain := ansi.Strip(sec.View())
	if !strings.Contains(plain, "skipped") {
		t.Fatalf("expected view to contain \"skipped\", got:\n%s", plain)
	}
}
