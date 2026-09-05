package uiadapter

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/events"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

// workflowNoticePool builds a pool over a session that already has a bus, the
// production shape: internal/clichat.attachSessionEventBus runs in
// dispatchChatSurface before the TUI launcher reaches NewCommandRunner.
func workflowNoticePool(t *testing.T) (*SessionPool, *events.Bus) {
	t.Helper()
	bus := events.New()
	t.Cleanup(bus.Close)
	sess := chat.NewSession(&config.Resolved{ProviderName: "fake", Model: "m1"}, nil)
	sess.SessionID = "session-workflow-notices"
	sess.EventBus = bus
	pool := NewSessionPool(sess, nil, nil, false)
	t.Cleanup(pool.CloseAll)
	return pool, bus
}

// awaitNotice returns the next TRANSCRIPT line on the stream.
//
// The stream carries two things now: NoticeBody lines that become transcript
// blocks, and WorkflowStatusBody values that only replace the status row. The
// status events are skipped here rather than asserted against, because they
// are the row's business - awaitStatus below is what reads those.
func awaitNotice(t *testing.T, ch <-chan uievent.Event) string {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case ev := <-ch:
			if _, ok := ev.Body.(uievent.WorkflowStatusBody); ok {
				continue
			}
			body, ok := ev.Body.(uievent.NoticeBody)
			if !ok {
				t.Fatalf("notice body = %T, want uievent.NoticeBody", ev.Body)
			}
			return body.Text
		case <-deadline:
			t.Fatal("timed out waiting for a workflow notice")
			return ""
		}
	}
}

// awaitStatus returns the next status-row value on the stream.
func awaitStatus(t *testing.T, ch <-chan uievent.Event) uievent.WorkflowStatusBody {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case ev := <-ch:
			if body, ok := ev.Body.(uievent.WorkflowStatusBody); ok {
				return body
			}
		case <-deadline:
			t.Fatal("timed out waiting for a workflow status")
			return uievent.WorkflowStatusBody{}
		}
	}
}

func workflowEvent(kind events.Kind, step, detail string) events.Event {
	return events.Event{
		Kind:      kind,
		Timestamp: time.Now(),
		Name:      "workflow",
		Detail:    detail,
		AgentName: "workflow:" + step,
		Metadata: map[string]string{
			"run_id":  "wfr-visible",
			"step":    step,
			"attempt": "1",
		},
	}
}

// TestWorkflowProgressReachesTheNoticeStream is the whole point of the
// subscriber: a workflow run started from the TUI publishes lifecycle events
// that outlive the turn that started it (workflow_run admits the run and
// returns), so TurnHandle.Events - which closes with the turn - can never
// carry them. Before this, nothing subscribed to the workflow kinds at all
// and a multi-hour run showed the operator nothing after the tool call
// returned.
func TestWorkflowProgressReachesTheNoticeStream(t *testing.T) {
	pool, bus := workflowNoticePool(t)
	notices := pool.Notices()

	for _, tc := range []struct {
		name string
		ev   events.Event
		want []string
	}{
		{"run started", workflowEvent(events.KindWorkflowRunStarted, "", ""), []string{"wfr-visible", "started"}},
		{"step started", workflowEvent(events.KindWorkflowStepStarted, "build", ""), []string{"wfr-visible", "build", "started"}},
		{"step completed", workflowEvent(events.KindWorkflowStepCompleted, "build", "succeeded"), []string{"build", "succeeded"}},
		{"gate result", workflowEvent(events.KindWorkflowGateResult, "review", "passed"), []string{"gate", "review", "passed"}},
		{"approval requested", workflowEvent(events.KindWorkflowApprovalRequested, "publish", ""), []string{"approval", "publish"}},
		{"delivery stage", workflowEvent(events.KindWorkflowDeliveryStage, "", "pushed"), []string{"delivery", "pushed"}},
		{"run finished", workflowEvent(events.KindWorkflowRunFinished, "", "succeeded"), []string{"wfr-visible", "finished", "succeeded"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bus.Publish(tc.ev)
			got := awaitNotice(t, notices)
			for _, want := range tc.want {
				if !strings.Contains(got, want) {
					t.Errorf("notice %q does not mention %q", got, want)
				}
			}
		})
	}
}

// TestWorkflowHeartbeatsDoNotReachTheNoticeStream pins the one kind that is
// deliberately kept out of the record, and the buffer pressure behind that
// decision. The controller emits a step heartbeat per watchdog tick
// (durableHeartbeatInterval, 15s), so a single overnight step would push
// thousands of identical "still running" lines into a transcript whose notice
// buffer holds 16 - evicting everything that carries information in order to
// report that nothing had happened. Heartbeats drive the status row instead,
// on their own replaceable stream (WorkflowStatus).
func TestWorkflowHeartbeatsDoNotReachTheNoticeStream(t *testing.T) {
	pool, bus := workflowNoticePool(t)
	notices := pool.Notices()

	for i := 0; i < 50; i++ {
		bus.Publish(workflowEvent(events.KindWorkflowStepHeartbeat, "build", "running"))
	}
	// A later state change must still arrive: proving heartbeats are dropped
	// is only half the claim if the subscription is simply dead.
	bus.Publish(workflowEvent(events.KindWorkflowStepCompleted, "build", "succeeded"))

	got := awaitNotice(t, notices)
	if !strings.Contains(got, "succeeded") {
		t.Fatalf("first notice = %q, want the step completion - a heartbeat leaked ahead of it", got)
	}
}

