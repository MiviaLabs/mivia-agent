package settings

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
)

func pressKey(sec *automationsSection, key string) *automationsSection {
	var msg tea.KeyPressMsg
	switch key {
	case "esc":
		msg = tea.KeyPressMsg{Text: "esc", Code: tea.KeyEsc}
	case "tab":
		msg = tea.KeyPressMsg{Text: "tab", Code: tea.KeyTab}
	case "shift+tab":
		msg = tea.KeyPressMsg{Text: "shift+tab", Code: tea.KeyTab, Mod: tea.ModShift}
	case "ctrl+s":
		msg = tea.KeyPressMsg{Text: "ctrl+s", Code: 's', Mod: tea.ModCtrl}
	case "enter":
		msg = tea.KeyPressMsg{Text: "enter", Code: tea.KeyEnter}
	case "space":
		msg = tea.KeyPressMsg{Text: " ", Code: tea.KeySpace}
	default:
		r := []rune(key)[0]
		msg = tea.KeyPressMsg{Text: key, Code: r}
	}
	next, _ := sec.Update(msg)
	return next.(*automationsSection)
}

func pressKeyCmd(sec *automationsSection, key string) (*automationsSection, tea.Cmd) {
	var msg tea.KeyPressMsg
	switch key {
	case "esc":
		msg = tea.KeyPressMsg{Text: "esc", Code: tea.KeyEsc}
	case "ctrl+s":
		msg = tea.KeyPressMsg{Text: "ctrl+s", Code: 's', Mod: tea.ModCtrl}
	default:
		r := []rune(key)[0]
		msg = tea.KeyPressMsg{Text: key, Code: r}
	}
	next, cmd := sec.Update(msg)
	return next.(*automationsSection), cmd
}

func typeAutomationText(sec *automationsSection, text string) *automationsSection {
	for _, ch := range text {
		sec = pressKey(sec, string(ch))
	}
	return sec
}

func TestPressingNOpensABlankAutomationEditor(t *testing.T) {
	h := newMockSettings()
	sec := newTestAutomationsSection(t, h.SettingsAdapters().Automations)

	sec = pressKey(sec, "n")
	if !sec.editing || !sec.isNew {
		t.Fatalf("expected 'n' to open a blank new-automation editor, got editing=%v isNew=%v", sec.editing, sec.isNew)
	}
	plain := ansi.Strip(sec.View())
	if !strings.Contains(plain, "Add New Automation") {
		t.Fatalf("expected Add New Automation header, got:\n%s", plain)
	}
}

func TestPressingNOnAnEmptyAutomationsListStillOpensTheEditor(t *testing.T) {
	h := newMockSettings()
	sec := newTestAutomationsSection(t, h.SettingsAdapters().Automations)
	sec.rows = nil // simulate a fresh workspace with no automations defined

	sec = pressKey(sec, "n")
	if !sec.editing {
		t.Fatalf("expected 'n' on an empty list to open the editor (regression pin), got editing=%v", sec.editing)
	}
}

