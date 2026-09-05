package controller

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/agentmsg"
	"github.com/MiviaLabs/mivia-agent/internal/coordinator"
	"github.com/MiviaLabs/mivia-agent/internal/evidencecheck"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

type stepHandler struct {
	out     json.RawMessage
	wait    bool
	seen    chan runtime.Request
	started chan struct{}
}

type inspectingCoordinator struct {
	coordinator.Coordinator
	ensure       coordinator.EnsureRunRequest
	badRunID     bool
	badTask      bool
	ensureErr    error
	rewriteRunID bool
	inspectErr   error
}

func (c *inspectingCoordinator) EnsureRun(ctx context.Context, req coordinator.EnsureRunRequest) (*coordinator.RunHandle, error) {
	c.ensure = req
	if c.ensureErr != nil {
		return nil, c.ensureErr
	}
	if c.rewriteRunID {
		req.RunID = coordinator.NewRunID()
	}
	return c.Coordinator.EnsureRun(ctx, req)
}

func (c *inspectingCoordinator) Inspect(ctx context.Context, handle *coordinator.RunHandle) (ledger.RunSnapshot, error) {
	if c.inspectErr != nil {
		return ledger.RunSnapshot{}, c.inspectErr
	}
	snap, err := c.Coordinator.Inspect(ctx, handle)
	if c.badRunID {
		snap.RunID = coordinator.NewRunID()
	}
	if c.badTask && len(snap.Tasks) > 0 {
		snap.Tasks[0].TaskID = "other-task"
	}
	return snap, err
}

func TestCoordinatorRunnerSurfacesCoordinatorBoundaryErrors(t *testing.T) {
	sentinel := errors.New("coordinator boundary failed")
	base := stepRunner(t, stepHandler{out: json.RawMessage(`{"ok":true}`)}).Coordinator
	for _, tc := range []struct {
		name  string
		coord *inspectingCoordinator
		want  string
	}{
		{name: "ensure", coord: &inspectingCoordinator{Coordinator: base, ensureErr: sentinel}, want: sentinel.Error()},
		{name: "returned run identity", coord: &inspectingCoordinator{Coordinator: base, rewriteRunID: true}, want: "coordinator returned run"},
		{name: "inspect", coord: &inspectingCoordinator{Coordinator: base, inspectErr: sentinel}, want: sentinel.Error()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spec := validStepRequest()
			spec.WorkflowRunID += "-" + strings.ReplaceAll(tc.name, " ", "-")
			result, err := NewCoordinatorRunner(tc.coord).RunStep(context.Background(), spec)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("result = %+v, error = %v, want %q", result, err, tc.want)
			}
			if result.TaskID != "task-1" {
				t.Fatalf("result identity = %+v", result)
			}
		})
	}
}

func TestCoordinatorRunnerRejectsIncompleteIdentity(t *testing.T) {
	spec := validStepRequest()
	spec.CoordinatorRunID = ""
	if _, err := stepRunner(t, stepHandler{}).RunStep(context.Background(), spec); err == nil {
		t.Fatal("step without a coordinator run ID was accepted")
	}
	if _, err := (*CoordinatorRunner)(nil).RunStep(context.Background(), validStepRequest()); err == nil {
		t.Fatal("nil runner was accepted")
	}
}

