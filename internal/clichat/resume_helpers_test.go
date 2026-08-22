package clichat

import (
	cliorchestrate "github.com/MiviaLabs/mivia-agent/internal/cliorchestrate"
	coordinator "github.com/MiviaLabs/mivia-agent/internal/coordinator"
)

// ResumeConfirmationInfo is re-exported from cliorchestrate for the resume
// slash tests that moved into this package.
type ResumeConfirmationInfo = cliorchestrate.ResumeConfirmationInfo

// FormatListedRuns is re-exported from cliorchestrate for tests.
var FormatListedRuns = cliorchestrate.FormatListedRuns

// FormatResumeConfirmation is re-exported from cliorchestrate for tests.
var FormatResumeConfirmation = cliorchestrate.FormatResumeConfirmation

// FormatResumeError is re-exported from cliorchestrate for tests.
var FormatResumeError = cliorchestrate.FormatResumeError

// ParseConfirmResponse is re-exported from cliorchestrate for tests.
var ParseConfirmResponse = cliorchestrate.ParseConfirmResponse

// FindCoordinator is re-exported from cliorchestrate for tests.
var FindCoordinator = cliorchestrate.FindCoordinator

// FindDispatcher is re-exported from cliorchestrate for tests.
var FindDispatcher = cliorchestrate.FindDispatcher

// ResumeRun is re-exported from cliorchestrate for tests.
var ResumeRun = cliorchestrate.ResumeRun

// ErrOrchestrationSwitchActive is re-exported from cliorchestrate for tests.
var ErrOrchestrationSwitchActive = cliorchestrate.ErrOrchestrationSwitchActive

// Coordinator is an alias kept for tests that reference the coordinator type.
type Coordinator = coordinator.Coordinator
