package localengine

import (
	"context"

	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// EnsureRunWorktreeForTest exposes ensureRunWorktree for external
// tests. Local to the package so coverage can drive the
// no-workspace-root and recorded-snapshot branches.
func (e *Engine) EnsureRunWorktreeForTest(ctx context.Context, runID string, recorded *workflowledger.RunSnapshot) (Identity, bool) {
	return e.ensureRunWorktree(ctx, runID, recorded)
}

// ResumeExistingInvocationForTest exposes resumeExistingInvocation.
func (e *Engine) ResumeExistingInvocationForTest(ctx context.Context, run workflowledger.RunSnapshot, req workflowledger.StartRequest) (workflowledger.StartResult, bool, error) {
	return e.resumeExistingInvocation(ctx, run, req)
}

// SetActiveRunForTest marks a run as locally active so the
// resumeExistingInvocation short-circuit triggers.
func (e *Engine) SetActiveRunForTest(runID string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.active == nil {
		e.active = make(map[string]*activeRun)
	}
	e.active[runID] = &activeRun{cancel: func() {}, done: make(chan struct{})}
}
