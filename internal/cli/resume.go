package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/coordinator"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
)

// resumeConfirmationInfo holds the information displayed before confirming
// a resume action, per §5 decision ii (show what will re-run and confirm).
type resumeConfirmationInfo struct {
	RunID         string
	DisplayName   string
	TaskCount     int
	PriorAttempts int // total attempt count across all tasks
}

// listInterruptedRuns returns the list of interrupted runs from the coordinator.
// Used by both the slash command (no argument) and the TUI dashboard.
func listInterruptedRuns(ctx context.Context, c coordinator.Coordinator) ([]coordinator.RecoveredRun, error) {
	return c.ListInterruptedRuns(ctx)
}

// resumeRun is the shared resume implementation behind the /resume slash
// command. It:
//  1. Calls ResumeInterruptedRun on the coordinator
//  2. Registers the resumed handle with the resuming caller's principal (§3.2)
//  3. Returns the orchestrationHandle for further use
//
// The caller and dispatcher/repo parameters are needed for handle registration.
// If c is nil, the function looks up the coordinator from the package-level map.
// If d is nil, looks up a dispatcher from the coordinator map.
func resumeRun(ctx context.Context, c coordinator.Coordinator, d *runtime.Dispatcher, runID string, repo ledger.LedgerRepository) (*orchestrationHandle, error) {
	if c == nil {
		if c = findCoordinator(); c == nil {
			return nil, errors.New("no active coordinator (no orchestration runs exist)")
		}
	}
	if d == nil {
		d = findDispatcher()
	}
	if repo == nil {
		repo = orchestrationRepoForDispatcher(d)
	}

	// Resolve the resuming caller BEFORE starting any work. A resumed run that
	// nobody can inspect or cancel is worse than a refused resume, so fail
	// closed rather than register it under an identity no session holds.
	ctx = sessionCallerContext(ctx)
	principal, ok := principalFromContext(ctx)
	if !ok {
		return nil, errors.New("no chat session identity available; a resumed run would not be inspectable or cancellable")
	}

	handle, err := c.ResumeInterruptedRun(ctx, runID)
	if err != nil {
		return nil, err
	}

	record := &orchestrationHandle{
		coord:      c,
		handle:     handle,
		repo:       effectiveOrchestrationRepo(repo),
		dispatcher: d,
		principal:  principal,
		retention:  defaultHandleRetention,
	}

	// Prefer the snapshot's run id as the key, but never leave a resumed run
	// unregistered. It is already executing and holding an execution claim, so
	// an Inspect failure must not strand it beyond the user's reach.
	key := runID
	if snap, inspectErr := c.Inspect(ctx, handle); inspectErr == nil && snap.RunID != "" {
		key = snap.RunID
	}

	// Overwrite rather than LoadOrStore: storeOrchestrationHandle deliberately
	// retains the original owner on a repeat key, and a resumed handle must be
	// owned by the resuming caller (§3.2).
	runHandles.Delete(runID)
	if key != runID {
		runHandles.Delete(key)
	}
	if d != nil {
		storeOrchestrationHandle(key, record)
	} else {
		// No dispatcher means no close hook to clean up; the handle is still
		// registered so the run stays reachable.
		runHandles.Store(key, record)
	}

	return record, nil
}

// formatResumeConfirmation builds the confirmation message shown to the user
// before re-spending budget on a resume, per §5 decision ii.
func formatResumeConfirmation(info resumeConfirmationInfo) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("Resume run %s", info.RunID))
	if info.DisplayName != "" {
		b.WriteString(fmt.Sprintf(" (%s)", info.DisplayName))
	}
	b.WriteString(":\n")
	if info.TaskCount > 0 {
		b.WriteString(fmt.Sprintf("  • %d tasks will re-execute\n", info.TaskCount))
	} else {
		b.WriteString("  • pending tasks will re-execute\n")
	}
	if info.PriorAttempts > 0 {
		b.WriteString(fmt.Sprintf("  • %d prior attempt(s) across tasks\n", info.PriorAttempts))
	}
	b.WriteString("This will re-spend model budget on work that previously ran.\n")
	b.WriteString("Resume? (y/N) ")
	return b.String()
}

// formatResumeError maps different resume errors to user-facing messages.
// Three distinct causes, three distinct messages (§3.3):
//  1. Held by another executor
//  2. Already terminal
//  3. Cannot be resumed (missing Input)
func formatResumeError(err error, runID string) string {
	if errors.Is(err, coordinator.ErrRunHeldByAnotherExecutor) {
		return fmt.Sprintf("cannot resume run %s: held by another executor", runID)
	}
	msg := err.Error()
	if strings.Contains(msg, "is already terminal") {
		return fmt.Sprintf("cannot resume run %s: already terminal", runID)
	}
	if strings.Contains(msg, "no persisted input") {
		return fmt.Sprintf("cannot resume run %s: cannot be resumed (missing task input data)", runID)
	}
	return fmt.Sprintf("cannot resume run %s: %v", runID, err)
}

// formatListedRuns formats a list of interrupted runs for display.
func formatListedRuns(runs []coordinator.RecoveredRun) string {
	if len(runs) == 0 {
		return "no interrupted runs"
	}
	now := time.Now()
	var b strings.Builder
	b.WriteString("Interrupted runs:\n")
	for _, r := range runs {
		b.WriteString(fmt.Sprintf("  %s", r.RunID))
		if r.DisplayName != "" {
			b.WriteString(fmt.Sprintf(" (%s)", r.DisplayName))
		}
		// Age matters here: startup only announces recent interruptions, so this
		// listing is where a long-abandoned run surfaces, and it must be
		// recognisable as one rather than as something that just broke.
		b.WriteString(fmt.Sprintf(" · %s", formatRunAge(r.CreatedAt, now)))
		if r.HeldByAnotherExecutor {
			b.WriteString(" [held by another process]")
		}
		b.WriteString("\n")
	}
	b.WriteString("Usage: /resume <run-id>")
	return b.String()
}

// parseConfirmResponse returns true if the user confirmed the resume.
func parseConfirmResponse(response string) bool {
	switch strings.ToLower(strings.TrimSpace(response)) {
	case "y", "yes":
		return true
	default:
		return false
	}
}

// findCoordinator looks up the package-level coordinator singleton.
func findCoordinator() coordinator.Coordinator {
	var c coordinator.Coordinator
	coordinators.Range(func(_, value any) bool {
		if coord, ok := value.(coordinator.Coordinator); ok {
			c = coord
			return false
		}
		return true
	})
	return c
}

// findDispatcher looks up a dispatcher from the coordinator map.
func findDispatcher() *runtime.Dispatcher {
	var d *runtime.Dispatcher
	coordinators.Range(func(key, _ any) bool {
		if disp, ok := key.(*runtime.Dispatcher); ok {
			d = disp
			return false
		}
		return true
	})
	return d
}
