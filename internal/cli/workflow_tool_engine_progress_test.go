package cli

import (
	"context"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/events"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/controller"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// recordingEventBusHandler collects events published on an events.Bus.
type recordingEventBusHandler struct {
	mu     sync.Mutex
	events []events.Event
}

// HandleEvent implements events.Handler.
func (h *recordingEventBusHandler) HandleEvent(_ context.Context, ev events.Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.events = append(h.events, ev)
}

// take returns a copy of the collected events.
func (h *recordingEventBusHandler) take() []events.Event {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]events.Event, len(h.events))
	copy(out, h.events)
	return out
}

// workflowEventKinds is every kind workflowBusProgressSink can publish.
var workflowEventKinds = []events.Kind{
	events.KindWorkflowStepStarted,
	events.KindWorkflowStepCompleted,
	events.KindWorkflowStepHeartbeat,
	events.KindWorkflowGateResult,
	events.KindWorkflowApprovalRequested,
	events.KindWorkflowRunFinished,
	events.KindWorkflowDeliveryStage,
}

// newRecordingWorkflowBus builds a bus with a recording handler subscribed to
// every workflow kind.
func newRecordingWorkflowBus(t *testing.T) (*events.Bus, *recordingEventBusHandler) {
	t.Helper()
	bus := events.New()
	t.Cleanup(bus.Close)
	h := &recordingEventBusHandler{}
	bus.SubscribeMany(workflowEventKinds, h)
	return bus, h
}

// TestWorkflowBusProgressSinkPublishesKinds proves the sink maps every
// controller progress kind onto the matching session event kind with the full
// metadata payload, name, attribution, and timestamp.
func TestWorkflowBusProgressSinkPublishesKinds(t *testing.T) {
	timestamp := time.Date(2024, 5, 1, 12, 0, 0, 0, time.UTC)
	base := controller.ProgressEvent{
		RunID: "wfr-x", StepID: "one", TaskID: "task-1",
		CoordinatorRunID: "wfr-coord", AttemptNo: 2, Detail: "detail", Timestamp: timestamp,
	}
	tests := []struct {
		name string
		kind controller.ProgressKind
		want events.Kind
	}{
		{name: "step started", kind: controller.ProgressStepStarted, want: events.KindWorkflowStepStarted},
		{name: "step completed", kind: controller.ProgressStepCompleted, want: events.KindWorkflowStepCompleted},
		{name: "step heartbeat", kind: controller.ProgressStepHeartbeat, want: events.KindWorkflowStepHeartbeat},
		{name: "gate started", kind: controller.ProgressGateStarted, want: events.KindWorkflowGateResult},
		{name: "approval requested", kind: controller.ProgressApprovalRequested, want: events.KindWorkflowApprovalRequested},
		{name: "run finished", kind: controller.ProgressRunFinished, want: events.KindWorkflowRunFinished},
		{name: "run failed", kind: controller.ProgressRunFailed, want: events.KindWorkflowRunFinished},
		{name: "panel refused", kind: controller.ProgressPanelRefused, want: events.KindWorkflowStepCompleted},
		{name: "delivery stage", kind: controller.ProgressDeliveryStage, want: events.KindWorkflowDeliveryStage},
		{name: "delivery refused", kind: controller.ProgressDeliveryRefused, want: events.KindWorkflowDeliveryStage},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bus, h := newRecordingWorkflowBus(t)
			progress := base
			progress.Kind = tt.kind
			workflowBusProgressSink{bus: bus}.Emit(progress)
			bus.Flush()
			got := h.take()
			if len(got) != 1 {
				t.Fatalf("events = %d, want 1", len(got))
			}
			ev := got[0]
			if ev.Kind != tt.want {
				t.Fatalf("kind = %s, want %s", ev.Kind, tt.want)
			}
			if ev.Name != "workflow" {
				t.Fatalf("name = %q, want %q", ev.Name, "workflow")
			}
			if ev.Detail != progress.Detail {
				t.Fatalf("detail = %q, want %q", ev.Detail, progress.Detail)
			}
			if ev.AgentTask != progress.TaskID {
				t.Fatalf("AgentTask = %q, want %q", ev.AgentTask, progress.TaskID)
			}
			if ev.AgentName != "workflow:"+progress.StepID {
				t.Fatalf("AgentName = %q, want %q", ev.AgentName, "workflow:"+progress.StepID)
			}
			if !ev.Timestamp.Equal(timestamp) {
				t.Fatalf("timestamp = %v, want %v", ev.Timestamp, timestamp)
			}
			wantMeta := map[string]string{
				"run_id":             progress.RunID,
				"step":               progress.StepID,
				"attempt":            strconv.Itoa(progress.AttemptNo),
				"coordinator_run_id": progress.CoordinatorRunID,
				"task_id":            progress.TaskID,
			}
			for k, want := range wantMeta {
				if gotV := ev.Metadata[k]; gotV != want {
					t.Fatalf("metadata[%q] = %q, want %q", k, gotV, want)
				}
			}
		})
	}
}

// TestWorkflowBusProgressSinkRunFailedCarriesFailure proves a failed run is
// published as workflow_run_finished with the failure cause in Detail.
func TestWorkflowBusProgressSinkRunFailedCarriesFailure(t *testing.T) {
	bus, h := newRecordingWorkflowBus(t)
	workflowBusProgressSink{bus: bus}.Emit(controller.ProgressEvent{
		Kind: controller.ProgressRunFailed, RunID: "wfr-fail", StepID: "two",
		AttemptNo: 2, Detail: "agent step failed",
	})
	bus.Flush()
	got := h.take()
	if len(got) != 1 || got[0].Kind != events.KindWorkflowRunFinished {
		t.Fatalf("events = %+v, want one workflow_run_finished", got)
	}
	if got[0].Detail != "agent step failed" {
		t.Fatalf("detail = %q, want the failure cause", got[0].Detail)
	}
}

