package uiadapter

// Workflow progress -> the pool's out-of-band notice stream.
//
// internal/cliworkflow publishes a controller progress event onto the owning
// session's bus for every workflow lifecycle transition
// (workflow_progress_bus.go), and every pooled session shares the launch
// session's bus. Nothing consumed those kinds: the only SubscribeAcross call
// sites outside internal/events are internal/hub's relay and
// internal/chatsync, and neither lists a workflow kind. A run started with
// workflow_run therefore produced one tool call and then nothing at all,
// however long it ran.
//
// The turn stream cannot be the destination. workflow_run ADMITS a run and
// returns, so the turn that started it ends within seconds while the run goes
// on for hours; TurnHandle.Events closes with its turn (ports.Notices' own doc
// comment states this), so a progress event published afterwards has no turn
// to belong to. ports.Notices is the stream for exactly that case - pool-wide,
// open for the life of the adapter, and already read once at UI startup, so it
// survives the /new and /resume switches a long run outlives.
//
// This keeps INV-TUI-29 intact: the subscription lives in internal/uiadapter,
// the sole bridge, and reaches the UI as ordinary uievent.KindNotice values.
// internal/ui learns nothing about internal/events or internal/cliworkflow.

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/events"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

// workflowNoticeRule records what an operator sees for one workflow event
// kind, and why. Silence is a decision here, not an omission - the whole
// defect this file fixes was a set of kinds nobody had decided about.
type workflowNoticeRule struct {
	// render is false for a kind deliberately kept off the stream.
	render bool
	// label prefixes the notice line for a rendered kind.
	label string
	// why records the reason a kind is silent. Empty for rendered kinds.
	why string
}

// workflowNoticePolicy classifies every workflow_* kind declared in
// internal/events/event.go. TestEveryWorkflowEventKindIsClassified reads that
// const block directly and fails when a new kind reaches it without a
// decision here.
//
// The cut is state transitions, not liveness. Each rendered kind reports
// something that changed and that an operator may need to act on - a step
// boundary, a gate verdict, a blocked approval, a delivery stage, a terminal
// result. The heartbeat reports only that the previous transition still
// holds, which the transition itself already said.
var workflowNoticePolicy = map[events.Kind]workflowNoticeRule{
	events.KindWorkflowRunStarted:        {render: true, label: "started"},
	events.KindWorkflowStepStarted:       {render: true, label: "step %s started"},
	events.KindWorkflowStepCompleted:     {render: true, label: "step %s"},
	events.KindWorkflowGateResult:        {render: true, label: "gate %s"},
	events.KindWorkflowApprovalRequested: {render: true, label: "approval requested: step %s"},
	events.KindWorkflowDeliveryStage:     {render: true, label: "delivery"},
	events.KindWorkflowRunFinished:       {render: true, label: "finished"},

	// The controller emits one heartbeat per watchdog tick
	// (controller.durableHeartbeatInterval, 15s) for as long as a step runs.
	// It never becomes a transcript line: the notice stream is a 16-slot
	// lossy buffer, so rendering these would evict every line that carries
	// information and leave a wall of identical "still running" text behind.
	//
	// It is still SUBSCRIBED, because it is the only evidence a quiet step is
	// alive. It drives the status row instead, on its own replaceable stream
	// (SessionPool.WorkflowStatus) where a superseded value costs nothing.
	events.KindWorkflowStepHeartbeat: {why: "one tick per 15s per running step; it repeats what step_started already said, so it drives the status row instead of the transcript"},
}

// workflowNoticeKinds is the subscription's kind list: every classified kind,
// rendered or not.
//
// The heartbeat is subscribed despite never reaching the transcript, because
// it is what keeps the status row's elapsed time honest for a step that
// produces no transitions for hours - the one case the row exists for.
// workflowNoticeText still filters it out of the record.
func workflowNoticeKinds() []events.Kind {
	out := make([]events.Kind, 0, len(workflowNoticePolicy))
	for kind := range workflowNoticePolicy {
		out = append(out, kind)
	}
	return out
}

// watchWorkflowProgressLocked subscribes the pool to bus's workflow lifecycle
// kinds, once per distinct bus. Every pooled session inherits the launch
// session's bus (inheritEntryStateLocked), so in practice this registers one
// subscription for the whole pool; keying by bus pointer keeps it correct
// (and idempotent) for an embedding that wires more than one.
//
// A released pool registers nothing: ReleaseLeases and CloseAll exist to stop
// background work, and a subscription armed afterwards would outlive them.
// Callers hold p.mu.
func (p *SessionPool) watchWorkflowProgressLocked(bus *events.Bus) {
	if p == nil || bus == nil || p.released.Load() {
		return
	}
	if _, done := p.workflowSubs[bus]; done {
		return
	}
	if p.workflowSubs == nil {
		p.workflowSubs = make(map[*events.Bus]*events.Subscription)
	}
	// SubscribeAcross, not SubscribeMany: one queue means the handler sees
	// the publisher's own order, so "step build started" cannot render after
	// the "step build succeeded" that ends it.
	sub := bus.SubscribeAcross(workflowNoticeKinds(), events.HandlerFunc(func(_ context.Context, ev events.Event) {
		// The status row first, so it never lags the line describing it.
		now := time.Now()
		p.pushWorkflowStatus(uievent.Event{
			Kind: uievent.KindWorkflowStatus,
			At:   now,
			Body: p.workflowStatus.apply(ev, now),
		})
		p.pushNotice(workflowNoticeText(ev))
	}), events.SubscribeOptions{BufSize: 256})
	p.workflowSubs[bus] = sub
}

