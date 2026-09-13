// Package automation owns user-defined automations: their TOML
// definitions, their schedules, and their durable run records.
//
// This file (runstore.go) is the run-lifecycle data layer: Go
// read/write helpers over storage.SQLite's automation_runs table
// (docs/design/automations.md, "Run History Schema"). It builds no session, no
// worktree, and fires no claim beyond the read of an existing one -
// admission and the crash-recovery sweep live in claim.go. Every method
// here handles s.db == nil gracefully (New already allows a nil db,
// meaning "no run persistence"), matching service.go's existing
// unconditional-empty style for Runs()/Run().
package automation

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/storage"
)

// RunState is this package's internal run-lifecycle vocabulary, mirroring
// ports.RunState's values exactly (including RunInterrupted) without
// importing ports into the storage round trip itself - runstore.go stays
// storage-shape-first; the mapper in service_map.go converts to/from
// ports.RunState at the Service boundary, the same pattern used for
// StepKind/TriggerKind/ScheduleKind.
type RunState string

const (
	RunPending     RunState = "pending"
	RunRunning     RunState = "running"
	RunSucceeded   RunState = "succeeded"
	RunFailed      RunState = "failed"
	RunCancelled   RunState = "cancelled"
	RunInterrupted RunState = "interrupted"
	// RunSkipped marks a fire that lost the fenced single-fire claim:
	// another fire already owns the automation's in-flight run,
	// so this fire is a documented no-op, not an error. It is recorded
	// as its own run row (rather than fabricating nothing) so a lost
	// dedup race is visible in run history instead of silently
	// vanishing.
	RunSkipped RunState = "skipped"
)

// RunFailKind mirrors ports.RunFailKind's values as a stored string,
// same rationale as RunState above.
type RunFailKind string

const (
	RunFailNone            RunFailKind = ""
	RunFailJobError        RunFailKind = "job_error"
	RunFailConditionNotMet RunFailKind = "condition_not_met"
	RunFailTimeout         RunFailKind = "timeout"
)

// Run is one automation execution, the exported round-trip shape for
// every column of automation_runs (storage.AutomationRun). EndedAt is nil
// until the run reaches a terminal state.
type Run struct {
	ID             string
	AutomationID   string
	Origin         string
	State          RunState
	StepIndex      int
	StepCount      int
	SessionName    string
	WorktreePath   string
	WorktreeBranch string
	ClaimToken     string
	StartedAt      time.Time
	EndedAt        *time.Time
	FailKind       RunFailKind
	Message        string
}

// errNoRunStore is returned by every runstore method when the Service was
// built with a nil db (New allows this - "no run persistence"). It is
// deliberately unexported: callers that need a graceful empty result
// (getRun/listRuns via service.go's Run/Runs, sweepInterrupted in
// claim.go) branch on s.db == nil themselves rather than on this error,
// since a nil db is a configuration fact, not a failure. createRun's
// callers (startRun, recordSkippedRun) return it wrapped.
var errNoRunStore = errors.New("automation: no run store configured (nil db)")

// toStorageRun converts a Run to its storage.AutomationRun row shape.
func toStorageRun(r Run) storage.AutomationRun {
	started := r.StartedAt
	if started.IsZero() {
		started = time.Now().UTC()
	}
	row := storage.AutomationRun{
		ID:             r.ID,
		AutomationID:   r.AutomationID,
		Origin:         r.Origin,
		State:          string(r.State),
		StepIndex:      r.StepIndex,
		StepCount:      r.StepCount,
		SessionName:    r.SessionName,
		WorktreePath:   r.WorktreePath,
		WorktreeBranch: r.WorktreeBranch,
		ClaimToken:     r.ClaimToken,
		StartedAt:      started.Format(time.RFC3339),
		FailKind:       string(r.FailKind),
		Message:        r.Message,
	}
	if r.EndedAt != nil {
		s := r.EndedAt.Format(time.RFC3339)
		row.EndedAt = &s
	}
	return row
}

