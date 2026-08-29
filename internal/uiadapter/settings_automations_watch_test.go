package uiadapter_test

// Watch hands out a channel and registers it in s.watchers. startRun appends
// the new run to s.runs and updates LastRun - and never touches s.watchers, so
// nothing ever sends on the channel it handed out.
//
// The consumer blocks on `run, ok := <-handle.Events()`, armed by the `t` key
// in the automations screen. Every press therefore leaked a permanently
// blocked goroutine, and the run it triggered never rendered. The only wake
// was close(ch) from Cancel.

import (
	"context"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/uiadapter"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
)

func automationStore(t *testing.T) *uiadapter.SettingsStore {
	t.Helper()
	res := &config.Resolved{Model: "test-model", ConfigPath: t.TempDir() + "/mivia.toml"}
	sess := chat.NewSession(res, nil)
	return uiadapter.NewSettingsStore(sess, res, nil)
}

// seedAutomation registers one automation so Watch and Trigger can find it.
func seedAutomation(t *testing.T, store *uiadapter.SettingsStore, id string) {
	t.Helper()
	handle, err := store.Settings().Automations.Apply(context.Background(), ports.ScopeUser,
		ports.UpsertAutomation{Automation: ports.Automation{ID: id, Name: id, Enabled: true}})
	if err != nil {
		t.Fatalf("seed automation: %v", err)
	}
	for range handle.Events() {
	}
}

func TestAutomationWatchReceivesTriggeredRun(t *testing.T) {
	store := automationStore(t)
	seedAutomation(t, store, "nightly")

	handle, err := store.Settings().Automations.Watch(context.Background(), "nightly")
	if err != nil {
		t.Fatalf("watch: %v", err)
	}
	defer handle.Cancel()

	saved, err := store.Settings().Automations.Apply(context.Background(), ports.ScopeUser,
		ports.TriggerAutomation{ID: "nightly"})
	if err != nil {
		t.Fatalf("trigger: %v", err)
	}
	for range saved.Events() {
	}

	select {
	case run, ok := <-handle.Events():
		if !ok {
			t.Fatal("watch channel closed instead of delivering the run")
		}
		if run.AutomationID != "nightly" {
			t.Errorf("run.AutomationID = %q, want %q", run.AutomationID, "nightly")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("triggering an automation published nothing to its watcher; the screen blocks forever and the run never renders")
	}
}

// Cancel must still close the channel, so a consumer that stops watching
// unblocks rather than leaking.
func TestAutomationWatchCancelClosesChannel(t *testing.T) {
	store := automationStore(t)
	seedAutomation(t, store, "nightly")

	handle, err := store.Settings().Automations.Watch(context.Background(), "nightly")
	if err != nil {
		t.Fatalf("watch: %v", err)
	}
	handle.Cancel()

	select {
	case _, ok := <-handle.Events():
		if ok {
			t.Fatal("expected the channel to be closed after Cancel")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Cancel did not close the watch channel")
	}
}
