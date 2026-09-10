package automation

// This file holds the tail of executor_test.go's own test suite,
// mechanically split out to keep both files under the project's
// per-file LOC cap (.mivia/policy/go-structure.json): spawnRunSession's
// and runStep's own remaining error-path and success-path branches
// (SetApprovalOverride failure, admitFire's real-error branch,
// startRun/runSteps/RunOnce's own store-error-wrap branches, StepSlash's
// execution-time validation rejection, and StepAgent's own
// ApplySessionAgent error and success paths). See executor_test.go's own
// header and helpers (recordingConversation, fakeExecSpawner,
// seedEnabledAutomation, newTestDB, dropAutomationRunsTable, etc.) for
// the shared fixtures every test below depends on - this file adds no
// new fixtures of its own.

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
)

// TestSpawnRunSessionPropagatesApprovalOverrideError covers
// spawnRunSession's own SetApprovalOverride-failure branch: the spawned
// session's D8 override cannot be installed, so RunOnce must abort the
// run RunFailed BEFORE any step is dispatched - a failure to establish
// the unattended posture must never fall through to running steps under
// an unknown/inherited posture.
func TestSpawnRunSessionPropagatesApprovalOverrideError(t *testing.T) {
	root := t.TempDir()
	db := newTestDB(t)
	automationID := seedEnabledAutomation(t, root, nil)
	spawn := &fakeExecSpawner{conv: newRecordingConversation(), setApprovalErr: fmt.Errorf("override install failed")}
	svc, err := New(root, db, spawn, Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	run, err := svc.RunOnce(context.Background(), automationID, ports.TriggerManual)
	if err != nil {
		t.Fatalf("RunOnce: got error %v, want nil (failure is recorded on the run)", err)
	}
	if run.State != ports.RunFailed {
		t.Fatalf("RunOnce State = %v, want RunFailed", run.State)
	}
	if !strings.Contains(run.Message, "override install failed") {
		t.Fatalf("run.Message = %q, want it to name the override install failure", run.Message)
	}
	if len(spawn.conv.sentTexts()) != 0 {
		t.Fatalf("sent texts = %v, want none (a failed approval override must precede any step dispatch)", spawn.conv.sentTexts())
	}
}

// TestRunOnceAdmitFirePropagatesRealError covers RunOnce's own
// admitFire-error-wrap-and-return branch: dropping run_claims before
// calling RunOnce makes admitFire fail with a real, non-ErrClaimHeld
// error, distinct from the documented lost-claim no-op every dedup test
// exercises.
func TestRunOnceAdmitFirePropagatesRealError(t *testing.T) {
	root := t.TempDir()
	db := newTestDB(t)
	automationID := seedEnabledAutomation(t, root, nil)
	spawn := &fakeExecSpawner{conv: newRecordingConversation()}
	svc, err := New(root, db, spawn, Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := dropRunClaimsTable(t, db); err != nil {
		t.Fatalf("drop run_claims table: %v", err)
	}
	if _, err := svc.RunOnce(context.Background(), automationID, ports.TriggerManual); err == nil {
		t.Fatal("RunOnce with run_claims dropped: got nil error, want admitFire's own real error")
	}
	if spawn.createCallCount() != 0 {
		t.Fatalf("CreateFreshInDir called %d times, want 0", spawn.createCallCount())
	}
}

// TestStartRunPropagatesUpdateRunStateError covers startRun's own
// second error-wrap branch directly (the pending->running
// updateRunState call): a SQLite trigger makes every UPDATE on
// automation_runs fail while INSERTs still succeed, so startRun's own
// createRun call succeeds but its immediately-following updateRunState
// call fails - the two calls are synchronous with no seam between them
// to interleave an out-of-band row mutation, so the trigger is the only
// way to fail the second write specifically.
func TestStartRunPropagatesUpdateRunStateError(t *testing.T) {
	root := t.TempDir()
	db := newTestDB(t)
	svc, err := New(root, db, &fakeExecSpawner{conv: newRecordingConversation()}, Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := forceAutomationRunsUpdateFailures(t, db); err != nil {
		t.Fatalf("install update-failing trigger: %v", err)
	}
	spec := Spec{ID: "auto-startrun-err", Steps: []Step{{Kind: StepPrompt, Prompt: "x"}}}
	if _, err := svc.startRun(context.Background(), spec, "auto-startrun-err", ports.TriggerManual, "holder-x"); err == nil {
		t.Fatal("startRun with automation_runs UPDATEs forced to fail: got nil error, want the second updateRunState error wrapped")
	}
}

// TestRunStepsPropagatesCheckpointError covers runSteps' own checkpoint
// updateRunState-error-wrap branch: the fake conversation drops the
// automation_runs table as a side effect of successfully completing its
// one step's Send call, so runStep returns nil (the step itself
// "succeeded") but the immediately following checkpoint updateRunState
// call - still inside runSteps, before it ever returns to RunOnce -
// fails against the now-missing table.
func TestRunStepsPropagatesCheckpointError(t *testing.T) {
	root := t.TempDir()
	db := newTestDB(t)
	automationID := seedEnabledAutomation(t, root, func(s *Spec) {
		s.Steps = []Step{{Kind: StepPrompt, Prompt: "only step"}}
	})
	conv := newRecordingConversation()
	conv.onSend = func() { _ = dropAutomationRunsTable(t, db) }
	spawn := &fakeExecSpawner{conv: conv}
	svc, err := New(root, db, spawn, Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	run, err := svc.RunOnce(context.Background(), automationID, ports.TriggerManual)
	if err != nil {
		t.Fatalf("RunOnce: got error %v, want nil (checkpoint failure is recorded via failRun, not returned)", err)
	}
	if run.State != ports.RunFailed {
		t.Fatalf("RunOnce State = %v, want RunFailed (checkpoint failure after a successful step)", run.State)
	}
	if !strings.Contains(run.Message, "checkpoint") {
		t.Fatalf("run.Message = %q, want it to name the checkpoint failure", run.Message)
	}
}

// TestRunOnceMarkSucceededPropagatesStoreError covers RunOnce's own
// final "mark run succeeded" error-wrap branch: a SQLite trigger allows
// the first three automation_runs UPDATEs (startRun's own
// pending->running transition, then chunk 8's own updateRunSession
// write-back, then the one step's own checkpoint) to succeed for real,
// then fails every UPDATE after that - so the run reaches a genuinely
// completed steps loop before RunOnce's own separate, final "mark
// succeeded" call is the one that hits the trigger.
func TestRunOnceMarkSucceededPropagatesStoreError(t *testing.T) {
	root := t.TempDir()
	db := newTestDB(t)
	automationID := seedEnabledAutomation(t, root, func(s *Spec) {
		s.Steps = []Step{{Kind: StepPrompt, Prompt: "only step"}}
	})
	if err := forceAutomationRunsUpdateFailuresAfter(t, db, 3); err != nil {
		t.Fatalf("install update-failing trigger: %v", err)
	}
	spawn := &fakeExecSpawner{conv: newRecordingConversation()}
	svc, err := New(root, db, spawn, Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := svc.RunOnce(context.Background(), automationID, ports.TriggerManual); err == nil {
		t.Fatal("RunOnce whose final mark-succeeded UPDATE is forced to fail: got nil error, want the wrapped store error")
	}
}

// TestRunStepSlashRejectedByExecutionTimeValidation covers runStep's
// StepSlash case's own validateStepSlash rejection branch, directly:
// re-validation at execution time (defense in depth) refuses a
// D15-rejected command before ever calling sendTurnHeadless.
func TestRunStepSlashRejectedByExecutionTimeValidation(t *testing.T) {
	root := t.TempDir()
	db := newTestDB(t)
	svc, err := New(root, db, &fakeExecSpawner{conv: newRecordingConversation()}, Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	conv := newRecordingConversation()
	step := Step{Kind: StepSlash, Ref: "/delete"} // session-lifecycle mutation, D15-rejected
	err = svc.runStep(context.Background(), "auto-x", "run-x", 0, root, conv, nil, step, time.Second)
	if err == nil {
		t.Fatal("runStep(StepSlash) with a D15-rejected command: got nil error, want rejection")
	}
	if len(conv.sentTexts()) != 0 {
		t.Fatalf("sent texts = %v, want none (a rejected slash command must never reach sendTurnHeadless)", conv.sentTexts())
	}
}

// TestRunStepAgentPropagatesApplySessionAgentError covers runStep's
// StepAgent case's own ApplySessionAgent error-wrap branch: a nil
// *chat.Session (what this test's dispatch always has, since this
// package's Service carries no real session-construction path in a unit
// test - see this file's own documented gap on StepAgent) makes
// ApplySessionAgent fail immediately and deterministically, before ever
// calling sendTurnHeadless.
func TestRunStepAgentPropagatesApplySessionAgentError(t *testing.T) {
	root := t.TempDir()
	db := newTestDB(t)
	svc, err := New(root, db, &fakeExecSpawner{conv: newRecordingConversation()}, Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	conv := newRecordingConversation()
	step := Step{Kind: StepAgent, Ref: "some-agent", Prompt: "do the thing"}
	err = svc.runStep(context.Background(), "auto-x", "run-x", 0, root, conv, nil, step, time.Second)
	if err == nil {
		t.Fatal("runStep(StepAgent) with a nil session: got nil error, want ApplySessionAgent's own rejection")
	}
	if len(conv.sentTexts()) != 0 {
		t.Fatalf("sent texts = %v, want none (a failed agent selection must never reach sendTurnHeadless)", conv.sentTexts())
	}
}

// TestRunStepAgentSucceedsWithRootAgentName covers runStep's StepAgent
// case's own SUCCESS path (the sendTurnHeadless call immediately after
// a successful ApplySessionAgent): per runStep's own documented
// StepAgent comment, this package's Service carries no wired
// *cliagents.AgentSessionState.Registry, so the only name
// ApplySessionAgent can ever resolve successfully here is
// config.RootAgentName - which ApplySessionAgent special-cases BEFORE
// ever consulting state.Registry (it routes to restoreRootSurface,
// which itself no-ops on a fresh, never-switched
// AgentSessionState{BaselineCaptured: false}). This is a REAL
// *chat.Session (chat.NewSession), not the nil boundSess every other
// StepAgent test in this file uses, so ApplySessionAgent's own nil
// guard cannot short-circuit it - the call genuinely reaches and
// returns from restoreRootSurface's real logic.
func TestRunStepAgentSucceedsWithRootAgentName(t *testing.T) {
	root := t.TempDir()
	db := newTestDB(t)
	svc, err := New(root, db, &fakeExecSpawner{conv: newRecordingConversation()}, Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	sess := chat.NewSession(&config.Resolved{ProviderName: "fake", Model: "model"}, nil)
	conv := newRecordingConversation()
	step := Step{Kind: StepAgent, Ref: config.RootAgentName, Prompt: "do the thing"}
	err = svc.runStep(context.Background(), "auto-x", "run-x", 0, root, conv, sess, step, time.Second)
	if err != nil {
		t.Fatalf("runStep(StepAgent) with a real session and RootAgentName: got error %v, want nil", err)
	}
	got := conv.sentTexts()
	if len(got) != 1 || got[0] != "do the thing" {
		t.Fatalf("sent texts = %v, want [\"do the thing\"] (StepAgent's own prompt sent after a successful agent selection)", got)
	}
}

// TestSpawnRunSessionPropagatesCreateFreshInDirError covers
// spawnRunSession's own CreateFreshInDir error-wrap branch directly:
// fakeExecSpawner.createErr makes CreateFreshInDir fail before bind is
// ever invoked, so spawnRunSession must return that error wrapped with
// "spawn session: " and zero every other return value.
func TestSpawnRunSessionPropagatesCreateFreshInDirError(t *testing.T) {
	root := t.TempDir()
	db := newTestDB(t)
	spawn := &fakeExecSpawner{conv: newRecordingConversation(), createErr: fmt.Errorf("boom-create")}
	svc, err := New(root, db, spawn, Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	conv, boundSess, savedName, err := svc.spawnRunSession(Spec{ID: "auto-x"}, "auto-x", "run-x", root)
	if err == nil {
		t.Fatal("spawnRunSession with a failing CreateFreshInDir: got nil error, want it propagated")
	}
	if !strings.Contains(err.Error(), "spawn session") || !strings.Contains(err.Error(), "boom-create") {
		t.Fatalf("spawnRunSession error = %q, want it naming both the spawn-session wrap and the underlying boom-create cause", err.Error())
	}
	if conv != nil || boundSess != nil || savedName != "" {
		t.Fatalf("spawnRunSession with a failing CreateFreshInDir returned (%v, %v, %q), want all zero", conv, boundSess, savedName)
	}
}