func (h stepHandler) Invoke(ctx context.Context, req runtime.Request) (json.RawMessage, error) {
	if h.seen != nil {
		h.seen <- req
	}
	if h.started != nil {
		h.started <- struct{}{}
	}
	if h.wait {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return h.out, nil
}

func stepRunner(t *testing.T, handler runtime.Handler) *CoordinatorRunner {
	t.Helper()
	d := runtime.New(runtime.Policy{})
	if err := d.Register(runtime.Subagent, "agent", handler); err != nil {
		t.Fatal(err)
	}
	p := subagents.New(d, subagents.Policy{Workers: 1})
	return NewCoordinatorRunner(coordinator.New(ledger.NewMemoryLedgerRepository(), p))
}

func validStepRequest() AgentStepRequest {
	return AgentStepRequest{
		WorkflowRunID: "wfr-run-1", StepID: "step-1", AttemptNo: 1, TaskID: "task-1", CoordinatorRunID: coordinator.NewRunID(),
		AgentName: "agent", AgentDigest: "sha256:agent", Scope: "read-only",
		Template: "task={{ inputs.task }} evidence={{ evidence.plan }}",
		Inputs:   map[string]any{"task": "build"}, Evidence: map[string]any{"plan": map[string]any{"ok": true}},
		MaxBindingBytes: 100, MaxContextBytes: 1000, Timeout: time.Second, Budget: 10,
		OutputSchema: map[string]any{"type": "object", "properties": map[string]any{"ok": map[string]any{"type": "boolean"}}, "required": []any{"ok"}, "additionalProperties": false},
	}
}

func TestCoordinatorRunnerRunsStepAndCapturesIdentity(t *testing.T) {
	runner := stepRunner(t, stepHandler{out: json.RawMessage(`{"ok":true}`)})
	got, err := runner.RunStep(context.Background(), validStepRequest())
	if err != nil {
		t.Fatal(err)
	}
	if got.CoordinatorRunID == "" || got.TaskID != "task-1" {
		t.Fatalf("identity = %+v", got)
	}
	if got.ValidatedOutput.(map[string]any)["ok"] != true {
		t.Fatalf("output = %#v", got.ValidatedOutput)
	}
	if len(got.EvidenceJSON) == 0 {
		t.Fatal("evidence selection is empty")
	}
}

func TestCoordinatorRunnerPropagatesRoutingAndLimits(t *testing.T) {
	seen := make(chan runtime.Request, 1)
	runner := stepRunner(t, stepHandler{out: json.RawMessage(`{"ok":true}`), seen: seen})
	spec := validStepRequest()
	spec.ProviderName = "provider-a"
	spec.Model = "model-a"
	spec.Skill = "skill-a"
	spec.Timeout = 3 * time.Second
	spec.Budget = 17
	ctx := runtime.ContextWithCaller(context.Background(), runtime.Caller{SessionID: "session-a", TurnID: "turn-a", Role: "role-a"})
	if _, err := runner.RunStep(ctx, spec); err != nil {
		t.Fatal(err)
	}
	req := <-seen
	if req.Scope != spec.Scope || req.AgentName != spec.AgentName || req.AgentDigest != spec.AgentDigest || req.ProviderName != spec.ProviderName || req.Model != spec.Model || req.Budget != spec.Budget {
		t.Fatalf("routing request = %+v", req)
	}
	if req.SessionID != "session-a" || req.TurnID != "turn-a" || req.Role != "role-a" {
		t.Fatalf("caller identity = %q/%q/%q", req.SessionID, req.TurnID, req.Role)
	}
}

func TestCoordinatorRunnerPropagatesExplicitForceResume(t *testing.T) {
	base := stepRunner(t, stepHandler{out: json.RawMessage(`{"ok":true}`)}).Coordinator
	observed := &inspectingCoordinator{Coordinator: base}
	runner := NewCoordinatorRunner(observed)
	spec := validStepRequest()
	spec.ForceResume = true
	if _, err := runner.RunStep(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	if !observed.ensure.ForceResume || observed.ensure.RunID != spec.CoordinatorRunID {
		t.Fatalf("ensure request = %+v", observed.ensure)
	}
}

func TestCoordinatorRunnerRejectsInspectedIdentityMismatch(t *testing.T) {
	for _, tc := range []struct {
		name     string
		badRunID bool
		badTask  bool
	}{
		{name: "run", badRunID: true},
		{name: "task", badTask: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base := stepRunner(t, stepHandler{out: json.RawMessage(`{"ok":true}`)}).Coordinator
			observed := &inspectingCoordinator{Coordinator: base, badRunID: tc.badRunID, badTask: tc.badTask}
			_, err := NewCoordinatorRunner(observed).RunStep(context.Background(), validStepRequest())
			if err == nil {
				t.Fatal("identity mismatch was accepted")
			}
		})
	}
}

func TestCoordinatorRunnerReturnsTypedSchemaFailure(t *testing.T) {
	runner := stepRunner(t, stepHandler{out: json.RawMessage(`{"ok":"no"}`)})
	_, err := runner.RunStep(context.Background(), validStepRequest())
	var schemaErr *SchemaValidationError
	if !errors.As(err, &schemaErr) {
		t.Fatalf("err = %v, want SchemaValidationError", err)
	}
}

func TestCoordinatorRunnerCancelsChild(t *testing.T) {
	started := make(chan struct{}, 1)
	runner := stepRunner(t, stepHandler{wait: true, started: started})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := runner.RunStep(ctx, validStepRequest()); done <- err }()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("child did not start")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("step did not stop after cancellation")
	}
}

