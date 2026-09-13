package uiadapter_test

// TestAutomationBackendDelegation proves the settingsAutomations methods
// delegate to a non-nil backend installed via SetAutomationBackend
// (D4), rather than the in-memory fallback path every other test in
// this package exercises. Every existing automations test uses the
// default nil backend; this is the first that installs one, closing the
// gap the backend-non-nil branches in settings_automations.go otherwise
// leave untested.

import (
	"context"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
)

// fakeAutomationBackend is a minimal, fully in-memory
// ports.AutomationSettings double, independent of the SettingsStore's
// own in-memory fallback, so a call reaching it is distinguishable from
// a call that fell through to the fallback (each method returns a
// sentinel or records that it was called).
type fakeAutomationBackend struct {
	automations []ports.Automation
	runs        []ports.Run
	applyCalls  int
	lastEdit    ports.AutomationEdit
	watchCalls  int
}

func (f *fakeAutomationBackend) Automations() []ports.Automation { return f.automations }

func (f *fakeAutomationBackend) Runs(automationID string, limit int) []ports.Run { return f.runs }

func (f *fakeAutomationBackend) Run(runID string) (ports.Run, bool) {
	for _, r := range f.runs {
		if r.ID == runID {
			return r, true
		}
	}
	return ports.Run{}, false
}

func (f *fakeAutomationBackend) Apply(_ context.Context, _ ports.Scope, e ports.AutomationEdit) (ports.SaveHandle, error) {
	f.applyCalls++
	f.lastEdit = e
	ch := make(chan ports.SaveEvent, 1)
	ch <- ports.SaveEvent{State: ports.SaveSaved}
	close(ch)
	return &fakeSaveHandle{ch: ch}, nil
}

func (f *fakeAutomationBackend) Watch(_ context.Context, automationID string) (ports.RunHandle, error) {
	f.watchCalls++
	ch := make(chan ports.Run)
	close(ch)
	return &fakeRunHandle{ch: ch}, nil
}

type fakeSaveHandle struct{ ch chan ports.SaveEvent }

func (h *fakeSaveHandle) ID() string                     { return "fake-backend-save" }
func (h *fakeSaveHandle) Events() <-chan ports.SaveEvent { return h.ch }
func (h *fakeSaveHandle) Cancel()                        {}

type fakeRunHandle struct{ ch chan ports.Run }

func (h *fakeRunHandle) Events() <-chan ports.Run { return h.ch }
func (h *fakeRunHandle) Cancel()                  {}

func TestAutomationBackendDelegation(t *testing.T) {
	store := automationStore(t)
	backend := &fakeAutomationBackend{
		automations: []ports.Automation{{ID: "from-backend", Name: "from backend"}},
		runs:        []ports.Run{{ID: "run-from-backend", AutomationID: "from-backend"}},
	}
	store.SetAutomationBackend(backend)

	settings := store.Settings().Automations

	got := settings.Automations()
	if len(got) != 1 || got[0].ID != "from-backend" {
		t.Fatalf("Automations() = %+v, want the backend's fixture, not the in-memory fallback", got)
	}

	runs := settings.Runs("from-backend", 10)
	if len(runs) != 1 || runs[0].ID != "run-from-backend" {
		t.Fatalf("Runs() = %+v, want the backend's fixture", runs)
	}

	if _, ok := settings.Run("run-from-backend"); !ok {
		t.Fatal("Run(\"run-from-backend\") = not found, want the backend's fixture run")
	}
	if _, ok := settings.Run("no-such-run"); ok {
		t.Fatal("Run(\"no-such-run\") = found, want not-found")
	}

	edit := ports.UpsertAutomation{Automation: ports.Automation{ID: "new-one"}}
	h, err := settings.Apply(context.Background(), ports.ScopeUser, edit)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	for range h.Events() {
	}
	if backend.applyCalls != 1 {
		t.Fatalf("backend.applyCalls = %d, want 1 (Apply must reach the backend, not the in-memory fallback)", backend.applyCalls)
	}
	if _, ok := backend.lastEdit.(ports.UpsertAutomation); !ok {
		t.Fatalf("backend.lastEdit = %#v, want the UpsertAutomation edit forwarded unchanged", backend.lastEdit)
	}

	wh, err := settings.Watch(context.Background(), "from-backend")
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	if backend.watchCalls != 1 {
		t.Fatalf("backend.watchCalls = %d, want 1 (Watch must reach the backend, not the in-memory fallback)", backend.watchCalls)
	}
	wh.Cancel()
}
