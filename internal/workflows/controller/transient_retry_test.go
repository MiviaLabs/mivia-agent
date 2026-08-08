package controller

import (
	"context"
	"errors"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/compiler"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// A transport fault must repeat the step, not end the run.
//
// This is the failure that ended three runs in one afternoon: a truncated
// body, a torn HTTP/2 stream, a reset connection. Each ended a run that had
// already finished a dozen steps, because every failure took one path.
func TestTransportFaultRetriesTheStep(t *testing.T) {
	ctx := context.Background()
	c := newTransientFixture(t)
	step := definition.Step{ID: "review", Kind: "agent"}

	cause := &provider.TransientError{Err: errors.New("openrouter: decode response: unexpected end of JSON input")}
	if !c.retryStepAfterTransient(ctx, step, cause) {
		t.Fatal("a transport fault must retry the step, not end the run")
	}
}

// An answer the caller can judge is a real outcome and must not be retried
// silently: it takes the on_failure route so the workflow decides.
func TestAnUnusableAnswerIsNotRetried(t *testing.T) {
	ctx := context.Background()
	c := newTransientFixture(t)
	step := definition.Step{ID: "review", Kind: "agent"}

	if c.retryStepAfterTransient(ctx, step, errors.New("output does not match schema: missing field \"verdict\"")) {
		t.Fatal("a schema violation is a real outcome; it must not be retried as a transport fault")
	}
}

// A cancelled run must not be revived by the retry.
func TestCancellationIsNotATransportFault(t *testing.T) {
	ctx := context.Background()
	c := newTransientFixture(t)
	step := definition.Step{ID: "review", Kind: "agent"}

	if c.retryStepAfterTransient(ctx, step, context.Canceled) {
		t.Fatal("a cancelled call must not be retried")
	}
}

// The retry is bounded: a provider that is down must not spin forever.
func TestTransportRetriesAreBounded(t *testing.T) {
	ctx := context.Background()
	c := newTransientFixture(t)
	c.Workflow.Limits.MaxTransientRetries = 2
	step := definition.Step{ID: "review", Kind: "agent"}
	cause := &provider.TransientError{Err: errors.New("stream error: INTERNAL_ERROR; received from peer")}

	for i := 0; i < 2; i++ {
		if !c.retryStepAfterTransient(ctx, step, cause) {
			t.Fatalf("retry %d refused; the budget is 2", i+1)
		}
		recordTransientFailure(t, ctx, c, step.ID, i+1, cause)
	}
	if c.retryStepAfterTransient(ctx, step, cause) {
		t.Fatal("the third try must be refused: the budget is spent")
	}
}

// Progress resets the count. A step that fails on transport, succeeds, then
// fails on transport again is not stuck.
func TestASucceededAttemptResetsTheTransportCount(t *testing.T) {
	ctx := context.Background()
	c := newTransientFixture(t)
	c.Workflow.Limits.MaxTransientRetries = 1
	step := definition.Step{ID: "review", Kind: "agent"}
	cause := &provider.TransientError{Err: errors.New("connection reset by peer")}

	recordTransientFailure(t, ctx, c, step.ID, 1, cause)
	recordSucceededAttempt(t, ctx, c, step.ID, 2)

	if !c.retryStepAfterTransient(ctx, step, cause) {
		t.Fatal("a success between faults must reset the count")
	}
}

func newTransientFixture(t *testing.T) *LinearController {
	t.Helper()
	repo := workflowledger.NewMemoryRepository()
	t.Cleanup(func() { _ = repo.Close() })
	run := workflowledger.RunSnapshot{RunID: "wfr-transient", Status: workflowledger.RunStatusPending, ActiveStepID: "review"}
	if err := repo.CreateRun(context.Background(), run, []byte("{}")); err != nil {
		t.Fatal(err)
	}
	wf := &compiler.CompiledWorkflow{Name: "t", Steps: []definition.Step{{ID: "review", Kind: "agent"}}}
	return &LinearController{RunID: run.RunID, Repo: repo, Workflow: wf}
}

func recordTransientFailure(t *testing.T, ctx context.Context, c *LinearController, stepID string, no int, cause error) {
	t.Helper()
	id := attemptIDFor(stepID, no)
	if err := c.Repo.CreateStepAttempt(ctx, workflowledger.StepAttempt{
		RunID: c.RunID, AttemptID: id, StepID: stepID, AttemptNo: no,
		Status: workflowledger.AttemptStatusRunning,
	}); err != nil {
		t.Fatal(err)
	}
	data := []byte(cause.Error())
	ref := "sha256:" + workflowledger.DigestHex(data)
	if err := c.Repo.StoreContent(ctx, ref, data); err != nil {
		t.Fatal(err)
	}
	fresh, err := c.Repo.GetStepAttempt(ctx, c.RunID, id)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Repo.CompleteStepAttempt(ctx, c.RunID, id, fresh.Version, workflowledger.AttemptOutcome{
		Status: workflowledger.AttemptStatusFailed, ErrorRef: ref, ToStepID: stepID,
	}); err != nil {
		t.Fatal(err)
	}
}

func recordSucceededAttempt(t *testing.T, ctx context.Context, c *LinearController, stepID string, no int) {
	t.Helper()
	id := attemptIDFor(stepID, no)
	if err := c.Repo.CreateStepAttempt(ctx, workflowledger.StepAttempt{
		RunID: c.RunID, AttemptID: id, StepID: stepID, AttemptNo: no,
		Status: workflowledger.AttemptStatusRunning,
	}); err != nil {
		t.Fatal(err)
	}
	data := []byte(`{"ok":true}`)
	ref := "sha256:" + workflowledger.DigestHex(data)
	if err := c.Repo.StoreContent(ctx, ref, data); err != nil {
		t.Fatal(err)
	}
	fresh, err := c.Repo.GetStepAttempt(ctx, c.RunID, id)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Repo.CompleteStepAttempt(ctx, c.RunID, id, fresh.Version, workflowledger.AttemptOutcome{
		Status: workflowledger.AttemptStatusSucceeded, OutputRef: ref, ToStepID: "next",
	}); err != nil {
		t.Fatal(err)
	}
}

func attemptIDFor(stepID string, no int) string {
	return "wfa-" + stepID + "-" + string(rune('0'+no))
}
