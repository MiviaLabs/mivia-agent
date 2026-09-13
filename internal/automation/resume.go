// Package automation owns user-defined automations: their TOML
// definitions, their schedules, and their durable run records.
//
// This file (resume.go) turns a RunInterrupted or RunFailed run row
// back into a live execution that continues from exactly where it
// stopped (docs/design/automations.md, "Resuming Runs"). It reuses the
// executor's own runSteps (executor.go), failRun (executor_run.go), and
// claim.go's admitFire/ReleaseClaim,
// but is user-initiated (via Apply's ResumeAutomationRun edit), not a
// scheduler fire - so a lost claim here is a NAMED refusal
// (ErrRunAlreadyActive), never the silent RunSkipped no-op RunOnce uses
// for a scheduled fire that lost the race.
package automation

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
)

// defaultSweepMaxAge is the crash-recovery sweep's staleness threshold
// used by the exported SweepInterrupted wrapper: a running run whose
// fenced claim has been unrenewed for at least this long is presumed
// abandoned ("Interrupted Run Sweep").
const defaultSweepMaxAge = 5 * time.Minute

// ErrRunNotFound is returned by ResumeRun when runID names no run.
var ErrRunNotFound = fmt.Errorf("automation: run not found")

// ErrRunNotResumable is returned by ResumeRun when the run exists but
// is not in a resumable state (only RunInterrupted and RunFailed are).
var ErrRunNotResumable = fmt.Errorf("automation: run not resumable")

// ErrRunSessionMissing is returned by admitResume when the run has no
// recorded session name - it was never durably saved (no context store
// configured at spawn time, or the run never reached spawn at all) and
// so has nothing to restore. It is the ONLY refusal of its kind: a name
// that is recorded but does not resolve surfaces through the spawner's
// own error, and a live-lease conflict is mapped separately in
// executeResume.
var ErrRunSessionMissing = fmt.Errorf("automation: run has no resumable session")

// ErrRunAlreadyActive is returned by ResumeRun when the automation's
// fenced single-fire claim is currently held by someone else - a
// NAMED refusal, not the silent RunSkipped no-op RunOnce uses for a
// scheduler fire that lost the same race: Resume is user-initiated and
// must surface the conflict rather than swallow it.
var ErrRunAlreadyActive = fmt.Errorf("automation: already running")

// ResumeRun restarts an interrupted or failed run from exactly the step
// it stopped at ("Resume Procedure"), synchronously on the caller's goroutine:
// admission (admitResume) then execution (executeResume). StepIndex
// already names the next step to run in both the success-checkpoint
// case (runSteps sets it to i+1 after step i succeeds) and the
// failure-checkpoint case (runSteps leaves it at i when step i itself
// failed, so resume re-runs that step whole).
func (s *Service) ResumeRun(ctx context.Context, runID string) (ports.Run, error) {
	adm, err := s.admitResume(ctx, runID)
	if err != nil {
		return ports.Run{}, err
	}
	if adm.complete {
		return runToPorts(adm.run), nil
	}
	return s.executeResume(ctx, adm)
}

// admitResume performs every synchronous admission step of a resume:
// load and check the run row, load the spec, refuse a run with no saved
// session, re-claim the automation's fenced single-fire claim (a lost
// claim is the NAMED refusal ErrRunAlreadyActive), and persist the new
// holder. A run whose StepIndex already covers every step is marked
// succeeded here and returned with complete set.
func (s *Service) admitResume(ctx context.Context, runID string) (admitted, error) {
	run, ok, err := s.getRun(ctx, runID)
	if err != nil {
		return admitted{}, err
	}
	if !ok {
		return admitted{}, ErrRunNotFound
	}
	if run.State != RunInterrupted && run.State != RunFailed {
		return admitted{}, fmt.Errorf("%w: %q state %s", ErrRunNotResumable, runID, run.State)
	}
	spec, err := s.findSpec(run.AutomationID)
	if err != nil {
		return admitted{}, err
	}
	if run.StepIndex >= len(spec.Steps) {
		// A stale interrupted flag or a race, not unfinished work.
		done, err := s.markResumedRunSucceeded(ctx, run, spec)
		if err != nil {
			return admitted{}, err
		}
		return admitted{spec: spec, run: done, complete: true}, nil
	}
	if run.SessionName == "" {
		return admitted{}, fmt.Errorf("%w: run %q", ErrRunSessionMissing, runID)
	}
	holder, ok, err := s.admitFire(ctx, run.AutomationID)
	if err != nil {
		return admitted{}, err
	}
	if !ok {
		return admitted{}, fmt.Errorf("%w: automation %q", ErrRunAlreadyActive, run.AutomationID)
	}
	// Persist the new holder so a later sweep sees the current owner,
	// not the dead one the interrupted/failed row still carries.
	if err := s.updateRunClaimToken(ctx, run.ID, holder); err != nil {
		_ = s.db.ReleaseClaim(context.WithoutCancel(ctx), claimKey(run.AutomationID), holder)
		return admitted{}, err
	}
	run.ClaimToken = holder
	return admitted{spec: spec, run: run, holder: holder, trigger: runOriginToTrigger(run.Origin)}, nil
}

