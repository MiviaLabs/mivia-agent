package localengine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/events"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/controller"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/delivery"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// This file covers the terminal-progress plumbing in engine_deliver.go
// (SetProgressSink, NewBusProgressSink, busProgressSink.Emit, localProgressKind,
// emitProgress, emitCanceledAttempts) and the abandonFence SetStepAttemptExecution
// forwarding in fence.go. The emission helpers are unexported, so these tests
// live in the internal test package and drive them both directly and through
// the real Engine.Cancel / Engine.Deliver flows.

const coverageDeliverTOML = `version = 1
name = "deliver-me"
initial_step = "one"

[inputs.task]
type = "string"
required = true
max_bytes = 100

[delivery]
kind = "pull_request"
mode = "draft"
provider = "github"
base = "main"

[[steps]]
id = "one"
kind = "agent"
agent = "one"
on_failure = "failure"

[[transitions]]
from = "one"
to = "success"
[transitions.match]
status = "succeeded"
`

const coverageTwoStepTOML = `version = 1
name = "two-step"
initial_step = "one"

[inputs.task]
type = "string"
required = true
max_bytes = 100

[[steps]]
id = "one"
kind = "agent"
agent = "one"
on_failure = "failure"

[[steps]]
id = "two"
kind = "agent"
agent = "two"
on_failure = "failure"

[[transitions]]
from = "one"
to = "two"
[transitions.match]
status = "succeeded"

[[transitions]]
from = "two"
to = "success"
[transitions.match]
status = "succeeded"
`

// coverageRecordingSink records every progress event delivered to it.
type coverageRecordingSink struct {
	mu     sync.Mutex
	events []controller.ProgressEvent
}

func (s *coverageRecordingSink) Emit(e controller.ProgressEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, e)
}

func (s *coverageRecordingSink) snapshot() []controller.ProgressEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]controller.ProgressEvent(nil), s.events...)
}

// coverageBusEventSink collects events published to an events.Bus.
type coverageBusEventSink struct {
	mu     sync.Mutex
	events []events.Event
}

func (s *coverageBusEventSink) HandleEvent(_ context.Context, ev events.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, ev)
}

func (s *coverageBusEventSink) snapshot() []events.Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]events.Event(nil), s.events...)
}

// coverageRecordingPR records PR boundary calls; FindByHead returns no
// existing PR. When failCreate is set, Create fails with that error.
type coverageRecordingPR struct {
	mu         sync.Mutex
	created    []delivery.PRInput
	failCreate error
}

func (r *coverageRecordingPR) FindByHead(context.Context, string, string) (*delivery.PRRef, error) {
	return nil, nil
}

func (r *coverageRecordingPR) IsMerged(context.Context, string, string) (bool, error) {
	return false, nil
}

func (r *coverageRecordingPR) Create(_ context.Context, _ string, in delivery.PRInput) (delivery.PRRef, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failCreate != nil {
		return delivery.PRRef{}, r.failCreate
	}
	r.created = append(r.created, in)
	return delivery.PRRef{RemoteID: strconv.Itoa(len(r.created)), URL: "https://example.com/pull/" + strconv.Itoa(len(r.created))}, nil
}

// coverageRecordingAttemptRepo records SetStepAttemptExecution forwarding.
type coverageRecordingAttemptRepo struct {
	workflowledger.Repository
	mu    sync.Mutex
	calls []string
}

func (r *coverageRecordingAttemptRepo) SetStepAttemptExecution(ctx context.Context, runID, attemptID, coordinatorRunID, taskID, reason string) error {
	r.mu.Lock()
	r.calls = append(r.calls, attemptID+"/"+coordinatorRunID+"/"+taskID+"/"+reason)
	r.mu.Unlock()
	return r.Repository.SetStepAttemptExecution(ctx, runID, attemptID, coordinatorRunID, taskID, reason)
}

func (r *coverageRecordingAttemptRepo) snapshotCalls() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.calls...)
}

