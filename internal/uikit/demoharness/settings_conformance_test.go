package demoharness

import (
	"context"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
)

func newTestHarness(t *testing.T) *Harness {
	t.Helper()
	h, err := New("smalltalk", 0)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

// drainOK waits for a SaveHandle to finish and fails the test on
// SaveFailed, returning the sequence of states observed.
func drainOK(t *testing.T, h ports.SaveHandle) []ports.SaveState {
	t.Helper()
	var states []ports.SaveState
	for ev := range h.Events() {
		states = append(states, ev.State)
		if ev.State == ports.SaveFailed {
			t.Fatalf("save failed: %s", ev.Message)
		}
	}
	return states
}

func drainWithFailure(h ports.SaveHandle) []ports.SaveState {
	var states []ports.SaveState
	for ev := range h.Events() {
		states = append(states, ev.State)
	}
	return states
}

func TestGeneralApplyRoundTrips(t *testing.T) {
	settings := newTestHarness(t).SettingsAdapters()
	before := settings.General.General()
	if before.Theme == "" {
		t.Fatal("seed General has no theme")
	}
	handle, err := settings.General.Apply(context.Background(), ports.ScopeUser, ports.SetTheme{Name: "mivia-light"})
	if err != nil {
		t.Fatal(err)
	}
	states := drainOK(t, handle)
	if len(states) < 3 || states[0] != ports.SavePending || states[len(states)-1] != ports.SaveSaved {
		t.Errorf("unexpected save state sequence: %v", states)
	}
	if got := settings.General.General().Theme; got != "mivia-light" {
		t.Errorf("General().Theme = %q, want mivia-light", got)
	}
}

func TestProviderCRUDAndActivate(t *testing.T) {
	settings := newTestHarness(t).SettingsAdapters()

	upsert, err := settings.Providers.Apply(context.Background(), ports.ScopeUser, ports.UpsertModel{
		Provider: "ollama", Model: ports.ModelView{Name: "llama3.2"},
	})
	if err != nil {
		t.Fatal(err)
	}
	drainOK(t, upsert)
	found := false
	for _, p := range settings.Providers.Providers() {
		if p.Name != "ollama" {
			continue
		}
		for _, m := range p.Models {
			if m.Name == "llama3.2" {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("UpsertModel did not add the model")
	}

	activate, err := settings.Providers.Apply(context.Background(), ports.ScopeUser, ports.ActivateModel{
		Provider: "ollama", Model: "llama3.2",
	})
	if err != nil {
		t.Fatal(err)
	}
	drainOK(t, activate)
	activeCount := 0
	for _, p := range settings.Providers.Providers() {
		if p.Name == "ollama" {
			if !p.Active || p.ActiveModel != "llama3.2" {
				t.Errorf("ollama not activated: %+v", p)
			}
		}
		if p.Active {
			activeCount++
		}
	}
	if activeCount != 1 {
		t.Errorf("expected exactly one active provider, got %d", activeCount)
	}

	bad, err := settings.Providers.Apply(context.Background(), ports.ScopeUser, ports.ActivateModel{Provider: "nope", Model: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if states := drainWithFailure(bad); states[len(states)-1] != ports.SaveFailed {
		t.Error("activating an unknown provider should fail")
	}
}

func TestAgentRemoveDefaultIsRejected(t *testing.T) {
	settings := newTestHarness(t).SettingsAdapters()
	h, err := settings.Agents.Apply(context.Background(), ports.ScopeUser, ports.RemoveAgent{Name: ports.DefaultAgentName})
	if err != nil {
		t.Fatal(err)
	}
	if states := drainWithFailure(h); states[len(states)-1] != ports.SaveFailed {
		t.Error("removing the default agent should fail")
	}
}

func TestAutomationTriggerProducesAWatchableRun(t *testing.T) {
	settings := newTestHarness(t).SettingsAdapters()
	automations := settings.Automations.Automations()
	if len(automations) == 0 {
		t.Fatal("no seeded automations")
	}
	id := automations[0].ID

	watch, err := settings.Automations.Watch(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	defer watch.Cancel()

	trigger, err := settings.Automations.Apply(context.Background(), ports.ScopeUser, ports.TriggerAutomation{ID: id})
	if err != nil {
		t.Fatal(err)
	}
	drainOK(t, trigger)

	var last ports.Run
	timeout := time.After(2 * time.Second)
	for last.State != ports.RunSucceeded {
		select {
		case r := <-watch.Events():
			last = r
		case <-timeout:
			t.Fatalf("run did not reach RunSucceeded in time, last=%+v", last)
		}
	}
	if last.AutomationID != id || last.Trigger != ports.TriggerManual {
		t.Errorf("unexpected run: %+v", last)
	}

	runs := settings.Automations.Runs(id, 10)
	if len(runs) == 0 || runs[0].ID != last.ID {
		t.Errorf("Runs() does not reflect the completed run: %+v", runs)
	}
	if got, ok := settings.Automations.Run(last.ID); !ok || got.State != ports.RunSucceeded {
		t.Errorf("Run(%q) = %+v, %v", last.ID, got, ok)
	}
}

func TestSkillsApplyRoundTrips(t *testing.T) {
	settings := newTestHarness(t).SettingsAdapters()
	initialSkills := settings.Skills.Skills()
	if len(initialSkills) == 0 {
		t.Fatal("no seeded skills")
	}

	// 1. Toggle UserInvocable
	target := initialSkills[0]
	h1, err := settings.Skills.Apply(context.Background(), ports.ScopeUser, ports.SetSkillUserInvocable{
		Name:   target.Name,
		Origin: target.Origin,
		On:     !target.UserInvocable,
	})
	if err != nil {
		t.Fatal(err)
	}
	drainOK(t, h1)

	// 2. SaveSkill (create new)
	h2, err := settings.Skills.Apply(context.Background(), ports.ScopeUser, ports.SaveSkill{
		Name:          "new-skill",
		Description:   "brand new skill",
		Origin:        "user",
		UserInvocable: true,
		Instructions:  "# New\nDo new stuff",
	})
	if err != nil {
		t.Fatal(err)
	}
	drainOK(t, h2)

	// 3. SaveSkill (update existing)
	h3, err := settings.Skills.Apply(context.Background(), ports.ScopeUser, ports.SaveSkill{
		Name:          "new-skill",
		Description:   "updated brand new skill",
		Origin:        "user",
		UserInvocable: true,
		Instructions:  "# New Updated\nDo new stuff updated",
	})
	if err != nil {
		t.Fatal(err)
	}
	drainOK(t, h3)

	// 4. RemoveSkill
	h4, err := settings.Skills.Apply(context.Background(), ports.ScopeUser, ports.RemoveSkill{
		Name:   "new-skill",
		Origin: "user",
	})
	if err != nil {
		t.Fatal(err)
	}
	drainOK(t, h4)

	// 5. Remove non-existent skill fails
	h5, err := settings.Skills.Apply(context.Background(), ports.ScopeUser, ports.RemoveSkill{
		Name:   "non-existent-xyz",
		Origin: "user",
	})
	if err != nil {
		t.Fatal(err)
	}
	states := drainWithFailure(h5)
	if states[len(states)-1] != ports.SaveFailed {
		t.Errorf("expected SaveFailed for non-existent skill, got %v", states)
	}
}
