package controller

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// transientFailRunner fails the first N calls with a transient provider error
// and then succeeds, recording every request so the test can assert the retry
// count and that each retry carries a fresh task identity.
type transientFailRunner struct {
	mu       sync.Mutex
	calls    []AgentStepRequest
	failures int
	err      error
}

func (r *transientFailRunner) RunStep(_ context.Context, req AgentStepRequest) (AgentStepResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, req)
	if len(r.calls) <= r.failures {
		return AgentStepResult{CoordinatorRunID: req.CoordinatorRunID, TaskID: req.TaskID, Status: "failed"}, r.err
	}
	return AgentStepResult{CoordinatorRunID: req.CoordinatorRunID, TaskID: req.TaskID, Status: "completed", Output: json.RawMessage(`{"ok":true}`)}, nil
}

// TestAgentStepRetriesTransientProviderError pins the step-level retry for
// transient LLM-provider failures (the zai 429 overload that killed workflow
// runs): the step re-runs with a fresh task identity and a fresh child
// context, and the attempt is persisted once, succeeded.
func TestAgentStepRetriesTransientProviderError(t *testing.T) {
	orig := stepTransientRetryBackoff
	stepTransientRetryBackoff = func(int) time.Duration { return 0 }
	t.Cleanup(func() { stepTransientRetryBackoff = orig })

	runner := &transientFailRunner{
		failures: 2,
		err:      errors.New("zai: provider error (HTTP 429, code 1305: service temporarily overloaded)"),
	}
	ctrl, repo := newErrorController(t, runner, "wfr-transient-retry")
	got, err := ctrl.Run(context.Background())
	if err != nil {
		t.Fatalf("run failed after transient retries: %v", err)
	}
	if got.Status != workflowledger.RunStatusSucceeded {
		t.Fatalf("status = %q, want succeeded", got.Status)
	}
	runner.mu.Lock()
	defer runner.mu.Unlock()
	if len(runner.calls) != 3 {
		t.Fatalf("RunStep calls = %d, want 3 (1 + 2 retries)", len(runner.calls))
	}
	ids := map[string]bool{}
	for _, call := range runner.calls {
		ids[call.TaskID] = true
	}
	if len(ids) != 3 {
		t.Fatalf("retries reused a task identity: %v", ids)
	}
	attempts, err := repo.ListStepAttempts(context.Background(), ctrl.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if len(attempts) != 1 || attempts[0].Status != workflowledger.AttemptStatusSucceeded {
		t.Fatalf("attempts = %+v, want one succeeded attempt (retries must not leak attempts)", attempts)
	}
}

// TestAgentStepDoesNotRetryNonTransientFailure pins the contract: a real
// agent failure (schema/binding/refusal) does not match the transient markers
// and is NOT retried — it fails immediately through on_failure.
func TestAgentStepDoesNotRetryNonTransientFailure(t *testing.T) {
	orig := stepTransientRetryBackoff
	stepTransientRetryBackoff = func(int) time.Duration { return 0 }
	t.Cleanup(func() { stepTransientRetryBackoff = orig })

	runner := &transientFailRunner{
		failures: 1,
		err:      errors.New("workflow step output schema validation failed"),
	}
	ctrl, _ := newErrorController(t, runner, "wfr-nontransient")
	got, err := ctrl.Run(context.Background())
	if err == nil {
		t.Fatalf("run succeeded = %+v; want failure", got)
	}
	if got.Status != workflowledger.RunStatusFailed {
		t.Fatalf("status = %q, want failed", got.Status)
	}
	runner.mu.Lock()
	defer runner.mu.Unlock()
	if len(runner.calls) != 1 {
		t.Fatalf("RunStep calls = %d, want exactly 1 (non-transient failures are not retried)", len(runner.calls))
	}
}
