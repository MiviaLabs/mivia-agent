package settings

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
)

func TestAutomationEditorIntervalHumanFormsAndStatusAndStepper(t *testing.T) {
	h := newMockSettings()
	sec := newTestAutomationsSection(t, h.SettingsAdapters().Automations)

	sec = pressKey(sec, "n")
	sec = typeAutomationText(sec, "human-interval-auto")
	sec = pressKey(sec, "tab") // Name
	sec = typeAutomationText(sec, "Human Interval")
	sec = pressKey(sec, "tab")   // Description
	sec = pressKey(sec, "tab")   // Enabled
	sec = pressKey(sec, "tab")   // Trigger
	sec = pressKey(sec, "space") // cycle to 'every'
	sec = pressKey(sec, "tab")   // Every field

	// Status displays invalid message initially for blank
	plain := ansi.Strip(sec.View())
	if !strings.Contains(plain, "Every must be a duration") {
		t.Fatalf("expected format rule in view for empty interval, got:\n%s", plain)
	}

	// Type human interval "2 days"
	sec = typeAutomationText(sec, "2 days")
	plain = ansi.Strip(sec.View())
	if !strings.Contains(plain, "Runs every 2d") {
		t.Fatalf("expected live status 'Runs every 2d', got:\n%s", plain)
	}

	sec = pressKey(sec, "tab") // Action
	sec = pressKey(sec, "tab") // Prompt
	sec = typeAutomationText(sec, "run test")

	sec, cmd := pressKeyCmd(sec, "ctrl+s")
	sec = awaitAutomationsSaveTest(t, sec, cmd)

	var created *ports.Automation
	for _, a := range h.SettingsAdapters().Automations.Automations() {
		if a.ID == "human-interval-auto" {
			aCopy := a
			created = &aCopy
		}
	}
	if created == nil {
		t.Fatal("expected 'human-interval-auto' in store")
	}
	if created.Trigger.Schedule.Every != 48*time.Hour {
		t.Fatalf("expected 48h (2 days), got %v", created.Trigger.Schedule.Every)
	}
}

func TestAutomationEditorIntervalStepperKeys(t *testing.T) {
	h := newMockSettings()
	sec := newTestAutomationsSection(t, h.SettingsAdapters().Automations)

	sec = pressKey(sec, "n")
	_, _, _, _, triggerIdx, everyIdx, _, _, _ := sec.automationFormIndices()
	for sec.formFocus != triggerIdx {
		sec = pressKey(sec, "tab")
	}
	sec = pressKey(sec, "space") // Trigger = every
	sec = pressKey(sec, "tab")   // focus on Every

	// Empty field stepping up seeds default (1h)
	sec = pressKey(sec, "up")
	if got := sec.formFields[everyIdx].Value(); got != "1h" {
		t.Fatalf("stepping up on blank expected 1h, got %q", got)
	}

	// Stepping up again moves to 3h
	sec = pressKey(sec, "up")
	if got := sec.formFields[everyIdx].Value(); got != "3h" {
		t.Fatalf("stepping up on 1h expected 3h, got %q", got)
	}

	// Stepping down moves back to 1h
	sec = pressKey(sec, "down")
	if got := sec.formFields[everyIdx].Value(); got != "1h" {
		t.Fatalf("stepping down on 3h expected 1h, got %q", got)
	}

	// Stepping down moves to 30m
	sec = pressKey(sec, "down")
	if got := sec.formFields[everyIdx].Value(); got != "30m" {
		t.Fatalf("stepping down on 1h expected 30m, got %q", got)
	}

	plain := ansi.Strip(sec.View())
	if !strings.Contains(plain, "Runs every 30m") {
		t.Fatalf("expected status 'Runs every 30m', got:\n%s", plain)
	}
	if !strings.Contains(plain, "up/down step interval") {
		t.Fatalf("expected hint 'up/down step interval', got:\n%s", plain)
	}
}

func TestStepperLeavesInvalidEveryTextUnchanged(t *testing.T) {
	h := newMockSettings()
	sec := newTestAutomationsSection(t, h.SettingsAdapters().Automations)

	sec = pressKey(sec, "n")
	_, _, _, _, triggerIdx, everyIdx, _, _, _ := sec.automationFormIndices()
	for sec.formFocus != triggerIdx {
		sec = pressKey(sec, "tab")
	}
	sec = pressKey(sec, "space") // Trigger = every
	sec = pressKey(sec, "tab")   // focus on Every

	sec = typeAutomationText(sec, "invalid-interval")
	if got := sec.formFields[everyIdx].Value(); got != "invalid-interval" {
		t.Fatalf("expected field value to be 'invalid-interval', got %q", got)
	}

	sec = pressKey(sec, "up")
	if got := sec.formFields[everyIdx].Value(); got != "invalid-interval" {
		t.Fatalf("expected 'up' on invalid text to leave it unchanged, got %q", got)
	}

	sec = pressKey(sec, "down")
	if got := sec.formFields[everyIdx].Value(); got != "invalid-interval" {
		t.Fatalf("expected 'down' on invalid text to leave it unchanged, got %q", got)
	}
}
