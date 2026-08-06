package agenttools_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/redact"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/agenttools"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

func seedRunningAttempt(t *testing.T, repo workflowledger.Repository, runID string) {
	seedRunningAttemptWithOutput(t, repo, runID, []byte(`{"ok":true,"verdict":"approved"}`))
}

func seedRunningAttemptWithOutput(t *testing.T, repo workflowledger.Repository, runID string, out []byte) {
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
	ref := "sha256:" + workflowledger.DigestHex(out)
	if err := repo.StoreContent(ctx, ref, out); err != nil {
		t.Fatal(err)
	}
	stored, _ := repo.GetStepAttempt(ctx, runID, attempt.AttemptID)
	decision, _ := json.Marshal(map[string]any{"selected": map[string]any{"output": map[string]any{"verdict": "approved"}}})
	if err := repo.CompleteStepAttempt(ctx, runID, attempt.AttemptID, stored.Version, workflowledger.AttemptOutcome{
		Status: workflowledger.AttemptStatusSucceeded, OutputRef: ref, OutputDigest: workflowledger.DigestHex(out),
		ToStepID: "two", TransitionIndex: 0, MatchDigest: "md", DecisionJSON: decision,
		CoordinatorRunID: "coord-1", TaskID: "task-1", EvidenceJSON: []byte(`[{"name":"task"}]`),
	}); err != nil {
		t.Fatal(err)
	}
}