// TestCoverageSetProgressSinkWiresPackageSink covers SetProgressSink and the
// emitProgress body: a zero timestamp must be defaulted before delivery, a
// caller-provided timestamp must pass through, and a nil sink must be a
// silent no-op.
func TestCoverageSetProgressSinkWiresPackageSink(t *testing.T) {
	sink := &coverageRecordingSink{}
	SetProgressSink(sink)
	defer func() { SetProgressSink(nil) }()

	// Zero timestamp: emitProgress must stamp it with the current time.
	emitProgress(controller.ProgressEvent{Kind: controller.ProgressRunFinished, RunID: "wfr-emit", Detail: "succeeded"})
	got := sink.snapshot()
	if len(got) != 1 {
		t.Fatalf("emitted events = %d, want 1", len(got))
	}
	if got[0].Timestamp.IsZero() {
		t.Fatal("zero timestamp was not defaulted by emitProgress")
	}
	if got[0].Kind != controller.ProgressRunFinished || got[0].RunID != "wfr-emit" || got[0].Detail != "succeeded" {
		t.Fatalf("event = %+v, want the delivered fields", got[0])
	}

	// Caller-provided timestamp: emitProgress must preserve it.
	ts := time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)
	emitProgress(controller.ProgressEvent{Kind: controller.ProgressStepCompleted, StepID: "build", Timestamp: ts})
	got = sink.snapshot()
	if len(got) != 2 {
		t.Fatalf("emitted events = %d, want 2", len(got))
	}
	if !got[1].Timestamp.Equal(ts) {
		t.Fatalf("timestamp = %v, want preserved %v", got[1].Timestamp, ts)
	}

	// Nil sink: emitProgress must not deliver anywhere.
	SetProgressSink(nil)
	emitProgress(controller.ProgressEvent{Kind: controller.ProgressRunFinished, RunID: "wfr-noop"})
	if got := sink.snapshot(); len(got) != 2 {
		t.Fatalf("nil sink still received %d events, want 2", len(got))
	}
}

// TestCoverageBusProgressSinkMapsAndPublishesKinds covers NewBusProgressSink,
// busProgressSink.Emit, and localProgressKind: step_completed maps to
// KindWorkflowStepCompleted, run_finished and run_failed map to
// KindWorkflowRunFinished, and an unmapped kind falls back to a heartbeat
// tick. The published event must carry the workflow name and full
// run/step/attempt/task attribution metadata.
func TestCoverageBusProgressSinkMapsAndPublishesKinds(t *testing.T) {
	bus := events.New()
	defer bus.Close()
	collected := &coverageBusEventSink{}
	bus.Subscribe(events.KindWorkflowStepCompleted, collected)
	bus.Subscribe(events.KindWorkflowRunFinished, collected)
	bus.Subscribe(events.KindWorkflowStepHeartbeat, collected)

	sink := NewBusProgressSink(bus)
	ts := time.Date(2025, 3, 4, 5, 6, 7, 0, time.UTC)

	sink.Emit(controller.ProgressEvent{
		Kind: controller.ProgressStepCompleted, RunID: "wfr-bus", StepID: "build",
		AttemptNo: 2, TaskID: "task-9", CoordinatorRunID: "coord-9",
		Detail: "succeeded", Timestamp: ts,
	})
	sink.Emit(controller.ProgressEvent{Kind: controller.ProgressRunFinished, RunID: "wfr-bus", Detail: "succeeded"})
	sink.Emit(controller.ProgressEvent{Kind: controller.ProgressRunFailed, RunID: "wfr-bus", Detail: "failed"})
	sink.Emit(controller.ProgressEvent{Kind: controller.ProgressPanelRefused, RunID: "wfr-bus", StepID: "panel", Detail: "refused"})

	bus.Flush()
	got := collected.snapshot()
	if len(got) != 4 {
		t.Fatalf("published events = %d, want 4: %+v", len(got), got)
	}

	// Each kind subscription delivers on its own goroutine, so match events by
	// kind instead of relying on delivery order.
	var step, runFinished, runFailed, fallback *events.Event
	for i := range got {
		switch got[i].Kind {
		case events.KindWorkflowStepCompleted:
			step = &got[i]
		case events.KindWorkflowRunFinished:
			if runFinished == nil && got[i].Detail == "succeeded" {
				runFinished = &got[i]
			} else if runFailed == nil && got[i].Detail == "failed" {
				runFailed = &got[i]
			}
		case events.KindWorkflowStepHeartbeat:
			fallback = &got[i]
		}
	}
	if step == nil || runFinished == nil || runFailed == nil || fallback == nil {
		t.Fatalf("published events missing a mapped kind: %+v", got)
	}

	if step.Name != "workflow" || step.AgentName != "workflow:build" || step.AgentTask != "task-9" || step.Detail != "succeeded" || !step.Timestamp.Equal(ts) {
		t.Fatalf("step event = %+v", step)
	}
	if step.Metadata["run_id"] != "wfr-bus" || step.Metadata["step"] != "build" || step.Metadata["attempt"] != "2" || step.Metadata["coordinator_run_id"] != "coord-9" || step.Metadata["task_id"] != "task-9" {
		t.Fatalf("step event metadata = %v", step.Metadata)
	}

	if runFinished.Detail != "succeeded" || runFinished.Name != "workflow" || runFinished.AgentName != "workflow:" {
		t.Fatalf("run_finished event = %+v", runFinished)
	}
	if runFailed.Detail != "failed" {
		t.Fatalf("run_failed event = %+v", runFailed)
	}
	if fallback.Detail != "refused" || fallback.AgentName != "workflow:panel" {
		t.Fatalf("unmapped-kind fallback event = %+v", fallback)
	}

	// A nil bus is a silent no-op for Emit.
	NewBusProgressSink(nil).Emit(controller.ProgressEvent{Kind: controller.ProgressRunFinished, RunID: "wfr-nil-bus"})
}

