package cli

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/agenttools"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/compiler"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// dialogTestWorkflowDefinition is a three-step workflow (agent, evidence_gate,
// human_gate) used to exercise every per-step state derivation.
const dialogTestWorkflowDefinition = `version = 1
name = "dialog-wf"
description = "Dialog test workflow."
initial_step = "plan"

[[steps]]
id = "plan"
kind = "agent"
agent = "test-agent"

[[steps]]
id = "lint"
kind = "evidence_gate"
verifier = "lint"

[[steps]]
id = "ship"
kind = "human_gate"

[[transitions]]
from = "plan"
to = "lint"
match = { status = "succeeded" }

[[transitions]]
from = "lint"
to = "ship"
match = { status = "succeeded" }

[[transitions]]
from = "ship"
to = "success"
match = { status = "succeeded" }
`

func dialogTestCompiled(t *testing.T) *compiler.CompiledWorkflow {
	t.Helper()
	wf, _, err := definition.ParseWorkflowTOML([]byte(dialogTestWorkflowDefinition), "dialog-wf.toml")
	if err != nil {
		t.Fatal(err)
	}
	compiled, err := compiler.Compile(&wf)
	if err != nil {
		t.Fatal(err)
	}
	return compiled
}

// recordingWorkflowEngine records mutating calls so tests can assert exactly
// which surface the dialog routed an action to. It never touches a ledger:
// dialog code must never mutate run state itself.
type recordingWorkflowEngine struct {
	mu       sync.Mutex
	err      error
	cancels  []string
	resumes  []string
	delivers []string
	deletes  []string
}

func (e *recordingWorkflowEngine) Start(_ context.Context, req agenttools.StartRequest) (agenttools.StartResult, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.resumes = append(e.resumes, req.RunID)
	return agenttools.StartResult{RunID: req.RunID, Status: string(workflowledger.RunStatusRunning), Resumed: true}, e.err
}

func (e *recordingWorkflowEngine) Cancel(_ context.Context, runID string) (agenttools.CancelResult, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.cancels = append(e.cancels, runID)
	return agenttools.CancelResult{RunID: runID, Status: string(workflowledger.RunStatusCanceled)}, e.err
}

func (e *recordingWorkflowEngine) Deliver(_ context.Context, runID string, allowPublish bool) (agenttools.DeliverResult, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.delivers = append(e.delivers, runID)
	return agenttools.DeliverResult{RunID: runID, Status: string(workflowledger.RunStatusSucceeded), Mode: "draft"}, e.err
}

func (e *recordingWorkflowEngine) Delete(_ context.Context, runID string) (agenttools.DeleteResult, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.deletes = append(e.deletes, runID)
	return agenttools.DeleteResult{RunID: runID, Status: string(workflowledger.RunStatusCanceled), Deleted: true}, e.err
}

func (e *recordingWorkflowEngine) called() (cancels, resumes, delivers, deletes int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.cancels), len(e.resumes), len(e.delivers), len(e.deletes)
}

