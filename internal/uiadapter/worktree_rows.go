package uiadapter

import "github.com/MiviaLabs/mivia-agent/internal/uikit/ports"

// summariesFor resolves the row source for worktree-aware router consults.
func (r *CommandRunner) summariesFor() []ports.SessionSummary {
	if r.summariesFn != nil {
		rows, err := r.summariesFn()
		if err != nil {
			return nil
		}
		return rows
	}
	return r.worktreeRowsSafe()
}

// worktreeRowsSafe degrades listing errors to nil rows so a degraded store
// cannot make plain typed /resume worse than before.
func (r *CommandRunner) worktreeRowsSafe() []ports.SessionSummary {
	rows, err := r.listSessionSummaries()
	if err != nil {
		return nil
	}
	return rows
}

// SetSummariesFnForTest overrides the row source used by the router consult.
func (r *CommandRunner) SetSummariesFnForTest(f func() ([]ports.SessionSummary, error)) {
	r.summariesFn = f
}
