package cli

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/coordinator"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
	"strings"
)

// fakeCoordinatorForResume implements coordinator.Coordinator for testing.
type fakeCoordinatorForResume struct {
	coordinator.Coordinator
	resumeFunc      func(ctx context.Context, runID string) (*coordinator.RunHandle, error)
	listInterrupted func(ctx context.Context) ([]coordinator.RecoveredRun, error)
	subscribeFn     func(fn coordinator.LifecycleSubscriber) func()
	spawnFn         func(ctx context.Context, tasks []subagents.Task, key string) (*coordinator.RunHandle, error)
	inspectFn       func(ctx context.Context, h *coordinator.RunHandle) (ledger.RunSnapshot, error)
}

func (f *fakeCoordinatorForResume) ResumeInterruptedRun(ctx context.Context, runID string) (*coordinator.RunHandle, error) {
	if f.resumeFunc != nil {
		return f.resumeFunc(ctx, runID)
	}
	return nil, errors.New("not implemented")
}

func (f *fakeCoordinatorForResume) ListInterruptedRuns(ctx context.Context) ([]coordinator.RecoveredRun, error) {
	if f.listInterrupted != nil {
		return f.listInterrupted(ctx)
	}
	return nil, nil
}

func (f *fakeCoordinatorForResume) SubscribeLifecycle(fn coordinator.LifecycleSubscriber) func() {
	if f.subscribeFn != nil {
		return f.subscribeFn(fn)
	}
	return func() {}
}

func (f *fakeCoordinatorForResume) Spawn(ctx context.Context, tasks []subagents.Task, key string) (*coordinator.RunHandle, error) {
	if f.spawnFn != nil {
		return f.spawnFn(ctx, tasks, key)
	}
	return nil, errors.New("not implemented")
}

func (f *fakeCoordinatorForResume) Inspect(ctx context.Context, h *coordinator.RunHandle) (ledger.RunSnapshot, error) {
	if f.inspectFn != nil {
		return f.inspectFn(ctx, h)
	}
	return ledger.RunSnapshot{RunID: "test-run"}, nil
}