// TestCoverageBusProgressSinkPublishesDeliveryKinds covers the delivery kind
// mapping in localProgressKind: ProgressDeliveryStage and
// ProgressDeliveryRefused both publish as KindWorkflowDeliveryStage with the
// synthetic "deliver" step attribution and the stage/refusal reason in Detail.
func TestCoverageBusProgressSinkPublishesDeliveryKinds(t *testing.T) {
	bus := events.New()
	defer bus.Close()
	collected := &coverageBusEventSink{}
	bus.Subscribe(events.KindWorkflowDeliveryStage, collected)

	sink := NewBusProgressSink(bus)
	sink.Emit(controller.ProgressEvent{Kind: controller.ProgressDeliveryStage, RunID: "wfr-dlv", StepID: "deliver", Detail: "push: push branch wf/x to origin"})
	sink.Emit(controller.ProgressEvent{Kind: controller.ProgressDeliveryRefused, RunID: "wfr-dlv", StepID: "deliver", Detail: "delivery requires allow_publish=true"})

	bus.Flush()
	got := collected.snapshot()
	if len(got) != 2 {
		t.Fatalf("published events = %d, want 2: %+v", len(got), got)
	}
	var stage, refused *events.Event
	for i := range got {
		if got[i].Kind != events.KindWorkflowDeliveryStage {
			t.Fatalf("event kind = %q, want workflow_delivery_stage: %+v", got[i].Kind, got[i])
		}
		if strings.HasPrefix(got[i].Detail, "push:") {
			stage = &got[i]
		} else {
			refused = &got[i]
		}
	}
	if stage == nil || refused == nil {
		t.Fatalf("missing a delivery event: %+v", got)
	}
	if stage.AgentName != "workflow:deliver" || stage.Detail != "push: push branch wf/x to origin" {
		t.Fatalf("delivery stage event = %+v", stage)
	}
	if refused.AgentName != "workflow:deliver" || !strings.Contains(refused.Detail, "allow_publish") {
		t.Fatalf("delivery refused event = %+v", refused)
	}
}