// TestWorkflowNoticesStopAfterCloseAll pins the release: a pool that has shut
// down must not keep a delivery goroutine pushing into a stream nobody reads.
func TestWorkflowNoticesStopAfterCloseAll(t *testing.T) {
	pool, bus := workflowNoticePool(t)
	notices := pool.Notices()
	pool.CloseAll()

	bus.Publish(workflowEvent(events.KindWorkflowRunFinished, "", "succeeded"))
	bus.Flush()
	select {
	case ev := <-notices:
		t.Fatalf("a released pool still pushed a workflow notice: %+v", ev.Body)
	case <-time.After(200 * time.Millisecond):
	}
}

// TestEveryWorkflowEventKindIsClassified is the bridge gate the subscriber
// needs to stay honest. It reads the Kind constants out of
// internal/events/event.go rather than any hand-kept list, so a NEW
// workflow_* kind added there fails here until someone decides whether the
// operator should see it. Silence must be a decision, which is what left
// every one of these kinds unconsumed in the first place.
func TestEveryWorkflowEventKindIsClassified(t *testing.T) {
	declared := workflowKindsDeclaredInSource(t)
	if len(declared) < 8 {
		t.Fatalf("parsed only %d workflow kinds from internal/events/event.go; the scan is broken, not the policy", len(declared))
	}
	for _, kind := range declared {
		if _, ok := workflowNoticePolicy[kind]; !ok {
			t.Errorf("events.Kind %q has no entry in workflowNoticePolicy: decide whether an operator should see it, then record the decision (render or silent, with the reason)", kind)
		}
	}
	for kind := range workflowNoticePolicy {
		found := false
		for _, d := range declared {
			if d == kind {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("workflowNoticePolicy classifies %q, which is not declared in internal/events/event.go", kind)
		}
	}
}

// workflowKindsDeclaredInSource returns every events.Kind constant whose
// string value starts with "workflow_", read from the events package source.
func workflowKindsDeclaredInSource(t *testing.T) []events.Kind {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "../events/event.go", nil, 0)
	if err != nil {
		t.Fatalf("parse internal/events/event.go: %v", err)
	}
	var out []events.Kind
	ast.Inspect(file, func(n ast.Node) bool {
		spec, ok := n.(*ast.ValueSpec)
		if !ok {
			return true
		}
		for _, value := range spec.Values {
			lit, ok := value.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				continue
			}
			text := strings.Trim(lit.Value, `"`)
			if strings.HasPrefix(text, "workflow_") {
				out = append(out, events.Kind(text))
			}
		}
		return true
	})
	return out
}

// TestHeartbeatsKeepTheStatusRowWithoutMovingItsClock pins the rule that
// makes the elapsed time on the row mean anything.
//
// The controller emits a heartbeat every 15s for as long as a step runs. If
// each one restarted Since, a step running for three hours would read "for
// 0m" forever - motion with no information, and worse than silence because it
// looks like progress. A heartbeat for the step already on the row must leave
// the clock alone.
func TestHeartbeatsKeepTheStatusRowWithoutMovingItsClock(t *testing.T) {
	pool, bus := workflowNoticePool(t)
	status := pool.WorkflowStatus()

	bus.Publish(workflowEvent(events.KindWorkflowStepStarted, "build", ""))
	started := awaitStatus(t, status)
	if !started.Active || started.Step != "build" {
		t.Fatalf("step start produced %+v, want an active row naming build", started)
	}

	for i := 0; i < 5; i++ {
		bus.Publish(workflowEvent(events.KindWorkflowStepHeartbeat, "build", "running"))
		beat := awaitStatus(t, status)
		if !beat.Since.Equal(started.Since) {
			t.Fatalf("heartbeat %d moved Since from %v to %v: the row would read 'for 0m' forever", i, started.Since, beat.Since)
		}
		if !beat.Active || beat.Step != "build" {
			t.Fatalf("heartbeat %d produced %+v, want the same active step", i, beat)
		}
	}
}

// TestAQuietRunReportsAStepItNeverSawStart covers the resume case: a UI that
// attaches to a run already past its first step sees heartbeats with no
// preceding start. Reporting the step with a 15s-ago lower bound beats
// reporting nothing, which is the failure the row exists to end.
func TestAQuietRunReportsAStepItNeverSawStart(t *testing.T) {
	pool, bus := workflowNoticePool(t)
	status := pool.WorkflowStatus()

	bus.Publish(workflowEvent(events.KindWorkflowStepHeartbeat, "review", "running"))
	got := awaitStatus(t, status)
	if !got.Active || got.Step != "review" {
		t.Fatalf("an unheralded heartbeat produced %+v, want an active row naming review", got)
	}
}

// TestAFinishedRunClearsTheStatus pins the terminal transition: the row must
// stop claiming work is in progress.
func TestAFinishedRunClearsTheStatus(t *testing.T) {
	pool, bus := workflowNoticePool(t)
	status := pool.WorkflowStatus()

	bus.Publish(workflowEvent(events.KindWorkflowStepStarted, "build", ""))
	awaitStatus(t, status)
	bus.Publish(workflowEvent(events.KindWorkflowRunFinished, "", "succeeded"))

	got := awaitStatus(t, status)
	if got.Active {
		t.Fatalf("a finished run left the status active: %+v", got)
	}
}
