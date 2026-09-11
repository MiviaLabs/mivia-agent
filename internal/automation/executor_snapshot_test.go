package automation

import (
	"context"
	"io"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
)

// TestSpawnRunSessionSavesUnderSessionOwnID pins the one-catalog-row
// scheme: spawnRunSession saves the bound session under its OWN id, not
// under a reserved "__auto__" fork, so the run and the chat layer share
// one catalog row. The assertion is on the row NAME only - whether the
// write lands as a plain snapshot or a live projection depends on
// EnsureSession timing this test does not control.
func TestSpawnRunSessionSavesUnderSessionOwnID(t *testing.T) {
	root := t.TempDir()
	db := newTestDB(t)
	sess := newContextEnabledSession(t, db)
	spawner := &sessionSpawner{sess: sess, conv: newRecordingConversation()}
	svc, err := New(root, db, spawner, Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	_, boundSess, savedName, err := svc.spawnRunSession(Spec{ID: "auto-x"}, "auto-x", root)
	if err != nil {
		t.Fatalf("spawnRunSession: %v", err)
	}
	if boundSess != sess {
		t.Fatal("spawnRunSession did not return the bind closure's captured session")
	}
	if savedName != sess.SessionID {
		t.Fatalf("savedName = %q, want the session's own id %q", savedName, sess.SessionID)
	}
	if _, _, err := db.LoadSession(context.Background(), sess.ContextPrincipal(), sess.SessionID); err != nil {
		t.Fatalf("catalog row under the session's own id %q: %v", sess.SessionID, err)
	}
}

// TestRunStepsSnapshotsSessionAfterEachSuccessfulStep proves runSteps
// re-saves the run's bound session under the session's own id after a
// step completes, not only once at spawn time. spawnRunSession's own
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
	if stored.SessionName != sess.SessionID {
		t.Fatalf("run SessionName = %q, want the session's own id %q", stored.SessionName, sess.SessionID)
	}

	_, info, err := db.LoadSession(context.Background(), sess.ContextPrincipal(), sess.SessionID)
	if err != nil {
		t.Fatalf("LoadSession(%q): %v", sess.SessionID, err)
	}
	if info.MessageCount < 1 {
		t.Fatalf("catalog message count = %d, want >= 1 (the step's turn), want runSteps to re-save after the step completed", info.MessageCount)
	}
}

// TestRunStepsSnapshotsSessionOnStepFailure proves the snapshot save also
// happens on the failure branch: a two-step run whose second step fails
// must still carry the first step's messages in its catalog row (under
// the session's own id), not just the spawn-time empty transcript. The
// second step's Send call fails before onSend runs (recordingConversation
// records failAt before invoking the hook), so only step one's turn adds
// messages - exactly the "messages up to and including the failed step"
// the row must keep.
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
	if stored.SessionName != sess.SessionID {
		t.Fatalf("run SessionName = %q, want the session's own id %q", stored.SessionName, sess.SessionID)
	}

	_, info, err := db.LoadSession(context.Background(), sess.ContextPrincipal(), sess.SessionID)
	if err != nil {
		t.Fatalf("LoadSession(%q): %v", sess.SessionID, err)
	}
	if info.MessageCount < 1 {
		t.Fatalf("catalog message count = %d, want >= 1 (step one's turn), want the failure branch to re-save the snapshot too", info.MessageCount)
	}
}