func TestNewAutomationSavesAManualPromptAutomation(t *testing.T) {
	h := newMockSettings()
	sec := newTestAutomationsSection(t, h.SettingsAdapters().Automations)

	sec = pressKey(sec, "n")
	sec = typeAutomationText(sec, "my-new-automation") // ID field
	sec = pressKey(sec, "tab")
	sec = typeAutomationText(sec, "My New Automation") // Name field
	sec = pressKey(sec, "tab")
	sec = typeAutomationText(sec, "does a thing") // Description field
	sec = pressKey(sec, "tab")                    // Enabled (choice)
	sec = pressKey(sec, "tab")                    // Trigger (choice, leave "manual")
	sec = pressKey(sec, "tab")                    // Every (skip, blank)
	sec = pressKey(sec, "tab")                    // Action (choice, leave "prompt")
	sec = pressKey(sec, "tab")                    // Prompt/Skill
	sec = typeAutomationText(sec, "do the thing")

	sec, cmd := pressKeyCmd(sec, "ctrl+s")
	sec = awaitAutomationsSaveTest(t, sec, cmd)

	var created *ports.Automation
	for _, a := range h.SettingsAdapters().Automations.Automations() {
		if a.ID == "my-new-automation" {
			aCopy := a
			created = &aCopy
		}
	}
	if created == nil {
		t.Fatal("expected new automation 'my-new-automation' in store, but not found")
	}
	if created.Name != "My New Automation" || created.Description != "does a thing" {
		t.Errorf("got name/description %q/%q, want My New Automation/does a thing", created.Name, created.Description)
	}
	if created.Trigger.Kind != ports.TriggerManual {
		t.Errorf("expected manual trigger, got %v", created.Trigger.Kind)
	}
	if len(created.Action.Steps) != 1 || created.Action.Steps[0].Kind != ports.ActionStepPrompt || created.Action.Steps[0].Prompt != "do the thing" {
		t.Errorf("unexpected action steps: %+v", created.Action.Steps)
	}
}

func TestNewAutomationWithIntervalScheduleSavesEvery(t *testing.T) {
	h := newMockSettings()
	sec := newTestAutomationsSection(t, h.SettingsAdapters().Automations)

	sec = pressKey(sec, "n")
	sec = typeAutomationText(sec, "scheduled-automation")
	sec = pressKey(sec, "tab")
	sec = typeAutomationText(sec, "Scheduled Automation")
	sec = pressKey(sec, "tab") // Description, blank
	sec = pressKey(sec, "tab") // Enabled
	sec = pressKey(sec, "tab") // now on Trigger
	sec = pressKey(sec, "space")
	if sec.formFields[sec.formFocus].Value() != "every" {
		t.Fatalf("expected trigger cycled to 'every', got %q", sec.formFields[sec.formFocus].Value())
	}
	sec = pressKey(sec, "tab") // Every field
	sec = typeAutomationText(sec, "30m")
	sec = pressKey(sec, "tab") // Action
	sec = pressKey(sec, "tab") // Prompt
	sec = typeAutomationText(sec, "run it")

	sec, cmd := pressKeyCmd(sec, "ctrl+s")
	sec = awaitAutomationsSaveTest(t, sec, cmd)

	var created *ports.Automation
	for _, a := range h.SettingsAdapters().Automations.Automations() {
		if a.ID == "scheduled-automation" {
			aCopy := a
			created = &aCopy
		}
	}
	if created == nil {
		t.Fatal("expected new automation 'scheduled-automation' in store")
	}
	if created.Trigger.Kind != ports.TriggerScheduled || created.Trigger.Schedule == nil {
		t.Fatalf("expected a scheduled trigger, got %+v", created.Trigger)
	}
	if created.Trigger.Schedule.Kind != ports.ScheduleInterval {
		t.Errorf("expected interval schedule, got %v", created.Trigger.Schedule.Kind)
	}
	if created.Trigger.Schedule.Every.String() != "30m0s" {
		t.Errorf("expected every=30m, got %v", created.Trigger.Schedule.Every)
	}
}