// fromStorageRun converts a storage.AutomationRun row back to a Run.
// Malformed timestamps (should never happen - this package is the only
// writer) fall back to the zero time rather than erroring the whole read,
// matching this package's own "a single bad row must not break listing"
// posture used nowhere else yet but consistent with LoadSpecs's per-entry
// strictness applying only to specs it authored, not raw storage rows.
func fromStorageRun(row storage.AutomationRun) Run {
	r := Run{
		ID:             row.ID,
		AutomationID:   row.AutomationID,
		Origin:         row.Origin,
		State:          RunState(row.State),
		StepIndex:      row.StepIndex,
		StepCount:      row.StepCount,
		SessionName:    row.SessionName,
		WorktreePath:   row.WorktreePath,
		WorktreeBranch: row.WorktreeBranch,
		ClaimToken:     row.ClaimToken,
		FailKind:       RunFailKind(row.FailKind),
		Message:        row.Message,
	}
	if t, err := time.Parse(time.RFC3339, row.StartedAt); err == nil {
		r.StartedAt = t
	}
	if row.EndedAt != nil {
		if t, err := time.Parse(time.RFC3339, *row.EndedAt); err == nil {
			r.EndedAt = &t
		}
	}
	return r
}

// createRun inserts a new run row. A nil db means run persistence is
// unconfigured: the caller (startRun, recordSkippedRun) gets the named
// errNoRunStore rather than a panic, and admission fails before any
// side effect.
func (s *Service) createRun(ctx context.Context, r Run) error {
	if s.db == nil {
		return errNoRunStore
	}
	if err := ValidateID(r.ID); err != nil {
		return err
	}
	if err := ValidateID(r.AutomationID); err != nil {
		return err
	}
	if r.State == "" {
		r.State = RunPending
	}
	if err := s.db.InsertAutomationRun(ctx, toStorageRun(r)); err != nil {
		return fmt.Errorf("automation: create run %q: %w", r.ID, err)
	}
	return nil
}

// ErrRunFenced is returned by updateRunStateFenced when the run row no
// longer carries the writer's claim token: another holder resumed the
// run, so this writer must not touch the row or publish its state.
var ErrRunFenced = errors.New("automation: run write fenced: claim token rotated")

// updateRunStateFenced transitions run's state, step_index, fail_kind,
// and message together in one write for a writer that owns a live
// claim: the UPDATE matches only while the row still carries
// run.ClaimToken. A rejected write is logged once here and reported as
// ErrRunFenced, so no caller publishes a state that was never stored.
// The sweep, which owns no claim, writes through interruptRunningRun.
func (s *Service) updateRunStateFenced(ctx context.Context, run Run, state RunState, stepIndex int, endedAt *time.Time, failKind RunFailKind, message string) error {
	if s.db == nil {
		return errNoRunStore
	}
	ok, err := s.db.UpdateAutomationRunStateFenced(ctx, run.ID, run.ClaimToken, string(state), stepIndex, rfc3339Ptr(endedAt), string(failKind), message)
	if err != nil {
		return fmt.Errorf("automation: update run %q: %w", run.ID, err)
	}
	if !ok {
		log.Printf("automation %q: run %q: %v", run.AutomationID, run.ID, ErrRunFenced)
		return fmt.Errorf("%w: run %q", ErrRunFenced, run.ID)
	}
	return nil
}

// ErrRunSettledElsewhere means the mid-run checkpoint found the row no
// longer running: a concurrent write - InterruptRunningAutomationRun
// (Close's crash-recovery path) or any other terminal write - already
// closed the run out between the step completing and this checkpoint.
// runSteps stops advancing and reports this distinct sentinel so the
// caller (endFailedRun) does nothing further: it must not publish
// RunRunning and must not write a terminal state on top of a row
// someone else already settled.
var ErrRunSettledElsewhere = errors.New("automation: run settled by a concurrent write")

