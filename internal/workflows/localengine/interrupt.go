package localengine

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/compiler"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/controller"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// Interrupt abandons an in-process controller as if the host process died:
// open attempts become interrupted, the run stays non-terminal (running),
// the claim is cleared, and the dying goroutine cannot settle the run.
func (e *Engine) Interrupt(runID string) error {
	if e == nil || e.Repo == nil {
		return fmt.Errorf("workflow engine is incomplete")
	}
	ctx := context.Background()
	// Fence terminal writes from the dying controller before mutating attempts.
	// Marking bumps the attempt version; an unfenced controller would settle the
	// run to failed on the version conflict before the fence exists.
	_ = e.ctrlRepo()
	e.mu.Lock()
	if resumeDone := e.resuming[runID]; resumeDone != nil {
		e.mu.Unlock()
		<-resumeDone
		return e.Interrupt(runID)
	}
	_, delivering := e.delivering[runID]
	if e.fence != nil {
		e.fence.abandon(runID)
	}
	active, ok := e.active[runID]
	if !ok && !delivering {
		e.mu.Unlock()
		return fmt.Errorf("workflow run %q is not active in this engine", runID)
	}
	if ok {
		if e.interrupting == nil {
			e.interrupting = make(map[string]uint)
		}
		e.interrupting[runID]++
	}
	e.mu.Unlock()
	if ok {
		defer e.finishInterrupt(runID)
	}
	// Mark open attempts interrupted before the dying controller can cancel them.
	if err := e.markOpenAttemptsInterrupted(ctx, runID); err != nil {
		if ok {
			active.cancel()
			<-active.done
		}
		return err
	}
	if ok {
		active.cancel()
		<-active.done
	}
	// Release the claim ONLY when this engine owns the controller (the run is
	// tracked in e.active): an abandoned controller's claim is the stale-owner
	// residue a resume must be able to claim over. A run that is mid-delivery
	// (this engine's delivery goroutine, or another host's publisher) or not
	// active here is left alone - clearing would strip a live delivery claim
	// and enable double-publish while the delivery keeps publishing.
	//
	// The release is holder-scoped to the interrupted controller's OWN holder:
	// the controller goroutine already released the claim (engine.go, before
	// close(done)), so a claim row still present here belongs either to this
	// controller (a failed release) or to a FOREIGN resume that won the claim
	// in the release-to-cleanup window. A holder-scoped ReleaseRun removes
	// only the former and never strips the latter - a blind ClearRunClaim
	// would delete a live foreign claim and neutralize the fence for the
	// foreign executor (or let a third executor double-run the step).
	if ok && !delivering {
		_ = e.Repo.ReleaseRun(ctx, runID, active.ctrl.Holder)
	}
	run, err := e.Repo.GetRun(ctx, runID)
	if err != nil {
		return err
	}
	if workflowledger.IsTerminalRunStatus(run.Status) {
		return fmt.Errorf("interrupt left run %q terminal (%s); want non-terminal for resume", runID, run.Status)
	}
	return nil
}

func (e *Engine) finishInterrupt(runID string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.interrupting[runID] > 1 {
		e.interrupting[runID]--
		return
	}
	delete(e.interrupting, runID)
}

func (e *Engine) markOpenAttemptsInterrupted(ctx context.Context, runID string) error {
	attempts, err := e.Repo.ListStepAttempts(ctx, runID)
	if err != nil {
		return err
	}
	for _, attempt := range attempts {
		if workflowledger.IsTerminalAttemptStatus(attempt.Status) {
			continue
		}
		if err := e.Repo.CompleteStepAttempt(ctx, runID, attempt.AttemptID, attempt.Version, workflowledger.AttemptOutcome{
			Status: workflowledger.AttemptStatusInterrupted,
		}); err != nil {
			return err
		}
	}
	return nil
}

func (e *Engine) runner() controller.AgentStepRunner {
	if e.NewRunner != nil {
		return e.NewRunner()
	}
	// Fail closed: never invent successful agent output.
	return &StaticStepRunner{Err: fmt.Errorf("workflow agent step runner is not configured")}
}

func (e *Engine) newRunID() string {
	if e.NewRunID != nil {
		return e.NewRunID()
	}
	return "wfr-" + randomToken(10)
}

func (e *Engine) loadWorkflow(name string) (*compiler.CompiledWorkflow, []byte, string, error) {
	if strings.TrimSpace(name) == "" {
		return nil, nil, "", fmt.Errorf("workflow name is required")
	}
	root := e.WorkspaceRoot
	if root == "" {
		root = "."
	}
	workflows, err := definition.DiscoverWorkflows(root)
	if err != nil {
		return nil, nil, "", err
	}
	var found *definition.DiscoveredWorkflow
	for i := range workflows {
		if workflows[i].Name == name {
			found = &workflows[i]
			break
		}
	}
	if found == nil {
		return nil, nil, "", fmt.Errorf("workflow %q was not found", name)
	}
	wf, _, err := definition.ParseWorkflowTOML(found.Raw, found.Name+".toml")
	if err != nil {
		return nil, nil, "", err
	}
	compiled, err := compiler.Compile(&wf)
	if err != nil {
		return nil, nil, "", err
	}
	return compiled, found.Raw, filepath.Dir(found.Path), nil
}
