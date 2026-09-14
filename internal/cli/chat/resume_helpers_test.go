package chat

import (
	"github.com/MiviaLabs/mivia-agent/internal/cli/orchestrate"
)

// ResumeConfirmationInfo is re-exported from cliorchestrate for the resume
// slash tests that moved into this package.
type ResumeConfirmationInfo = orchestrate.ResumeConfirmationInfo

// FormatListedRuns is re-exported from cliorchestrate for tests.
var FormatListedRuns = orchestrate.FormatListedRuns

// FormatResumeConfirmation is re-exported from cliorchestrate for tests.
var FormatResumeConfirmation = orchestrate.FormatResumeConfirmation

// FormatResumeError is re-exported from cliorchestrate for tests.
var FormatResumeError = orchestrate.FormatResumeError

// ParseConfirmResponse is re-exported from cliorchestrate for tests.
var ParseConfirmResponse = orchestrate.ParseConfirmResponse

// FindCoordinator is re-exported from cliorchestrate for tests.
var FindCoordinator = orchestrate.FindCoordinator

// FindDispatcher is re-exported from cliorchestrate for tests.
var FindDispatcher = orchestrate.FindDispatcher

// ResumeRun is re-exported from cliorchestrate for tests.
var ResumeRun = orchestrate.ResumeRun

// ErrOrchestrationSwitchActive is re-exported from cliorchestrate for tests.
var ErrOrchestrationSwitchActive = orchestrate.ErrOrchestrationSwitchActive

// Coordinator is an alias kept for tests that reference the coordinator type:
// it names clichat's narrow chatCoordinator consumer view.