// checkpointRunFenced persists the mid-run progress checkpoint
// (state=running, step_index=stepIndex) but only while the row is still
// running and still carries run.ClaimToken - narrower than
// updateRunStateFenced, which every OTHER writer here (succeedRun,
// endRun, executeResume's Running-transition write,
// markResumedRunSucceeded) still uses as-is, since those writes are
// themselves settling the row to a state and do not need this extra
// guard.
//
// A rejected write (0 rows matched) is one of two distinct cases, told
// apart by a follow-up read: the claim token no longer matches - another
// holder resumed the run - reports ErrRunFenced, exactly
// updateRunStateFenced's own pre-existing contract; the token still
// matches but the state has already moved off running - a concurrent
// InterruptRunningAutomationRun (Close's crash-recovery path), or any
// other terminal write, closed the row out between the step completing
// and this checkpoint - reports ErrRunSettledElsewhere instead. Without
// this added state guard the checkpoint's claim-token-only fence could
// overwrite an already-interrupted row back to running with a later
// step_index, resurrecting a row crash recovery already closed out.
func (s *Service) checkpointRunFenced(ctx context.Context, run Run, stepIndex int) error {
	if s.db == nil {
		return errNoRunStore
	}
	ok, err := s.db.UpdateAutomationRunStateFencedIfRunning(ctx, run.ID, run.ClaimToken, string(RunRunning), stepIndex, nil, string(RunFailNone), "")
	if err != nil {
		return fmt.Errorf("automation: checkpoint run %q: %w", run.ID, err)
	}
	if ok {
		return nil
	}
	stored, found, gerr := s.getRun(ctx, run.ID)
	if gerr != nil {
		return fmt.Errorf("automation: checkpoint run %q: reread after fenced write: %w", run.ID, gerr)
	}
	if found && stored.ClaimToken != run.ClaimToken {
		log.Printf("automation %q: run %q: %v", run.AutomationID, run.ID, ErrRunFenced)
		return fmt.Errorf("%w: run %q", ErrRunFenced, run.ID)
	}
	return ErrRunSettledElsewhere
}

// interruptRunningRun marks runID interrupted only while its row is
// still running, and keeps its step_index. It reports false when the
// run reached a terminal state first, so the caller publishes nothing.
func (s *Service) interruptRunningRun(ctx context.Context, runID string, endedAt time.Time, message string) (bool, error) {
	if s.db == nil {
		return false, errNoRunStore
	}
	ok, err := s.db.InterruptRunningAutomationRun(ctx, runID, endedAt.Format(time.RFC3339), message)
	if err != nil {
		return false, fmt.Errorf("automation: interrupt run %q: %w", runID, err)
	}
	return ok, nil
}

// rfc3339Ptr formats an optional terminal time for the store.
func rfc3339Ptr(t *time.Time) *string {
	if t == nil {
		return nil
	}
	v := t.Format(time.RFC3339)
	return &v
}

// getRun reads one run by id. A nil db reports not-found rather than
// erroring, so service.go's Run() stays empty when persistence is not
// configured.
func (s *Service) getRun(ctx context.Context, runID string) (Run, bool, error) {
	if s.db == nil {
		return Run{}, false, nil
	}
	row, ok, err := s.db.GetAutomationRun(ctx, runID)
	if err != nil {
		return Run{}, false, fmt.Errorf("automation: get run %q: %w", runID, err)
	}
	if !ok {
		return Run{}, false, nil
	}
	return fromStorageRun(row), true, nil
}

// listRuns reads automationID's runs, most-recently started first. A nil
// db returns an empty slice, so service.go's Runs() stays empty when
// persistence is not configured.
func (s *Service) listRuns(ctx context.Context, automationID string, limit int) ([]Run, error) {
	if s.db == nil {
		return nil, nil
	}
	rows, err := s.db.ListAutomationRuns(ctx, automationID, limit)
	if err != nil {
		return nil, fmt.Errorf("automation: list runs %q: %w", automationID, err)
	}
	out := make([]Run, 0, len(rows))
	for _, row := range rows {
		out = append(out, fromStorageRun(row))
	}
	return out, nil
}

// updateRunSession persists sessionName onto an existing run row - a
// follow-up write to a row createRun already inserted, needed because
// the session name is only known after the run's session is created.
func (s *Service) updateRunSession(ctx context.Context, runID, sessionName string) error {
	if s.db == nil {
		return errNoRunStore
	}
	if err := s.db.UpdateAutomationRunSession(ctx, runID, sessionName); err != nil {
		return fmt.Errorf("automation: update run session %q: %w", runID, err)
	}
	return nil
}

// updateRunClaimToken persists claimToken onto an existing run row - a
// follow-up write to a row createRun already inserted, needed because
// the claim token is only known after the run's claim is acquired.
func (s *Service) updateRunClaimToken(ctx context.Context, runID, claimToken string) error {
	if s.db == nil {
		return errNoRunStore
	}
	if err := s.db.UpdateAutomationRunClaimToken(ctx, runID, claimToken); err != nil {
		return fmt.Errorf("automation: update run claim token %q: %w", runID, err)
	}
	return nil
}
