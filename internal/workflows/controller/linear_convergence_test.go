package controller

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// --- workflow-convergence plan v3: latest-output-attempt resolution ---

// latestOutputHarness stores one review output artifact and returns a
// controller plus a review step whose context binds the review step's OWN
// prior output (prior_findings), the shape used by review templates.
func latestOutputHarness(t *testing.T) (*LinearController, definition.Step, string) {
	t.Helper()
	ctx := context.Background()
	repo := workflowledger.NewMemoryRepository()
	output := []byte(`{"verdict":"changes_requested","findings":[{"id":"R0-f1"}]}`)
	ref := "sha256:" + workflowledger.DigestHex(output)
	if err := repo.StoreContent(ctx, ref, output); err != nil {
		t.Fatal(err)
	}
	ctrl, err := NewLinearController(repo, &linearRunner{}, linearWorkflow(t), nil, nil, "wfr-latest-output", []byte("snapshot"))
	if err != nil {
		t.Fatal(err)
	}
	step := definition.Step{ID: "review", Kind: "agent_gate", Context: []definition.ContextBinding{
		{From: "steps.review.output", As: "prior_findings", Optional: true},
	}}
	return ctrl, step, ref
}

// TestLatestOutputAttemptSkipsInFlightWithoutOutputRef pins that a steps.X.output
// binding resolves to the LATEST attempt of X with a non-empty OutputRef: the
// review step's own in-flight attempt (Running, no OutputRef yet) must not
// shadow the previous COMPLETED review's output.
func TestLatestOutputAttemptSkipsInFlightWithoutOutputRef(t *testing.T) {
	ctx := context.Background()
	ctrl, step, ref := latestOutputHarness(t)
	attempts := []workflowledger.StepAttempt{
		{AttemptID: "wfa-review-1", StepID: "review", AttemptNo: 1, Status: workflowledger.AttemptStatusSucceeded, OutputRef: ref},
		{AttemptID: "wfa-review-2", StepID: "review", AttemptNo: 2, Status: workflowledger.AttemptStatusRunning},
	}
	_, evidence, refs, err := ctrl.contextForStep(ctx, step, attempts)
	if err != nil {
		t.Fatalf("in-flight attempt without output must not break the self-binding: %v", err)
	}
	if evidence["prior_findings"] == nil {
		t.Fatal("prior_findings must resolve to the previous completed review")
	}
	entry, ok := refs["prior_findings"]
	if !ok {
		t.Fatal("refs missing prior_findings")
	}
	if entry.Step != "review" || entry.Attempt != 1 || entry.Ref != ref {
		t.Fatalf("prior_findings ref = %+v, want attempt 1 of review", entry)
	}
}

// TestLatestOutputAttemptSkipsFailedAttemptWithoutOutput pins that a failed
// attempt WITHOUT output must not shadow a prior successful output.
func TestLatestOutputAttemptSkipsFailedAttemptWithoutOutput(t *testing.T) {
	ctx := context.Background()
	ctrl, step, ref := latestOutputHarness(t)
	attempts := []workflowledger.StepAttempt{
		{AttemptID: "wfa-review-1", StepID: "review", AttemptNo: 1, Status: workflowledger.AttemptStatusSucceeded, OutputRef: ref},
		{AttemptID: "wfa-review-2", StepID: "review", AttemptNo: 2, Status: workflowledger.AttemptStatusFailed},
	}
	_, evidence, refs, err := ctrl.contextForStep(ctx, step, attempts)
	if err != nil {
		t.Fatalf("failed attempt without output must not break the self-binding: %v", err)
	}
	if evidence["prior_findings"] == nil {
		t.Fatal("prior_findings must resolve to the prior successful output")
	}
	entry, ok := refs["prior_findings"]
	if !ok {
		t.Fatal("refs missing prior_findings")
	}
	if entry.Attempt != 1 {
		t.Fatalf("prior_findings ref = %+v, want attempt 1 of review", entry)
	}
}

