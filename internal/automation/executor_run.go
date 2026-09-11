// Package automation owns user-defined automations: their TOML
// definitions, their schedules, and their durable run records.
//
// This file (executor_run.go) holds the run lifecycle shared by RunOnce
// and Apply(TriggerAutomation): admission (admitRun), execution
// (executeRun), and every terminal write. Each state write of a run in
// flight is fenced by its claim token (updateRunStateFenced), so a
// writer that lost its claim changes nothing and publishes nothing.
package automation

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
)

// errUserCancel is the cancel cause CancelRun sets on a run context.
// endFailedRun maps it to RunCancelled; every other cancellation of the
// run context (Close, a cancelled Serve ctx) maps to RunInterrupted.
var errUserCancel = errors.New("automation: run cancelled by user")

// Messages recorded on a run this process ended, rather than the
// crash-recovery sweep. closedBeforeStartMessage marks a trigger row
// admitted after Close: it has no session, so it is RunFailed, not
// resumable.
const (
	shutdownInterruptMessage = "interrupted: run cancelled by shutdown"
	closedInterruptMessage   = "interrupted: service closed"
	closedBeforeStartMessage = "service closed before the run started"
)

// admitted is a fire that won admission: its spec, its durable run row
// (already RunRunning), the fenced-claim holder token, and the trigger.
// complete marks a resume whose row already covered every step, so
// there is nothing left to execute.
type admitted struct {
	spec     Spec
	run      Run
	holder   string
	trigger  ports.TriggerKind
	complete bool
}

// admitRun performs every synchronous admission step of a fire: load
// the spec, refuse a disabled automation, win the fenced single-fire
// claim (docs/design/automations.md, "Fenced Single-Fire Claims"), and
// create the run row. A lost claim records a RunSkipped row and returns
// it together with ErrRunAlreadyActive, so the caller chooses between
// RunOnce's silent no-op and Apply's named refusal. On any error after
// the claim was won, the claim is released before returning.
func (s *Service) admitRun(ctx context.Context, automationID string, trigger ports.TriggerKind) (admitted, error) {
	spec, err := s.findSpec(automationID)
	if err != nil {
		return admitted{}, err
	}
	if !spec.Enabled {
		return admitted{}, fmt.Errorf("%w: %q", ErrAutomationDisabled, automationID)
	}
	holder, ok, err := s.admitFire(ctx, automationID)
	if err != nil {
		return admitted{}, err
	}
	if !ok {
		skipped, err := s.recordSkippedRun(ctx, automationID, trigger, len(spec.Steps))
		if err != nil {
			return admitted{}, err
		}
		return admitted{spec: spec, run: skipped, trigger: trigger}, fmt.Errorf("%w: automation %q", ErrRunAlreadyActive, automationID)
	}
	run, err := s.startRun(ctx, spec, automationID, trigger, holder)
	if err != nil {
		_ = s.db.ReleaseClaim(context.WithoutCancel(ctx), claimKey(automationID), holder)
		return admitted{}, err
	}
	return admitted{spec: spec, run: run, holder: holder, trigger: trigger}, nil
}

// RunOnce turns a fire into a real, synchronous execution: admission
// (admitRun), then execution (executeRun) on the caller's goroutine. A
// lost claim is a documented no-op: the RunSkipped row is returned with
// a nil error.
func (s *Service) RunOnce(ctx context.Context, automationID string, trigger ports.TriggerKind) (ports.Run, error) {
	adm, err := s.admitRun(ctx, automationID, trigger)
	if errors.Is(err, ErrRunAlreadyActive) {
		return runToPorts(adm.run), nil
	}
	if err != nil {
		return ports.Run{}, err
	}
	return s.executeRun(ctx, adm)
}

