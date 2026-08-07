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
	if e.fence != nil {
		e.fence.abandon(runID)
	}
	e.mu.Unlock()
	// Mark open attempts interrupted before the dying controller can cancel them.
	if err := e.markOpenAttemptsInterrupted(ctx, runID); err != nil {
		return err
	}
	e.mu.Lock()
	active, ok := e.active[runID]
	if ok {
		delete(e.active, runID)
	}
	e.mu.Unlock()
	_ = e.Repo.ClearRunClaim(ctx, runID)
	if ok {
		active.cancel()
		<-active.done
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