// TestCoverageEmitCanceledAttemptsPublishesPerAttempt covers the
// emitCanceledAttempts loop body: one step_completed(canceled) event per
// settled attempt, carrying the attempt's step/attempt/task/coordinator
// identity.
func TestCoverageEmitCanceledAttemptsPublishesPerAttempt(t *testing.T) {
	sink := &coverageRecordingSink{}
	SetProgressSink(sink)
	defer func() { SetProgressSink(nil) }()

	attempts := []workflowledger.StepAttempt{
		{RunID: "wfr-cancel", StepID: "one", AttemptNo: 1, TaskID: "task-1", CoordinatorRunID: "coord-1"},
		{RunID: "wfr-cancel", StepID: "two", AttemptNo: 2, TaskID: "task-2", CoordinatorRunID: "coord-2"},
	}
	emitCanceledAttempts("wfr-cancel", attempts)

	got := sink.snapshot()
	if len(got) != 2 {
		t.Fatalf("emitted events = %d, want 2: %+v", len(got), got)
	}
	if got[0].Kind != controller.ProgressStepCompleted || got[0].RunID != "wfr-cancel" || got[0].StepID != "one" || got[0].AttemptNo != 1 || got[0].TaskID != "task-1" || got[0].CoordinatorRunID != "coord-1" || got[0].Detail != "canceled" {
		t.Fatalf("first canceled event = %+v", got[0])
	}
	if got[1].Kind != controller.ProgressStepCompleted || got[1].RunID != "wfr-cancel" || got[1].StepID != "two" || got[1].AttemptNo != 2 || got[1].TaskID != "task-2" || got[1].CoordinatorRunID != "coord-2" || got[1].Detail != "canceled" {
		t.Fatalf("second canceled event = %+v", got[1])
	}
}