// TestResumeRunListsInterruptedRuns verifies that listInterruptedRuns returns
// the interrupted runs from the coordinator.
func TestResumeRunListsInterruptedRuns(t *testing.T) {
	fake := &fakeCoordinatorForResume{
		listInterrupted: func(ctx context.Context) ([]coordinator.RecoveredRun, error) {
			return []coordinator.RecoveredRun{
				{RunID: "run-1", DisplayName: "test-run", Status: "interrupted", WasInterrupted: true},
			}, nil
		},
	}

	runs, err := listInterruptedRuns(context.Background(), fake)
	if err != nil {
		t.Fatalf("listInterruptedRuns: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("expected 1 run, got %d", len(runs))
	}
	if runs[0].RunID != "run-1" {
		t.Fatalf("expected run-1, got %s", runs[0].RunID)
	}
	if !runs[0].WasInterrupted {
		t.Fatal("expected WasInterrupted=true")
	}
}

// initResumeTestStoreHandle creates a minimal orchestrationHandle record
// and stores it in the runHandles map for testing.
func initResumeTestStoreHandle(runID string, record *orchestrationHandle) {
	runHandles.Store(runID, record)
}

// withActiveSession mirrors production: the CLI surfaces pass a bare context and
// rely on the chat session principal recorded at startup by runChat. Tests that
// drive resumeRun must establish the same identity, or they exercise a path
// production never takes.
func withActiveSession(t *testing.T, sessionID string) {
	t.Helper()
	prev := activeSessionCaller.Load()
	setActiveSessionCaller(runtime.Caller{SessionID: sessionID})
	t.Cleanup(func() {
		if prev != nil {
			activeSessionCaller.Store(prev)
			return
		}
		activeSessionCaller.Store(&runtime.Caller{})
	})
}

// TestResumeRunRefusesHeldRun verifies that a run held by another executor
// is refused with ErrRunHeldByAnotherExecutor.
func TestResumeRunRefusesHeldRun(t *testing.T) {
	withActiveSession(t, "session-held")
	fake := &fakeCoordinatorForResume{
		resumeFunc: func(ctx context.Context, runID string) (*coordinator.RunHandle, error) {
			return nil, coordinator.ErrRunHeldByAnotherExecutor
		},
	}

	_, err := resumeRun(context.Background(), fake, nil, "run-held", nil)
	if err == nil {
		t.Fatal("expected error for held run, got nil")
	}
	if !errors.Is(err, coordinator.ErrRunHeldByAnotherExecutor) {
		t.Fatalf("expected ErrRunHeldByAnotherExecutor, got %v", err)
	}
}

// TestResumeRunRefusesTerminalRun verifies that a terminal run produces
// a distinct message from the held-by-another-executor case.
func TestResumeRunRefusesTerminalRun(t *testing.T) {
	withActiveSession(t, "session-terminal")
	fake := &fakeCoordinatorForResume{
		resumeFunc: func(ctx context.Context, runID string) (*coordinator.RunHandle, error) {
			return nil, errors.New("resume: run \"run-term\" is already terminal (completed)")
		},
	}

	_, err := resumeRun(context.Background(), fake, nil, "run-term", nil)
	if err == nil {
		t.Fatal("expected error for terminal run, got nil")
	}
	if errors.Is(err, coordinator.ErrRunHeldByAnotherExecutor) {
		t.Fatal("terminal run error should NOT be ErrRunHeldByAnotherExecutor")
	}
	if !strings.Contains(err.Error(), "is already terminal") {
		t.Fatalf("expected terminal run error, got %v", err)
	}
}

// TestResumeRunRefusesUnresumableRun verifies that a run with no persisted
// input produces a distinct message (missing-Input case).
func TestResumeRunRefusesUnresumableRun(t *testing.T) {
	withActiveSession(t, "session-unresumable")
	fake := &fakeCoordinatorForResume{
		resumeFunc: func(ctx context.Context, runID string) (*coordinator.RunHandle, error) {
			return nil, errors.New("resume: task \"t1\" has no persisted input (created before task inputs were recorded; cannot resume this run)")
		},
	}

	_, err := resumeRun(context.Background(), fake, nil, "run-no-input", nil)
	if err == nil {
		t.Fatal("expected error for unresumable run, got nil")
	}
	if errors.Is(err, coordinator.ErrRunHeldByAnotherExecutor) {
		t.Fatal("unresumable run error should NOT be ErrRunHeldByAnotherExecutor")
	}
	if strings.Contains(err.Error(), "is already terminal") {
		t.Fatal("unresumable run error should NOT be classified as terminal")
	}
	if !strings.Contains(err.Error(), "no persisted input") {
		t.Fatalf("expected unresumable (no persisted input) error, got %v", err)
	}
}

// TestResumeRunRegistersHandleWithResumingPrincipal verifies that after
// resume, the handle is stored with the resuming caller's principal (§3.2).
// This is the load-bearing test: M1 and M2 mutations must fail it.
//
// M1 (skip handle registration / skip Delete): the test pre-populates
// runHandles with a dummy handle before resumeRun. If runHandles.Delete
// or Store is not called, the old handle remains and the assertion on
// the stored record's principal fails.
//
// M2 (register with persisted principal): the test asserts the stored
// handle's principal sessionID is the RESUMING caller's, not the original's.
func TestResumeRunRegistersHandleWithResumingPrincipal(t *testing.T) {
	// Pre-populate runHandles with a dummy handle for this runID to catch
	// M1: if resumeRun skips runHandles.Delete, the old handle survives
	// and the new one (with the resuming principal) is never stored.
	oldPrincipal := orchestrationPrincipal{sessionID: "old-session", role: "user"}
	runHandles.Store("run-to-resume", &orchestrationHandle{
		principal: oldPrincipal,
	})

	// Create a minimal RunHandle.
	handle := &coordinator.RunHandle{}
	fake := &fakeCoordinatorForResume{
		resumeFunc: func(ctx context.Context, runID string) (*coordinator.RunHandle, error) {
			return handle, nil
		},
		inspectFn: func(ctx context.Context, h *coordinator.RunHandle) (ledger.RunSnapshot, error) {
			return ledger.RunSnapshot{RunID: "run-to-resume", DisplayName: "test"}, nil
		},
	}

	// Set up a principal for the resuming caller.
	ctx := runtime.ContextWithCaller(context.Background(), runtime.Caller{
		SessionID: "resume-session-1",
		Role:      "user",
	})

	record, err := resumeRun(ctx, fake, nil, "run-to-resume", nil)
	if err != nil {
		t.Fatalf("resumeRun: %v", err)
	}
	if record == nil {
		t.Fatal("expected non-nil orchestrationHandle record")
	}
	if record.principal.sessionID != "resume-session-1" {
		t.Fatalf("expected principal sessionID 'resume-session-1', got %q", record.principal.sessionID)
	}

	// Verify the handle is in the map with the RESUMING principal (M1 catch).
	loaded, ok := runHandles.Load("run-to-resume")
	if !ok {
		t.Fatal("run handle not found in runHandles map after resume")
	}
	loadedRecord, ok := loaded.(*orchestrationHandle)
	if !ok {
		t.Fatal("loaded value is not an orchestrationHandle")
	}
	if loadedRecord.principal.sessionID != "resume-session-1" {
		t.Fatalf("stored handle principal = %q (old=%q), want %q (resuming caller)",
			loadedRecord.principal.sessionID, oldPrincipal.sessionID, "resume-session-1")
	}

	// Cleanup.
	runHandles.Delete("run-to-resume")
}

// TestResumeConfirmationShowsWhatWillReRun verifies that the confirmation
// message includes task count and prior attempt info (§5).
func TestResumeConfirmationShowsWhatWillReRun(t *testing.T) {
	info := resumeConfirmationInfo{
		RunID:         "run-confirm",
		DisplayName:   "test-run",
		TaskCount:     3,
		PriorAttempts: 2,
	}
	msg := formatResumeConfirmation(info)
	if msg == "" {
		t.Fatal("expected non-empty confirmation message")
	}
	if !strings.Contains(msg, "run-confirm") {
		t.Fatal("confirmation should contain run ID")
	}
	if !strings.Contains(msg, "3 tasks") {
		t.Fatalf("confirmation should contain task count, got: %s", msg)
	}
	if !strings.Contains(msg, "2 prior attempt") {
		t.Fatalf("confirmation should contain prior attempts, got: %s", msg)
	}
	if !strings.Contains(msg, "re-spend") {
		t.Fatal("confirmation should mention re-spending budget")
	}
	if !strings.Contains(msg, "Resume? (y/N)") {
		t.Fatal("confirmation should prompt for confirmation")
	}
}

// TestResumeConfirmationHandlesEmptyFields verifies that the confirmation
// works gracefully with minimal info.
func TestResumeConfirmationHandlesEmptyFields(t *testing.T) {
	info := resumeConfirmationInfo{
		RunID: "run-minimal",
	}
	msg := formatResumeConfirmation(info)
	if msg == "" {
		t.Fatal("expected non-empty confirmation message")
	}
	if !strings.Contains(msg, "run-minimal") {
		t.Fatal("confirmation should contain run ID")
	}
	if !strings.Contains(msg, "pending tasks") {
		t.Fatal("confirmation without task count should mention pending tasks")
	}
}

// ensureCleanRunHandles cleans up any test residues.
func init() {
	// Clean up any handles left by other tests that might interfere.
	runHandles.Range(func(key, _ any) bool {
		runHandles.Delete(key)
		return true
	})
}

var _ = time.Now // reference to avoid unused import

// --- Regression coverage for the CLI-surface principal defect ---
//
// The original implementation passed context.Background() from every production
// call site and minted a fresh ephemeral principal when the context carried no
// caller. The handle was then owned by a session id nothing held, so the run the
// user had just resumed could not be inspected, joined or cancelled — the exact
// outcome §3.2 exists to prevent. The pre-existing principal test injected its
// own caller, so it passed while production was broken. These drive the
// production shape: a bare context plus the session principal from startup.

func newResumeFake(runID string) *fakeCoordinatorForResume {
	handle := &coordinator.RunHandle{}
	return &fakeCoordinatorForResume{
		resumeFunc: func(context.Context, string) (*coordinator.RunHandle, error) {
			return handle, nil
		},
		inspectFn: func(context.Context, *coordinator.RunHandle) (ledger.RunSnapshot, error) {
			return ledger.RunSnapshot{RunID: runID}, nil
		},
	}
}

func TestResumeFromCLISurfaceUsesSessionPrincipal(t *testing.T) {
	withActiveSession(t, "chat-session-1")
	t.Cleanup(func() { runHandles.Delete("run-cli") })

	// Bare context, exactly as handleSlashResume and the dashboard key pass it.
	record, err := resumeRun(context.Background(), newResumeFake("run-cli"), nil, "run-cli", nil)
	if err != nil {
		t.Fatalf("resumeRun: %v", err)
	}
	if record.principal.sessionID != "chat-session-1" {
		t.Fatalf("handle principal = %q, want the chat session %q (an ephemeral principal makes the run unreachable)",
			record.principal.sessionID, "chat-session-1")
	}
	stored, ok := runHandles.Load("run-cli")
	if !ok {
		t.Fatal("resumed handle not registered")
	}
	if got := stored.(*orchestrationHandle).principal.sessionID; got != "chat-session-1" {
		t.Fatalf("stored principal = %q, want %q", got, "chat-session-1")
	}
}

// §7's negative half: the resuming session can reach the handle and a different
// principal cannot. Asserts enforcement, not just the stored field value.
func TestResumedHandleRejectsForeignPrincipal(t *testing.T) {
	withActiveSession(t, "owner-session")
	t.Cleanup(func() { runHandles.Delete("run-owned") })

	record, err := resumeRun(context.Background(), newResumeFake("run-owned"), nil, "run-owned", nil)
	if err != nil {
		t.Fatalf("resumeRun: %v", err)
	}

	ownerCtx := runtime.ContextWithCaller(context.Background(), runtime.Caller{SessionID: "owner-session"})
	if !orchestrationHandleAccessible(ownerCtx, record, record.dispatcher, record.repo) {
		t.Fatal("resuming session must be able to reach the run it resumed")
	}
	foreignCtx := runtime.ContextWithCaller(context.Background(), runtime.Caller{SessionID: "other-session"})
	if orchestrationHandleAccessible(foreignCtx, record, record.dispatcher, record.repo) {
		t.Fatal("a foreign principal must not reach another session's resumed run")
	}
}

// Fail closed: with no session identity, refuse before resuming. Starting a run
// nobody can control is worse than refusing to start it.
func TestResumeRefusesWithoutSessionIdentity(t *testing.T) {
	withActiveSession(t, "")

	resumeCalled := false
	fake := &fakeCoordinatorForResume{
		resumeFunc: func(context.Context, string) (*coordinator.RunHandle, error) {
			resumeCalled = true
			return &coordinator.RunHandle{}, nil
		},
	}
	if _, err := resumeRun(context.Background(), fake, nil, "run-anon", nil); err == nil {
		t.Fatal("expected refusal when no session identity is available")
	}
	if resumeCalled {
		t.Fatal("must not resume a run before establishing who will own it")
	}
}
