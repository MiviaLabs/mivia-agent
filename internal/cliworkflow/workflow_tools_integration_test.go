package cliworkflow

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// This file closes the gap the workflow audit named as its headline finding.
//
// Every other test that drives the model-facing workflow_* tools through their
// real JSON entry point does so against localengine.Engine - an engine an AST
// gate (localengine/engine_production_gate_test.go) FORBIDS in production,
// because it lacks MCP digest validation on resume. The engine the shipped
// tools actually get is cliworkflow's sessionWorkflowEngine, and nothing
// exercised it through the tool surface at all: its own tests call the Go API
// (Start/Cancel/Deliver) directly. Any divergence between the two engines was
// therefore invisible to the suite.
//
// These tests drive the SHIPPED construction path - WorkflowToolServiceWithBus,
// the one production calls - over a real git workspace, and then speak to it
// only in JSON, exactly as a model does.

// newToolSurfaceFixture builds a workspace and returns the workflow tool
// service built the way production builds it.
//
// It deliberately does NOT hand-assemble a workflowledger.Service: going
// through WorkflowToolServiceWithBus is what makes this a test of the shipped
// wiring rather than of a parallel construction only tests use.
func newToolSurfaceFixture(t *testing.T) (*workflowledger.Service, string) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"{\"ok\":true}"}}]}`)
	}))
	t.Cleanup(server.Close)

	root := t.TempDir()
	t.Setenv("MIVIA_ALLOW_INSECURE_HTTP", "1")
	writeWorkflowRunFixture(t, root, server.URL, filepath.Join(root, "workflow.db"))
	// Production always runs inside a repository, and admission resolves the
	// run's base identity from one. The prior session-engine tests ran on a
	// bare temp directory, so nothing here ever touched that path.
	initWorkflowGitRepo(t, root)

	res, err := config.Load(config.LoadOptions{
		ConfigPath:    filepath.Join(root, "config.toml"),
		WorkspaceRoot: root,
	})
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	// nil bus provider and runSweep=false: this is a test session, not an
	// interactive one, so no recovery sweep goroutine outlives the test.
	svc := WorkflowToolServiceWithBus(root, res, nil, false, true, nil)
	if svc == nil {
		t.Fatal("WorkflowToolServiceWithBus returned nil; the fixture workspace declares workflows")
	}
	return svc, root
}

// toolNamed returns one shipped tool by name.
func toolNamed(t *testing.T, svc *workflowledger.Service, name string) workflowledger.Tool {
	t.Helper()
	for _, tool := range workflowledger.Tools(svc) {
		if tool.Name() == name {
			return tool
		}
	}
	t.Fatalf("tool %q is not on the shipped surface", name)
	return nil
}

// execTool calls one tool exactly as a model does: JSON in, JSON out.
func execTool(t *testing.T, svc *workflowledger.Service, name, payload string) (string, error) {
	t.Helper()
	return toolNamed(t, svc, name).Execute(context.Background(), json.RawMessage(payload))
}

// mustExecTool fails the test if the tool call errors.
func mustExecTool(t *testing.T, svc *workflowledger.Service, name, payload string) string {
	t.Helper()
	out, err := execTool(t, svc, name, payload)
	if err != nil {
		t.Fatalf("%s(%s): %v", name, payload, err)
	}
	return out
}

// awaitTerminalStatus polls workflow_status THROUGH THE TOOL until the run is
// terminal. Polling the tool rather than reaching into engine internals keeps
// this an observer a model could be: if the status a model can see never
// reports terminal, the test fails, which is the property worth holding.
func awaitTerminalStatus(t *testing.T, svc *workflowledger.Service, runID string) workflowledger.StatusView {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	var last workflowledger.StatusView
	for time.Now().Before(deadline) {
		out := mustExecTool(t, svc, workflowledger.ToolWorkflowStatus, fmt.Sprintf(`{"run_id":%q}`, runID))
		if err := json.Unmarshal([]byte(out), &last); err != nil {
			t.Fatalf("decode workflow_status: %v (body %s)", err, out)
		}
		if workflowledger.IsTerminalRunStatus(workflowledger.RunStatus(last.Status)) {
			return last
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("run %s never reached a terminal status through workflow_status; last = %+v", runID, last)
	return last
}

// TestIntegrationShippedToolsDriveTheProductionEngine is the core of the gap:
// admit, observe, inspect and list a real run entirely through the tool JSON
// entry points, against the engine that actually ships.
func TestIntegrationShippedToolsDriveTheProductionEngine(t *testing.T) {
	svc, _ := newToolSurfaceFixture(t)

	// 1. workflow_run admits the run.
	startOut := mustExecTool(t, svc, workflowledger.ToolWorkflowRun,
		`{"workflow":"two-step","inputs":{"task":"build"}}`)
	var started workflowledger.StartResult
	if err := json.Unmarshal([]byte(startOut), &started); err != nil {
		t.Fatalf("decode workflow_run: %v (body %s)", err, startOut)
	}
	if started.RunID == "" || started.Status == "" {
		t.Fatalf("workflow_run = %+v, want a run id and a status", started)
	}

	// 2. workflow_status reports it terminal, with both steps recorded.
	status := awaitTerminalStatus(t, svc, started.RunID)
	if status.Status != string(workflowledger.RunStatusSucceeded) {
		t.Fatalf("status = %q, want succeeded (body %+v)", status.Status, status)
	}
	if len(status.Attempts) < 2 {
		t.Fatalf("attempts = %d, want at least one per step", len(status.Attempts))
	}
	for _, a := range status.Attempts {
		if a.Status == string(workflowledger.AttemptStatusSucceeded) && a.OutputDigest == "" {
			t.Fatalf("succeeded attempt has no output digest: %+v", a)
		}
	}

	// 3. workflow_events returns the durable trail.
	evOut := mustExecTool(t, svc, workflowledger.ToolWorkflowEvents,
		fmt.Sprintf(`{"run_id":%q,"limit":50}`, started.RunID))
	var events workflowledger.EventsPage
	if err := json.Unmarshal([]byte(evOut), &events); err != nil {
		t.Fatalf("decode workflow_events: %v (body %s)", err, evOut)
	}
	if events.Count < 2 {
		t.Fatalf("events count = %d, want at least 2", events.Count)
	}

	// 4. workflow_inspect resolves one attempt's detail, including the child
	//    identity the coordinator recorded and the transition it routed on.
	insOut := mustExecTool(t, svc, workflowledger.ToolWorkflowInspect,
		fmt.Sprintf(`{"run_id":%q,"step":"one","attempt":1}`, started.RunID))
	var inspect workflowledger.InspectView
	if err := json.Unmarshal([]byte(insOut), &inspect); err != nil {
		t.Fatalf("decode workflow_inspect: %v (body %s)", err, insOut)
	}
	if inspect.Output == nil {
		t.Fatalf("inspect has no output: %+v", inspect)
	}
	if inspect.CoordinatorRunID == "" || inspect.TaskID == "" {
		t.Fatalf("inspect has no child identity: %+v", inspect)
	}
	if inspect.Transition == nil || inspect.Transition.ToStep == "" {
		t.Fatalf("inspect has no routed transition: %+v", inspect)
	}

	// 5. workflow_list_runs lists it.
	listOut := mustExecTool(t, svc, workflowledger.ToolWorkflowListRuns, `{}`)
	var list workflowledger.ListRunsView
	if err := json.Unmarshal([]byte(listOut), &list); err != nil {
		t.Fatalf("decode workflow_list_runs: %v (body %s)", err, listOut)
	}
	var found bool
	for _, r := range list.Runs {
		if r.RunID == started.RunID {
			found = true
			if r.Workflow != "two-step" || r.Status != string(workflowledger.RunStatusSucceeded) {
				t.Fatalf("listed run = %+v, want workflow two-step and status succeeded", r)
			}
		}
	}
	if !found {
		t.Fatalf("workflow_list_runs did not list %s: %s", started.RunID, listOut)
	}
}

// TestIntegrationShippedToolSurfaceIsComplete guards the surface itself. A
// service that silently stopped exposing a tool would make every other test
// here fail for a confusing reason; this one names it directly.
func TestIntegrationShippedToolSurfaceIsComplete(t *testing.T) {
	svc, _ := newToolSurfaceFixture(t)
	got := map[string]bool{}
	for _, tool := range workflowledger.Tools(svc) {
		got[tool.Name()] = true
		if strings.TrimSpace(tool.Description()) == "" {
			t.Errorf("tool %q ships with no description", tool.Name())
		}
		if tool.Parameters() == nil {
			t.Errorf("tool %q ships with no parameter schema", tool.Name())
		}
	}
	for _, want := range []string{
		workflowledger.ToolWorkflowRun,
		workflowledger.ToolWorkflowStatus,
		workflowledger.ToolWorkflowEvents,
		workflowledger.ToolWorkflowInspect,
		workflowledger.ToolWorkflowListRuns,
		workflowledger.ToolWorkflowDeliver,
		workflowledger.ToolWorkflowCancel,
	} {
		if !got[want] {
			t.Errorf("shipped tool surface is missing %q", want)
		}
	}
}

// TestIntegrationDeliverRefusesWithoutAllowPublish drives the publication gate
// through the production engine. workflow_deliver exists to withhold
// publication until the caller asks for it, and that refusal was only ever
// asserted against the non-production engine.
//
// It asserts the gate is what DECIDES, not merely that the call failed: without
// the flag the tool returns a refusal naming allow_publish and never reaches
// the delivery policy, while with the flag the same run gets PAST the gate and
// fails later, on the policy. A gate that stopped working would turn the first
// call into the second, which this test detects.
func TestIntegrationDeliverRefusesWithoutAllowPublish(t *testing.T) {
	svc, _ := newToolSurfaceFixture(t)
	startOut := mustExecTool(t, svc, workflowledger.ToolWorkflowRun,
		`{"workflow":"two-step","inputs":{"task":"build"}}`)
	var started workflowledger.StartResult
	if err := json.Unmarshal([]byte(startOut), &started); err != nil {
		t.Fatal(err)
	}
	awaitTerminalStatus(t, svc, started.RunID)

	// Without the flag: a clean, structured refusal that names the gate.
	out, err := execTool(t, svc, workflowledger.ToolWorkflowDeliver,
		fmt.Sprintf(`{"run_id":%q}`, started.RunID))
	if err != nil {
		t.Fatalf("workflow_deliver without allow_publish errored (%v); the gate must refuse cleanly, not fail", err)
	}
	var refusal workflowledger.DeliverResult
	if jsonErr := json.Unmarshal([]byte(out), &refusal); jsonErr != nil {
		t.Fatalf("decode workflow_deliver refusal: %v (body %s)", jsonErr, out)
	}
	if !refusal.Refused {
		t.Fatalf("workflow_deliver without allow_publish was not refused: %s", out)
	}
	if !strings.Contains(refusal.Reason, "allow_publish") {
		t.Fatalf("refusal reason = %q, want it to name allow_publish", refusal.Reason)
	}

	// With the flag: the SAME run must get past the gate. It still fails -
	// two-step declares no [delivery] policy - but on the policy, not the
	// gate. This is the half that fails if the gate stops gating.
	flagged, flaggedErr := execTool(t, svc, workflowledger.ToolWorkflowDeliver,
		fmt.Sprintf(`{"run_id":%q,"allow_publish":true}`, started.RunID))
	if flaggedErr == nil {
		var passed workflowledger.DeliverResult
		if jsonErr := json.Unmarshal([]byte(flagged), &passed); jsonErr == nil && passed.Refused &&
			strings.Contains(passed.Reason, "allow_publish") {
			t.Fatalf("allow_publish=true was still refused by the gate: %s", flagged)
		}
	} else if strings.Contains(flaggedErr.Error(), "allow_publish") {
		t.Fatalf("allow_publish=true still hit the gate: %v", flaggedErr)
	}
}

// TestIntegrationCancelOnTerminalRunIsIdempotent pins workflow_cancel through
// the production engine: cancelling an already-finished run must report its
// settled state, never error and never reopen it.
func TestIntegrationCancelOnTerminalRunIsIdempotent(t *testing.T) {
	svc, _ := newToolSurfaceFixture(t)
	startOut := mustExecTool(t, svc, workflowledger.ToolWorkflowRun,
		`{"workflow":"two-step","inputs":{"task":"build"}}`)
	var started workflowledger.StartResult
	if err := json.Unmarshal([]byte(startOut), &started); err != nil {
		t.Fatal(err)
	}
	before := awaitTerminalStatus(t, svc, started.RunID)

	if _, err := execTool(t, svc, workflowledger.ToolWorkflowCancel,
		fmt.Sprintf(`{"run_id":%q}`, started.RunID)); err != nil {
		t.Fatalf("workflow_cancel on a terminal run = %v, want it to settle quietly", err)
	}
	after := mustExecTool(t, svc, workflowledger.ToolWorkflowStatus,
		fmt.Sprintf(`{"run_id":%q}`, started.RunID))
	var status workflowledger.StatusView
	if err := json.Unmarshal([]byte(after), &status); err != nil {
		t.Fatal(err)
	}
	if status.Status != before.Status {
		t.Fatalf("status after cancelling a terminal run = %q, want it unchanged at %q", status.Status, before.Status)
	}
}

// TestIntegrationRunRefusesUnknownWorkflow covers the admission refusal a
// model is most likely to trigger, through the shipped entry point.
func TestIntegrationRunRefusesUnknownWorkflow(t *testing.T) {
	svc, _ := newToolSurfaceFixture(t)
	if _, err := execTool(t, svc, workflowledger.ToolWorkflowRun,
		`{"workflow":"no-such-workflow","inputs":{"task":"build"}}`); err == nil {
		t.Fatal("workflow_run admitted a workflow the workspace does not define")
	}
}

// TestIntegrationRunRefusesMissingRequiredInput pins input validation on the
// production path: two-step declares task as required.
func TestIntegrationRunRefusesMissingRequiredInput(t *testing.T) {
	svc, _ := newToolSurfaceFixture(t)
	if _, err := execTool(t, svc, workflowledger.ToolWorkflowRun,
		`{"workflow":"two-step","inputs":{}}`); err == nil {
		t.Fatal("workflow_run admitted a run with no value for the required input")
	}
}
