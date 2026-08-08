package agenttools_test

import (
	"bytes"
	"context"
	"encoding/json"
	"math"
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

func TestInspectValidatesOffsetAndLimit(t *testing.T) {
	repo := workflowledger.NewMemoryRepository()
	runID := "wfr-inspect-page-validate"
	seedRunningAttempt(t, repo, runID)
	svc := testService(t, repo, nil)
	ctx := context.Background()

	if _, err := svc.Inspect(ctx, runID, "one", 1, -1, 0); err == nil || !strings.Contains(err.Error(), "offset") {
		t.Fatalf("negative offset error = %v, want offset validation", err)
	}
	if _, err := svc.Inspect(ctx, runID, "one", 1, 0, -1); err == nil || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("negative limit error = %v, want limit validation", err)
	}
}

func TestInspectClampsLimitAndDefaultsPageSize(t *testing.T) {
	repo := workflowledger.NewMemoryRepository()
	runID := "wfr-inspect-page-clamp"
	// Larger than the page ceiling so the paging path is active.
	blob := bytes.Repeat([]byte("a"), agenttools.DefaultInspectPageBytes+64)
	seedRunningAttemptWithOutput(t, repo, runID, blob)
	svc := testService(t, repo, nil)
	ctx := context.Background()

	// limit 0 defaults to DefaultInspectPageBytes.
	view, err := svc.Inspect(ctx, runID, "one", 1, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(view.OutputText) != agenttools.DefaultInspectPageBytes {
		t.Fatalf("default page length = %d, want %d", len(view.OutputText), agenttools.DefaultInspectPageBytes)
	}
	if view.OutputNextOffset != agenttools.DefaultInspectPageBytes {
		t.Fatalf("default page next offset = %d, want %d", view.OutputNextOffset, agenttools.DefaultInspectPageBytes)
	}

	// A limit above the page ceiling clamps to DefaultInspectPageBytes.
	view, err = svc.Inspect(ctx, runID, "one", 1, 0, agenttools.DefaultInspectPageBytes*10)
	if err != nil {
		t.Fatal(err)
	}
	if len(view.OutputText) != agenttools.DefaultInspectPageBytes {
		t.Fatalf("clamped page length = %d, want %d", len(view.OutputText), agenttools.DefaultInspectPageBytes)
	}
	if view.OutputNextOffset != agenttools.DefaultInspectPageBytes {
		t.Fatalf("clamped page next offset = %d, want %d", view.OutputNextOffset, agenttools.DefaultInspectPageBytes)
	}
	if view.Output != nil {
		t.Fatalf("paged view unexpectedly carries full parsed output: %#v", view.Output)
	}
	if view.OutputBytes != len(blob) {
		t.Fatalf("OutputBytes = %d, want %d", view.OutputBytes, len(blob))
	}
}

func TestInspectPagesOutputTextWithNextOffset(t *testing.T) {
	repo := workflowledger.NewMemoryRepository()
	runID := "wfr-inspect-page"
	blob := bytes.Repeat([]byte("abcdefghij"), 30) // 300 bytes
	seedRunningAttemptWithOutput(t, repo, runID, blob)
	svc := testService(t, repo, nil)
	ctx := context.Background()

	view, err := svc.Inspect(ctx, runID, "one", 1, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if view.OutputText != string(blob[0:100]) {
		t.Fatalf("page 0 text = %q, want %q", view.OutputText, blob[0:100])
	}
	if view.OutputOffset != 0 || view.OutputBytes != len(blob) {
		t.Fatalf("page 0 framing = offset %d bytes %d, want 0/%d", view.OutputOffset, view.OutputBytes, len(blob))
	}
	if view.OutputNextOffset != 100 {
		t.Fatalf("page 0 next offset = %d, want 100", view.OutputNextOffset)
	}
	if view.Output != nil {
		t.Fatalf("paged page 0 unexpectedly carries full parsed output: %#v", view.Output)
	}

	view, err = svc.Inspect(ctx, runID, "one", 1, 100, 100)
	if err != nil {
		t.Fatal(err)
	}
	if view.OutputText != string(blob[100:200]) {
		t.Fatalf("page 1 text = %q, want %q", view.OutputText, blob[100:200])
	}
	if view.OutputOffset != 100 || view.OutputNextOffset != 200 {
		t.Fatalf("page 1 framing = offset %d next %d, want 100/200", view.OutputOffset, view.OutputNextOffset)
	}

	// Final page: OutputNextOffset is 0 (exhausted) and omitted by omitempty.
	view, err = svc.Inspect(ctx, runID, "one", 1, 200, 100)
	if err != nil {
		t.Fatal(err)
	}
	if view.OutputText != string(blob[200:300]) {
		t.Fatalf("page 2 text = %q, want %q", view.OutputText, blob[200:300])
	}
	if view.OutputOffset != 200 || view.OutputNextOffset != 0 {
		t.Fatalf("page 2 framing = offset %d next %d, want 200/0", view.OutputOffset, view.OutputNextOffset)
	}
}

func TestInspectRefusesAbovePageableCeiling(t *testing.T) {
	repo := workflowledger.NewMemoryRepository()
	runID := "wfr-inspect-ceiling"
	blob := bytes.Repeat([]byte("x"), agenttools.MaxPageableBytes+1)
	seedRunningAttemptWithOutput(t, repo, runID, blob)
	svc := testService(t, repo, nil)
	ctx := context.Background()

	_, err := svc.Inspect(ctx, runID, "one", 1, 0, 0)
	if err == nil {
		t.Fatal("inspect of an artifact above the pageable ceiling: expected refusal")
	}
	if !strings.Contains(err.Error(), "ceiling") {
		t.Fatalf("ceiling refusal = %q, want ceiling wording", err.Error())
	}
	if !strings.Contains(err.Error(), "sha256:") {
		t.Fatalf("ceiling refusal = %q, want the output ref named", err.Error())
	}
}

func TestInspectBudgetGuardHalvesPageOnce(t *testing.T) {
	repo := workflowledger.NewMemoryRepository()
	runID := "wfr-inspect-budget"
	blob := bytes.Repeat([]byte("a"), agenttools.DefaultInspectPageBytes*2)
	seedRunningAttemptWithOutput(t, repo, runID, blob)
	// A tight inspect budget that a full default page (64 KiB of text) would
	// exceed but a halved page (32 KiB) fits: the guard must halve once and
	// rebuild, never fail closed on framing.
	ctx := context.Background()
	svc, err := agenttools.NewService(agenttools.ServiceOptions{
		Repo: func(context.Context) (workflowledger.Repository, func(), error) {
			return repo, func() {}, nil
		},
		InspectBudgetBytes: 48 << 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	view, err := svc.Inspect(ctx, runID, "one", 1, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	want := agenttools.DefaultInspectPageBytes / 2
	if len(view.OutputText) != want {
		t.Fatalf("budget-guarded page length = %d, want %d (page halved once)", len(view.OutputText), want)
	}
	if view.OutputNextOffset != want {
		t.Fatalf("budget-guarded next offset = %d, want %d", view.OutputNextOffset, want)
	}
	encoded, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) > 48<<10 {
		t.Fatalf("budget-guarded view marshals to %d bytes, still over budget %d", len(encoded), 48<<10)
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

// TestListRunsHugeLimitDoesNotOverflow: the list_runs schema admits limit up to
// MaxInt64 with no maximum, so offset+limit can wrap negative, which used to
// slice runs[1:negative] and panic. A huge limit must clamp to the remainder
// after offset, never panic.
func TestListRunsHugeLimitDoesNotOverflow(t *testing.T) {
	repo := workflowledger.NewMemoryRepository()
	seedRunningAttempt(t, repo, "wfr-list-huge-1")
	seedRunningAttempt(t, repo, "wfr-list-huge-2")
	svc := testService(t, repo, nil)
	ctx := context.Background()
	all, err := svc.ListRuns(ctx, "", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	page, err := svc.ListRuns(ctx, "", math.MaxInt, 1)
	if err != nil {
		t.Fatal(err)
	}
	if page.Count != all.Count-1 {
		t.Fatalf("ListRuns(MaxInt, 1) Count = %d, want %d (remainder after offset 1)", page.Count, all.Count-1)
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
func (stubEngine) Delete(context.Context, string) (agenttools.DeleteResult, error) {
	return agenttools.DeleteResult{RunID: "wfr-stub", Status: "succeeded", Deleted: true}, nil
}
