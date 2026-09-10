// Package automation owns user-defined automations: their TOML
// definitions, their schedules, and their durable run records.
//
// This file (resume.go) is chunk 8: turning an RunInterrupted or
// RunFailed run row back into a live execution that continues from
// exactly where it stopped (D13). It reuses the executor's own
// runSteps/failRun (executor.go) and claim.go's admitFire/ReleaseClaim,
// but is user-initiated (via Apply's ResumeAutomationRun edit), not a
// scheduler fire - so a lost claim here is a NAMED refusal
// (ErrRunAlreadyActive), never the silent RunSkipped no-op RunOnce uses
// for a scheduled fire that lost the race.
package automation

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
)

// defaultSweepMaxAge is the crash-recovery sweep's staleness threshold
// used by the exported SweepInterrupted wrapper: a running run whose
// fenced claim has been unrenewed for at least this long is presumed
// abandoned (D13).
const defaultSweepMaxAge = 5 * time.Minute

// ErrRunNotFound is returned by ResumeRun when runID names no run.
var ErrRunNotFound = fmt.Errorf("automation: run not found")

// ErrRunNotResumable is returned by ResumeRun when the run exists but
// is not in a resumable state (only RunInterrupted and RunFailed are).
var ErrRunNotResumable = fmt.Errorf("automation: run not resumable")

// ErrRunSessionMissing is returned by ResumeRun when the run has no
// recorded session name (D11) - it was never durably saved (no context
// store configured at spawn time, or the run never reached spawn at
// all) and so has nothing to Load back.
var ErrRunSessionMissing = fmt.Errorf("automation: run has no resumable session")

// ErrRunAlreadyActive is returned by ResumeRun when the automation's
// fenced single-fire claim (D7) is currently held by someone else - a
// NAMED refusal, not the silent RunSkipped no-op RunOnce uses for a
// scheduler fire that lost the same race: Resume is user-initiated and
// must surface the conflict rather than swallow it.
var ErrRunAlreadyActive = fmt.Errorf("automation: already running")

// ResumeRun restarts an interrupted or failed run from exactly the step
// it stopped at (D13): re-claim the automation's fenced single-fire
// claim, re-spawn a session bound to the run's saved conversation
// (D11), install the same D8 unattended posture, and dispatch
// spec.Steps starting at run.StepIndex - no +1 adjustment, since
// StepIndex already names the next step to run in both the
// success-checkpoint case (runSteps sets it to i+1 after step i
// succeeds) and the failure-checkpoint case (runSteps leaves it at i
// when step i itself failed, so resume re-runs that step whole).
func (s *Service) ResumeRun(ctx context.Context, runID string) (ports.Run, error) {
	run, ok, err := s.getRun(ctx, runID)
	if err != nil {
		return ports.Run{}, err
	}
	if !ok {
		return ports.Run{}, ErrRunNotFound
	}
	if run.State != RunInterrupted && run.State != RunFailed {
		return ports.Run{}, fmt.Errorf("%w: %q state %s", ErrRunNotResumable, runID, run.State)
	}

	spec, err := s.findSpec(run.AutomationID)
	if err != nil {
		return ports.Run{}, err
	}

	if run.StepIndex >= len(spec.Steps) {
		// The run's own step index already covers every step: this is a
		// race or a stale interrupted flag, not genuinely unfinished
		// work. Mark it succeeded and return without spawning anything.
		return s.markResumedRunSucceeded(ctx, run, spec)
	}

	if run.SessionName == "" {
		// Fail fast, before any spawn attempt: a run with no recorded
		// session name has nothing to Load back into.
		return ports.Run{}, fmt.Errorf("%w: run %q", ErrRunSessionMissing, runID)
	}

	holder, ok, err := s.admitFire(ctx, run.AutomationID)
	if err != nil {
		return ports.Run{}, err
	}
	if !ok {
		return ports.Run{}, fmt.Errorf("%w: automation %q", ErrRunAlreadyActive, run.AutomationID)
	}
	defer func() {
		_ = s.db.ReleaseClaim(context.Background(), claimKey(run.AutomationID), holder)
	}()
	// Persist the new holder so a later sweep sees the current owner,
	// not the dead one the interrupted/failed row still carries.
	if err := s.updateRunClaimToken(ctx, run.ID, holder); err != nil {
		return ports.Run{}, err
	}

	workDir, err := resumeWorkDir(s.root, run.WorktreePath)
	if err != nil {
		return s.failRun(ctx, run, run.StepIndex, err), nil
	}

	conv, boundSess, err := s.spawnAndLoadResumeSession(spec, run, workDir)
	if err != nil {
		return s.failRun(ctx, run, run.StepIndex, err), nil
	}

	if err := s.updateRunState(ctx, run.ID, RunRunning, run.StepIndex, nil, RunFailNone, ""); err != nil {
		return ports.Run{}, err
	}
	run.State = RunRunning

	startIndex := run.StepIndex
	if err := s.runSteps(ctx, spec, run.AutomationID, workDir, conv, boundSess, &run, startIndex); err != nil {
		return s.failRun(ctx, run, run.StepIndex, err), nil
	}
	return s.markResumedRunSucceeded(ctx, run, spec)
}

