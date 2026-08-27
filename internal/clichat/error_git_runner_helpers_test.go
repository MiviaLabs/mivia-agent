package clichat

// errorGitRunner fails every git invocation. Duplicated from
// internal/cliworkflow (stack_drive_completed_test.go).

import (
	"context"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/delivery"
)

type errorGitRunner struct{ err error }

// Run fails every git invocation with the injected error.
func (g errorGitRunner) Run(context.Context, delivery.GitContext, ...string) (string, error) {
	return "", g.err
}