// TestWorkflowStepState pins the pure per-step state derivation: terminal
// attempts name their outcome, a gate step with a pending approval reads
// waiting, run.ActiveStepID (no attempt row) reads active, and everything else
// is pending.
func TestWorkflowStepState(t *testing.T) {
	gate := definition.Step{ID: "ship", Kind: "human_gate"}
	agentStep := definition.Step{ID: "plan", Kind: "agent"}
	run := workflowledger.RunSnapshot{RunID: "wfr-ST1", ActiveStepID: "plan"}
	attempt := func(status workflowledger.AttemptStatus) *workflowledger.StepAttempt {
		return &workflowledger.StepAttempt{StepID: "plan", AttemptNo: 1, Status: status, StartedAt: time.Unix(1, 0)}
	}
	approval := func(status string) []workflowledger.ApprovalRecord {
		return []workflowledger.ApprovalRecord{{ApprovalID: "wfa-1", RunID: "wfr-ST1", StepID: "ship", Status: status}}
	}
	cases := []struct {
		name      string
		step      definition.Step
		attempt   *workflowledger.StepAttempt
		approvals []workflowledger.ApprovalRecord
		want      workflowStepState
	}{
		{"active step without attempt row", agentStep, nil, nil, workflowStepActive},
		{"not active, no attempt", definition.Step{ID: "other", Kind: "agent"}, nil, nil, workflowStepPending},
		{"succeeded attempt is done", agentStep, attempt(workflowledger.AttemptStatusSucceeded), nil, workflowStepDone},
		{"failed attempt is failed", agentStep, attempt(workflowledger.AttemptStatusFailed), nil, workflowStepFailed},
		{"canceled attempt is canceled", agentStep, attempt(workflowledger.AttemptStatusCanceled), nil, workflowStepCanceled},
		{"timed out attempt is timed_out", agentStep, attempt(workflowledger.AttemptStatusTimedOut), nil, workflowStepTimedOut},
		{"interrupted attempt is interrupted", agentStep, attempt(workflowledger.AttemptStatusInterrupted), nil, workflowStepInterrupted},
		{"running attempt is active", agentStep, attempt(workflowledger.AttemptStatusRunning), nil, workflowStepActive},
		{"gate with pending approval is waiting", gate, attempt(workflowledger.AttemptStatusRunning), approval("pending"), workflowStepWaiting},
		{"gate with resolved approval falls through to attempt", gate, attempt(workflowledger.AttemptStatusSucceeded), approval("approved"), workflowStepDone},
		{"agent step with pending approval is not waiting", agentStep, nil, approval("pending"), workflowStepPending},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := stepState(tc.step, run, tc.attempt, tc.approvals); got != tc.want {
				t.Fatalf("stepState = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestWorkflowRunActionsByStatus pins action availability per status. Deliver
// is the only recovery surface for delivery_failed (resume refuses it), and
// approve/reject require a pending approval record on top of waiting_approval.
func TestWorkflowRunActionsByStatus(t *testing.T) {
	cases := []struct {
		name            string
		status          workflowledger.RunStatus
		pendingApproval bool
		want            []string
		notWant         []string
	}{
		{"pending", workflowledger.RunStatusPending, false, []string{"c cancel", "r resume"}, []string{"d deliver", "a approve", "x reject", "D delete", "u cleanup"}},
		{"running", workflowledger.RunStatusRunning, false, []string{"c cancel", "r resume"}, []string{"d deliver", "a approve", "x reject", "D delete", "u cleanup"}},
		{"waiting_approval no approval", workflowledger.RunStatusWaitingApproval, false, []string{"c cancel", "r resume"}, []string{"a approve", "x reject"}},
		{"waiting_approval pending approval", workflowledger.RunStatusWaitingApproval, true, []string{"c cancel", "r resume", "a approve", "x reject"}, []string{"d deliver"}},
		{"delivery_pending", workflowledger.RunStatusDeliveryPending, false, []string{"d deliver", "D delete", "u cleanup"}, []string{"c cancel", "r resume", "a approve"}},
		{"delivery_failed", workflowledger.RunStatusDeliveryFailed, false, []string{"d deliver", "D delete", "u cleanup"}, []string{"c cancel", "r resume", "a approve"}},
		{"succeeded", workflowledger.RunStatusSucceeded, false, []string{"D delete", "u cleanup"}, []string{"c cancel", "r resume", "d deliver"}},
		{"failed", workflowledger.RunStatusFailed, false, []string{"D delete", "u cleanup"}, []string{"c cancel", "r resume", "d deliver"}},
		{"canceled", workflowledger.RunStatusCanceled, false, []string{"D delete", "u cleanup"}, []string{"c cancel", "r resume"}},
		{"timed_out", workflowledger.RunStatusTimedOut, false, []string{"D delete", "u cleanup"}, []string{"c cancel", "r resume"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := workflowRunActions(tc.status, tc.pendingApproval)
			labels := make([]string, 0, len(got))
			for _, a := range got {
				labels = append(labels, a.key+" "+a.label)
			}
			for _, want := range tc.want {
				found := false
				for _, l := range labels {
					if l == want {
						found = true
						break
					}
				}
				if !found {
					t.Fatalf("actions = %v, missing %q", labels, want)
				}
			}
			for _, banned := range tc.notWant {
				for _, l := range labels {
					if l == banned {
						t.Fatalf("actions = %v, must not contain %q", labels, banned)
					}
				}
			}
		})
	}
}

// TestBuildWorkflowRunView pins the header facts, the compiled-step order with
// derived states, and the negative paths: an empty run id errors and a missing
// definition degrades to header facts plus a notice with no step list.
func TestBuildWorkflowRunView(t *testing.T) {
	compiled := dialogTestCompiled(t)
	run := workflowledger.RunSnapshot{
		RunID: "wfr-VIEW1", WorkflowName: "dialog-wf", Status: workflowledger.RunStatusRunning,
		ActiveStepID: "lint", StartedAt: time.Now().Add(-2 * time.Minute),
	}
	attempts := []workflowledger.StepAttempt{
		{StepID: "plan", AttemptNo: 1, Status: workflowledger.AttemptStatusSucceeded, StartedAt: time.Unix(1, 0)},
	}
	approvals := []workflowledger.ApprovalRecord{{ApprovalID: "wfa-1", RunID: run.RunID, StepID: "ship", Status: "pending"}}
	view, err := buildWorkflowRunView(run, compiled, attempts, approvals, time.Now(), workflowRunDeliveryClaim{})
	if err != nil {
		t.Fatal(err)
	}
	header := strings.Join(view.header, "\n")
	for _, want := range []string{"workflow: dialog-wf", "description: Dialog test workflow.", "run: wfr-VIEW1", "status: running", "started:", "elapsed"} {
		if !strings.Contains(header, want) {
			t.Fatalf("header missing %q:\n%s", want, header)
		}
	}
	if len(view.steps) != 3 {
		t.Fatalf("steps = %d, want 3 in declaration order", len(view.steps))
	}
	if view.steps[0].id != "plan" || view.steps[0].state != workflowStepDone || view.steps[0].active {
		t.Fatalf("step 0 = %#v, want plan done inactive", view.steps[0])
	}
	if view.steps[1].id != "lint" || view.steps[1].state != workflowStepActive || !view.steps[1].active {
		t.Fatalf("step 1 = %#v, want lint active with the here marker", view.steps[1])
	}
	if view.steps[2].id != "ship" || view.steps[2].state != workflowStepWaiting {
		t.Fatalf("step 2 = %#v, want ship waiting", view.steps[2])
	}
	if view.pendingApprovalID != "wfa-1" {
		t.Fatalf("pendingApprovalID = %q, want wfa-1", view.pendingApprovalID)
	}

	// Missing definition: header facts plus a notice, no step list, no panic.
	missing, err := buildWorkflowRunView(run, nil, nil, nil, time.Now(), workflowRunDeliveryClaim{})
	if err != nil {
		t.Fatal(err)
	}
	if missing.notice != "definition unavailable" || len(missing.steps) != 0 {
		t.Fatalf("missing definition view notice=%q steps=%d", missing.notice, len(missing.steps))
	}
	if len(missing.header) == 0 {
		t.Fatal("missing definition view has no header facts")
	}

	// Empty run id is an error (the run vanished before open).
	if _, err := buildWorkflowRunView(workflowledger.RunSnapshot{}, compiled, nil, nil, time.Now(), workflowRunDeliveryClaim{}); err == nil {
		t.Fatal("empty run id must error")
	}
}

// TestBuildWorkflowRunViewElapsedUsesFinishedAt pins that a finished run's
// elapsed time freezes at FinishedAt - StartedAt instead of growing with the
// wall clock: a delivered run's elapsed must not keep counting forever while
// the dialog stays open.
func TestBuildWorkflowRunViewElapsedUsesFinishedAt(t *testing.T) {
	started := time.Now().Add(-2 * time.Hour)
	finished := started.Add(5 * time.Minute)
	run := workflowledger.RunSnapshot{
		RunID: "wfr-ELAPSED1", WorkflowName: "alpha", Status: workflowledger.RunStatusSucceeded,
		StartedAt: started, FinishedAt: &finished,
	}
	// The wall clock moved two hours past start; the finished run's elapsed
	// must stay frozen at the five minutes it actually ran.
	view, err := buildWorkflowRunView(run, nil, nil, nil, started.Add(2*time.Hour), workflowRunDeliveryClaim{})
	if err != nil {
		t.Fatal(err)
	}
	header := strings.Join(view.header, "\n")
	want := "elapsed " + formatDuration(5*time.Minute)
	if !strings.Contains(header, want) {
		t.Fatalf("finished-run header missing the frozen elapsed %q:\n%s", want, header)
	}
	if strings.Contains(header, "elapsed "+formatDuration(2*time.Hour)) {
		t.Fatalf("finished-run elapsed must not grow with the wall clock:\n%s", header)
	}
}

// TestBuildWorkflowRunViewElapsedRunningUsesNow pins the live case: a running
// run (no FinishedAt) keeps counting from the wall clock. The finished-run
// freeze is only correct when the running case still counts.
func TestBuildWorkflowRunViewElapsedRunningUsesNow(t *testing.T) {
	started := time.Now().Add(-2 * time.Minute)
	run := workflowledger.RunSnapshot{
		RunID: "wfr-ELAPSED2", WorkflowName: "alpha", Status: workflowledger.RunStatusRunning,
		StartedAt: started,
	}
	view, err := buildWorkflowRunView(run, nil, nil, nil, started.Add(2*time.Minute), workflowRunDeliveryClaim{})
	if err != nil {
		t.Fatal(err)
	}
	header := strings.Join(view.header, "\n")
	if !strings.Contains(header, "elapsed "+formatDuration(2*time.Minute)) {
		t.Fatalf("running-run header missing the live elapsed:\n%s", header)
	}
}

// TestBuildWorkflowRunViewElapsedDeliverySettledUsesLastAttempt pins the
// delivery-settled case: a delivery_pending/delivery_failed run is NOT
// terminal (the ledger persists FinishedAt only for terminal statuses), so
// its elapsed must freeze at the latest completed attempt instead of
// counting the delivery wait forever.
func TestBuildWorkflowRunViewElapsedDeliverySettledUsesLastAttempt(t *testing.T) {
	started := time.Now().Add(-59 * time.Hour)
	lastFinished := started.Add(2 * time.Hour)
	attempts := []workflowledger.StepAttempt{
		{StepID: "one", AttemptNo: 1, Status: workflowledger.AttemptStatusSucceeded, StartedAt: started, FinishedAt: &lastFinished},
	}
	for _, status := range []workflowledger.RunStatus{workflowledger.RunStatusDeliveryPending, workflowledger.RunStatusDeliveryFailed} {
		t.Run(string(status), func(t *testing.T) {
			run := workflowledger.RunSnapshot{
				RunID: "wfr-ELAPSED3", WorkflowName: "alpha", Status: status,
				StartedAt: started, ActiveStepID: "success",
			}
			// The wall clock moved 59 hours past start; the settled run's
			// elapsed must stay frozen at the two hours its steps ran.
			view, err := buildWorkflowRunView(run, nil, attempts, nil, started.Add(59*time.Hour), workflowRunDeliveryClaim{})
			if err != nil {
				t.Fatal(err)
			}
			header := strings.Join(view.header, "\n")
			if !strings.Contains(header, "elapsed "+formatDuration(2*time.Hour)) {
				t.Fatalf("settled-run header missing the frozen elapsed:\n%s", header)
			}
			if strings.Contains(header, "elapsed "+formatDuration(59*time.Hour)) {
				t.Fatalf("settled-run elapsed must not count the delivery wait:\n%s", header)
			}
		})
	}
}

// TestBuildWorkflowRunViewShowsDeliveryRecord pins that the dialog surfaces
// the run's durable delivery record: status, PR url, and commit. A delivered
// run at delivery_pending then explains itself instead of looking like the
// PR never happened.
func TestBuildWorkflowRunViewShowsDeliveryRecord(t *testing.T) {
	run := workflowledger.RunSnapshot{RunID: "wfr-DELREC1", WorkflowName: "alpha", Status: workflowledger.RunStatusDeliveryPending, StartedAt: time.Now()}
	cases := []struct {
		name string
		rec  workflowledger.DeliveryRecord
		want string
	}{
		{"succeeded with url", workflowledger.DeliveryRecord{Status: "succeeded", URL: "https://example.com/pull/74", CommitSHA: "40718667a7b2d59bf751195374c622fc02ab60fb"}, "delivery: succeeded · https://example.com/pull/74 · commit 40718667a7b2"},
		{"succeeded with remote id", workflowledger.DeliveryRecord{Status: "succeeded", RemoteID: "42", URL: "https://example.com/pull/74", CommitSHA: "40718667a7b2d59bf751195374c622fc02ab60fb"}, "delivery: succeeded · PR #42 · https://example.com/pull/74 · commit 40718667a7b2"},
		{"pending with commit", workflowledger.DeliveryRecord{Status: "pending", CommitSHA: "95520b476b2c"}, "delivery: pending · commit 95520b476b2c"},
		{"failed without url", workflowledger.DeliveryRecord{Status: "failed"}, "delivery: failed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			view, err := buildWorkflowRunView(run, nil, nil, nil, time.Now(), workflowRunDeliveryClaim{}, []workflowledger.DeliveryRecord{tc.rec})
			if err != nil {
				t.Fatal(err)
			}
			header := strings.Join(view.header, "\n")
			if !strings.Contains(header, tc.want) {
				t.Fatalf("header missing %q:\n%s", tc.want, header)
			}
		})
	}
}

// TestBuildWorkflowRunViewDeliveryLiveness pins the delivery_pending liveness
// header line: a fresh execution claim reads "delivery: in flight" with the
// claim age, a stale claim (past the ledger's claim lease) marks stale, and
// no claim reads "delivery: waiting".
func TestBuildWorkflowRunViewDeliveryLiveness(t *testing.T) {
	run := workflowledger.RunSnapshot{RunID: "wfr-DLVLIV1", WorkflowName: "alpha", Status: workflowledger.RunStatusDeliveryPending, StartedAt: time.Now()}
	now := time.Now()
	header := func(claim workflowRunDeliveryClaim) string {
		view, err := buildWorkflowRunView(run, nil, nil, nil, now, claim)
		if err != nil {
			t.Fatal(err)
		}
		return strings.Join(view.header, "\n")
	}
	fresh := header(workflowRunDeliveryClaim{at: now.Add(-12 * time.Second), ok: true})
	if !strings.Contains(fresh, "delivery: in flight · claim") || !strings.Contains(fresh, "ago") {
		t.Fatalf("fresh-claim header missing the in-flight claim line:\n%s", fresh)
	}
	stale := header(workflowRunDeliveryClaim{at: now.Add(-workflowledger.DefaultClaimLease - time.Minute), ok: true})
	if !strings.Contains(stale, "delivery: in flight") || !strings.Contains(stale, "stale") {
		t.Fatalf("stale-claim header missing the in-flight + stale marker:\n%s", stale)
	}
	waiting := header(workflowRunDeliveryClaim{})
	if !strings.Contains(waiting, "delivery: waiting") {
		t.Fatalf("no-claim header missing the waiting line:\n%s", waiting)
	}
	// A future claim (clock skew) is fresh and its age clamps to zero.
	future := header(workflowRunDeliveryClaim{at: now.Add(5 * time.Second), ok: true})
	if !strings.Contains(future, "delivery: in flight · claim") || strings.Contains(future, "stale") {
		t.Fatalf("future-claim header must read fresh, not stale:\n%s", future)
	}
}

// TestWorkflowRunDialogRendersStepMarkers pins the visible markers and state
// tags for done/active/waiting steps plus the "here" marker on the active step.
func TestWorkflowRunDialogRendersStepMarkers(t *testing.T) {
	compiled := dialogTestCompiled(t)
	run := workflowledger.RunSnapshot{
		RunID: "wfr-MARK1", WorkflowName: "dialog-wf", Status: workflowledger.RunStatusRunning,
		ActiveStepID: "lint", StartedAt: time.Now().Add(-time.Minute),
	}
	attempts := []workflowledger.StepAttempt{
		{StepID: "plan", AttemptNo: 1, Status: workflowledger.AttemptStatusSucceeded, StartedAt: time.Unix(1, 0)},
	}
	approvals := []workflowledger.ApprovalRecord{{ApprovalID: "wfa-1", RunID: run.RunID, StepID: "ship", Status: "pending"}}
	view, err := buildWorkflowRunView(run, compiled, attempts, approvals, time.Now(), workflowRunDeliveryClaim{})
	if err != nil {
		t.Fatal(err)
	}
	d := &workflowRunDialog{runID: run.RunID, view: view}
	panel, _ := d.ViewAt(90, 24)
	text := stripANSI(panel)
	for _, want := range []string{"[done] plan", "▶ [active] lint", "[waiting] ship", "human_gate: human", "evidence_gate: lint"} {
		if !strings.Contains(text, want) {
			t.Fatalf("dialog missing %q:\n%s", want, text)
		}
	}
}

// TestWorkflowRunDialogFooterActionHints pins that the footer advertises only
// the actions valid for the current status (with the engine wired so the full
// set is visible).
func TestWorkflowRunDialogFooterActionHints(t *testing.T) {
	run := func(status workflowledger.RunStatus) workflowledger.RunSnapshot {
		return workflowledger.RunSnapshot{RunID: "wfr-FOOT1", WorkflowName: "alpha", Status: status}
	}
	cases := []struct {
		name            string
		status          workflowledger.RunStatus
		pendingApproval bool
		want            []string
		notWant         []string
	}{
		{"running", workflowledger.RunStatusRunning, false, []string{"c cancel", "r resume"}, []string{"d deliver", "a approve", "D delete"}},
		{"waiting_approval", workflowledger.RunStatusWaitingApproval, true, []string{"c cancel", "r resume", "a approve", "x reject"}, []string{"d deliver"}},
		{"delivery_pending", workflowledger.RunStatusDeliveryPending, false, []string{"d deliver", "D delete", "u cleanup"}, []string{"c cancel", "r resume"}},
		{"succeeded", workflowledger.RunStatusSucceeded, false, []string{"D delete", "u cleanup"}, []string{"c cancel", "r resume", "d deliver"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			view, err := buildWorkflowRunView(run(tc.status), nil, nil, nil, time.Now(), workflowRunDeliveryClaim{})
			if err != nil {
				t.Fatal(err)
			}
			if tc.pendingApproval {
				view.pendingApprovalID = "wfa-1"
				view.actions = workflowRunActions(tc.status, true)
			}
			d := &workflowRunDialog{runID: "wfr-FOOT1", engine: &recordingWorkflowEngine{}, view: view}
			footer := stripANSI(d.footer())
			for _, want := range tc.want {
				if !strings.Contains(footer, want) {
					t.Fatalf("footer missing %q:\n%s", want, footer)
				}
			}
			for _, banned := range tc.notWant {
				if strings.Contains(footer, banned) {
					t.Fatalf("footer must not contain %q:\n%s", banned, footer)
				}
			}
		})
	}
}

// TestWorkflowRunDialogEscQClose pins that esc and q close the dialog and
// restore the workflows sidebar focus.
func TestWorkflowRunDialogEscQClose(t *testing.T) {
	m := newReadyChatModel(40, 100)
	m.workflowsSidebar = newWorkflowsSidebar()
	m.workflowRunDlg = &workflowRunDialog{runID: "wfr-CLOSE1", view: &workflowRunView{run: workflowledger.RunSnapshot{RunID: "wfr-CLOSE1"}}}
	m.setFocus(focusWorkflowsSidebar)

	for _, key := range []string{"esc", "q"} {
		m.workflowRunDlg = &workflowRunDialog{runID: "wfr-CLOSE1", view: &workflowRunView{run: workflowledger.RunSnapshot{RunID: "wfr-CLOSE1"}}}
		ok, _, _ := m.handleWorkflowRunDialogKey(key)
		if !ok {
			t.Fatalf("%q was not handled", key)
		}
		if m.workflowRunDlg != nil {
			t.Fatalf("%q did not close the dialog", key)
		}
		if m.focus != focusWorkflowsSidebar {
			t.Fatalf("focus after %q = %v, want workflows sidebar", key, m.focus)
		}
	}
}

// TestWorkflowRunDialogOpenShowsLoadingPlaceholder pins the transient state
// before the first async ledger read lands: the dialog renders a loading line
// and never panics with a nil view.
func TestWorkflowRunDialogOpenShowsLoadingPlaceholder(t *testing.T) {
	m := newReadyChatModel(40, 100)
	m.workflowRunDlg = &workflowRunDialog{runID: "wfr-LOAD1"}
	panel, _ := m.workflowRunDlg.ViewAt(60, 20)
	if !strings.Contains(stripANSI(panel), "loading run details") {
		t.Fatalf("loading placeholder missing:\n%s", stripANSI(panel))
	}
}

// TestWorkflowRunDialogPagerScroll pins j/k paging when the content overflows
// the canvas: scrolling clamps at the top and bottom and shows later rows.
func TestWorkflowRunDialogPagerScroll(t *testing.T) {
	compiled := &compiler.CompiledWorkflow{Name: "big", Steps: make([]definition.Step, 40)}
	for i := range compiled.Steps {
		compiled.Steps[i] = definition.Step{ID: fmt.Sprintf("step-%02d", i), Kind: "agent", Agent: "test-agent"}
	}
	run := workflowledger.RunSnapshot{RunID: "wfr-BIG1", WorkflowName: "big", Status: workflowledger.RunStatusRunning, ActiveStepID: "step-00"}
	view, err := buildWorkflowRunView(run, compiled, nil, nil, time.Now(), workflowRunDeliveryClaim{})
	if err != nil {
		t.Fatal(err)
	}
	d := &workflowRunDialog{runID: run.RunID, view: view}

	first := func() string {
		panel, _ := d.ViewAt(60, 12)
		return stripANSI(panel)
	}
	before := first()
	if !strings.Contains(before, "step-00") {
		t.Fatalf("top of the pager missing the first step:\n%s", before)
	}

	d.move(100, 60, 12)
	if d.scroll != d.maxScroll(60, 12) {
		t.Fatalf("scroll = %d, want clamped to %d", d.scroll, d.maxScroll(60, 12))
	}
	after := first()
	if !strings.Contains(after, "step-39") {
		t.Fatalf("bottom of the pager missing the last step:\n%s", after)
	}

	d.move(-100, 60, 12)
	if d.scroll != 0 {
		t.Fatalf("scroll after -100 = %d, want 0", d.scroll)
	}
	d.move(1, 60, 12)
	if d.scroll != 1 {
		t.Fatalf("scroll after +1 = %d, want 1", d.scroll)
	}
}

// TestWorkflowRunDialogGeometry renders the dialog at in-canvas edge sizes
// (mirroring the effort dialog tests): no panic, valid UTF-8, and every
// rendered line within the canvas width.
func TestWorkflowRunDialogGeometry(t *testing.T) {
	compiled := &compiler.CompiledWorkflow{Name: "big", Steps: make([]definition.Step, 40)}
	for i := range compiled.Steps {
		compiled.Steps[i] = definition.Step{ID: fmt.Sprintf("step-%02d", i), Kind: "agent", Agent: strings.Repeat("\U0001F642", 6)}
	}
	run := workflowledger.RunSnapshot{RunID: "wfr-GEO1", WorkflowName: "big", Status: workflowledger.RunStatusRunning, ActiveStepID: "step-00", StartedAt: time.Now().Add(-time.Minute)}
	view, err := buildWorkflowRunView(run, compiled, nil, nil, time.Now(), workflowRunDeliveryClaim{})
	if err != nil {
		t.Fatal(err)
	}
	for _, size := range [][2]int{{1, 1}, {2, 8}, {24, 2}, {90, 24}} {
		w, h := size[0], size[1]
		d := &workflowRunDialog{runID: run.RunID, view: view}
		panel, layout := d.ViewAt(w, h)
		if !utf8.ValidString(panel) {
			t.Fatalf("%dx%d: dialog output is not valid UTF-8", w, h)
		}
		for _, line := range strings.Split(stripANSI(panel), "\n") {
			if runeWidth(line) > w {
				t.Fatalf("%dx%d: line width %d exceeds canvas: %q", w, h, runeWidth(line), line)
			}
		}
		if layout.rect.w > w || layout.rect.h > h {
			t.Fatalf("%dx%d: dialog rect %dx%d exceeds the canvas", w, h, layout.rect.w, layout.rect.h)
		}
	}
}

// TestWorkflowsSidebarDoubleClick pins the sidebar double-click window: a
// second click on the same row within 400ms activates and consumes the state;
// a click on a different row (or after the window) only re-arms.
func TestWorkflowsSidebarDoubleClick(t *testing.T) {
	s := newWorkflowsSidebar()
	now := time.Now()
	if s.doubleClick(0, now) {
		t.Fatal("first click must not activate")
	}
	if !s.doubleClick(0, now.Add(100*time.Millisecond)) {
		t.Fatal("second click on the same row within the window must activate")
	}
	// The state was consumed: a third click inside the window is a fresh
	// first click, not another activation.
	if s.doubleClick(0, now.Add(200*time.Millisecond)) {
		t.Fatal("consumed double-click state must not re-activate")
	}

	// A different row resets the window without activating.
	s2 := newWorkflowsSidebar()
	s2.doubleClick(1, now)
	if s2.doubleClick(2, now.Add(50*time.Millisecond)) {
		t.Fatal("different-row click must not activate")
	}
}

// TestWorkflowsSidebarDoubleClickStaleClickDoesNotActivate pins that a second
// click on the same row outside the 400ms window never opens the dialog.
func TestWorkflowsSidebarDoubleClickStaleClickDoesNotActivate(t *testing.T) {
	s := newWorkflowsSidebar()
	now := time.Now()
	s.doubleClick(0, now)
	if s.doubleClick(0, now.Add(500*time.Millisecond)) {
		t.Fatal("stale click must not activate")
	}
}

// TestBuildWorkflowRunViewShowsHeartbeatLine pins the dialog's last-heartbeat
// header line: a fresh heartbeat renders the age, a stale one renders the age
// with a stale marker, and a run with no running attempt renders none. The
// fresh line uses the info style and the stale line the error style, so
// freshness is visually distinct.
func TestBuildWorkflowRunViewShowsHeartbeatLine(t *testing.T) {
	run := workflowledger.RunSnapshot{RunID: "wfr-HBL1", WorkflowName: "alpha", Status: workflowledger.RunStatusRunning, ActiveStepID: "plan", StartedAt: time.Now().Add(-10 * time.Minute)}
	now := time.Now()
	attempt := func(hbAt time.Time) []workflowledger.StepAttempt {
		return []workflowledger.StepAttempt{{AttemptID: "att-hb", RunID: run.RunID, StepID: "plan", AttemptNo: 1, Status: workflowledger.AttemptStatusRunning, StartedAt: now.Add(-2 * time.Minute), LastHeartbeatAt: hbAt}}
	}

	// Fresh: the age renders with the info style and no stale marker.
	fresh, err := buildWorkflowRunView(run, nil, attempt(now.Add(-12*time.Second)), nil, now, workflowRunDeliveryClaim{})
	if err != nil {
		t.Fatal(err)
	}
	freshLine := "last heartbeat: " + formatDuration(12*time.Second) + " ago"
	header := strings.Join(fresh.header, "\n")
	if !strings.Contains(header, freshLine) {
		t.Fatalf("header missing %q:\n%s", freshLine, header)
	}
	if !strings.Contains(header, tuiInfoStyle.Render(freshLine)) {
		t.Fatalf("fresh heartbeat line not styled with the info style:\n%q", header)
	}
	if strings.Contains(header, "stale") || strings.Contains(header, "last heartbeat: none") {
		t.Fatalf("fresh header carries a stale marker:\n%s", header)
	}

	// Stale: the age renders with a stale marker and the error style.
	stale, err := buildWorkflowRunView(run, nil, attempt(now.Add(-3*time.Minute)), nil, now, workflowRunDeliveryClaim{})
	if err != nil {
		t.Fatal(err)
	}
	staleLine := "last heartbeat: " + formatDuration(3*time.Minute) + " ago · stale"
	header = strings.Join(stale.header, "\n")
	if !strings.Contains(header, staleLine) {
		t.Fatalf("header missing %q:\n%s", staleLine, header)
	}
	if !strings.Contains(header, tuiErrorStyle.Render(staleLine)) {
		t.Fatalf("stale heartbeat line not styled with the error style:\n%q", header)
	}

	// No running attempt: the line renders none.
	none, err := buildWorkflowRunView(run, nil, nil, nil, now, workflowRunDeliveryClaim{})
	if err != nil {
		t.Fatal(err)
	}
	header = strings.Join(none.header, "\n")
	if !strings.Contains(header, "last heartbeat: none") {
		t.Fatalf("heartbeat-less header missing the none line:\n%s", header)
	}
}