// resumeWorkDir resolves ResumeRun's working directory: the run's
// recorded worktree path when it has one (failing loudly if that path
// no longer exists on disk - a resumed run must not silently fall back
// to root and operate on the wrong tree), or root when the run never
// used a managed worktree.
func resumeWorkDir(root, worktreePath string) (string, error) {
	if worktreePath == "" {
		return root, nil
	}
	if _, statErr := os.Stat(worktreePath); statErr != nil {
		return "", fmt.Errorf("resume: worktree %q missing: %w", worktreePath, statErr)
	}
	return worktreePath, nil
}

// spawnAndLoadResumeSession spawns the resume session and restores its
// saved transcript, in the exact order D13 requires: spawn, THEN Load,
// THEN the approval override - mirroring
// uiadapter.SetApprovalOverride's own documented ordering rule
// (CreateFreshInDir invokes the bind closure before wireEntryLocked's
// own approval inheritance, so any durable session mutation performed
// inside the bind closure would race that ordering; Load is exactly
// such a mutation).
func (s *Service) spawnAndLoadResumeSession(spec Spec, run Run, workDir string) (ports.Conversation, *chat.Session, error) {
	conv, boundSess, err := s.spawnResumeSession(spec, run.SessionName, workDir)
	if err != nil {
		return nil, nil, err
	}
	if err := boundSess.Load(run.SessionName); err != nil {
		return nil, nil, fmt.Errorf("%w: %v", ErrRunSessionMissing, err)
	}
	gate, policy := unattendedGateFor(run.AutomationID, spec.Unattended)
	if err := s.spawn.SetApprovalOverride(conv.ID(), gate, policy); err != nil {
		return nil, nil, fmt.Errorf("install approval override: %w", err)
	}
	return conv, boundSess, nil
}

// markResumedRunSucceeded handles ResumeRun's "already complete" branch:
// run.StepIndex already covers every step of spec, so there is no work
// left to (re)do - mark the row RunSucceeded and return it without ever
// spawning a session or touching the fenced claim.
func (s *Service) markResumedRunSucceeded(ctx context.Context, run Run, spec Spec) (ports.Run, error) {
	endedAt := time.Now().UTC()
	if err := s.updateRunState(ctx, run.ID, RunSucceeded, len(spec.Steps), &endedAt, RunFailNone, ""); err != nil {
		return ports.Run{}, fmt.Errorf("automation: mark already-complete run succeeded: %w", err)
	}
	run.State = RunSucceeded
	run.StepIndex = len(spec.Steps)
	run.EndedAt = &endedAt
	return runToPorts(run), nil
}

// spawnResumeSession spawns the ONE session ResumeRun drives for the
// rest of the run: the bind closure performs ONLY the capture (no
// Save/Load), matching spawnRunSession's own ordering rule - Load
// happens in the CALLER (ResumeRun) after CreateFreshInDir returns, the
// same ordering SetApprovalOverride already requires.
func (s *Service) spawnResumeSession(spec Spec, sessionName, workDir string) (ports.Conversation, *chat.Session, error) {
	var boundSess *chat.Session
	bindFn := func(sess *chat.Session) (string, error) {
		boundSess = sess
		return "", nil
	}
	conv, err := s.spawn.CreateFreshInDir(bindFn, workDir)
	if err != nil {
		return nil, nil, fmt.Errorf("spawn resume session: %w", err)
	}
	return conv, boundSess, nil
}

// SweepInterrupted is the exported wrapper claim.go's sweepInterrupted
// needs to be reachable from outside the package (e.g. a periodic
// crash-recovery job): it applies the package's own default staleness
// threshold (D13) rather than requiring every caller to choose one.
func (s *Service) SweepInterrupted(ctx context.Context) (int, error) {
	return s.sweepInterrupted(ctx, defaultSweepMaxAge)
}
