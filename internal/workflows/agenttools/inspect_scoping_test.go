package agenttools_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
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

// TestInspectToolPagingParams pins plan v3 P2: workflow_inspect exposes
// optional offset/limit parameters, forwards them to the service layer
// (which validates non-negative values), and treats missing parameters as 0.
// A negative offset reaching the service validation is the observable proof
// that Execute forwards the values rather than dropping them.
func TestInspectToolPagingParams(t *testing.T) {
	repo := workflowledger.NewMemoryRepository()
	runID := "wfr-paging-1"
	seedRunningAttempt(t, repo, runID)
	svc := testService(t, repo, nil)
	tool := findTool(t, svc, agenttools.ToolWorkflowInspect)

	t.Run("missing paging params behave as 0", func(t *testing.T) {
		out, err := tool.Execute(context.Background(),
			json.RawMessage(`{"run_id":"`+runID+`","step":"one","attempt":1}`))
		if err != nil {
			t.Fatalf("inspect without offset/limit: %v", err)
		}
		var view agenttools.InspectView
		if err := json.Unmarshal([]byte(out), &view); err != nil {
			t.Fatal(err)
		}
		if view.RunID != runID || view.Step != "one" || view.Attempt != 1 {
			t.Fatalf("inspect view = %+v, want run %q step one attempt 1", view, runID)
		}
	})

	t.Run("explicit zero offset and limit accepted", func(t *testing.T) {
		if _, err := tool.Execute(context.Background(),
			json.RawMessage(`{"run_id":"`+runID+`","step":"one","attempt":1,"limit":0,"offset":0}`)); err != nil {
			t.Fatalf("inspect with limit=0 offset=0: %v", err)
		}
	})

	t.Run("negative offset rejected by service", func(t *testing.T) {
		_, err := tool.Execute(context.Background(),
			json.RawMessage(`{"run_id":"`+runID+`","step":"one","attempt":1,"offset":-1}`))
		if err == nil {
			t.Fatal("inspect with negative offset: expected service-layer error")
		}
		if !strings.Contains(err.Error(), "limit and offset must be >= 0") {
			t.Fatalf("negative offset error = %q, want service-layer range error", err.Error())
		}
	})

	t.Run("negative limit rejected by service", func(t *testing.T) {
		if _, err := tool.Execute(context.Background(),
			json.RawMessage(`{"run_id":"`+runID+`","step":"one","attempt":1,"limit":-1}`)); err == nil {
			t.Fatal("inspect with negative limit: expected service-layer error")
		}
	})

	t.Run("parameters declare offset and limit", func(t *testing.T) {
		params := tool.Parameters()
		props, ok := params["properties"].(map[string]any)
		if !ok {
			t.Fatalf("inspect parameters properties = %T, want map", params["properties"])
		}
		for _, name := range []string{"offset", "limit"} {
			prop, ok := props[name].(map[string]any)
			if !ok {
				t.Fatalf("parameter %q missing from inspect parameters", name)
			}
			if prop["type"] != "integer" {
				t.Fatalf("parameter %q type = %v, want integer", name, prop["type"])
			}
			if prop["minimum"] != 0 {
				t.Fatalf("parameter %q minimum = %v, want 0", name, prop["minimum"])
			}
			desc, _ := prop["description"].(string)
			if desc == "" {
				t.Fatalf("parameter %q missing description", name)
			}
		}
	})

	t.Run("description mentions paging", func(t *testing.T) {
		if !strings.Contains(strings.ToLower(tool.Description()), "page") {
			t.Fatalf("inspect description does not mention paging: %q", tool.Description())
		}
	})
}
