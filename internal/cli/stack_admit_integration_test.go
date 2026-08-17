package cli

// Pins a bug an adversarial audit found: the final integration run is always
// unadmittable. chunkRunInputs unconditionally set stack_mode="chunk", but
// validateStackingReservedInputs requires stack_part to be PRESENT for
// stack_mode=chunk, and driveIntegrationRun always passes an empty stackPart
// (chunkRunInputs only sets the key when non-empty). Every multi-chunk stack
// therefore failed at "integration run failed: ..." the moment every chunk
// merged. The integration run must admit as stack_mode=single (it runs the
// workflow's own plan+implement steps inline, per driveIntegrationRun's own
// doc comment), which requires none of chunk/pr_base/stack_part.

import (
	"context"
	"strings"
	"testing"

	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/tasks"
)

func TestIntegrationRunInputsAdmitAsStackModeSingle(t *testing.T) {
	inputs, snapshot := integrationRunInputs(map[string]string{"task": "whole feature"}, "master")
	if inputs["stack_mode"] != "single" {
		t.Fatalf("inputs[stack_mode] = %v, want single", inputs["stack_mode"])
	}
	if snapshot["stack_mode"] != "single" {
		t.Fatalf("snapshot[stack_mode] = %v, want single", snapshot["stack_mode"])
	}
	for _, forbidden := range []string{"chunk", "stack_part", "chunk_plan"} {
		if _, present := inputs[forbidden]; present {
			t.Fatalf("inputs[%s] present for the integration run; stack_mode=single forbids/never needs it", forbidden)
		}
		if _, present := snapshot[forbidden]; present {
			t.Fatalf("snapshot[%s] present for the integration run", forbidden)
		}
	}
	if inputs["task"] != "whole feature" {
		t.Fatalf("inputs[task] = %v, want the replayed plan input", inputs["task"])
	}
}

// A plan run admits with an explicit chunk_plan input (the no-stack_mode path
// of validateStackingReservedInputs returns "plan" without checking it), so
// replaying the plan inputs verbatim carries chunk_plan over - and
// stack_mode=single forbids it: every such stack then died at "integration
// run failed: reserved input chunk_plan is forbidden in stack_mode=single".
// The replay must strip chunk_plan.
func TestIntegrationRunInputsStripsReplayedChunkPlan(t *testing.T) {
	inputs, snapshot := integrationRunInputs(map[string]string{
		"task":       "whole feature",
		"chunk_plan": `{"id":"c1","title":"leak","files":["a.go"]}`,
	}, "master")
	if _, present := inputs["chunk_plan"]; present {
		t.Fatalf("inputs[chunk_plan] = %v, want stripped (forbidden in stack_mode=single)", inputs["chunk_plan"])
	}
	if _, present := snapshot["chunk_plan"]; present {
		t.Fatalf("snapshot[chunk_plan] = %v, want stripped (forbidden in stack_mode=single)", snapshot["chunk_plan"])
	}
	if inputs["stack_mode"] != "single" || snapshot["stack_mode"] != "single" {
		t.Fatalf("stack_mode = %v/%v, want single/single", inputs["stack_mode"], snapshot["stack_mode"])
	}
	if inputs["task"] != "whole feature" {
		t.Fatalf("inputs[task] = %v, want the replayed plan input", inputs["task"])
	}
}

// TestIntegrationRunInputsStripSiblingFiles pins the symmetric strip: a
// plan run may admit with sibling_files reserved input replayed from plan
// inputs; the integration run (stack_mode=single) must strip it like
// chunk_plan or admission fails.
func TestIntegrationRunInputsStripSiblingFiles(t *testing.T) {
	inputs, snapshot := integrationRunInputs(map[string]string{
		"task": "whole feature", "sibling_files": `["internal/b/b.go"]`,
	}, "master")
	if _, present := inputs["sibling_files"]; present {
		t.Fatalf("inputs[sibling_files] = %v, want stripped (forbidden in stack_mode=single)", inputs["sibling_files"])
	}
	if _, present := snapshot["sibling_files"]; present {
		t.Fatalf("snapshot[sibling_files] = %v, want stripped", snapshot["sibling_files"])
	}
}

// TestClassifyStackPlanRunDeliveryFailedChunk proves a terminally failed chunk
// task moves the gate from Incomplete to Failed, so callers can fail-settle
// the plan run instead of refusing it forever.
func TestClassifyStackPlanRunDeliveryFailedChunk(t *testing.T) {
	root, _, store, repo, compiled := newWorkflowBuildFixture(t)
	ctx := context.Background()
	runID := "wfr-plan-failed-chunk"
	snap := workflowledger.RunSnapshot{
		RunID: runID, WorkflowName: compiled.Name, WorkflowDigest: compiled.Digest,
		Status: workflowledger.RunStatusPending,
	}
	if err := repo.CreateRun(ctx, snap, []byte("{}")); err != nil {
		t.Fatal(err)
	}
	seedSucceededDecomposeAttempt(t, repo, runID, []byte(multiChunkPlanOutput))

	_, chunks, _, _, err := parseStackPlanOutput([]byte(multiChunkPlanOutput))
	if err != nil || len(chunks) != 2 {
		t.Fatalf("parse chunks = %v, %v; want 2", chunks, err)
	}
	ledger := tasks.NewStore(store)
	if err := seedStackLedger(ledger, runID, chunks); err != nil {
		t.Fatal(err)
	}
	if err := ledger.TransitionTask(runID, "c2", stackStatusFailed); err != nil {
		t.Fatal(err)
	}

	if got := classifyStackPlanRunDelivery(ctx, root, store, repo, runID, true); got != stackPlanRunFailed {
		t.Fatalf("classifyStackPlanRunDelivery() = %v, want stackPlanRunFailed for a failed chunk", got)
	}
	failed, reason := stackPlanRunFailureReason(ctx, root, store, repo, runID)
	if !failed {
		t.Fatal("stackPlanRunFailureReason() = false, want true")
	}
	if !strings.Contains(reason, "chunk c2 failed terminally") {
		t.Fatalf("reason = %q, want substring \"chunk c2 failed terminally\"", reason)
	}
}

// TestClassifyStackPlanRunDeliveryFailedIntegration proves a terminally failed
// integration run moves the gate to Failed.
func TestClassifyStackPlanRunDeliveryFailedIntegration(t *testing.T) {
	ctx := context.Background()
	root, store, repo, stackID := seedFailedIntegrationStack(t, workflowledger.RunStatusFailed)

	if got := classifyStackPlanRunDelivery(ctx, root, store, repo, stackID, true); got != stackPlanRunFailed {
		t.Fatalf("classifyStackPlanRunDelivery() = %v, want stackPlanRunFailed for a failed integration run", got)
	}
	failed, reason := stackPlanRunFailureReason(ctx, root, store, repo, stackID)
	if !failed {
		t.Fatal("stackPlanRunFailureReason() = false, want true")
	}
	if !strings.Contains(reason, "integration run") || !strings.Contains(reason, string(workflowledger.RunStatusFailed)) {
		t.Fatalf("reason = %q, want failed integration run substring", reason)
	}
}