// executeResume drives an admitted resume to a terminal state: re-spawn
// a session bound to the run's saved conversation, install the same
// unattended approval posture, and dispatch spec.Steps from
// run.StepIndex. The claim is heartbeated while steps run and released
// on every exit path. The error is non-nil only for a store failure on
// the RunRunning transition, a fenced checkpoint, or a failed or fenced
// terminal success write.
func (s *Service) executeResume(ctx context.Context, adm admitted) (ports.Run, error) {
	spec, run := adm.spec, adm.run
	defer func() {
		_ = s.db.ReleaseClaim(context.WithoutCancel(ctx), claimKey(run.AutomationID), adm.holder)
	}()
	stopRefresh := s.startClaimRefresh(run.AutomationID, adm.holder)
	defer stopRefresh()

	workDir, err := resumeWorkDir(s.root, run.WorktreePath)
	if err != nil {
		return s.failRun(ctx, run, run.StepIndex, err), nil
	}
	conv, boundSess, err := s.spawnAndLoadResumeSession(spec, run, workDir)
	if err != nil {
		var live *contextstate.SessionLiveError
		if errors.As(err, &live) {
			// A deliberate refusal, not a missing snapshot: a new-format
			// run resumed by ANOTHER process while the first is still
			// alive must name the lease conflict (the old reserved-name
			// fork silently allowed two writers on one session).
			return s.failRun(ctx, run, run.StepIndex, fmt.Errorf("resume session is in use by another mivia process (lease held): %w", err)), nil
		}
		return s.failRun(ctx, run, run.StepIndex, err), nil
	}
	// The stored name and the restored session can disagree: a row
	// written by an older build (a legacy reserved name), or a pool that
	// resolved the saved name onto a session carrying a different live
	// id. Re-point the row to the id the restored session actually has,
	// BEFORE the RunRunning transition and any publish, so every later
	// checkpoint save - and any later resume - lands on this same row.
	if boundSess != nil && boundSess.SessionID != run.SessionName {
		if err := s.updateRunSession(ctx, run.ID, boundSess.SessionID); err != nil {
			return s.failRun(ctx, run, run.StepIndex, fmt.Errorf("record run session: %w", err)), nil
		}
		run.SessionName = boundSess.SessionID
	}
	// Same ownership contract as executeRun: the session is this run's
	// until the wrapper exits, whatever the terminal write turns out to
	// be.
	s.registerRunSession(run.ID, conv.ID())
	defer s.clearRunSession(run.ID)
	if err := s.updateRunStateFenced(ctx, run, RunRunning, run.StepIndex, nil, RunFailNone, ""); err != nil {
		return ports.Run{}, err
	}
	run.State = RunRunning
	run.EndedAt = nil
	run.FailKind = RunFailNone
	run.Message = ""
	s.publishRun(run)

	if err := s.runSteps(ctx, spec, run.AutomationID, workDir, conv, boundSess, &run, run.StepIndex); err != nil {
		return s.endFailedRun(ctx, run, err)
	}
	return s.succeedRun(ctx, run, len(spec.Steps))
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

// spawnAndLoadResumeSession returns the run's restored session through
// the spawner's GetOrResumeInDir: the implementor restores history
// itself on the miss path, so there is no caller-side Load and no bind
// closure (nothing durable ever runs before SetApprovalOverride - see
// uiadapter.SetApprovalOverride's own documented ordering rule). The
// approval override still installs strictly after the spawn returns.
func (s *Service) spawnAndLoadResumeSession(spec Spec, run Run, workDir string) (ports.Conversation, *chat.Session, error) {
	conv, boundSess, err := s.spawn.GetOrResumeInDir(run.SessionName, workDir)
	if err != nil {
		return nil, nil, fmt.Errorf("spawn resume session: %w", err)
	}
	gate, policy := unattendedGateFor(run.AutomationID, spec.Unattended)
	if err := s.spawn.SetApprovalOverride(conv.ID(), gate, policy); err != nil {
		return nil, nil, fmt.Errorf("install approval override: %w", err)
	}
	return conv, boundSess, nil
}

// markResumedRunSucceeded handles a resume's "already complete" branch:
// run.StepIndex already covers every step of spec, so there is no work
// left - mark the row RunSucceeded, publish it, and return it without
// spawning a session or touching the fenced claim. The write carries the
// row's own claim token and survives cancellation, like every other
// terminal write.
func (s *Service) markResumedRunSucceeded(ctx context.Context, run Run, spec Spec) (Run, error) {
	endedAt := time.Now().UTC()
	if err := s.updateRunStateFenced(context.WithoutCancel(ctx), run, RunSucceeded, len(spec.Steps), &endedAt, RunFailNone, ""); err != nil {
		return Run{}, fmt.Errorf("automation: mark already-complete run succeeded: %w", err)
	}
	run.State = RunSucceeded
	run.StepIndex = len(spec.Steps)
	run.EndedAt = &endedAt
	run.FailKind = RunFailNone
	run.Message = ""
	s.publishRun(run)
	return run, nil
}

// SweepInterrupted is the exported wrapper claim.go's sweepInterrupted
// needs to be reachable from outside the package (e.g. a periodic
// crash-recovery job): it applies the package's own default staleness
// threshold ("Interrupted Run Sweep") rather than requiring every caller to choose one.
func (s *Service) SweepInterrupted(ctx context.Context) (int, error) {
	return s.sweepInterrupted(ctx, defaultSweepMaxAge)
}