func TestNewAutomationWithSkillActionSetsRefNotPrompt(t *testing.T) {
	h := newMockSettings()
	sec := newTestAutomationsSection(t, h.SettingsAdapters().Automations)

	sec = pressKey(sec, "n")
	sec = typeAutomationText(sec, "skill-automation")
	sec = pressKey(sec, "tab")
	sec = typeAutomationText(sec, "Skill Automation")
	sec = pressKey(sec, "tab") // Description
	sec = pressKey(sec, "tab") // Enabled
	sec = pressKey(sec, "tab") // Trigger
	sec = pressKey(sec, "tab") // Every
	sec = pressKey(sec, "tab") // Action
	sec = pressKey(sec, "space")
	if sec.formFields[sec.formFocus].Value() != "skill" {
		t.Fatalf("expected action cycled to 'skill', got %q", sec.formFields[sec.formFocus].Value())
	}
	sec = pressKey(sec, "tab") // Prompt/Skill
	sec = typeAutomationText(sec, "code-review")

	sec, cmd := pressKeyCmd(sec, "ctrl+s")
	sec = awaitAutomationsSaveTest(t, sec, cmd)

	var created *ports.Automation
	for _, a := range h.SettingsAdapters().Automations.Automations() {
		if a.ID == "skill-automation" {
			aCopy := a
			created = &aCopy
		}
	}
	if created == nil {
		t.Fatal("expected new automation 'skill-automation' in store")
	}
	if len(created.Action.Steps) != 1 || created.Action.Steps[0].Kind != ports.ActionStepSkill {
		t.Fatalf("expected a single skill step, got %+v", created.Action.Steps)
	}
	if created.Action.Steps[0].Ref != "code-review" {
		t.Errorf("expected Ref=code-review, got %q", created.Action.Steps[0].Ref)
	}
	if created.Action.Steps[0].Prompt != "" {
		t.Errorf("expected empty Prompt for a skill step, got %q", created.Action.Steps[0].Prompt)
	}
}

func TestEnterOnARowOpensThePrefilledEditor(t *testing.T) {
	h := newMockSettings()
	h.automations = append(h.automations, ports.Automation{
		ID: "blank-steps-automation", Name: "Blank Steps Automation",
		Enabled: true,
		Trigger: ports.TriggerSpec{Kind: ports.TriggerManual},
	})
	sec := newTestAutomationsSection(t, h.SettingsAdapters().Automations)

	idx := -1
	for i, r := range sec.rows {
		if r.ID == "blank-steps-automation" {
			idx = i
		}
	}
	if idx < 0 {
		t.Fatal("fixture row not found")
	}
	sec.cursor = idx

	sec = pressKey(sec, "enter")
	if !sec.editing || sec.isNew {
		t.Fatalf("expected enter on a manual-trigger, zero-step row to open a prefilled editor, got editing=%v isNew=%v", sec.editing, sec.isNew)
	}
	plain := ansi.Strip(sec.View())
	if !strings.Contains(plain, "Edit Automation: blank-steps-automation") {
		t.Fatalf("expected Edit Automation header for blank-steps-automation, got:\n%s", plain)
	}
}

func TestEditingNameDoesNotDropWorktreeOrLastRun(t *testing.T) {
	h := newMockSettings()
	lastRun := &ports.RunSummary{ID: "run-1", State: ports.RunSucceeded, StartedAt: timeNow()}
	h.automations = append(h.automations, ports.Automation{
		ID: "worktree-automation", Name: "Worktree Automation",
		Enabled:  true,
		Trigger:  ports.TriggerSpec{Kind: ports.TriggerManual},
		Action:   ports.ActionRef{Steps: []ports.ActionStep{{Kind: ports.ActionStepPrompt, Prompt: "do it"}}},
		Worktree: ports.WorktreeSpec{Mode: 1, BaseRef: "main"},
		LastRun:  lastRun,
	})
	sec := newTestAutomationsSection(t, h.SettingsAdapters().Automations)

	// find our fixture row
	idx := -1
	for i, r := range sec.rows {
		if r.ID == "worktree-automation" {
			idx = i
		}
	}
	if idx < 0 {
		t.Fatal("fixture row not found")
	}
	sec.cursor = idx

	sec = pressKey(sec, "enter")
	if !sec.editing {
		t.Fatal("expected editor to open for the worktree fixture")
	}
	// formFocus starts on Name (index 0 for edit form)
	sec.formFields[sec.formFocus].SetValue("Renamed Worktree Automation")

	sec, cmd := pressKeyCmd(sec, "ctrl+s")
	sec = awaitAutomationsSaveTest(t, sec, cmd)

	var updated *ports.Automation
	for _, a := range h.SettingsAdapters().Automations.Automations() {
		if a.ID == "worktree-automation" {
			aCopy := a
			updated = &aCopy
		}
	}
	if updated == nil {
		t.Fatal("automation missing from store after edit")
	}
	if updated.Name != "Renamed Worktree Automation" {
		t.Errorf("expected renamed automation, got name %q", updated.Name)
	}
	if updated.Worktree.Mode != 1 || updated.Worktree.BaseRef != "main" {
		t.Errorf("expected Worktree to survive unchanged, got %+v", updated.Worktree)
	}
	if updated.LastRun == nil || updated.LastRun.ID != "run-1" {
		t.Errorf("expected LastRun to survive unchanged, got %+v", updated.LastRun)
	}
}