func TestCoordinatorRunnerUsesStableIdempotencyKey(t *testing.T) {
	spec := validStepRequest()
	k1 := idempotencyKey(spec)
	k2 := idempotencyKey(spec)
	if k1 != k2 {
		t.Fatal("idempotency key is not stable")
	}
	other := spec
	other.AttemptNo++
	if idempotencyKey(spec) == idempotencyKey(other) {
		t.Fatal("attempts share an idempotency key")
	}
}

func TestCoordinatorRunnerResumesCompletedChildWithoutDuplicateDispatch(t *testing.T) {
	seen := make(chan runtime.Request, 2)
	runner := stepRunner(t, stepHandler{out: json.RawMessage(`{"ok":true}`), seen: seen})
	spec := validStepRequest()
	first, err := runner.RunStep(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	second, err := runner.RunStep(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	if first.CoordinatorRunID != second.CoordinatorRunID || first.TaskID != second.TaskID {
		t.Fatalf("child identity changed: first=%+v second=%+v", first, second)
	}
	if string(first.Output) != string(second.Output) {
		t.Fatalf("recovered output = %q, want %q", second.Output, first.Output)
	}
	select {
	case <-seen:
	default:
		t.Fatal("first dispatch was not observed")
	}
	select {
	case req := <-seen:
		t.Fatalf("duplicate dispatch observed: %+v", req)
	default:
	}
}

func TestCoordinatorRunnerUsesExplicitPromptVerbatim(t *testing.T) {
	seen := make(chan runtime.Request, 1)
	runner := stepRunner(t, stepHandler{out: json.RawMessage(`{"ok":true}`), seen: seen})
	spec := validStepRequest()
	const want = "You are the plan reviewer.\n\nEvidence refs:\n- plan @ attempt 1 (sha256:abc, 1234 bytes)\n- diff @ attempt 2 (sha256:def, 22 bytes)"
	spec.Prompt = want
	if _, err := runner.RunStep(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	req := <-seen
	var got string
	if err := json.Unmarshal(req.Input, &got); err != nil {
		t.Fatalf("task input is not a JSON string: %s", req.Input)
	}
	if got != want {
		t.Fatalf("task input = %q, want the exact prompt %q", got, want)
	}
}

func TestCoordinatorRunnerRendersTemplateWhenPromptEmpty(t *testing.T) {
	seen := make(chan runtime.Request, 1)
	runner := stepRunner(t, stepHandler{out: json.RawMessage(`{"ok":true}`), seen: seen})
	spec := validStepRequest()
	spec.Prompt = ""
	if _, err := runner.RunStep(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	req := <-seen
	var got string
	if err := json.Unmarshal(req.Input, &got); err != nil {
		t.Fatalf("task input is not a JSON string: %s", req.Input)
	}
	const want = `task=build evidence={"ok":true}`
	if got != want {
		t.Fatalf("task input = %q, want template-rendered prompt %q", got, want)
	}
}

func TestCoordinatorRunnerAcceptsNilAndEmptyEvidenceRefs(t *testing.T) {
	runner := stepRunner(t, stepHandler{out: json.RawMessage(`{"ok":true}`)})
	spec := validStepRequest()
	spec.EvidenceRefs = nil
	if _, err := runner.RunStep(context.Background(), spec); err != nil {
		t.Fatalf("nil EvidenceRefs: %v", err)
	}
	spec.EvidenceRefs = map[string]ArtifactRef{}
	if _, err := runner.RunStep(context.Background(), spec); err != nil {
		t.Fatalf("empty EvidenceRefs: %v", err)
	}
}

func TestRecordStepResultStoresChildIdentityAndEvidence(t *testing.T) {
	runner := stepRunner(t, stepHandler{out: json.RawMessage(`{"ok":true}`)})
	spec := validStepRequest()
	result, err := runner.RunStep(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	repo := workflowledger.NewMemoryRepository()
	run := spec.WorkflowRunID
	if err := repo.CreateRun(context.Background(), workflowledger.RunSnapshot{RunID: run, Status: workflowledger.RunStatusPending, ActiveStepID: spec.StepID}, func() []byte {
		b, _ := workflowledger.MarshalSnapshot(workflowledger.Snapshot{SchemaVersion: 1, DefinitionTOML: []byte("x"), DefinitionDigest: "d"})
		return b
	}()); err != nil {
		t.Fatal(err)
	}
	attempt := workflowledger.StepAttempt{AttemptID: "attempt-1", RunID: run, StepID: spec.StepID, AttemptNo: 1}
	if err := RecordStepResult(context.Background(), repo, attempt, result, workflowledger.AttemptStatusSucceeded); err != nil {
		t.Fatal(err)
	}
	got, err := repo.GetStepAttempt(context.Background(), run, attempt.AttemptID)
	if err != nil {
		t.Fatal(err)
	}
	if got.CoordinatorRunID != result.CoordinatorRunID || got.TaskID != result.TaskID {
		t.Fatalf("child identity = %q/%q", got.CoordinatorRunID, got.TaskID)
	}
	if len(got.EvidenceJSON) == 0 {
		t.Fatal("evidence selection was not stored")
	}
}

func TestValidateOutput_EvidenceClaims(t *testing.T) {
	emptyReport := json.RawMessage(`"# Agent Report\n\nFormat: mivia-report/v1\n\n## Summary\n\nAll good.\n"`)
	if _, err := validateOutput("step-1", emptyReport, nil); err != nil {
		t.Fatalf("expected empty report to pass, got %v", err)
	}

	validReport := json.RawMessage(`"# Agent Report\n\nFormat: mivia-report/v1\n\n## Evidence\n\n- make verify: PASS\n"`)
	if _, err := validateOutput("step-1", validReport, nil); err != nil {
		t.Fatalf("expected valid report to pass syntax validation, got %v", err)
	}

	invalidReport := json.RawMessage(`"# Agent Report\n\nFormat: mivia-report/v1\n\n## Evidence\n\n- : PASS\n"`)
	if _, err := validateOutput("step-1", invalidReport, nil); err == nil {
		t.Fatal("expected invalid empty command claim in report to fail")
	}

	// Test object-wrapped report
	objReport := json.RawMessage(`{"report": "# Agent Report\n\nFormat: mivia-report/v1\n\n## Evidence\n\n- : PASS\n"}`)
	if _, err := validateOutput("step-1", objReport, nil); err == nil {
		t.Fatal("expected invalid empty command claim in object report to fail")
	}

	// Test ValidateReportEvidence cross-check
	history := []evidencecheck.ToolExecutionRecord{
		{ToolName: "run_command", Argv: []string{"make", "verify"}, ExitCode: 0},
	}
	if err := ValidateReportEvidence("# Agent Report\n\nFormat: mivia-report/v1\n\n## Evidence\n\n- make verify: PASS\n", history); err != nil {
		t.Fatalf("expected executed claim to validate, got %v", err)
	}

	if err := ValidateReportEvidence("# Agent Report\n\nFormat: mivia-report/v1\n\n## Evidence\n\n- make test: PASS\n", history); err == nil {
		t.Fatal("expected unexecuted claim to fail ValidateReportEvidence")
	}
}

func TestCoordinatorRunner_EvidenceCrossCheck(t *testing.T) {
	// Report claiming "make verify: PASS" without any recorded tool executions in the coordinator ledger.
	unexecutedReport := json.RawMessage(`{"output": {"report": "# Agent Report\n\nFormat: mivia-report/v1\n\n## Evidence\n\n- make verify: PASS\n"}, "status": "completed"}`)
	runner := stepRunner(t, stepHandler{out: unexecutedReport})

	spec := validStepRequest()
	spec.OutputSchema = nil
	spec.CoordinatorRunID = coordinator.NewRunID()
	_, err := runner.RunStep(context.Background(), spec)
	if err == nil {
		t.Fatal("expected RunStep to fail when report contains unexecuted PASS claim")
	}
	var schemaErr *SchemaValidationError
	if !errors.As(err, &schemaErr) || schemaErr.Err == nil || !strings.Contains(schemaErr.Err.Error(), "evidence verification failed") {
		t.Fatalf("expected evidence verification error, got: %v (wrapped: %v)", err, errors.Unwrap(err))
	}
}

// evidencePostingHandler posts a run-message FINDING claiming a passing
// command, exactly as a child agent can through post_message. It is the spoof
// the evidence gate must refuse: the child is authoring its own audit trail.
type evidencePostingHandler struct {
	coord coordinator.Coordinator
	runID string
	out   json.RawMessage
}

func (h *evidencePostingHandler) Invoke(ctx context.Context, req runtime.Request) (json.RawMessage, error) {
	msg, _ := agentmsg.NewMessage(h.runID, agentmsg.KindFinding, agentmsg.Party{TaskID: "task-1"}, agentmsg.Party{}, `{"tool_name":"run_command","argv":["make","verify"],"exit_code":0}`, nil, agentmsg.Options{})
	_ = h.coord.PostTaskMessage(ctx, h.runID, "task-1", msg)
	return h.out, nil
}

// recordedExecutionHandler drives the HOST's own recording seam, the tool-call
// sink the coordinator installs on every task context. This is what a real
// agent loop's tool_start/tool_end events land in, and it is the only source
// the evidence gate trusts.
type recordedExecutionHandler struct {
	argv     []string
	endOut   string
	out      json.RawMessage
	recorded bool
}

func (h *recordedExecutionHandler) Invoke(ctx context.Context, req runtime.Request) (json.RawMessage, error) {
	if sink, ok := subagents.ToolCallSinkFrom(ctx); ok {
		h.recorded = true
		input, _ := json.Marshal(map[string]any{"argv": h.argv})
		sink(subagents.ToolCallStep{ToolCallID: "call-1", Name: "run_command", Kind: "start", Input: string(input)})
		sink(subagents.ToolCallStep{ToolCallID: "call-1", Name: "run_command", Kind: "end", Output: h.endOut})
	}
	return h.out, nil
}

const evidencePassReport = `{"output": {"report": "# Agent Report\n\nFormat: mivia-report/v1\n\n## Evidence\n\n- make verify: PASS\n"}, "status": "completed"}`

// TestCoordinatorRunner_EvidenceCrossCheck_RefusesSelfAttestedFinding is the
// security contract, and it used to assert the opposite.
//
// The evidence gate cross-checks PASS claims against recorded executions, but
// it read those records from the run-message blackboard - which the audited
// child writes itself. A child that never ran `make verify` could post a
// finding whose body decoded as a ToolExecutionRecord with its own chosen
// exit_code, and the gate validated its report. The audit asked the audited
// party for its own evidence. Findings are now inadmissible.
func TestCoordinatorRunner_EvidenceCrossCheck_RefusesSelfAttestedFinding(t *testing.T) {
	d := runtime.New(runtime.Policy{})
	h := &evidencePostingHandler{out: json.RawMessage(evidencePassReport)}
	if err := d.Register(runtime.Subagent, "agent", h); err != nil {
		t.Fatal(err)
	}
	p := subagents.New(d, subagents.Policy{Workers: 1})
	coord := coordinator.New(ledger.NewMemoryLedgerRepository(), p)
	h.coord = coord
	runner := NewCoordinatorRunner(coord)

	spec := validStepRequest()
	spec.OutputSchema = nil
	spec.CoordinatorRunID = coordinator.NewRunID()
	h.runID = spec.CoordinatorRunID
	_, err := runner.RunStep(context.Background(), spec)
	if err == nil {
		t.Fatal("RunStep accepted a PASS claim backed only by the child's own finding")
	}
	var schemaErr *SchemaValidationError
	if !errors.As(err, &schemaErr) || schemaErr.Err == nil || !strings.Contains(schemaErr.Err.Error(), "evidence verification failed") {
		t.Fatalf("error = %v (wrapped: %v), want an evidence verification failure", err, errors.Unwrap(err))
	}
}

// TestCoordinatorRunner_EvidenceCrossCheck_AcceptsRecordedExecution is the
// other half: a PASS claim backed by the HOST's recorded run_command, exiting
// 0, still passes. Without this the fix above could pass by refusing
// everything.
func TestCoordinatorRunner_EvidenceCrossCheck_AcceptsRecordedExecution(t *testing.T) {
	d := runtime.New(runtime.Policy{})
	h := &recordedExecutionHandler{
		argv:   []string{"make", "verify"},
		endOut: "exit=0\nall gates passed\n",
		out:    json.RawMessage(evidencePassReport),
	}
	if err := d.Register(runtime.Subagent, "agent", h); err != nil {
		t.Fatal(err)
	}
	p := subagents.New(d, subagents.Policy{Workers: 1})
	coord := coordinator.New(ledger.NewMemoryLedgerRepository(), p)
	runner := NewCoordinatorRunner(coord)

	spec := validStepRequest()
	spec.OutputSchema = nil
	spec.CoordinatorRunID = coordinator.NewRunID()
	got, err := runner.RunStep(context.Background(), spec)
	if err != nil {
		t.Fatalf("RunStep rejected a PASS claim backed by a recorded passing command: %v", err)
	}
	if !h.recorded {
		t.Fatal("the task context carried no tool-call sink, so this test proved nothing")
	}
	if got.Status != "completed" {
		t.Fatalf("status = %q, want completed", got.Status)
	}
}

// TestCoordinatorRunner_EvidenceCrossCheck_RefusesRecordedFailure proves the
// recorded exit status is what decides: a command that really ran and really
// failed cannot be reported as PASS.
func TestCoordinatorRunner_EvidenceCrossCheck_RefusesRecordedFailure(t *testing.T) {
	d := runtime.New(runtime.Policy{})
	h := &recordedExecutionHandler{
		argv:   []string{"make", "verify"},
		endOut: "FAIL internal/x\nexit=2\n",
		out:    json.RawMessage(evidencePassReport),
	}
	if err := d.Register(runtime.Subagent, "agent", h); err != nil {
		t.Fatal(err)
	}
	p := subagents.New(d, subagents.Policy{Workers: 1})
	coord := coordinator.New(ledger.NewMemoryLedgerRepository(), p)
	runner := NewCoordinatorRunner(coord)

	spec := validStepRequest()
	spec.OutputSchema = nil
	spec.CoordinatorRunID = coordinator.NewRunID()
	if _, err := runner.RunStep(context.Background(), spec); err == nil {
		t.Fatal("RunStep accepted a PASS claim for a command recorded as exiting 2")
	}
}