// TestLatestOutputAttemptKeepsOptionalAbsentEmpty pins that a missing prior
// output (no attempt with an OutputRef) still resolves an OPTIONAL binding to
// an empty string, with no artifact ref.
func TestLatestOutputAttemptKeepsOptionalAbsentEmpty(t *testing.T) {
	ctx := context.Background()
	ctrl, step, _ := latestOutputHarness(t)
	attempts := []workflowledger.StepAttempt{
		{AttemptID: "wfa-review-1", StepID: "review", AttemptNo: 1, Status: workflowledger.AttemptStatusRunning},
	}
	_, evidence, refs, err := ctrl.contextForStep(ctx, step, attempts)
	if err != nil {
		t.Fatalf("optional absent binding must resolve to empty: %v", err)
	}
	if got, ok := evidence["prior_findings"].(string); !ok || got != "" {
		t.Fatalf("optional absent prior_findings = %#v, want empty string", evidence["prior_findings"])
	}
	if _, ok := refs["prior_findings"]; ok {
		t.Fatal("refs must not address an absent optional binding")
	}
}

// TestNextAttemptNoUsesLatestAttemptRegardlessOfOutput pins that attempt
// NUMBERING keeps using the latest attempt even when that attempt has no
// OutputRef: latestOutputAttempt only replaces output-binding resolution.
func TestNextAttemptNoUsesLatestAttemptRegardlessOfOutput(t *testing.T) {
	attempts := []workflowledger.StepAttempt{
		{AttemptID: "wfa-review-1", StepID: "review", AttemptNo: 1, Status: workflowledger.AttemptStatusSucceeded, OutputRef: "sha256:one"},
		{AttemptID: "wfa-review-2", StepID: "review", AttemptNo: 2, Status: workflowledger.AttemptStatusRunning},
	}
	if got := nextAttemptNo(attempts, "review"); got != 3 {
		t.Fatalf("nextAttemptNo = %d, want 3 (latest attempt numbering, not latest output)", got)
	}
}

// --- workflow-convergence plan v3: inputs.round injection ---

// roundController builds the repair review-loop workflow with the loop
// counter seeded on demand by the caller.
func roundController(t *testing.T) (*LinearController, workflowledger.Repository) {
	t.Helper()
	ctx := context.Background()
	wf := repairWorkflow(t, -1, 16)
	repo := workflowledger.NewMemoryRepository()
	ctrl, err := NewLinearController(repo, &linearRunner{}, wf, map[string]StepRuntime{
		"implement": {Agent: agents.ResolvedAgent{Name: "dev"}},
		"review":    {Agent: agents.ResolvedAgent{Name: "rev"}},
	}, map[string]any{"task": "x"}, "wfr-round-inject", []byte("snap"))
	if err != nil {
		t.Fatal(err)
	}
	if err := ctrl.Start(ctx); err != nil {
		t.Fatal(err)
	}
	return ctrl, repo
}

func roundAttempt(t *testing.T, ctx context.Context, repo workflowledger.Repository, runID, stepID string, no int) workflowledger.StepAttempt {
	t.Helper()
	attempt := workflowledger.StepAttempt{
		AttemptID: fmt.Sprintf("wfa-%s-%d", stepID, no), RunID: runID, StepID: stepID, AttemptNo: no,
		Status: workflowledger.AttemptStatusRunning, CoordinatorRunID: "coord-" + stepID, TaskID: "task-" + stepID,
	}
	if err := repo.CreateStepAttempt(ctx, attempt); err != nil {
		t.Fatal(err)
	}
	return attempt
}

// TestAgentStepRequestInjectsRoundOnFirstLoopIteration pins that a step whose
// own outgoing transition is a loop back-edge receives inputs.round = 0
// before the first back-edge (no loop counter recorded yet).
func TestAgentStepRequestInjectsRoundOnFirstLoopIteration(t *testing.T) {
	ctx := context.Background()
	ctrl, repo := roundController(t)
	step, ok := ctrl.WorkflowStep("review")
	if !ok {
		t.Fatal("review step missing")
	}
	attempt := roundAttempt(t, ctx, repo, ctrl.RunID, "review", 1)
	req, err := ctrl.agentStepRequest(ctx, workflowledger.RunSnapshot{RunID: ctrl.RunID}, step, StepRuntime{}, attempt, nil)
	if err != nil {
		t.Fatal(err)
	}
	round, ok := req.Inputs["round"].(int)
	if !ok || round != 0 {
		t.Fatalf("round = %#v (%T), want int 0 on the first loop iteration", req.Inputs["round"], req.Inputs["round"])
	}
}

