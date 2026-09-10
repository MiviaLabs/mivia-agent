// Package automation owns user-defined automations: their TOML
// definitions, their schedules, and their durable run records.
//
// This file (runstore.go) is Chunk 5's run-lifecycle data layer: Go
// read/write helpers over storage.SQLite's automation_runs table
// (D1/D7/D13, docs/design/automations.md). It builds no session, no
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
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/storage"
)

// RunState is this package's internal run-lifecycle vocabulary, mirroring
// ports.RunState's values exactly (including RunInterrupted, D13) without
// importing ports into the storage round trip itself - runstore.go stays
// storage-shape-first; service.go's mapper (below, and in service.go)
// converts to/from ports.RunState at the Service boundary, the same
// pattern chunk 2 established for StepKind/TriggerKind/ScheduleKind.
type RunState string

const (
	RunPending     RunState = "pending"
	RunRunning     RunState = "running"
	RunSucceeded   RunState = "succeeded"
	RunFailed      RunState = "failed"
	RunCancelled   RunState = "cancelled"
	RunInterrupted RunState = "interrupted"
	// RunSkipped marks a fire that lost the fenced single-fire claim
	// (D7): another fire already owns the automation's in-flight run,
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
// (createRun's would-be callers in chunk 6, getRun/listRuns via
// service.go's Run/Runs) branch on s.db == nil themselves rather than on
// this error, since a nil db is a configuration fact, not a failure.
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

// createRun inserts a new run row. A nil db is not an error: it means run
// persistence is unconfigured, so the caller (chunk 6's executor) gets a
// named error it can log and treat as "the run still happened, but was
// not durably recorded" rather than a panic.
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

// updateRunState transitions runID's state, step_index, fail_kind, and
// message together in one write. endedAt is nil for a non-terminal
// transition (e.g. pending->running) and set for a terminal one
// (succeeded/failed/cancelled/interrupted).
func (s *Service) updateRunState(ctx context.Context, runID string, state RunState, stepIndex int, endedAt *time.Time, failKind RunFailKind, message string) error {
	if s.db == nil {
		return errNoRunStore
	}
	var endedStr *string
	if endedAt != nil {
		v := endedAt.Format(time.RFC3339)
		endedStr = &v
	}
	if err := s.db.UpdateAutomationRunState(ctx, runID, string(state), stepIndex, endedStr, string(failKind), message); err != nil {
		if errors.Is(err, storage.ErrAutomationRunNotFound) {
			return fmt.Errorf("automation: update run %q: %w", runID, err)
		}
		return fmt.Errorf("automation: update run %q: %w", runID, err)
	}
	return nil
}

// getRun reads one run by id. A nil db reports not-found rather than
// erroring, matching service.go's pre-chunk-5 Run() behavior for callers
// that have not yet checked whether persistence is configured.
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
// db returns an empty slice, matching service.go's pre-chunk-5 Runs()
// behavior.
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