// executeRun drives an admitted run to a terminal state: optional
// managed worktree, ONE spawned session with its unattended approval
// posture installed before any step, the steps in order with per-step
// checkpoints, and the terminal write (docs/design/automations.md,
// "Execution Model"). Both RunOnce (caller's goroutine and ctx) and
// Apply (Service goroutine, run ctx) reach it. The fenced claim is
// heartbeated while steps run and released on every exit path. A ctx
// cancellation ends the run as RunCancelled or RunInterrupted (see
// endFailedRun). The error is non-nil only when a terminal write was
// fenced out or the success write failed.
func (s *Service) executeRun(ctx context.Context, adm admitted) (ports.Run, error) {
	spec, run, automationID := adm.spec, adm.run, adm.run.AutomationID
	defer func() {
		_ = s.db.ReleaseClaim(context.WithoutCancel(ctx), claimKey(automationID), adm.holder)
	}()
	stopRefresh := s.startClaimRefresh(automationID, adm.holder)
	defer stopRefresh()

	workDir := s.root
	if spec.Worktree == WorktreeNew {
		wtDir, wtBranch, failErr := s.createRunWorktree(spec, run.ID)
		if failErr != nil {
			return s.failRun(ctx, run, 0, failErr), nil
		}
		workDir = wtDir
		run.WorktreePath = wtDir
		run.WorktreeBranch = wtBranch
	}

	conv, boundSess, savedName, err := s.spawnRunSession(spec, automationID, run.ID, workDir)
	if err != nil {
		return s.failRun(ctx, run, 0, err), nil
	}
	// A run that spawned but whose session name cannot be recorded is
	// not safely resumable (ResumeRun refuses a run with no
	// SessionName), so this is a spawn failure. savedName == "" is not
	// an error: it is the "no context store configured" state.
	if err := s.updateRunSession(ctx, run.ID, savedName); err != nil {
		return s.failRun(ctx, run, 0, fmt.Errorf("record run session: %w", err)), nil
	}
	run.SessionName = savedName

	if err := s.runSteps(ctx, spec, automationID, workDir, conv, boundSess, &run, 0); err != nil {
		return s.endFailedRun(ctx, run, err)
	}
	return s.succeedRun(ctx, run, len(spec.Steps))
}

// endFailedRun maps a runSteps error to its terminal state. A fenced
// checkpoint is returned as ErrRunFenced with no write: another holder
// owns the row, so this writer reports nothing about it. A checkpoint
// that found the row already settled elsewhere (ErrRunSettledElsewhere -
// a concurrent InterruptRunningAutomationRun, or any other terminal
// write, landed between the step completing and the checkpoint) is
// likewise reported with no further write: whoever changed the row
// already closed it out, so this writer only re-reads and returns the
// row as stored, with a nil error - not a step failure and not a lost
// fence. Otherwise the run ctx decides: still live means a step failure
// (RunFailed, which includes a per-step timeout, since that deadline
// lives on a child ctx); ended with cause errUserCancel means CancelRun
// (RunCancelled); ended for any other reason means shutdown
// (RunInterrupted, which a resume admits). StepIndex is not advanced in
// any case.
func (s *Service) endFailedRun(ctx context.Context, run Run, err error) (ports.Run, error) {
	if errors.Is(err, ErrRunFenced) {
		return ports.Run{}, err
	}
	if errors.Is(err, ErrRunSettledElsewhere) {
		wctx := context.WithoutCancel(ctx)
		if stored, ok, gerr := s.getRun(wctx, run.ID); gerr == nil && ok {
			return runToPorts(stored), nil
		}
		return ports.Run{}, nil
	}
	if ctx.Err() == nil {
		return s.failRun(ctx, run, run.StepIndex, err), nil
	}
	if errors.Is(context.Cause(ctx), errUserCancel) {
		return s.cancelRun(ctx, run, run.StepIndex, err), nil
	}
	return s.endRun(ctx, run, RunInterrupted, run.StepIndex, RunFailNone, shutdownInterruptMessage), nil
}