// TestAgentStepRequestInjectsRoundFromLoopCounter pins that inputs.round
// equals the step's loop iteration counter read from GetLoopCounters.
func TestAgentStepRequestInjectsRoundFromLoopCounter(t *testing.T) {
	ctx := context.Background()
	ctrl, repo := roundController(t)
	if _, err := repo.IncrementLoopCounter(ctx, ctrl.RunID, "review_repair"); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.IncrementLoopCounter(ctx, ctrl.RunID, "review_repair"); err != nil {
		t.Fatal(err)
	}
	step, ok := ctrl.WorkflowStep("review")
	if !ok {
		t.Fatal("review step missing")
	}
	attempt := roundAttempt(t, ctx, repo, ctrl.RunID, "review", 3)
	req, err := ctrl.agentStepRequest(ctx, workflowledger.RunSnapshot{RunID: ctrl.RunID}, step, StepRuntime{}, attempt, nil)
	if err != nil {
		t.Fatal(err)
	}
	round, ok := req.Inputs["round"].(int)
	if !ok || round != 2 {
		t.Fatalf("round = %#v (%T), want int 2 from the loop counter", req.Inputs["round"], req.Inputs["round"])
	}
}

// TestAgentStepRequestOmitsRoundOutsideLoop pins that a step with no loop
// back-edge (the implement step) does NOT receive the synthetic round input.
func TestAgentStepRequestOmitsRoundOutsideLoop(t *testing.T) {
	ctx := context.Background()
	ctrl, repo := roundController(t)
	step, ok := ctrl.WorkflowStep("implement")
	if !ok {
		t.Fatal("implement step missing")
	}
	attempt := roundAttempt(t, ctx, repo, ctrl.RunID, "implement", 1)
	req, err := ctrl.agentStepRequest(ctx, workflowledger.RunSnapshot{RunID: ctrl.RunID}, step, StepRuntime{}, attempt, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := req.Inputs["round"]; ok {
		t.Fatalf("round injected for a step outside a loop: %#v", req.Inputs)
	}
}

// --- workflow-convergence plan v3: zero-progress review check ---

// TestReviewZeroProgressFailsRunOnIdenticalFindings pins the zero-progress
// gate: when an agent_gate review succeeds with verdict changes_requested and
// its normalized finding-id set equals the review's OWN previous completed
// output (ids differ only by the R<digits>- round prefix), the controller
// must NOT take the loop back-edge: the attempt fails with the durable
// zero-progress cause and the run stops.
func TestReviewZeroProgressFailsRunOnIdenticalFindings(t *testing.T) {
	wf := repairWorkflow(t, -1, 16)
	runner := &scriptedRunner{outputsByStepCall: map[string]json.RawMessage{
		"implement#1": json.RawMessage(`{"summary":"v1"}`),
		"review#1":    json.RawMessage(`{"verdict":"changes_requested","findings":[{"id":"R0-f1","severity":"high","reason":"x"}]}`),
		"implement#2": json.RawMessage(`{"summary":"v2"}`),
		"review#2":    json.RawMessage(`{"verdict":"changes_requested","findings":[{"id":"R1-f1","severity":"high","reason":"x"}]}`),
	}}
	repo := workflowledger.NewMemoryRepository()
	ctrl, err := NewLinearController(repo, runner, wf, map[string]StepRuntime{
		"implement": {Agent: agents.ResolvedAgent{Name: "dev"}},
		"review":    {Agent: agents.ResolvedAgent{Name: "rev"}},
	}, map[string]any{"task": "x"}, "wfr-zero-progress", []byte("snap"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := ctrl.Run(context.Background())
	if err == nil || got.Status != workflowledger.RunStatusFailed {
		t.Fatalf("run = %+v err=%v; want failed on identical findings", got, err)
	}
	if !strings.Contains(err.Error(), "review made no progress across rounds") {
		t.Fatalf("err = %v, want the zero-progress cause", err)
	}
	attempts, listErr := repo.ListStepAttempts(context.Background(), ctrl.RunID)
	if listErr != nil {
		t.Fatal(listErr)
	}
	var secondReview workflowledger.StepAttempt
	for _, a := range attempts {
		if a.StepID == "review" && a.AttemptNo == 2 {
			secondReview = a
		}
	}
	if secondReview.Status != workflowledger.AttemptStatusFailed {
		t.Fatalf("review#2 status = %q, want failed", secondReview.Status)
	}
	if secondReview.ToStepID != "failure" {
		t.Fatalf("review#2 route = %q, want failure (no loop back-edge)", secondReview.ToStepID)
	}
	if secondReview.ErrorRef == "" {
		t.Fatal("review#2 must carry an ErrorRef")
	}
	raw, loadErr := repo.LoadContent(context.Background(), secondReview.ErrorRef)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if want := "review made no progress across rounds (identical findings set); run failed"; string(raw) != want {
		t.Fatalf("ErrorRef body = %q, want %q", raw, want)
	}
	// Only review#1's back-edge may have incremented the loop counter.
	counters, counterErr := repo.GetLoopCounters(context.Background(), ctrl.RunID)
	if counterErr != nil {
		t.Fatal(counterErr)
	}
	if len(counters) != 1 || counters[0].LoopName != "review_repair" || counters[0].Iterations != 1 {
		t.Fatalf("loop counters = %+v, want review_repair=1", counters)
	}
	// No implement#3 dispatch after the no-progress failure.
	runner.mu.Lock()
	defer runner.mu.Unlock()
	if len(runner.calls) != 4 {
		t.Fatalf("runner calls = %d, want 4 (no implement#3): %+v", len(runner.calls), runner.calls)
	}
}

// TestReviewZeroProgressAllowsChangedFindings pins the negative case: a review
// whose normalized finding-id set CHANGED across rounds takes the loop
// back-edge normally and the loop converges on an approved verdict.
func TestReviewZeroProgressAllowsChangedFindings(t *testing.T) {
	wf := repairWorkflow(t, -1, 16)
	runner := &scriptedRunner{outputsByStepCall: map[string]json.RawMessage{
		"implement#1": json.RawMessage(`{"summary":"v1"}`),
		"review#1":    json.RawMessage(`{"verdict":"changes_requested","findings":[{"id":"R0-f1","severity":"high","reason":"x"}]}`),
		"implement#2": json.RawMessage(`{"summary":"v2"}`),
		"review#2":    json.RawMessage(`{"verdict":"changes_requested","findings":[{"id":"R1-f2","severity":"high","reason":"y"}]}`),
		"implement#3": json.RawMessage(`{"summary":"v3"}`),
		"review#3":    json.RawMessage(`{"verdict":"approved","findings":[]}`),
	}}
	repo := workflowledger.NewMemoryRepository()
	ctrl, err := NewLinearController(repo, runner, wf, map[string]StepRuntime{
		"implement": {Agent: agents.ResolvedAgent{Name: "dev"}},
		"review":    {Agent: agents.ResolvedAgent{Name: "rev"}},
	}, map[string]any{"task": "x"}, "wfr-zero-progress-changed", []byte("snap"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := ctrl.Run(context.Background())
	if err != nil || got.Status != workflowledger.RunStatusSucceeded {
		t.Fatalf("run = %+v err=%v; want succeeded after a changed findings set", got, err)
	}
	counters, counterErr := repo.GetLoopCounters(context.Background(), ctrl.RunID)
	if counterErr != nil {
		t.Fatal(counterErr)
	}
	if len(counters) != 1 || counters[0].LoopName != "review_repair" || counters[0].Iterations != 2 {
		t.Fatalf("loop counters = %+v, want review_repair=2", counters)
	}
	attempts, listErr := repo.ListStepAttempts(context.Background(), ctrl.RunID)
	if listErr != nil {
		t.Fatal(listErr)
	}
	reviewCount := 0
	for _, a := range attempts {
		if a.StepID == "review" {
			reviewCount++
		}
	}
	if reviewCount != 3 {
		t.Fatalf("review attempts = %d, want 3", reviewCount)
	}
}

// TestReviewZeroProgressFirstRoundRoutesNormally pins that the FIRST review
// round (no previous completed review output) is never zero-progress: its
// changes_requested verdict takes the loop back-edge normally.
func TestReviewZeroProgressFirstRoundRoutesNormally(t *testing.T) {
	wf := repairWorkflow(t, -1, 16)
	runner := &scriptedRunner{outputsByStepCall: map[string]json.RawMessage{
		"implement#1": json.RawMessage(`{"summary":"v1"}`),
		"review#1":    json.RawMessage(`{"verdict":"changes_requested","findings":[{"id":"R0-f1","severity":"high","reason":"x"}]}`),
		"implement#2": json.RawMessage(`{"summary":"v2"}`),
		"review#2":    json.RawMessage(`{"verdict":"approved","findings":[]}`),
	}}
	repo := workflowledger.NewMemoryRepository()
	ctrl, err := NewLinearController(repo, runner, wf, map[string]StepRuntime{
		"implement": {Agent: agents.ResolvedAgent{Name: "dev"}},
		"review":    {Agent: agents.ResolvedAgent{Name: "rev"}},
	}, map[string]any{"task": "x"}, "wfr-zero-progress-first", []byte("snap"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := ctrl.Run(context.Background())
	if err != nil || got.Status != workflowledger.RunStatusSucceeded {
		t.Fatalf("run = %+v err=%v; want succeeded via the first-round back-edge", got, err)
	}
	counters, counterErr := repo.GetLoopCounters(context.Background(), ctrl.RunID)
	if counterErr != nil {
		t.Fatal(counterErr)
	}
	if len(counters) != 1 || counters[0].Iterations != 1 {
		t.Fatalf("loop counters = %+v, want review_repair=1", counters)
	}
}

// TestReviewZeroProgressRequiresFindingsIDs pins that identical findings with
// NO string ids (empty normalized set on both rounds) never trip the gate:
// the check fires only when the previous round's findings set is non-empty.
func TestReviewZeroProgressRequiresFindingsIDs(t *testing.T) {
	wf := repairWorkflow(t, -1, 16)
	runner := &scriptedRunner{outputsByStepCall: map[string]json.RawMessage{
		"implement#1": json.RawMessage(`{"summary":"v1"}`),
		"review#1":    json.RawMessage(`{"verdict":"changes_requested","findings":[{"severity":"high","reason":"x"}]}`),
		"implement#2": json.RawMessage(`{"summary":"v2"}`),
		"review#2":    json.RawMessage(`{"verdict":"changes_requested","findings":[{"severity":"high","reason":"x"}]}`),
		"implement#3": json.RawMessage(`{"summary":"v3"}`),
		"review#3":    json.RawMessage(`{"verdict":"approved","findings":[]}`),
	}}
	repo := workflowledger.NewMemoryRepository()
	ctrl, err := NewLinearController(repo, runner, wf, map[string]StepRuntime{
		"implement": {Agent: agents.ResolvedAgent{Name: "dev"}},
		"review":    {Agent: agents.ResolvedAgent{Name: "rev"}},
	}, map[string]any{"task": "x"}, "wfr-zero-progress-noids", []byte("snap"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := ctrl.Run(context.Background())
	if err != nil || got.Status != workflowledger.RunStatusSucceeded {
		t.Fatalf("run = %+v err=%v; want succeeded (no-id findings must not trip the gate)", got, err)
	}
}

// loadFaultRepository injects a ledger read failure for one content ref, the
// exact failure the zero-progress check's prior-findings read can hit (e.g. a
// digest mismatch surfaced by LoadContent's sha256 verification).
type loadFaultRepository struct {
	workflowledger.Repository
	failLoadRef string
}

func (r *loadFaultRepository) LoadContent(ctx context.Context, ref string) ([]byte, error) {
	if ref == r.failLoadRef {
		return nil, errors.New("content store is down")
	}
	return r.Repository.LoadContent(ctx, ref)
}

// TestReviewZeroProgressLedgerReadFailureFailsStep pins the Step-5 audit fix:
// the zero-progress gate's ledger-read failure is a HARD step failure (matching
// agentStepRequest's GetLoopCounters error behavior at linear_execution.go), not
// a log-and-continue that routes the loop back-edge on a guess. A review whose
// prior findings cannot be read must not spin the repair loop.
func TestReviewZeroProgressLedgerReadFailureFailsStep(t *testing.T) {
	wf := repairWorkflow(t, -1, 16)
	review1 := json.RawMessage(`{"verdict":"changes_requested","findings":[{"id":"R0-f1","severity":"high","reason":"x"}]}`)
	runner := &scriptedRunner{outputsByStepCall: map[string]json.RawMessage{
		"implement#1": json.RawMessage(`{"summary":"v1"}`),
		"review#1":    review1,
		"implement#2": json.RawMessage(`{"summary":"v2"}`),
		"review#2":    json.RawMessage(`{"verdict":"changes_requested","findings":[{"id":"R1-f1","severity":"high","reason":"x"}]}`),
	}}
	// review#1's OutputRef is minted as "sha256:" + DigestHex of the stored
	// output; the zero-progress check reads exactly that ref when settling
	// review#2, so failing it proves the gate fails the step.
	review1Ref := "sha256:" + workflowledger.DigestHex(review1)
	repo := &loadFaultRepository{Repository: workflowledger.NewMemoryRepository(), failLoadRef: review1Ref}
	ctrl, err := NewLinearController(repo, runner, wf, map[string]StepRuntime{
		"implement": {Agent: agents.ResolvedAgent{Name: "dev"}},
		"review":    {Agent: agents.ResolvedAgent{Name: "rev"}},
	}, map[string]any{"task": "x"}, "wfr-zero-progress-read-fail", []byte("snap"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := ctrl.Run(context.Background())
	if err == nil || got.Status != workflowledger.RunStatusFailed {
		t.Fatalf("run = %+v err=%v; want failed when the zero-progress ledger read fails", got, err)
	}
	if !strings.Contains(err.Error(), "zero-progress check") {
		t.Fatalf("err = %v, want a clear zero-progress ledger-read failure", err)
	}
	attempts, listErr := repo.ListStepAttempts(context.Background(), ctrl.RunID)
	if listErr != nil {
		t.Fatal(listErr)
	}
	var secondReview workflowledger.StepAttempt
	for _, a := range attempts {
		if a.StepID == "review" && a.AttemptNo == 2 {
			secondReview = a
		}
	}
	if secondReview.Status != workflowledger.AttemptStatusFailed {
		t.Fatalf("review#2 status = %q, want failed", secondReview.Status)
	}
	if secondReview.ToStepID != "failure" {
		t.Fatalf("review#2 route = %q, want failure (no loop back-edge)", secondReview.ToStepID)
	}
	if secondReview.ErrorRef == "" {
		t.Fatal("review#2 must carry an ErrorRef with the ledger-read cause")
	}
	raw, loadErr := repo.Repository.LoadContent(context.Background(), secondReview.ErrorRef)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if !strings.Contains(string(raw), "zero-progress check") {
		t.Fatalf("ErrorRef body = %q, want the zero-progress ledger-read cause", raw)
	}
}

// TestReviewZeroProgressDetectsOscillation is the regression guard for the
// A-B-A oscillation gap: a reviewer that alternates between two finding sets
// never repeats CONSECUTIVELY, so comparing against only the immediately
// prior round let it run until the loop cap and die at the duration bound as
// an undiagnosed timeout. Round 3 reproduces round 1's set and must fail.
func TestReviewZeroProgressDetectsOscillation(t *testing.T) {
	wf := repairWorkflow(t, -1, 16)
	runner := &scriptedRunner{outputsByStepCall: map[string]json.RawMessage{
		"implement#1": json.RawMessage(`{"summary":"v1"}`),
		"review#1":    json.RawMessage(`{"verdict":"changes_requested","findings":[{"id":"R0-fA","severity":"high","reason":"x"}]}`),
		"implement#2": json.RawMessage(`{"summary":"v2"}`),
		"review#2":    json.RawMessage(`{"verdict":"changes_requested","findings":[{"id":"R1-fB","severity":"high","reason":"y"}]}`),
		"implement#3": json.RawMessage(`{"summary":"v3"}`),
		"review#3":    json.RawMessage(`{"verdict":"changes_requested","findings":[{"id":"R2-fA","severity":"high","reason":"x"}]}`),
	}}
	repo := workflowledger.NewMemoryRepository()
	ctrl, err := NewLinearController(repo, runner, wf, map[string]StepRuntime{
		"implement": {Agent: agents.ResolvedAgent{Name: "dev"}},
		"review":    {Agent: agents.ResolvedAgent{Name: "rev"}},
	}, map[string]any{"task": "x"}, "wfr-oscillation", []byte("snap"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := ctrl.Run(context.Background())
	if err == nil || got.Status != workflowledger.RunStatusFailed {
		t.Fatalf("run = %+v err=%v; want failed on an A-B-A oscillation", got, err)
	}
	if !strings.Contains(err.Error(), "review made no progress across rounds") {
		t.Fatalf("err = %v, want the zero-progress cause", err)
	}
	attempts, listErr := repo.ListStepAttempts(context.Background(), ctrl.RunID)
	if listErr != nil {
		t.Fatal(listErr)
	}
	for _, a := range attempts {
		if a.StepID == "review" && a.AttemptNo == 3 {
			if a.Status != workflowledger.AttemptStatusFailed {
				t.Fatalf("review#3 status = %q, want failed", a.Status)
			}
			if a.ToStepID != "failure" {
				t.Fatalf("review#3 route = %q, want failure", a.ToStepID)
			}
		}
	}
}

// TestPriorOutputAttemptsWindow pins the ordering and bound of the comparison
// window: newest first, capped, and attempts without output excluded.
func TestPriorOutputAttemptsWindow(t *testing.T) {
	attempts := []workflowledger.StepAttempt{
		{StepID: "review", AttemptNo: 1, OutputRef: "sha256:a"},
		{StepID: "review", AttemptNo: 2},
		{StepID: "other", AttemptNo: 3, OutputRef: "sha256:c"},
		{StepID: "review", AttemptNo: 4, OutputRef: "sha256:d"},
		{StepID: "review", AttemptNo: 3, OutputRef: "sha256:b"},
	}
	got := priorOutputAttempts(attempts, "review", 2)
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2 (limit)", len(got))
	}
	if got[0].AttemptNo != 4 || got[1].AttemptNo != 3 {
		t.Fatalf("order = %d,%d; want 4,3 (newest first)", got[0].AttemptNo, got[1].AttemptNo)
	}
	if all := priorOutputAttempts(attempts, "review", 0); len(all) != 3 {
		t.Fatalf("unbounded len = %d, want 3 (attempt 2 has no output, 'other' excluded)", len(all))
	}
}

// TestReviewZeroProgressSkipsUnparsablePriorRound pins that a prior round
// whose stored output is not valid JSON is skipped, not treated as a match.
// Failing a run because an old blob is unreadable would be a false positive.
func TestReviewZeroProgressSkipsUnparsablePriorRound(t *testing.T) {
	ctx := context.Background()
	repo := workflowledger.NewMemoryRepository()
	runID := "wfr-unparsable"
	snapshotJSON, err := workflowledger.MarshalSnapshot(workflowledger.Snapshot{
		SchemaVersion: 1, DefinitionTOML: []byte("x"), DefinitionDigest: "d",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CreateRun(ctx, workflowledger.RunSnapshot{
		RunID: runID, WorkflowName: "wf", Status: workflowledger.RunStatusPending,
	}, snapshotJSON); err != nil {
		t.Fatal(err)
	}
	stored, err := repo.GetRun(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CompareAndSetRunStatus(ctx, runID, stored.Version, workflowledger.RunStatusRunning, nil); err != nil {
		t.Fatal(err)
	}

	body := []byte("not json")
	ref := fmt.Sprintf("sha256:%x", sha256.Sum256(body))
	if err := repo.StoreContent(ctx, ref, body); err != nil {
		t.Fatal(err)
	}
	attempt := workflowledger.StepAttempt{AttemptID: "att-1", RunID: runID, StepID: "review", AttemptNo: 1}
	if err := repo.CreateStepAttempt(ctx, attempt); err != nil {
		t.Fatal(err)
	}
	storedAttempt, err := repo.GetStepAttempt(ctx, runID, attempt.AttemptID)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CompleteStepAttempt(ctx, runID, attempt.AttemptID, storedAttempt.Version, workflowledger.AttemptOutcome{
		Status: workflowledger.AttemptStatusSucceeded, OutputRef: ref, ToStepID: "implement",
	}); err != nil {
		t.Fatal(err)
	}

	ctrl := &LinearController{Repo: repo, RunID: runID}
	step := definition.Step{ID: "review", Kind: "agent_gate"}
	output := map[string]any{
		"verdict":  "changes_requested",
		"findings": []any{map[string]any{"id": "R1-f1"}},
	}
	got, err := ctrl.reviewMadeNoProgress(ctx, step, RouteDecision{Loop: "review_repair"}, output)
	if err != nil {
		t.Fatalf("reviewMadeNoProgress error = %v, want nil", err)
	}
	if got {
		t.Error("an unparsable prior round must not count as zero progress")
	}
}
