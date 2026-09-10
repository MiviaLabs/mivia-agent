package automation

// This file holds resume_test.go's own error-propagation branches,
// mechanically split out (matching executor_test.go/
// executor_error_paths_test.go's own split) to keep resume_test.go
// under the project's per-file LOC soft cap: every ResumeRun/
// spawnAndLoadResumeSession/spawnResumeSession/markResumedRunSucceeded/
// resumeWorkDir error-wrap branch that resume_test.go's original 13
// tests never drove a real, non-sentinel error through. See
// resume_test.go's own header and helpers (sessionSpawner,
// newContextEnabledSession, seedResumableRun, seedEnabledAutomation)
// and runstore_test.go's own fault-injection helpers
// (dropAutomationRunsTable, dropRunClaimsTable,
// forceAutomationRunsUpdateFailures[After]) for the shared fixtures
// every test below depends on - this file adds no new fixtures of its
// own beyond one local helper (seedResumableRunWithSessionName).

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
)

// TestResumeRunGetRunPropagatesRealStoreError covers ResumeRun's own
// getRun-error-and-return branch directly: dropping automation_runs
// makes the very first store call inside ResumeRun fail with a real
// error, distinct from ErrRunNotFound (which reaches the store
// successfully and finds nothing).
func TestResumeRunGetRunPropagatesRealStoreError(t *testing.T) {
	root := t.TempDir()
	db := newTestDB(t)
	svc, err := New(root, db, &sessionSpawner{conv: newRecordingConversation()}, Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := dropAutomationRunsTable(t, db); err != nil {
		t.Fatalf("drop automation_runs table: %v", err)
	}
	if _, err := svc.ResumeRun(context.Background(), "any-run-id"); err == nil {
		t.Fatal("ResumeRun with automation_runs dropped: got nil error, want getRun's own real error")
	} else if errors.Is(err, ErrRunNotFound) {
		t.Fatalf("ResumeRun with automation_runs dropped: err = %v, want a real store error, not ErrRunNotFound", err)
	}
}

// TestResumeRunFindSpecPropagatesLoadError covers ResumeRun's own
// findSpec-error-and-return branch: a run row that exists and is
// resumable (getRun succeeds, state check passes) reaches findSpec,
// which fails against a malformed automations.toml - distinct from
// every other ResumeRun test, which always has a valid spec on disk.
func TestResumeRunFindSpecPropagatesLoadError(t *testing.T) {
	root := t.TempDir()
	db := newTestDB(t)
	svc, err := New(root, db, &sessionSpawner{conv: newRecordingConversation()}, Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	runID := "resume-findspec-err"
	if err := svc.createRun(context.Background(), Run{
		ID: runID, AutomationID: "auto-findspec-err", State: RunInterrupted,
		StepCount: 1, StartedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("createRun: %v", err)
	}
	path, err := automationsFilePath(ports.ScopeProject, root)
	if err != nil {
		t.Fatalf("automationsFilePath: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte("not [valid toml"), 0o644); err != nil {
		t.Fatalf("write malformed automations.toml: %v", err)
	}

	if _, err := svc.ResumeRun(context.Background(), runID); err == nil {
		t.Fatal("ResumeRun against a malformed automations.toml: got nil error, want findSpec's own load-wrap error")
	}
}

// seedResumableRunWithSessionName is a narrower variant of
// resume_test.go's own seedResumableRun: it stamps run.SessionName to
// the given value WITHOUT ever actually Saving a session under that
// name, letting a test reach ResumeRun's own spawnAndLoadResumeSession
// call (SessionName != "" clears the earlier ErrRunSessionMissing
// guard) while boundSess.Load(sessionName) itself still fails to find
// anything - the "session missing" and "session name present but
// unresolvable" cases are deliberately distinct code paths.
func seedResumableRunWithSessionName(t *testing.T, svc *Service, root, sessionName string, prompts []string) (automationID, runID string) {
	t.Helper()
	steps := make([]Step, 0, len(prompts))
	for _, p := range prompts {
		steps = append(steps, Step{Kind: StepPrompt, Prompt: p})
	}
	automationID = seedEnabledAutomation(t, root, func(s *Spec) { s.Steps = steps })
	runID = "resume-run-" + automationID
	if err := svc.createRun(context.Background(), Run{
		ID: runID, AutomationID: automationID, Origin: "manual",
		State: RunInterrupted, StepIndex: 0, StepCount: len(steps),
		SessionName: sessionName, StartedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("createRun: %v", err)
	}
	return automationID, runID
}

// TestResumeRunAdmitFirePropagatesRealError covers ResumeRun's own
// admitFire-error-and-return branch: dropping run_claims before calling
// ResumeRun makes admitFire fail with a real, non-ErrClaimHeld error,
// distinct from TestResumeRunHeldClaimReturnsErrRunAlreadyActive's
// documented lost-claim refusal.
func TestResumeRunAdmitFirePropagatesRealError(t *testing.T) {
	root := t.TempDir()
	db := newTestDB(t)
	spawner := &sessionSpawner{conv: newRecordingConversation()}
	svc, err := New(root, db, spawner, Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, runID := seedResumableRunWithSessionName(t, svc, root, "some-session", []string{"one", "two"})
	if err := dropRunClaimsTable(t, db); err != nil {
		t.Fatalf("drop run_claims table: %v", err)
	}

	if _, err := svc.ResumeRun(context.Background(), runID); err == nil {
		t.Fatal("ResumeRun with run_claims dropped: got nil error, want admitFire's own real error")
	} else if errors.Is(err, ErrRunAlreadyActive) {
		t.Fatalf("ResumeRun with run_claims dropped: err = %v, want a real error, not ErrRunAlreadyActive", err)
	}
	if spawner.createCallCount() != 0 {
		t.Fatalf("CreateFreshInDir called %d times, want 0", spawner.createCallCount())
	}
}

// TestResumeRunUpdateRunClaimTokenPropagatesStoreError covers
// ResumeRun's own updateRunClaimToken-error-and-return branch: a
// SQLite trigger fails every automation_runs UPDATE from installation
// onward, so admitFire's own real claim win (run_claims, a separate
// table) still succeeds but the immediately-following
// updateRunClaimToken call - the FIRST automation_runs UPDATE ResumeRun
// issues - is the one that fails.
func TestResumeRunUpdateRunClaimTokenPropagatesStoreError(t *testing.T) {
	root := t.TempDir()
	db := newTestDB(t)
	spawner := &sessionSpawner{conv: newRecordingConversation()}
	svc, err := New(root, db, spawner, Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, runID := seedResumableRunWithSessionName(t, svc, root, "some-session", []string{"one", "two"})
	if err := forceAutomationRunsUpdateFailures(t, db); err != nil {
		t.Fatalf("install update-failing trigger: %v", err)
	}

	if _, err := svc.ResumeRun(context.Background(), runID); err == nil {
		t.Fatal("ResumeRun with automation_runs UPDATEs forced to fail: got nil error, want updateRunClaimToken's own error")
	}
	if spawner.createCallCount() != 0 {
		t.Fatalf("CreateFreshInDir called %d times, want 0 (updateRunClaimToken must fail before any spawn attempt)", spawner.createCallCount())
	}
}

// TestResumeRunSpawnAndLoadSessionCreateErrorFailsRun covers THREE
// nested error-wrap branches at once, in the order execution actually
// reaches them: spawnResumeSession's own CreateFreshInDir-error wrap,
// spawnAndLoadResumeSession's own pass-through of that error, and
// ResumeRun's own failRun call for a failed spawnAndLoadResumeSession.
func TestResumeRunSpawnAndLoadSessionCreateErrorFailsRun(t *testing.T) {
	root := t.TempDir()
	db := newTestDB(t)
	sess := newContextEnabledSession(t, db)
	spawner := &sessionSpawner{conv: newRecordingConversation(), sess: sess}
	svc, err := New(root, db, spawner, Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, runID, _ := seedResumableRun(t, svc, root, sess, []string{"one", "two"}, RunInterrupted, 0, true)
	spawner.createErr = errFmtBoom("boom-resume-create")

	run, err := svc.ResumeRun(context.Background(), runID)
	if err != nil {
		t.Fatalf("ResumeRun: got error %v, want nil (spawn failure is recorded on the run via failRun)", err)
	}
	if run.State != ports.RunFailed {
		t.Fatalf("ResumeRun State = %v, want RunFailed", run.State)
	}
	if !strings.Contains(run.Message, "spawn resume session") || !strings.Contains(run.Message, "boom-resume-create") {
		t.Fatalf("run.Message = %q, want it to name both the spawn-resume-session wrap and the underlying cause", run.Message)
	}
}

// TestResumeRunSpawnAndLoadSessionLoadErrorFailsRun covers
// spawnAndLoadResumeSession's own boundSess.Load-error branch directly:
// the spawn itself succeeds (a real *chat.Session), but run.SessionName
// names a session that was never actually Saved anywhere, so Load
// fails to find it - distinct from the earlier CreateFreshInDir-error
// test above.
func TestResumeRunSpawnAndLoadSessionLoadErrorFailsRun(t *testing.T) {
	root := t.TempDir()
	db := newTestDB(t)
	sess := newContextEnabledSession(t, db)
	spawner := &sessionSpawner{conv: newRecordingConversation(), sess: sess}
	svc, err := New(root, db, spawner, Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, runID := seedResumableRunWithSessionName(t, svc, root, "session-never-saved", []string{"one"})

	run, err := svc.ResumeRun(context.Background(), runID)
	if err != nil {
		t.Fatalf("ResumeRun: got error %v, want nil (Load failure is recorded on the run via failRun)", err)
	}
	if run.State != ports.RunFailed {
		t.Fatalf("ResumeRun State = %v, want RunFailed", run.State)
	}
	if !strings.Contains(run.Message, "resumable session") {
		t.Fatalf("run.Message = %q, want it to name the missing-resumable-session failure", run.Message)
	}
}

// TestResumeRunSpawnAndLoadSessionApprovalOverrideErrorFailsRun covers
// spawnAndLoadResumeSession's own SetApprovalOverride-error branch
// directly: spawn AND Load both succeed for real (a pre-saved session,
// found and loaded), but installing the D8 approval override fails -
// distinct from both error branches above, which each fail before Load
// ever succeeds.
func TestResumeRunSpawnAndLoadSessionApprovalOverrideErrorFailsRun(t *testing.T) {
	root := t.TempDir()
	db := newTestDB(t)
	sess := newContextEnabledSession(t, db)
	spawner := &sessionSpawner{conv: newRecordingConversation(), sess: sess}
	svc, err := New(root, db, spawner, Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, runID, _ := seedResumableRun(t, svc, root, sess, []string{"one", "two"}, RunInterrupted, 0, true)
	spawner.setApprovalErr = errFmtBoom("boom-resume-approval")

	run, err := svc.ResumeRun(context.Background(), runID)
	if err != nil {
		t.Fatalf("ResumeRun: got error %v, want nil (approval-override failure is recorded on the run via failRun)", err)
	}
	if run.State != ports.RunFailed {
		t.Fatalf("ResumeRun State = %v, want RunFailed", run.State)
	}
	if !strings.Contains(run.Message, "install approval override") || !strings.Contains(run.Message, "boom-resume-approval") {
		t.Fatalf("run.Message = %q, want it to name both the install-approval-override wrap and the underlying cause", run.Message)
	}
}

// TestResumeRunUpdateRunStateRunningPropagatesStoreError covers
// ResumeRun's own "mark RunRunning" updateRunState-error-and-return
// branch: a SQLite trigger allows the first automation_runs UPDATE
// (updateRunClaimToken) to succeed for real, then fails every UPDATE
// after that - so ResumeRun reaches a genuinely successful spawn/Load/
// approval-override sequence before its own separate RunRunning
// transition is the one that fails.
func TestResumeRunUpdateRunStateRunningPropagatesStoreError(t *testing.T) {
	root := t.TempDir()
	db := newTestDB(t)
	sess := newContextEnabledSession(t, db)
	spawner := &sessionSpawner{conv: newRecordingConversation(), sess: sess}
	svc, err := New(root, db, spawner, Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, runID, _ := seedResumableRun(t, svc, root, sess, []string{"one", "two"}, RunInterrupted, 0, true)
	if err := forceAutomationRunsUpdateFailuresAfter(t, db, 1); err != nil {
		t.Fatalf("install update-failing trigger: %v", err)
	}

	if _, err := svc.ResumeRun(context.Background(), runID); err == nil {
		t.Fatal("ResumeRun whose RunRunning UPDATE is forced to fail: got nil error, want the wrapped store error")
	}
	if spawner.createCallCount() != 1 {
		t.Fatalf("CreateFreshInDir called %d times, want exactly 1 (spawn must have already succeeded before the RunRunning transition)", spawner.createCallCount())
	}
}

// TestResumeRunMarkAlreadyCompletePropagatesStoreError covers
// markResumedRunSucceeded's own updateRunState-error-wrap branch,
// reached through ResumeRun's "already complete" (StepIndex >=
// len(spec.Steps)) branch: a run whose own step index already covers
// every step reaches markResumedRunSucceeded directly, before
// admitFire or any spawn attempt, so the very first automation_runs
// UPDATE a failing trigger sees is this call's own.
func TestResumeRunMarkAlreadyCompletePropagatesStoreError(t *testing.T) {
	root := t.TempDir()
	db := newTestDB(t)
	sess := newContextEnabledSession(t, db)
	spawner := &sessionSpawner{conv: newRecordingConversation(), sess: sess}
	svc, err := New(root, db, spawner, Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, runID, _ := seedResumableRun(t, svc, root, sess, []string{"one", "two"}, RunInterrupted, 2, true)
	if err := forceAutomationRunsUpdateFailures(t, db); err != nil {
		t.Fatalf("install update-failing trigger: %v", err)
	}

	if _, err := svc.ResumeRun(context.Background(), runID); err == nil {
		t.Fatal("ResumeRun (already-complete branch) with automation_runs UPDATEs forced to fail: got nil error, want markResumedRunSucceeded's own wrapped error")
	}
	if spawner.createCallCount() != 0 {
		t.Fatalf("CreateFreshInDir called %d times, want 0 (already-complete run needs no spawn)", spawner.createCallCount())
	}
}

// TestResumeWorkDirBranches pins all three of resumeWorkDir's own
// branches directly: empty worktreePath passes root through unchanged,
// a worktreePath that exists on disk passes THAT path through unchanged
// (the branch every other ResumeRun-level test leaves uncovered, since
// they either omit WorktreePath entirely or stamp one that is missing
// on disk), and a worktreePath that does not exist on disk fails
// loudly.
func TestResumeWorkDirBranches(t *testing.T) {
	root := "/some/root"
	if got, err := resumeWorkDir(root, ""); err != nil || got != root {
		t.Fatalf("resumeWorkDir(root, \"\") = (%q, %v), want (%q, nil)", got, err, root)
	}

	existing := t.TempDir()
	if got, err := resumeWorkDir(root, existing); err != nil || got != existing {
		t.Fatalf("resumeWorkDir(root, existing) = (%q, %v), want (%q, nil)", got, err, existing)
	}

	missing := filepath.Join(existing, "nonexistent-worktree")
	if _, err := resumeWorkDir(root, missing); err == nil {
		t.Fatal("resumeWorkDir(root, missing worktree): got nil error, want rejection")
	}
}

// errFmtBoom is a tiny error constructor local to this file: every
// error injected here is a plain sentinel-free string, and importing
// "fmt" solely for fmt.Errorf("%s", ...) in a handful of one-line spots
// would be the only use of that import in this file otherwise.
type errFmtBoom string

func (e errFmtBoom) Error() string { return string(e) }
