package cli

// resume_wrappers.go re-exports orchestration resume symbols from
// cliorchestrate so callers that import cli do not need a separate import.
// See cliorchestrate/resume.go for the authoritative implementations.

import (
	"context"

	cliorchestrate "github.com/MiviaLabs/mivia-agent/internal/cliorchestrate"
	"github.com/MiviaLabs/mivia-agent/internal/coordinator"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
)

// ResumeConfirmationInfo is the type alias for cliorchestrate.ResumeConfirmationInfo.
type ResumeConfirmationInfo = cliorchestrate.ResumeConfirmationInfo

// ErrOrchestrationSwitchActive re-exports the sentinel from cliorchestrate so
// callers that import cli can use errors.Is without an extra import.
// Invariant: both values are the same pointer; errors.Is works across the alias.
var ErrOrchestrationSwitchActive = cliorchestrate.ErrOrchestrationSwitchActive

// FindCoordinator delegates to cliorchestrate.FindCoordinator.
func FindCoordinator() coordinator.Coordinator {
	return cliorchestrate.FindCoordinator()
}

// FindDispatcher delegates to cliorchestrate.FindDispatcher.
func FindDispatcher() *runtime.Dispatcher {
	return cliorchestrate.FindDispatcher()
}

// ListInterruptedRuns delegates to cliorchestrate.ListInterruptedRuns.
func ListInterruptedRuns(ctx context.Context, c coordinator.Coordinator) ([]coordinator.RecoveredRun, error) {
	return cliorchestrate.ListInterruptedRuns(ctx, c)
}

// FormatListedRuns delegates to cliorchestrate.FormatListedRuns.
func FormatListedRuns(runs []coordinator.RecoveredRun) string {
	return cliorchestrate.FormatListedRuns(runs)
}

// FormatResumeConfirmation delegates to cliorchestrate.FormatResumeConfirmation.
func FormatResumeConfirmation(info ResumeConfirmationInfo) string {
	return cliorchestrate.FormatResumeConfirmation(info)
}

// ResumeRun delegates to cliorchestrate.ResumeRun.
func ResumeRun(ctx context.Context, c coordinator.Coordinator, d *runtime.Dispatcher, runID string, repo ledger.LedgerRepository) (*cliorchestrate.OrchestrationHandleForTest, error) {
	return cliorchestrate.ResumeRun(ctx, c, d, runID, repo)
}

// FormatResumeError delegates to cliorchestrate.FormatResumeError.
func FormatResumeError(err error, runID string) string {
	return cliorchestrate.FormatResumeError(err, runID)
}

// ParseConfirmResponse delegates to cliorchestrate.ParseConfirmResponse.
func ParseConfirmResponse(response string) bool {
	return cliorchestrate.ParseConfirmResponse(response)
}
