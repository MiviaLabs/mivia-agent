// service_sessions_test.go pins the session-to-run registration the
// live-view guard depends on: RunActiveForSession is true for the whole
// execution of a run on that session - stable across inter-step gaps,
// unlike a turn-in-flight check - and false again after every exit.
package automation

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/sdkadapter"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
)

// sessionIDSpawner returns one fixed conversation from every spawn, so
// a test can assert registration keyed on that conversation's ID.
type sessionIDSpawner struct {
	conv ports.Conversation
}

func (f *sessionIDSpawner) CreateFreshInDir(bind func(*chat.Session) (string, error), dir string) (ports.Conversation, error) {
	return f.conv, nil
}

func (f *sessionIDSpawner) GetOrResumeInDir(id string, dir string) (ports.Conversation, *chat.Session, error) {
	return f.conv, nil, nil
}

func (f *sessionIDSpawner) SetApprovalOverride(sessionID string, gate func(ctx context.Context, name string, args json.RawMessage) sdkadapter.ApprovalResult, policy string) error {
	return nil
}

// TestRunActiveForSessionTrueDuringAndFalseAfterSuccess observes the
// predicate from inside a step (via recordingConversation's onSend hook,
// which fires between one step's completion and the next) and after the
// run's terminal write.
func TestRunActiveForSessionTrueDuringAndFalseAfterSuccess(t *testing.T) {
	root := t.TempDir()
	db := newTestDB(t)
	conv := newRecordingConversation()
	var svc *Service
	observedDuring := false
	conv.onSend = func() {
		if svc.RunActiveForSession("exec-conv") {
			observedDuring = true
		}
	}
	var err error
	svc, err = New(root, db, &sessionIDSpawner{conv: conv}, Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if svc.RunActiveForSession("exec-conv") {
		t.Fatal("predicate true before any run")
	}
	automationID := seedEnabledAutomation(t, root, func(s *Spec) {
		s.Steps = []Step{
			{Kind: StepPrompt, Prompt: "one"},
			{Kind: StepPrompt, Prompt: "two"},
		}
	})
	if _, err := svc.RunOnce(context.Background(), automationID, ports.TriggerManual); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if !observedDuring {
		t.Fatal("predicate was never true during execution; registration did not span the steps")
	}
	if svc.RunActiveForSession("exec-conv") {
		t.Fatal("predicate still true after a succeeded run")
	}
}

// TestRunActiveForSessionClearedOnStepFailure covers the failure exit:
// the predicate holds while earlier steps run and is false once the run
// failed.
func TestRunActiveForSessionClearedOnStepFailure(t *testing.T) {
	root := t.TempDir()
	db := newTestDB(t)
	conv := newRecordingConversation()
	conv.failAt = 2
	observedDuring := false
	var svc *Service
	conv.onSend = func() {
		if svc.RunActiveForSession("exec-conv") {
			observedDuring = true
		}
	}
	svc, err := New(root, db, &sessionIDSpawner{conv: conv}, Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	automationID := seedEnabledAutomation(t, root, func(s *Spec) {
		s.Steps = []Step{
			{Kind: StepPrompt, Prompt: "one"},
			{Kind: StepPrompt, Prompt: "two"},
			{Kind: StepPrompt, Prompt: "three"},
		}
	})
	run, err := svc.RunOnce(context.Background(), automationID, ports.TriggerManual)
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if run.State != ports.RunFailed {
		t.Fatalf("run state %v, want failed", run.State)
	}
	if !observedDuring {
		t.Fatal("predicate was never true during execution")
	}
	if svc.RunActiveForSession("exec-conv") {
		t.Fatal("predicate still true after a failed run")
	}
}

// TestRunActiveForSessionUnsetOnCompleteResume covers the resume branch
// that never spawns (the row's step index already covers every step, so
// admitResume marks it succeeded and returns): no registration may
// appear and nothing may leak.
func TestRunActiveForSessionUnsetOnCompleteResume(t *testing.T) {
	root := t.TempDir()
	db := newTestDB(t)
	svc, err := New(root, db, &sessionIDSpawner{conv: newRecordingConversation()}, Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	automationID := seedEnabledAutomation(t, root, func(s *Spec) {
		s.Steps = []Step{{Kind: StepPrompt, Prompt: "one"}}
	})
	runID := "resume-complete-" + automationID
	if err := svc.createRun(context.Background(), Run{
		ID: runID, AutomationID: automationID, Origin: "manual",
		State: RunInterrupted, StepIndex: 1, StepCount: 1,
		SessionName: "exec-conv", StartedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("createRun: %v", err)
	}
	run, err := svc.ResumeRun(context.Background(), runID)
	if err != nil {
		t.Fatalf("ResumeRun: %v", err)
	}
	if run.State != ports.RunSucceeded {
		t.Fatalf("run state %v, want succeeded (the already-complete branch)", run.State)
	}
	if svc.RunActiveForSession("exec-conv") {
		t.Fatal("predicate true after a resume that never spawned a session")
	}
}