// TestCoverageCancelFlowEmitsCanceledAttemptEvent drives Engine.Cancel over a
// run whose controller is NOT running in this engine (an orphaned run: the
// ledger holds the run in running status with an in-flight attempt, e.g. a
// controller that crashed or a run admitted by another host). The engine
// settles it through controller.CancelRunWithAttempts and reports one
// step_completed(canceled) event per settled attempt via emitCanceledAttempts,
// so hosts observing the engine see the operator cancel.
func TestCoverageCancelFlowEmitsCanceledAttemptEvent(t *testing.T) {
	repo := workflowledger.NewMemoryRepository()
	ctx := context.Background()
	raw, err := workflowledger.MarshalSnapshot(workflowledger.Snapshot{
		SchemaVersion:    workflowledger.SnapshotSchemaVersion,
		DefinitionTOML:   []byte(coverageTwoStepTOML),
		DefinitionDigest: "digest",
		Inputs:           map[string]string{"task": "x"},
	})
	if err != nil {
		t.Fatal(err)
	}
	snap := workflowledger.RunSnapshot{
		RunID: "wfr-cov-cancel", WorkflowName: "two-step", WorkflowDigest: "digest",
		ActiveStepID: "one", BaseRef: "main", BaseCommit: "deadbeef",
		Status: workflowledger.RunStatusPending,
	}
	if err := repo.CreateRun(ctx, snap, raw); err != nil {
		t.Fatal(err)
	}
	cur, err := repo.GetRun(ctx, snap.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CompareAndSetRunStatus(ctx, snap.RunID, cur.Version, workflowledger.RunStatusRunning, nil); err != nil {
		t.Fatal(err)
	}
	attempt := workflowledger.StepAttempt{
		RunID: snap.RunID, StepID: "one", AttemptID: "wfa-cov-1", AttemptNo: 1,
		CoordinatorRunID: "coord-cov", TaskID: "task-cov", Status: workflowledger.AttemptStatusRunning,
	}
	if err := repo.CreateStepAttempt(ctx, attempt); err != nil {
		t.Fatal(err)
	}

	sink := &coverageRecordingSink{}
	SetProgressSink(sink)
	defer func() { SetProgressSink(nil) }()

	engine := &Engine{Repo: repo}
	res, err := engine.Cancel(ctx, snap.RunID)
	if err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if res.Status != string(workflowledger.RunStatusCanceled) {
		t.Fatalf("cancel result status = %q, want canceled", res.Status)
	}

	got := sink.snapshot()
	if len(got) != 1 {
		t.Fatalf("emitted events = %d, want 1: %+v", len(got), got)
	}
	ev := got[0]
	if ev.Kind != controller.ProgressStepCompleted || ev.Detail != "canceled" || ev.RunID != snap.RunID || ev.StepID != "one" || ev.AttemptNo != 1 || ev.TaskID != "task-cov" || ev.CoordinatorRunID != "coord-cov" {
		t.Fatalf("canceled event = %+v", ev)
	}

	fresh, err := repo.GetRun(ctx, snap.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.Status != workflowledger.RunStatusCanceled {
		t.Fatalf("run status after cancel = %q, want canceled", fresh.Status)
	}
}

// TestCoverageDeliverEmitsStageEvents drives Engine.Deliver through the real
// git-run path (resolve worktree, commit, push, PR) with the package progress
// sink wired and asserts the delivery publishes one workflow_delivery_stage
// progress event per numbered stage, attributed to the run and the synthetic
// "deliver" step. The first stage is the entry guard.
func TestCoverageDeliverEmitsStageEvents(t *testing.T) {
	repoRoot, originURL := coverageDeliveryRepo(t)
	repo := workflowledger.NewMemoryRepository()
	run := coverageSeededPendingRun(t, repoRoot, originURL, repo)

	sink := &coverageRecordingSink{}
	SetProgressSink(sink)
	defer func() { SetProgressSink(nil) }()

	engine := &Engine{WorkspaceRoot: repoRoot, Repo: repo, PR: &coverageRecordingPR{}}
	res, err := engine.Deliver(context.Background(), run.RunID, true)
	if err != nil {
		t.Fatalf("Engine.Deliver: %v", err)
	}
	if res.Status != string(workflowledger.RunStatusSucceeded) {
		t.Fatalf("deliver result = %+v, want succeeded", res)
	}
	got := sink.snapshot()
	var stages []string
	for _, ev := range got {
		if ev.Kind == controller.ProgressDeliveryStage && ev.RunID == run.RunID {
			if ev.StepID != "deliver" {
				t.Fatalf("stage event step = %q, want deliver", ev.StepID)
			}
			stages = append(stages, ev.Detail)
		}
	}
	if len(stages) == 0 {
		t.Fatalf("no delivery stage events published: %+v", got)
	}
	if !strings.HasPrefix(stages[0], "guard:") {
		t.Fatalf("first stage = %q, want the guard stage", stages[0])
	}
}

// TestCoverageDeliverRefusalEmitsRefusedEvent pins the engine-level refusal:
// Engine.Deliver with allow_publish=false publishes exactly one
// delivery_refused progress event and refuses without touching the run.
func TestCoverageDeliverRefusalEmitsRefusedEvent(t *testing.T) {
	repo := workflowledger.NewMemoryRepository()
	sink := &coverageRecordingSink{}
	SetProgressSink(sink)
	defer func() { SetProgressSink(nil) }()

	engine := &Engine{Repo: repo}
	res, err := engine.Deliver(context.Background(), "wfr-refuse", false)
	if err != nil {
		t.Fatalf("Engine.Deliver refusal: %v", err)
	}
	if !res.Refused {
		t.Fatalf("deliver result = %+v, want Refused", res)
	}
	got := sink.snapshot()
	if len(got) != 1 {
		t.Fatalf("emitted events = %d, want 1: %+v", len(got), got)
	}
	if got[0].Kind != controller.ProgressDeliveryRefused || got[0].RunID != "wfr-refuse" || got[0].StepID != "deliver" {
		t.Fatalf("refused event = %+v, want delivery_refused for the run", got[0])
	}
}

// TestCoverageDeliverPolicyInactiveEmitsRefusedEvent drives deliverPending with
// a workflow that declares no [delivery] policy: the attempt is refused before
// any git work, the run stays delivery_pending, and one delivery_refused
// progress event is published.
func TestCoverageDeliverPolicyInactiveEmitsRefusedEvent(t *testing.T) {
	repo := workflowledger.NewMemoryRepository()
	ctx := context.Background()
	raw, err := workflowledger.MarshalSnapshot(workflowledger.Snapshot{
		SchemaVersion:    workflowledger.SnapshotSchemaVersion,
		DefinitionTOML:   []byte(coverageTwoStepTOML), // no [delivery] policy
		DefinitionDigest: "digest",
		Inputs:           map[string]string{"task": "x"},
	})
	if err != nil {
		t.Fatal(err)
	}
	snap := workflowledger.RunSnapshot{
		RunID: "wfr-nopolicy", WorkflowName: "two-step", WorkflowDigest: "digest",
		ActiveStepID: "success", BaseRef: "main", BaseCommit: "deadbeef",
		Status: workflowledger.RunStatusPending,
	}
	if err := repo.CreateRun(ctx, snap, raw); err != nil {
		t.Fatal(err)
	}
	// pending -> running -> delivery_pending, mirroring the ledger's status
	// transitions for a workflow body that finished and entered delivery.
	for _, status := range []workflowledger.RunStatus{
		workflowledger.RunStatusRunning,
		workflowledger.RunStatusDeliveryPending,
	} {
		cur, err := repo.GetRun(ctx, snap.RunID)
		if err != nil {
			t.Fatal(err)
		}
		if err := repo.CompareAndSetRunStatus(ctx, snap.RunID, cur.Version, status, nil); err != nil {
			t.Fatal(err)
		}
	}

	sink := &coverageRecordingSink{}
	SetProgressSink(sink)
	defer func() { SetProgressSink(nil) }()

	engine := &Engine{Repo: repo}
	if _, err := engine.Deliver(ctx, snap.RunID, true); err == nil {
		t.Fatal("Deliver with an inactive delivery policy must error")
	}
	got := sink.snapshot()
	var refused bool
	for _, ev := range got {
		if ev.Kind == controller.ProgressDeliveryRefused && ev.RunID == snap.RunID {
			refused = true
			if !strings.Contains(ev.Detail, "policy is not active") {
				t.Fatalf("refused event detail = %q, want the policy-inactive reason", ev.Detail)
			}
		}
	}
	if !refused {
		t.Fatalf("no delivery_refused event published for the policy-inactive run: %+v", got)
	}
}

// TestCoverageDeliverCompletionEmitsRunFinishedEvent drives Engine.Deliver
// through the real git-run path (resolve worktree, verify git dir, commit the
// intended diff, push to origin, open a PR) with the package progress sink
// wired, and asserts the completion path publishes exactly one
// run_finished(succeeded) event.
func TestCoverageDeliverCompletionEmitsRunFinishedEvent(t *testing.T) {
	repoRoot, originURL := coverageDeliveryRepo(t)
	repo := workflowledger.NewMemoryRepository()
	run := coverageSeededPendingRun(t, repoRoot, originURL, repo)

	sink := &coverageRecordingSink{}
	SetProgressSink(sink)
	defer func() { SetProgressSink(nil) }()

	engine := &Engine{WorkspaceRoot: repoRoot, Repo: repo, PR: &coverageRecordingPR{}}
	res, err := engine.Deliver(context.Background(), run.RunID, true)
	if err != nil {
		t.Fatalf("Engine.Deliver: %v", err)
	}
	if res.Status != string(workflowledger.RunStatusSucceeded) {
		t.Fatalf("deliver result = %+v, want succeeded", res)
	}
	fresh, err := repo.GetRun(context.Background(), run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.Status != workflowledger.RunStatusSucceeded {
		t.Fatalf("run status = %q, want succeeded", fresh.Status)
	}

	got := sink.snapshot()
	var finished int
	for _, ev := range got {
		if ev.Kind == controller.ProgressRunFinished && ev.RunID == run.RunID && ev.Detail == "succeeded" {
			finished++
		}
	}
	if finished != 1 {
		t.Fatalf("run_finished(succeeded) events = %d, want 1: %+v", finished, got)
	}
	// The push stage must have published the branch to the origin remote.
	if out := runGit(t, originURL, "show-ref", "--verify", "--hash", "refs/heads/wf/wt-test"); out == "" {
		t.Fatal("branch wf/wt-test was not pushed to origin")
	}
}

// TestCoverageDeliverFailurePRCreateErrorReportsError drives Engine.Deliver to
// a failure at the result-reporting boundary: the git-run stages succeed and
// the branch is pushed, but the PR create fails. The error must propagate as
// a plain error, the run must stay delivery_pending (a transient failure, not
// a permanent refusal), and no run_finished event may be published.
func TestCoverageDeliverFailurePRCreateErrorReportsError(t *testing.T) {
	repoRoot, originURL := coverageDeliveryRepo(t)
	repo := workflowledger.NewMemoryRepository()
	run := coverageSeededPendingRun(t, repoRoot, originURL, repo)

	sink := &coverageRecordingSink{}
	SetProgressSink(sink)
	defer func() { SetProgressSink(nil) }()

	engine := &Engine{
		WorkspaceRoot: repoRoot,
		Repo:          repo,
		PR:            &coverageRecordingPR{failCreate: errors.New("PR create failed")},
	}
	res, err := engine.Deliver(context.Background(), run.RunID, true)
	if err == nil {
		t.Fatalf("Deliver succeeded with a failing PR client: %+v", res)
	}
	fresh, getErr := repo.GetRun(context.Background(), run.RunID)
	if getErr != nil {
		t.Fatal(getErr)
	}
	if fresh.Status != workflowledger.RunStatusDeliveryPending {
		t.Fatalf("run status after failed deliver = %q, want delivery_pending", fresh.Status)
	}
	for _, ev := range sink.snapshot() {
		if ev.Kind == controller.ProgressRunFinished {
			t.Fatalf("failed delivery must not publish run_finished: %+v", ev)
		}
	}
}

// TestCoverageFenceSetStepAttemptExecutionForwardsAndFailsClosed covers the
// abandonFence SetStepAttemptExecution forwarding: a live run's write reaches
// the inner repository through the fence, and an abandoned run fails closed
// with ErrConflict without reaching the inner repository.
func TestCoverageFenceSetStepAttemptExecutionForwardsAndFailsClosed(t *testing.T) {
	inner := workflowledger.NewMemoryRepository()
	run := workflowledger.RunSnapshot{RunID: "wfr-fence-exec", Status: workflowledger.RunStatusPending, Version: 1}
	if err := inner.CreateRun(context.Background(), run, []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	attempt := workflowledger.StepAttempt{
		RunID: run.RunID, StepID: "build", AttemptID: "att-1", AttemptNo: 1,
		CoordinatorRunID: "coord-0", TaskID: "task-0",
	}
	if err := inner.CreateStepAttempt(context.Background(), attempt); err != nil {
		t.Fatal(err)
	}
	recording := &coverageRecordingAttemptRepo{Repository: inner}
	fence := newAbandonFence(recording)

	// A live run: the write forwards through the fence to the inner repo.
	if err := fence.SetStepAttemptExecution(context.Background(), run.RunID, "att-1", "coord-1", "task-1", "provider overloaded: retry"); err != nil {
		t.Fatalf("SetStepAttemptExecution on live run: %v", err)
	}
	if calls := recording.snapshotCalls(); len(calls) != 1 || calls[0] != "att-1/coord-1/task-1/provider overloaded: retry" {
		t.Fatalf("forwarded calls = %v, want the inner call recorded", calls)
	}

	// An abandoned run: the fence fails closed with ErrConflict and never
	// reaches the inner repo.
	fence.abandon(run.RunID)
	if err := fence.SetStepAttemptExecution(context.Background(), run.RunID, "att-1", "coord-2", "task-2", "retry again"); !errors.Is(err, workflowledger.ErrConflict) {
		t.Fatalf("abandoned SetStepAttemptExecution error = %v, want ErrConflict", err)
	}
	if calls := recording.snapshotCalls(); len(calls) != 1 {
		t.Fatalf("abandoned write reached inner repo: calls = %v", calls)
	}
}

// --- fixtures ---

// coverageWriteFile writes a file, creating parent directories.
func coverageWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// coverageDeliveryRepo builds a main repo with one base commit on main, a
// bare origin remote, and the base pushed to origin.
func coverageDeliveryRepo(t *testing.T) (repoRoot, originURL string) {
	t.Helper()
	repoRoot = t.TempDir()
	runGit(t, repoRoot, "init", "-b", "main")
	runGit(t, repoRoot, "config", "user.email", "test@example.com")
	runGit(t, repoRoot, "config", "user.name", "Test")
	coverageWriteFile(t, filepath.Join(repoRoot, "a.txt"), "base\n")
	runGit(t, repoRoot, "add", "a.txt")
	runGit(t, repoRoot, "commit", "-m", "base")
	originDir := filepath.Join(t.TempDir(), "origin.git")
	runGit(t, filepath.Dir(originDir), "init", "--bare", filepath.Base(originDir))
	runGit(t, repoRoot, "remote", "add", "origin", originDir)
	runGit(t, repoRoot, "push", "-u", "origin", "main")
	return repoRoot, originDir
}

// coverageSeededPendingRun seeds one delivery_pending run with a real git
// worktree at <root>/.mivia/worktrees/wt-test on branch wf/wt-test (the CLI
// layout workflowspace.Resolve accepts) and an uncommitted change, so
// Engine.Deliver runs the real pinned-git path end to end.
func coverageSeededPendingRun(t *testing.T, repoRoot, originURL string, repo workflowledger.Repository) workflowledger.RunSnapshot {
	t.Helper()
	baseCommit := runGit(t, repoRoot, "rev-parse", "HEAD")

	worktreeRoot := filepath.Join(repoRoot, ".mivia", "worktrees", "wt-test")
	runGit(t, repoRoot, "worktree", "add", "-b", "wf/wt-test", worktreeRoot, baseCommit)
	runGit(t, worktreeRoot, "config", "user.email", "test@example.com")
	runGit(t, worktreeRoot, "config", "user.name", "Test")
	// Uncommitted change so the intended diff is non-empty.
	coverageWriteFile(t, filepath.Join(worktreeRoot, "a.txt"), "base\nchanged\n")

	return coveragePendingRun(t, repo, workflowledger.RunSnapshot{
		RunID: "wfr-cov-deliver", WorkflowName: "deliver-me", WorkflowDigest: "digest",
		ActiveStepID: "success", BaseRef: "main", BaseCommit: baseCommit,
		WorktreeName: "wt-test", RemoteURL: originURL,
	})
}

// coveragePendingRun admits a pending run and CASes it along the
// pending->running->delivery_pending chain. The snapshot carries the
// deliver-me definition so Engine.Deliver can rebuild the compiled delivery
// policy.
func coveragePendingRun(t *testing.T, repo workflowledger.Repository, snap workflowledger.RunSnapshot) workflowledger.RunSnapshot {
	t.Helper()
	ctx := context.Background()
	raw, err := workflowledger.MarshalSnapshot(workflowledger.Snapshot{
		SchemaVersion:    workflowledger.SnapshotSchemaVersion,
		DefinitionTOML:   []byte(coverageDeliverTOML),
		DefinitionDigest: "digest",
		Inputs:           map[string]string{"task": "build"},
		Delivery:         &workflowledger.DeliverySnapshot{Mode: "draft", Provider: "github", Base: "main"},
	})
	if err != nil {
		t.Fatalf("MarshalSnapshot: %v", err)
	}
	snap.Status = workflowledger.RunStatusPending
	if err := repo.CreateRun(ctx, snap, raw); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	cur, err := repo.GetRun(ctx, snap.RunID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	for _, next := range []workflowledger.RunStatus{workflowledger.RunStatusRunning, workflowledger.RunStatusDeliveryPending} {
		if cur.Status == workflowledger.RunStatusDeliveryPending {
			return cur
		}
		if err := repo.CompareAndSetRunStatus(ctx, snap.RunID, cur.Version, next, nil); err != nil {
			t.Fatalf("CAS to %s: %v", next, err)
		}
		cur, err = repo.GetRun(ctx, snap.RunID)
		if err != nil {
			t.Fatalf("GetRun: %v", err)
		}
	}
	return cur
}

// TestCoverageLocalProgressKindChunkScopeDrop pins the mapper: a chunk
// finding-scope drop is a named observation with detail, never a liveness
// heartbeat tick.
func TestCoverageLocalProgressKindChunkScopeDrop(t *testing.T) {
	if got := localProgressKind(controller.ProgressChunkScopeDropped); got != events.KindWorkflowDeliveryStage {
		t.Fatalf("localProgressKind(chunk_scope_dropped) = %v, want KindWorkflowDeliveryStage", got)
	}
}