func multiStepAutomation() ports.Automation {
	return ports.Automation{
		ID: "multi-step", Name: "Multi Step", Enabled: true,
		Trigger: ports.TriggerSpec{Kind: ports.TriggerManual},
		Action: ports.ActionRef{Steps: []ports.ActionStep{
			{Kind: ports.ActionStepPrompt, Prompt: "step one"},
			{Kind: ports.ActionStepPrompt, Prompt: "step two"},
		}},
	}
}

func TestEditorRefusesAMultiStepAutomation(t *testing.T) {
	h := newMockSettings()
	h.automations = append(h.automations, multiStepAutomation())
	sec := newTestAutomationsSection(t, h.SettingsAdapters().Automations)

	idx := -1
	for i, r := range sec.rows {
		if r.ID == "multi-step" {
			idx = i
		}
	}
	if idx < 0 {
		t.Fatal("fixture row not found")
	}
	sec.cursor = idx

	sec = pressKey(sec, "enter")
	if sec.editing {
		t.Fatal("expected the editor to refuse a multi-step automation")
	}
	if !strings.Contains(sec.notice, "not available in this build yet") {
		t.Errorf("expected a refusal notice, got %q", sec.notice)
	}
}

func cronScheduledAutomation() ports.Automation {
	return ports.Automation{
		ID: "cron-automation", Name: "Cron Automation", Enabled: true,
		Trigger: ports.TriggerSpec{Kind: ports.TriggerScheduled, Schedule: &ports.ScheduleSpec{
			Kind: ports.ScheduleRecurring, Cron: "0 2 * * *", TZ: "UTC",
		}},
		Action: ports.ActionRef{Steps: []ports.ActionStep{{Kind: ports.ActionStepPrompt, Prompt: "audit"}}},
	}
}

func TestEditorRefusesACronScheduledAutomation(t *testing.T) {
	h := newMockSettings()
	h.automations = append(h.automations, cronScheduledAutomation())
	sec := newTestAutomationsSection(t, h.SettingsAdapters().Automations)

	idx := -1
	for i, r := range sec.rows {
		if r.ID == "cron-automation" {
			idx = i
		}
	}
	if idx < 0 {
		t.Fatal("fixture row not found")
	}
	sec.cursor = idx

	sec = pressKey(sec, "enter")
	if sec.editing {
		t.Fatal("expected the editor to refuse a cron-scheduled automation")
	}
	if !strings.Contains(sec.notice, "not available in this build yet") {
		t.Errorf("expected a refusal notice, got %q", sec.notice)
	}
}