// releaseWorkflowWatchesLocked unsubscribes every workflow subscription. The
// handler calls pushNotice, so it must stop before the pool is treated as
// finished. Callers hold p.mu.
func (p *SessionPool) releaseWorkflowWatchesLocked() {
	for bus, sub := range p.workflowSubs {
		sub.Unsubscribe()
		delete(p.workflowSubs, bus)
	}
}

// workflowNoticeText renders one workflow event as a single advisory line.
//
// The run id leads every line because it is what the operator needs to act:
// workflow_status, workflow_events, and workflow_cancel all take it. An event
// carrying no run id still renders - a line without the id is worth more than
// silence, which is the failure this whole file exists to end.
func workflowNoticeText(ev events.Event) string {
	rule, ok := workflowNoticePolicy[ev.Kind]
	if !ok || !rule.render {
		return ""
	}
	label := rule.label
	if strings.Contains(label, "%s") {
		label = fmt.Sprintf(label, workflowNoticeStep(ev))
	}
	line := "workflow " + workflowNoticeRun(ev) + ": " + label
	if detail := strings.TrimSpace(ev.Detail); detail != "" {
		line += " " + detail
	}
	if attempt := workflowNoticeRetry(ev); attempt != "" {
		line += " " + attempt
	}
	return line
}

// workflowNoticeRun reads the run id the progress sink stamps into Metadata.
func workflowNoticeRun(ev events.Event) string {
	if id := strings.TrimSpace(ev.Metadata["run_id"]); id != "" {
		return id
	}
	return "run"
}

// workflowNoticeStep reads the step id, falling back to the AgentName the
// progress sink builds as "workflow:<step>" and then to a placeholder, so a
// label with a %s never renders a bare gap.
func workflowNoticeStep(ev events.Event) string {
	if step := strings.TrimSpace(ev.Metadata["step"]); step != "" {
		return step
	}
	if name, ok := strings.CutPrefix(ev.AgentName, "workflow:"); ok && strings.TrimSpace(name) != "" {
		return strings.TrimSpace(name)
	}
	return "?"
}

// workflowNoticeRetry annotates a retried attempt. The first attempt is the
// normal case and says nothing; a second one is the operator's first sign
// that a step is failing and being re-driven.
func workflowNoticeRetry(ev events.Event) string {
	attempt := strings.TrimSpace(ev.Metadata["attempt"])
	if attempt == "" || attempt == "0" || attempt == "1" {
		return ""
	}
	return "(attempt " + attempt + ")"
}

// workflowStatusTracker holds the liveness of the workflow run currently
// executing, and folds each incoming event into it.
//
// One run, not a list. Concurrent workflow runs are possible but rare, and a
// row that grows a line per run is a feed again - which the transcript
// already does better. The newest run wins the row; every run's transitions
// stay in the record regardless.
//
// Its own mutex, not the pool's: this is written from the bus handler, which
// must never contend with a pool operation, and it shares no state with the
// pool's maps.
type workflowStatusTracker struct {
	mu  sync.Mutex
	cur uievent.WorkflowStatusBody
}

// apply folds one workflow event into the current status and returns it.
//
// Since carries the whole value of the row, so when it MOVES is the substance
// here:
//
//   - a step starting moves it: that step has been running for zero seconds;
//   - a heartbeat for the step already on the row does NOT move it, or the
//     elapsed time would reset every 15s and a three-hour step would read
//     "for 0m" forever - motion without information, and worse than silence
//     because it looks like progress;
//   - a heartbeat for some OTHER step moves it, because that step's start was
//     missed (a resume that joined a run already past its first step) and
//     one tick ago is the best lower bound available;
//   - a step ending moves it and clears Step: the run is between steps, and
//     how long it has been between them is the useful fact;
//   - the run finishing clears the row entirely.
func (t *workflowStatusTracker) apply(ev events.Event, now time.Time) uievent.WorkflowStatusBody {
	t.mu.Lock()
	defer t.mu.Unlock()

	run := workflowNoticeRun(ev)
	step := workflowNoticeStep(ev)

	switch ev.Kind {
	case events.KindWorkflowRunFinished:
		t.cur = uievent.WorkflowStatusBody{Run: run}
	case events.KindWorkflowStepHeartbeat:
		if t.cur.Active && t.cur.Run == run && t.cur.Step == step {
			return t.cur // still the same step: the row is already correct
		}
		t.cur = uievent.WorkflowStatusBody{Run: run, Step: step, Since: now, Active: true}
	case events.KindWorkflowStepCompleted, events.KindWorkflowGateResult:
		t.cur = uievent.WorkflowStatusBody{Run: run, Since: now, Active: true}
	case events.KindWorkflowRunStarted:
		t.cur = uievent.WorkflowStatusBody{Run: run, Since: now, Active: true}
	default:
		// Step started, approval requested, delivery stage: each names the
		// thing now in progress.
		t.cur = uievent.WorkflowStatusBody{Run: run, Step: step, Since: now, Active: true}
	}
	return t.cur
}
