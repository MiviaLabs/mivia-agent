package cli

// resume_wrappers.go re-exports orchestration resume symbols from
// cliorchestrate so callers that import cli do not need a separate import.
// See cliorchestrate/resume.go for the authoritative implementations.

import (
	"context"

	"github.com/MiviaLabs/mivia-agent/internal/cli/orchestrate"
	"github.com/MiviaLabs/mivia-agent/internal/coordinator"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
)

// ResumeConfirmationInfo is the type alias for orchestrate.ResumeConfirmationInfo.
type ResumeConfirmationInfo = orchestrate.ResumeConfirmationInfo

// ErrOrchestrationSwitchActive re-exports the sentinel from cliorchestrate so
// callers that import cli can use errors.Is without an extra import.
// Invariant: both values are the same pointer; errors.Is works across the alias.
var ErrOrchestrationSwitchActive = orchestrate.ErrOrchestrationSwitchActive

// ResumeCoordinator re-exports the cliorchestrate narrow resume coordinator
// interface so callers that import cli can name it.
type ResumeCoordinator = orchestrate.ResumeCoordinator

// FindCoordinator delegates to orchestrate.FindCoordinator.
func FindCoordinator() ResumeCoordinator {
	return orchestrate.FindCoordinator()
}

// FindDispatcher delegates to orchestrate.FindDispatcher.
func FindDispatcher() *runtime.Dispatcher {
	return orchestrate.FindDispatcher()
}

// ListInterruptedRuns delegates to orchestrate.ListInterruptedRuns.
func ListInterruptedRuns(ctx context.Context, c ResumeCoordinator) ([]coordinator.RecoveredRun, error) {
	return orchestrate.ListInterruptedRuns(ctx, c)
}

// FormatListedRuns delegates to orchestrate.FormatListedRuns.
func FormatListedRuns(runs []coordinator.RecoveredRun) string {
	return orchestrate.FormatListedRuns(runs)
}

// FormatResumeConfirmation delegates to orchestrate.FormatResumeConfirmation.
func FormatResumeConfirmation(info ResumeConfirmationInfo) string {
	return orchestrate.FormatResumeConfirmation(info)
}

// ResumeRun delegates to orchestrate.ResumeRun.
func ResumeRun(ctx context.Context, c OrchestrationCoordinator, d *runtime.Dispatcher, runID string, repo ledger.LedgerRepository) (*orchestrate.OrchestrationHandleForTest, error) {
	return orchestrate.ResumeRun(ctx, c, d, runID, repo)
}

// FormatResumeError delegates to orchestrate.FormatResumeError.
func FormatResumeError(err error, runID string) string {
	return orchestrate.FormatResumeError(err, runID)
}

// ParseConfirmResponse delegates to orchestrate.ParseConfirmResponse.
func ParseConfirmResponse(response string) bool {
	return orchestrate.ParseConfirmResponse(response)
}