func TestTogglingEnabledStillWorksOnANonRepresentableAutomation(t *testing.T) {
	h := newMockSettings()
	h.automations = append(h.automations, multiStepAutomation())
	sec := newTestAutomationsSection(t, h.SettingsAdapters().Automations)

	idx := -1
	for i, r := range sec.rows {
		if r.ID == "multi-step" {
			idx = i
		}
	}
	if idx < 0 {
		t.Fatal("fixture row not found")
	}
	sec.cursor = idx
	before := sec.rows[idx]

	sec, cmd := pressKeyCmd(sec, "space")
	sec = awaitAutomationsSaveTest(t, sec, cmd)

	var after ports.Automation
	for _, a := range h.SettingsAdapters().Automations.Automations() {
		if a.ID == "multi-step" {
			after = a
		}
	}
	if after.Enabled == before.Enabled {
		t.Errorf("expected Enabled to flip, still %v", after.Enabled)
	}
	if len(after.Action.Steps) != 2 {
		t.Errorf("expected Steps to remain untouched (2 steps), got %d", len(after.Action.Steps))
	}
}

func TestEscCancelsTheAutomationEditorWithoutSaving(t *testing.T) {
	h := newMockSettings()
	sec := newTestAutomationsSection(t, h.SettingsAdapters().Automations)
	before := len(h.SettingsAdapters().Automations.Automations())

	sec = pressKey(sec, "n")
	sec = typeAutomationText(sec, "would-not-be-saved")
	sec = pressKey(sec, "esc")

	if sec.editing {
		t.Error("expected esc to exit editing mode")
	}
	if got := len(h.SettingsAdapters().Automations.Automations()); got != before {
		t.Errorf("expected no automation saved after esc, count changed from %d to %d", before, got)
	}
}

func TestEmptyAutomationIDIsRejectedBeforeApply(t *testing.T) {
	h := newMockSettings()
	sec := newTestAutomationsSection(t, h.SettingsAdapters().Automations)
	before := len(h.SettingsAdapters().Automations.Automations())

	sec = pressKey(sec, "n")
	// leave ID blank, go save directly
	sec, cmd := pressKeyCmd(sec, "ctrl+s")
	if cmd != nil {
		t.Error("expected nil Cmd when ID is blank")
	}
	if !strings.Contains(sec.notice, "an automation id is required") {
		t.Errorf("expected id-required notice, got %q", sec.notice)
	}
	if got := len(h.SettingsAdapters().Automations.Automations()); got != before {
		t.Errorf("expected store unchanged, count changed from %d to %d", before, got)
	}
}

func TestNonDurationEveryIsRejectedBeforeApply(t *testing.T) {
	h := newMockSettings()
	sec := newTestAutomationsSection(t, h.SettingsAdapters().Automations)

	sec = pressKey(sec, "n")
	sec = typeAutomationText(sec, "bad-every")
	sec = pressKey(sec, "tab") // Name
	sec = typeAutomationText(sec, "Bad Every")
	sec = pressKey(sec, "tab") // Description
	sec = pressKey(sec, "tab") // Enabled
	sec = pressKey(sec, "tab") // Trigger
	sec = pressKey(sec, "space")
	sec = pressKey(sec, "tab") // Every
	sec = typeAutomationText(sec, "not-a-duration")

	sec, cmd := pressKeyCmd(sec, "ctrl+s")
	if cmd != nil {
		t.Error("expected nil Cmd for a non-duration Every value")
	}
	if !strings.Contains(sec.notice, "Every must be a duration") {
		t.Errorf("expected duration-format notice, got %q", sec.notice)
	}
}

func TestZeroEveryIsRejectedBeforeApply(t *testing.T) {
	h := newMockSettings()
	sec := newTestAutomationsSection(t, h.SettingsAdapters().Automations)

	sec = pressKey(sec, "n")
	sec = typeAutomationText(sec, "zero-every")
	sec = pressKey(sec, "tab")
	sec = typeAutomationText(sec, "Zero Every")
	sec = pressKey(sec, "tab") // Description
	sec = pressKey(sec, "tab") // Enabled
	sec = pressKey(sec, "tab") // Trigger
	sec = pressKey(sec, "space")
	sec = pressKey(sec, "tab") // Every
	sec = typeAutomationText(sec, "0s")

	sec, cmd := pressKeyCmd(sec, "ctrl+s")
	if cmd != nil {
		t.Error("expected nil Cmd for a zero Every value")
	}
	if !strings.Contains(sec.notice, "Every must be positive") {
		t.Errorf("expected positive-duration notice, got %q", sec.notice)
	}
}