// succeedRun writes and publishes the RunSucceeded terminal state. The
// write uses a context that survives cancellation, so a run that
// completed is always recorded as such. A failed or fenced write is
// returned as a wrapped error; nothing is published in that case, since
// the durable row did not change.
func (s *Service) succeedRun(ctx context.Context, run Run, stepCount int) (ports.Run, error) {
	endedAt := time.Now().UTC()
	if err := s.updateRunStateFenced(context.WithoutCancel(ctx), run, RunSucceeded, stepCount, &endedAt, RunFailNone, ""); err != nil {
		return ports.Run{}, fmt.Errorf("automation: mark run succeeded: %w", err)
	}
	run.State = RunSucceeded
	run.StepIndex = stepCount
	run.EndedAt = &endedAt
	s.publishRun(run)
	return runToPorts(run), nil
}

// startRun creates the run row (RunPending) and immediately marks it
// RunRunning. A crash between the two leaves a durable RunPending row
// in the run history. The sweep reads only running rows, so that row is
// never swept; it is visible, not recovered.
func (s *Service) startRun(ctx context.Context, spec Spec, automationID string, trigger ports.TriggerKind, holder string) (Run, error) {
	run := Run{
		ID:           newHolderToken("run-"),
		AutomationID: automationID,
		Origin:       originForTrigger(trigger),
		State:        RunPending,
		StepCount:    len(spec.Steps),
		ClaimToken:   holder,
		StartedAt:    time.Now().UTC(),
	}
	if err := s.createRun(ctx, run); err != nil {
		return Run{}, fmt.Errorf("automation: create run: %w", err)
	}
	s.publishRun(run)
	if err := s.updateRunStateFenced(ctx, run, RunRunning, 0, nil, RunFailNone, ""); err != nil {
		return Run{}, fmt.Errorf("automation: mark run running: %w", err)
	}
	run.State = RunRunning
	s.publishRun(run)
	return run, nil
}

// failRun transitions run to RunFailed/RunFailJobError at stepIndex,
// publishes it, and returns its ports.Run view. The write uses a
// context that survives cancellation. A write error is logged, not
// returned: a failed run whose own failure cannot be recorded must
// still report the original failure, not a bookkeeping error.
func (s *Service) failRun(ctx context.Context, run Run, stepIndex int, cause error) ports.Run {
	return s.endRun(ctx, run, RunFailed, stepIndex, RunFailJobError, cause.Error())
}

// cancelRun mirrors failRun for a run CancelRun cancelled: the row
// becomes RunCancelled at stepIndex, which is never advanced.
func (s *Service) cancelRun(ctx context.Context, run Run, stepIndex int, cause error) ports.Run {
	return s.endRun(ctx, run, RunCancelled, stepIndex, RunFailNone, cause.Error())
}

// endRun writes one terminal state through the fence and returns the
// view. It publishes only when the row changed: a fenced-out write was
// already logged by the fence, any other write error is logged here,
// and in both cases no watcher sees a state the row does not carry.
// When the write did not land, the returned view is the row as stored,
// re-read with a context that survives cancellation. Only when that
// read fails too is the wanted state returned unpublished, since
// nothing else is known about the row.
func (s *Service) endRun(ctx context.Context, run Run, state RunState, stepIndex int, failKind RunFailKind, message string) ports.Run {
	endedAt := time.Now().UTC()
	wctx := context.WithoutCancel(ctx)
	landed := true
	if err := s.updateRunStateFenced(wctx, run, state, stepIndex, &endedAt, failKind, message); err != nil {
		landed = false
		if !errors.Is(err, ErrRunFenced) {
			log.Printf("automation %q: run %q: mark %s: %v", run.AutomationID, run.ID, state, err)
		}
		if stored, ok, gerr := s.getRun(wctx, run.ID); gerr == nil && ok {
			return runToPorts(stored)
		}
	}
	run.State = state
	run.StepIndex = stepIndex
	run.EndedAt = &endedAt
	run.FailKind = failKind
	run.Message = message
	if landed {
		s.publishRun(run)
	}
	return runToPorts(run)
}
