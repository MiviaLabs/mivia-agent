package agenttools_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/agenttools"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// seedRunForCoordinator mirrors seedRunningAttempt's shape but records the
// given coordinator run id on the attempt, so a caller-identity scoping test
// can build a run that belongs to a different coordinator.
func seedRunForCoordinator(t *testing.T, repo workflowledger.Repository, runID, coordinatorRunID string) {
	t.Helper()
	ctx := context.Background()
	snapshot, err := workflowledger.MarshalSnapshot(workflowledger.Snapshot{
		SchemaVersion: 1, DefinitionTOML: []byte("name=x"), DefinitionDigest: "digest",
		Inputs: map[string]string{"task": "build"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CreateRun(ctx, workflowledger.RunSnapshot{
		RunID: runID, WorkflowName: "two-step", WorkflowDigest: "digest",
		SnapshotDigest: workflowledger.SnapshotDigest(snapshot),
		InputDigest:    workflowledger.InputDigest(map[string]string{"task": "build"}),
		Status:         workflowledger.RunStatusPending, ActiveStepID: "one",
		StartedAt: time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC),
	}, snapshot); err != nil {
		t.Fatal(err)
	}
	if err := repo.CompareAndSetRunStatus(ctx, runID, 1, workflowledger.RunStatusRunning, nil); err != nil {
		t.Fatal(err)
	}
	attempt := workflowledger.StepAttempt{
		AttemptID: "wfa-one-1", RunID: runID, StepID: "one", AttemptNo: 1,
		Status: workflowledger.AttemptStatusRunning,
	}
	if err := repo.CreateStepAttempt(ctx, attempt); err != nil {
		t.Fatal(err)
	}
	out := []byte(`{"ok":true,"verdict":"approved"}`)
	ref := "sha256:" + workflowledger.DigestHex(out)
	if err := repo.StoreContent(ctx, ref, out); err != nil {
		t.Fatal(err)
	}
	stored, _ := repo.GetStepAttempt(ctx, runID, attempt.AttemptID)
	decision, _ := json.Marshal(map[string]any{"selected": map[string]any{"output": map[string]any{"verdict": "approved"}}})
	if err := repo.CompleteStepAttempt(ctx, runID, attempt.AttemptID, stored.Version, workflowledger.AttemptOutcome{
		Status: workflowledger.AttemptStatusSucceeded, OutputRef: ref, OutputDigest: workflowledger.DigestHex(out),
		ToStepID: "two", TransitionIndex: 0, MatchDigest: "md", DecisionJSON: decision,
		CoordinatorRunID: coordinatorRunID, TaskID: "task-1", EvidenceJSON: []byte(`[{"name":"task"}]`),
	}); err != nil {
		t.Fatal(err)
	}
}

// TestInspectScopedToCallerRun pins the caller-identity participant gate on
// workflow_inspect (plan 59): with a TaskIdentity on ctx, the requested run is
// visible only when one of its attempts names the identity's RunID as the
// coordinator run; otherwise the refusal is indistinguishable from the run not
// existing. Without an identity (root/interactive session) the tool is
// unchanged and allowed.
func TestInspectScopedToCallerRun(t *testing.T) {
	coordinatorRunID := "coord-1" // matches seedRunningAttempt's coordinator.
	childRunID := "wfr-child-1"
	otherRunID := "wfr-other-1"

	repo := workflowledger.NewMemoryRepository()
	// A run whose attempt records CoordinatorRunID == coordinatorRunID.
	seedRunningAttempt(t, repo, childRunID)
	// A different, real run whose attempt belongs to another coordinator.
	seedRunForCoordinator(t, repo, otherRunID, "coord-other")

	svc := testService(t, repo, nil)
	identity := runtime.TaskIdentity{RunID: coordinatorRunID, TaskID: "task-x", Agent: "workflow-engineer"}

	t.Run("own run allowed", func(t *testing.T) {
		ctx := runtime.ContextWithTaskIdentity(context.Background(), identity)
		out, err := findTool(t, svc, agenttools.ToolWorkflowInspect).Execute(
			ctx, json.RawMessage(`{"run_id":"`+childRunID+`","step":"one","attempt":1}`))
		if err != nil {
			t.Fatalf("inspect own run: %v", err)
		}
		var view agenttools.InspectView
		if err := json.Unmarshal([]byte(out), &view); err != nil {
			t.Fatal(err)
		}
		if view.RunID != childRunID || view.CoordinatorRunID != coordinatorRunID {
			t.Fatalf("inspect view = %+v, want run %q coordinator %q", view, childRunID, coordinatorRunID)
		}
		if view.Output == nil || view.Transition == nil || view.Transition.ToStep != "two" {
			t.Fatalf("inspect view missing detail: %+v", view)
		}
	})

	t.Run("non-member run refused indistinguishably", func(t *testing.T) {
		ctx := runtime.ContextWithTaskIdentity(context.Background(), identity)
		_, err := findTool(t, svc, agenttools.ToolWorkflowInspect).Execute(
			ctx, json.RawMessage(`{"run_id":"`+otherRunID+`","step":"one","attempt":1}`))
		if err == nil {
			t.Fatal("inspect non-member run: expected refusal")
		}
		want := fmt.Sprintf("workflow run %q not found", otherRunID)
		if err.Error() != want {
			t.Fatalf("non-member refusal = %q, want indistinguishable not-found %q", err.Error(), want)
		}
		// The same identity against a truly absent run yields the identical
		// not-found class, so the caller cannot distinguish membership.
		_, ghostErr := findTool(t, svc, agenttools.ToolWorkflowInspect).Execute(
			ctx, json.RawMessage(`{"run_id":"wfr-ghost","step":"one","attempt":1}`))
		if ghostErr == nil {
			t.Fatal("inspect ghost run: expected not-found")
		}
		if ghostErr.Error() != fmt.Sprintf("workflow run %q not found", "wfr-ghost") {
			t.Fatalf("ghost refusal = %q, want not-found class", ghostErr.Error())
		}
	})

	t.Run("no identity allowed", func(t *testing.T) {
		out, err := findTool(t, svc, agenttools.ToolWorkflowInspect).Execute(
			context.Background(), json.RawMessage(`{"run_id":"`+childRunID+`","step":"one","attempt":1}`))
		if err != nil {
			t.Fatalf("inspect without identity: %v", err)
		}
		var view agenttools.InspectView
		if err := json.Unmarshal([]byte(out), &view); err != nil {
			t.Fatal(err)
		}
		if view.RunID != childRunID {
			t.Fatalf("inspect view = %+v, want run %q", view, childRunID)
		}
	})
}