// TestSubSecondEveryIsRejectedBeforeApply covers a hostile-audit finding:
// automation.portsTriggerToSpec's wire format truncates Every to whole
// seconds (int64(Every / time.Second)), so a sub-second value like
// "500ms" would parse successfully, pass the d<=0 guard, and silently
// save as every_seconds=0 - a broken schedule that can never fire, with
// no error surfaced at save time. Must be rejected client-side before
// that truncation can happen.
func TestSubSecondEveryIsRejectedBeforeApply(t *testing.T) {
	h := newMockSettings()
	sec := newTestAutomationsSection(t, h.SettingsAdapters().Automations)

	sec = pressKey(sec, "n")
	sec = typeAutomationText(sec, "sub-second-every")
	sec = pressKey(sec, "tab") // Name
	sec = typeAutomationText(sec, "Sub Second Every")
	sec = pressKey(sec, "tab") // Description
	sec = pressKey(sec, "tab") // Enabled
	sec = pressKey(sec, "tab") // Trigger
	sec = pressKey(sec, "space")
	sec = pressKey(sec, "tab") // Every
	sec = typeAutomationText(sec, "500ms")

	sec, cmd := pressKeyCmd(sec, "ctrl+s")
	if cmd != nil {
		t.Error("expected nil Cmd for a sub-second Every value")
	}
	if !strings.Contains(sec.notice, "whole number of seconds") {
		t.Errorf("expected whole-seconds notice, got %q", sec.notice)
	}
	for _, a := range h.SettingsAdapters().Automations.Automations() {
		if a.ID == "sub-second-every" {
			t.Error("expected no automation saved for a rejected sub-second Every")
		}
	}
}

func TestAutomationsEditorCapturesInput(t *testing.T) {
	h := newMockSettings()
	sec := newTestAutomationsSection(t, h.SettingsAdapters().Automations)
	if sec.CapturingInput() {
		t.Error("expected CapturingInput false before editing")
	}
	sec = pressKey(sec, "n")
	if !sec.CapturingInput() {
		t.Error("expected CapturingInput true while editing")
	}
}

func TestAutomationsEditorHintsSwitchWhileEditing(t *testing.T) {
	h := newMockSettings()
	sec := newTestAutomationsSection(t, h.SettingsAdapters().Automations)
	normalHints := sec.Hints()
	if len(normalHints) == 0 {
		t.Fatal("expected non-empty Hints() outside editing")
	}
	sec = pressKey(sec, "n")
	editHints := sec.Hints()
	if len(editHints) != 4 {
		t.Errorf("expected 4 hints while editing, got %d", len(editHints))
	}
}

func TestTabCyclesAutomationEditorFocus(t *testing.T) {
	h := newMockSettings()
	sec := newTestAutomationsSection(t, h.SettingsAdapters().Automations)
	sec = pressKey(sec, "n")

	n := len(sec.formFields)
	if sec.formFocus != 0 {
		t.Fatalf("expected initial focus 0, got %d", sec.formFocus)
	}
	for i := 1; i < n; i++ {
		sec = pressKey(sec, "tab")
		if sec.formFocus != i {
			t.Fatalf("after %d tabs, expected focus %d, got %d", i, i, sec.formFocus)
		}
	}
	// one more tab wraps back to 0
	sec = pressKey(sec, "tab")
	if sec.formFocus != 0 {
		t.Fatalf("expected tab to wrap to 0, got %d", sec.formFocus)
	}
	// shift+tab wraps backward to last field
	sec = pressKey(sec, "shift+tab")
	if sec.formFocus != n-1 {
		t.Fatalf("expected shift+tab to wrap to %d, got %d", n-1, sec.formFocus)
	}
}