// TestWorkflowBusProgressSinkNilBusSafe proves Emit on a nil bus is a no-op.
func TestWorkflowBusProgressSinkNilBusSafe(t *testing.T) {
	workflowBusProgressSink{}.Emit(controller.ProgressEvent{Kind: controller.ProgressStepStarted})
}

// TestWorkflowBusProgressSinkUnknownKindFallsBackToHeartbeat proves an
// unrecognised progress kind still publishes a neutral progress tick.
func TestWorkflowBusProgressSinkUnknownKindFallsBackToHeartbeat(t *testing.T) {
	bus, h := newRecordingWorkflowBus(t)
	workflowBusProgressSink{bus: bus}.Emit(controller.ProgressEvent{Kind: controller.ProgressKind("future_kind")})
	bus.Flush()
	got := h.take()
	if len(got) != 1 || got[0].Kind != events.KindWorkflowStepHeartbeat {
		t.Fatalf("events = %+v, want one workflow_step_heartbeat", got)
	}
}

// TestSessionWorkflowEngineSetEventBusStoresBus proves SetEventBus stores a
// provider on the engine that returns the attached bus (nil disables progress
// publishing).
func TestSessionWorkflowEngineSetEventBusStoresBus(t *testing.T) {
	e := newSessionWorkflowEngine(t.TempDir(), "")
	bus := events.New()
	t.Cleanup(bus.Close)
	e.SetEventBus(bus)
	e.mu.Lock()
	provider := e.bus
	e.mu.Unlock()
	if provider == nil {
		t.Fatal("engine bus provider is nil after SetEventBus")
	}
	if got := provider(); got != bus {
		t.Fatalf("provider bus = %v, want the attached bus", got)
	}
	e.SetEventBus(nil)
	e.mu.Lock()
	provider = e.bus
	e.mu.Unlock()
	if provider == nil || provider() != nil {
		t.Fatal("engine bus provider after nil does not return nil")
	}
}

// TestSessionWorkflowEngineLazyBusProvesRealOrdering is the regression test
// for the production wiring order: configureChatWorkspace wires the engine
// with sess.EventBus before runTUI creates the bus, so a provider read at
// attach time is the only way progress reaches the bus. The engine is
// constructed with a provider reading a mutable slot, the slot is filled with
// a recording bus AFTER construction, and startCLI must still publish
// workflow_* events.
func TestSessionWorkflowEngineLazyBusProvesRealOrdering(t *testing.T) {
	root, _, configPath, _ := newDeliveryFixture(t)
	var slot *events.Bus
	e := newSessionWorkflowEngine(root, configPath)
	e.SetEventBusProvider(func() *events.Bus { return slot })

	// The bus is created after the engine is wired, exactly like runTUI.
	bus, h := newRecordingWorkflowBus(t)
	slot = bus

	result, err := e.Start(context.Background(), ledger.StartRequest{
		Workflow: "two-step",
		Inputs:   map[string]any{"task": "compile"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.RunID == "" {
		t.Fatal("start returned an empty run id")
	}
	waitForSessionEngineIdle(t, e, result.RunID)
	bus.Flush()
	got := h.take()
	if len(got) == 0 {
		t.Fatal("no workflow events published with a bus installed after wiring; the provider must be read at attach time")
	}
	for _, ev := range got {
		if ev.Name != "workflow" {
			t.Fatalf("event name = %q, want %q", ev.Name, "workflow")
		}
	}
}

// TestSessionWorkflowEngineStartCLIPublishesProgress proves startCLI with an
// attached bus publishes workflow lifecycle events onto that bus for a real
// tiny workflow run.
func TestSessionWorkflowEngineStartCLIPublishesProgress(t *testing.T) {
	root, _, configPath, _ := newDeliveryFixture(t)
	bus, h := newRecordingWorkflowBus(t)
	e := newSessionWorkflowEngine(root, configPath)
	e.SetEventBus(bus)

	result, err := e.Start(context.Background(), ledger.StartRequest{
		Workflow: "two-step",
		Inputs:   map[string]any{"task": "compile"},
		// Auto-delivery must run so the delivery_pending park settles to
		// succeeded and the delivery-completion run_finished is published.
		AllowPublish: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.RunID == "" {
		t.Fatal("start returned an empty run id")
	}
	waitForSessionEngineIdle(t, e, result.RunID)
	bus.Flush()
	got := h.take()
	if len(got) == 0 {
		t.Fatal("no workflow events published for a session-engine run")
	}
	foundRunFinished := false
	for _, ev := range got {
		if ev.Name != "workflow" {
			t.Fatalf("event name = %q, want %q", ev.Name, "workflow")
		}
		if ev.Metadata["run_id"] != result.RunID {
			t.Fatalf("run_id metadata = %q, want %q", ev.Metadata["run_id"], result.RunID)
		}
		if ev.Kind == events.KindWorkflowRunFinished {
			foundRunFinished = true
		}
	}
	if !foundRunFinished {
		t.Fatal("no workflow_run_finished event for the settled run")
	}
}