func testService(t *testing.T, repo workflowledger.Repository, engine agenttools.Engine) *agenttools.Service {
	t.Helper()
	svc, err := agenttools.NewService(agenttools.ServiceOptions{
		Engine: engine,
		Repo: func(context.Context) (workflowledger.Repository, func(), error) {
			return repo, func() {}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return svc
}

func TestStatusFromLedger(t *testing.T) {
	repo := workflowledger.NewMemoryRepository()
	runID := "wfr-status-1"
	seedRunningAttempt(t, repo, runID)
	svc := testService(t, repo, nil)
	outStr, err := findTool(t, svc, agenttools.ToolWorkflowStatus).Execute(
		context.Background(), json.RawMessage(`{"run_id":"`+runID+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	var status agenttools.StatusView
	if err := json.Unmarshal([]byte(outStr), &status); err != nil {
		t.Fatal(err)
	}
	if status.RunID != runID || status.Workflow != "two-step" || status.Status != "running" {
		t.Fatalf("status = %+v", status)
	}
	if len(status.Attempts) != 1 || status.Attempts[0].Verdict != "approved" || status.Attempts[0].ToStep != "two" {
		t.Fatalf("attempts = %+v", status.Attempts)
	}
}

func TestEventsFromLedger(t *testing.T) {
	repo := workflowledger.NewMemoryRepository()
	runID := "wfr-events-1"
	seedRunningAttempt(t, repo, runID)
	svc := testService(t, repo, nil)
	evOut, err := findTool(t, svc, agenttools.ToolWorkflowEvents).Execute(
		context.Background(), json.RawMessage(`{"run_id":"`+runID+`","limit":10}`))
	if err != nil {
		t.Fatal(err)
	}
	var page agenttools.EventsPage
	if err := json.Unmarshal([]byte(evOut), &page); err != nil {
		t.Fatal(err)
	}
	if page.Count < 1 || page.Events[0].Kind == "" {
		t.Fatalf("events = %+v", page)
	}
	for _, ev := range page.Events {
		if strings.Contains(ev.Detail, "api_key") || strings.Contains(ev.Detail, "sk-") {
			t.Fatalf("event detail leaks secret material: %q", ev.Detail)
		}
	}
}

func TestInspectFromLedger(t *testing.T) {
	repo := workflowledger.NewMemoryRepository()
	runID := "wfr-inspect-1"
	seedRunningAttempt(t, repo, runID)
	svc := testService(t, repo, nil)
	insOut, err := findTool(t, svc, agenttools.ToolWorkflowInspect).Execute(
		context.Background(), json.RawMessage(`{"run_id":"`+runID+`","step":"one","attempt":1}`))
	if err != nil {
		t.Fatal(err)
	}
	var inspect agenttools.InspectView
	if err := json.Unmarshal([]byte(insOut), &inspect); err != nil {
		t.Fatal(err)
	}
	if inspect.CoordinatorRunID != "coord-1" || inspect.TaskID != "task-1" {
		t.Fatalf("inspect identity = %+v", inspect)
	}
	if inspect.Output == nil || inspect.Transition == nil || inspect.Transition.ToStep != "two" {
		t.Fatalf("inspect detail = %+v", inspect)
	}
}

func TestInspectRedactsConfiguredOutput(t *testing.T) {
	policy, err := redact.Compile([]string{`secret-[a-z0-9]+`}, []string{"api_key"}, "")
	if err != nil {
		t.Fatal(err)
	}
	previous := redact.Current()
	redact.SetPolicy(policy)
	t.Cleanup(func() { redact.SetPolicy(previous) })

	repo := workflowledger.NewMemoryRepository()
	runID := "wfr-inspect-redaction-1"
	seedRunningAttemptWithOutput(t, repo, runID, []byte(`{"api_key":"test-secret-placeholder","note":"secret-abc123"}`))
	svc := testService(t, repo, nil)
	insOut, err := findTool(t, svc, agenttools.ToolWorkflowInspect).Execute(
		context.Background(), json.RawMessage(`{"run_id":"`+runID+`","step":"one","attempt":1}`))
	if err != nil {
		t.Fatal(err)
	}
	var inspect agenttools.InspectView
	if err := json.Unmarshal([]byte(insOut), &inspect); err != nil {
		t.Fatal(err)
	}
	output, ok := inspect.Output.(map[string]any)
	if !ok {
		t.Fatalf("inspect output type = %T, want object", inspect.Output)
	}
	if output["api_key"] != "[redacted]" || output["note"] != "[redacted]" {
		t.Fatalf("inspect output = %#v, want configured redaction", output)
	}
	if strings.Contains(insOut, "test-secret-placeholder") || strings.Contains(insOut, "secret-abc123") {
		t.Fatalf("inspect result contains unredacted output: %s", insOut)
	}
}

func TestListRunsFromLedger(t *testing.T) {
	repo := workflowledger.NewMemoryRepository()
	runID := "wfr-list-1"
	seedRunningAttempt(t, repo, runID)
	svc := testService(t, repo, nil)
	listOut, err := findTool(t, svc, agenttools.ToolWorkflowListRuns).Execute(
		context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	var list agenttools.ListRunsView
	if err := json.Unmarshal([]byte(listOut), &list); err != nil {
		t.Fatal(err)
	}
	if list.Count < 1 || list.Runs[0].RunID != runID {
		t.Fatalf("list = %+v", list)
	}
}

func TestDeliverWithoutAllowPublishRefuses(t *testing.T) {
	svc := testService(t, workflowledger.NewMemoryRepository(), &stubEngine{})
	out, err := findTool(t, svc, agenttools.ToolWorkflowDeliver).Execute(
		context.Background(), json.RawMessage(`{"run_id":"wfr-x"}`))
	if err != nil {
		t.Fatal(err)
	}
	var result agenttools.DeliverResult
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatal(err)
	}
	if !result.Refused || result.Reason == "" {
		t.Fatalf("deliver without allow_publish = %+v", result)
	}
}

func TestRunRequiresWorkflowName(t *testing.T) {
	svc := testService(t, workflowledger.NewMemoryRepository(), &stubEngine{})
	_, err := findTool(t, svc, agenttools.ToolWorkflowRun).Execute(
		context.Background(), json.RawMessage(`{}`))
	if err == nil || !strings.Contains(err.Error(), "workflow name") {
		t.Fatalf("error = %v, want workflow name required", err)
	}
}

func TestToolDescriptionsAreGeneric(t *testing.T) {
	svc := testService(t, workflowledger.NewMemoryRepository(), nil)
	bias := []string{"go test", "cmd/mivia", "github.com/MiviaLabs", "*.go", "golang"}
	for _, tool := range agenttools.Tools(svc) {
		text := tool.Description() + "\n" + flattenDescs(tool.Parameters())
		for _, b := range bias {
			if strings.Contains(strings.ToLower(text), strings.ToLower(b)) {
				t.Errorf("%s description contains language bias %q", tool.Name(), b)
			}
		}
		if tool.ResultBudgetBytes() <= 0 {
			t.Errorf("%s missing result budget", tool.Name())
		}
	}
}

func findTool(t *testing.T, svc *agenttools.Service, name string) agenttools.Tool {
	t.Helper()
	for _, tool := range agenttools.Tools(svc) {
		if tool.Name() == name {
			return tool
		}
	}
	t.Fatalf("tool %q not found", name)
	return nil
}

func flattenDescs(v any) string {
	var b strings.Builder
	switch x := v.(type) {
	case map[string]any:
		if d, ok := x["description"].(string); ok {
			b.WriteString(d)
			b.WriteByte('\n')
		}
		for _, child := range x {
			b.WriteString(flattenDescs(child))
		}
	case []any:
		for _, child := range x {
			b.WriteString(flattenDescs(child))
		}
	}
	return b.String()
}

type stubEngine struct{}

func (stubEngine) Start(context.Context, agenttools.StartRequest) (agenttools.StartResult, error) {
	return agenttools.StartResult{RunID: "wfr-stub", Status: "running"}, nil
}
func (stubEngine) Cancel(context.Context, string) (agenttools.CancelResult, error) {
	return agenttools.CancelResult{RunID: "wfr-stub", Status: "canceled"}, nil
}
func (stubEngine) Deliver(context.Context, string, bool) (agenttools.DeliverResult, error) {
	return agenttools.DeliverResult{RunID: "wfr-stub", Status: "succeeded"}, nil
}
