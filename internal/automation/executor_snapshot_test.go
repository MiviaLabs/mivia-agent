package automation

import (
	"context"
	"io"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
)

// TestRunStepsSnapshotsSessionAfterEachSuccessfulStep proves runSteps
// re-saves the run's bound session under its reserved catalog name after
// a step completes, not only once at spawn time. spawnRunSession's own
// Save call runs before any step, so it always captures an empty
// transcript; onSend stands in for a real turn adding messages to the
// bound session mid-step (recordingConversation is a pure test double
// that never touches *chat.Session itself). If runSteps never saves
// again, the catalog row stays at the spawn-time snapshot (0 messages);
// if it does, the row reflects the messages the step's turn added.
func TestRunStepsSnapshotsSessionAfterEachSuccessfulStep(t *testing.T) {
	root := t.TempDir()
	db := newTestDB(t)
	sess := newContextEnabledSession(t, db)
	conv := newRecordingConversation()
	conv.onSend = func() {
		if _, err := sess.SendUser(context.Background(), "step turn", io.Discard); err != nil {
			t.Errorf("SendUser during step: %v", err)
		}
	}
	spawner := &sessionSpawner{sess: sess, conv: conv}
	svc, err := New(root, db, spawner, Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	automationID := seedEnabledAutomation(t, root, func(s *Spec) {
		s.Steps = []Step{{Kind: StepPrompt, Prompt: "one"}}
	})

	run, err := svc.RunOnce(context.Background(), automationID, ports.TriggerManual)
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if run.State != ports.RunSucceeded {
		t.Fatalf("RunOnce state = %v, want RunSucceeded", run.State)
	}

	stored, ok, err := svc.getRun(context.Background(), run.ID)
	if err != nil || !ok {
		t.Fatalf("getRun: ok=%v err=%v", ok, err)
	}
	if stored.SessionName == "" {
		t.Fatal("run has no SessionName, want the reserved automation session name")
	}

	_, info, err := db.LoadSession(context.Background(), sess.ContextPrincipal(), stored.SessionName)
	if err != nil {
		t.Fatalf("LoadSession(%q): %v", stored.SessionName, err)
	}
	if info.MessageCount < 1 {
		t.Fatalf("catalog message count = %d, want >= 1 (the step's turn), want runSteps to re-save after the step completed", info.MessageCount)
	}
}

// TestRunStepsSnapshotsSessionOnStepFailure proves the snapshot save also
// happens on the failure branch: a two-step run whose second step fails
// must still carry the first step's messages in its catalog row, not
// just the spawn-time empty transcript. The second step's Send call
// fails before onSend runs (recordingConversation records failAt before
// invoking the hook), so only step one's turn adds messages - exactly
// the "messages up to and including the failed step" the row must keep.
func TestRunStepsSnapshotsSessionOnStepFailure(t *testing.T) {
	root := t.TempDir()
	db := newTestDB(t)
	sess := newContextEnabledSession(t, db)
	conv := newRecordingConversation()
	conv.failAt = 2
	conv.onSend = func() {
		if _, err := sess.SendUser(context.Background(), "step turn", io.Discard); err != nil {
			t.Errorf("SendUser during step: %v", err)
		}
	}
	spawner := &sessionSpawner{sess: sess, conv: conv}
	svc, err := New(root, db, spawner, Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	automationID := seedEnabledAutomation(t, root, func(s *Spec) {
		s.Steps = []Step{{Kind: StepPrompt, Prompt: "one"}, {Kind: StepPrompt, Prompt: "two"}}
	})

	run, err := svc.RunOnce(context.Background(), automationID, ports.TriggerManual)
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if run.State != ports.RunFailed {
		t.Fatalf("RunOnce state = %v, want RunFailed", run.State)
	}

	stored, ok, err := svc.getRun(context.Background(), run.ID)
	if err != nil || !ok {
		t.Fatalf("getRun: ok=%v err=%v", ok, err)
	}
	if stored.SessionName == "" {
		t.Fatal("run has no SessionName, want the reserved automation session name")
	}

	_, info, err := db.LoadSession(context.Background(), sess.ContextPrincipal(), stored.SessionName)
	if err != nil {
		t.Fatalf("LoadSession(%q): %v", stored.SessionName, err)
	}
	if info.MessageCount < 1 {
		t.Fatalf("catalog message count = %d, want >= 1 (step one's turn), want the failure branch to re-save the snapshot too", info.MessageCount)
	}
}
