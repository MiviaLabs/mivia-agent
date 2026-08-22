// Workflow-run detail modal for the /workflows right sidebar. Enter (or mouse
// double-click) on a sidebar row opens it; the dialog shows the workflow's
// header facts, every compiled definition step in order with its live run
// state (done / active / pending / failed / waiting / canceled / timed_out /
// interrupted), and the run-control actions that actually exist for the run's
// status. Every action routes through an existing fenced engine/tool surface;
// the dialog never mutates run state and never claims a run. This file owns
// the derived content; the async ledger data flow lives in
// workflow_run_dialog_load.go and the interactive half (key handling,
// rendering, action dispatch) in workflow_run_dialog_keys.go.
package legacytui

import (
	"errors"
	"fmt"
	"github.com/MiviaLabs/mivia-agent/internal/cliworkflow"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
	"github.com/charmbracelet/lipgloss"
)

// workflowStepState is one step's live run state as shown in the detail
// dialog. States are derived from typed ledger records (StepAttempt,
// ApprovalRecord, run.ActiveStepID); none of it is parsed user input.
type workflowStepState string

const (
	workflowStepDone        workflowStepState = "done"
	workflowStepActive      workflowStepState = "active"
	workflowStepPending     workflowStepState = "pending"
	workflowStepFailed      workflowStepState = "failed"
	workflowStepWaiting     workflowStepState = "waiting"
	workflowStepCanceled    workflowStepState = "canceled"
	workflowStepTimedOut    workflowStepState = "timed_out"
	workflowStepInterrupted workflowStepState = "interrupted"
)

// workflowStepRow is one ordered definition step with its derived live state.
type workflowStepRow struct {
	id     string
	kind   string
	actor  string // agent, verifier, panel summary, or "" (never untrusted text)
	state  workflowStepState
	active bool // step.ID == run.ActiveStepID; drives the visible "here" marker
}

// workflowDialogAction is one run-control action the dialog offers. Every
// action is a wrapper over an existing fenced surface; the dialog never
// mutates run state itself.
type workflowDialogAction struct {
	key         string
	label       string
	confirm     workflowConfirmAction
	needsEngine bool // cancel/resume/deliver/delete route through the engine
}

// workflowRunDeliveryClaim is one run's execution claim as read for the
// delivery liveness surface: ok=false means the run holds no claim (for a
// delivery_pending run, waiting for a delivery attempt). It is a pure read -
// observing a claim never acquires, refreshes, or releases it.
type workflowRunDeliveryClaim struct {
	at time.Time
	ok bool
}

// workflowRunView is the immutable content snapshot one dialog render is
// based on.
type workflowRunView struct {
	run               workflowledger.RunSnapshot
	header            []string
	notice            string // definition unavailable, run vanished, …
	steps             []workflowStepRow
	actions           []workflowDialogAction
	pendingApprovalID string
}

// buildWorkflowRunView derives the dialog content from one run's typed ledger
// records and the compiled definition. Step states come from typed
// StepAttempt/ApprovalRecord rows and run.ActiveStepID; the definition was
// already parsed and compiled by the existing definition.ParseWorkflowTOML +
// definition.Compile path (workflowCompiledByName), never re-parsed here. A
// missing definition degrades to header facts plus a notice, never an error;
// an empty run id is the only error (the run vanished before open). The
// variadic deliveries carry the run's durable delivery records; existing
// call sites that render no delivery records need no change. claim carries
// the run's execution claim read for delivery_pending liveness (fresh claim =
// delivery in flight, stale = crashed delivery, none = waiting).
func buildWorkflowRunView(run workflowledger.RunSnapshot, compiled *definition.CompiledWorkflow, attempts []workflowledger.StepAttempt, approvals []workflowledger.ApprovalRecord, now time.Time, claim workflowRunDeliveryClaim, deliveries ...[]workflowledger.DeliveryRecord) (*workflowRunView, error) {
	if run.RunID == "" {
		return nil, errors.New("workflow run not found")
	}
	v := &workflowRunView{run: run}
	v.header = []string{"workflow: " + run.WorkflowName}
	if compiled != nil && compiled.Description != "" {
		v.header = append(v.header, "description: "+oneLineNotice(compiled.Description))
	}
	v.header = append(v.header, "run: "+run.RunID, "status: "+string(run.Status))
	if !run.StartedAt.IsZero() {
		started := run.StartedAt.Local().Format("2006-01-02 15:04:05")
		elapsed := ""
		end := now
		if run.FinishedAt != nil {
			// Terminal runs froze their finish time at settlement; the
			// elapsed must never grow past it.
			end = *run.FinishedAt
		} else if workflowRunStepGraphDone(run.Status) {
			// delivery_pending/delivery_failed runs are NOT terminal, so the
			// ledger persists no FinishedAt for them (it stamps one only for
			// terminal statuses). Their step graph is complete, so the
			// elapsed freezes at the latest completed attempt instead of
			// counting the delivery wait forever.
			var lastAttemptFinish *time.Time
			for i := range attempts {
				if attempts[i].FinishedAt != nil && (lastAttemptFinish == nil || attempts[i].FinishedAt.After(*lastAttemptFinish)) {
					fin := *attempts[i].FinishedAt
					lastAttemptFinish = &fin
				}
			}
			if lastAttemptFinish != nil {
				end = *lastAttemptFinish
			}
		}
		if d := end.Sub(run.StartedAt); d >= 0 {
			elapsed = " · elapsed " + FormatDuration(d)
		}
		v.header = append(v.header, "started: "+started+elapsed)
	}
	hb := workflowRunHeartbeatHeaderLine(run, attempts, now, claim)
	v.header = append(v.header, hb)
	for i := range deliveries {
		for _, rec := range deliveries[i] {
			line := "delivery: " + rec.Status
			if rec.RemoteID != "" {
				line += " · PR #" + rec.RemoteID
			}
			if rec.URL != "" {
				line += " · " + rec.URL
			}
			if rec.CommitSHA != "" {
				line += " · commit " + cliworkflow.ShortDigest(rec.CommitSHA)
			}
			v.header = append(v.header, line)
		}
	}
	pendingApproval := ""
	for i := range approvals {
		if approvals[i].Status == "pending" {
			pendingApproval = approvals[i].ApprovalID
			break
		}
	}
	v.pendingApprovalID = pendingApproval
	v.actions = workflowRunActions(run.Status, pendingApproval != "")
	if compiled == nil {
		v.notice = "definition unavailable"
		return v, nil
	}
	v.steps = buildWorkflowRunSteps(compiled, run, attempts, approvals)
	return v, nil
}

// workflowRunStepGraphDone reports whether the run's step graph completed
// while the run itself is not terminal: delivery_pending (work done, waiting
// to publish) and delivery_failed (work done, publication refused or failed).
// The ledger persists FinishedAt only for terminal statuses, so these runs
// carry no finish time of their own; callers freeze their elapsed at the
// latest completed step attempt.
func workflowRunStepGraphDone(status workflowledger.RunStatus) bool {
	return status == workflowledger.RunStatusDeliveryPending || status == workflowledger.RunStatusDeliveryFailed
}

// workflowHeartbeatFreshWindow is how old a running attempt's last heartbeat
// may be before the TUI reads the run as stale. The engine throttles durable
// heartbeats to ~15s per attempt, so the 60s window tolerates a missed tick
// without calling a live run dead.
const workflowHeartbeatFreshWindow = 60 * time.Second

// workflowHeartbeatFresh reports whether heartbeatAt is a live heartbeat:
// nonzero and no older than window at now. A zero heartbeat (never set) is
// never fresh; a future heartbeat (clock skew) is fresh.
func workflowHeartbeatFresh(heartbeatAt, now time.Time, window time.Duration) bool {
	if heartbeatAt.IsZero() {
		return false
	}
	return now.Sub(heartbeatAt) <= window
}

// workflowActiveAttemptHeartbeat returns the active attempt's LastHeartbeatAt
// for a run: the newest running attempt on the run's active step, falling
// back to the newest running attempt overall (attempts arrive ordered by
// event sequence, so the newest start wins). A zero time means no running
// attempt carries a heartbeat yet. Terminal and non-running attempts never
// count, because heartbeats exist only for RUNNING attempts.
func workflowActiveAttemptHeartbeat(run workflowledger.RunSnapshot, attempts []workflowledger.StepAttempt) time.Time {
	var onStep, any workflowledger.StepAttempt
	for i := range attempts {
		a := attempts[i]
		if a.Status != workflowledger.AttemptStatusRunning {
			continue
		}
		if any.AttemptID == "" || a.StartedAt.After(any.StartedAt) {
			any = a
		}
		if a.StepID == run.ActiveStepID && (onStep.AttemptID == "" || a.StartedAt.After(onStep.StartedAt)) {
			onStep = a
		}
	}
	if onStep.AttemptID != "" {
		return onStep.LastHeartbeatAt
	}
	return any.LastHeartbeatAt
}

// workflowRunHeartbeatHeaderLine renders the dialog's heartbeat header line
// for one run. A delivery_pending run shows its delivery liveness instead of
// an attempt heartbeat: a fresh claim means a delivery attempt is in flight,
// a stale claim means a delivery crashed mid-publish, and no claim means the
// run waits for a delivery. For every other status the line reports the last
// running-attempt heartbeat: the age when a running attempt recorded a
// heartbeat (styled info when fresh, error with a stale marker when aged
// out), or a dimmed "none" line when no running attempt carries one.
func workflowRunHeartbeatHeaderLine(run workflowledger.RunSnapshot, attempts []workflowledger.StepAttempt, now time.Time, claim workflowRunDeliveryClaim) string {
	if run.Status == workflowledger.RunStatusDeliveryPending {
		return workflowDeliveryClaimLine(claim, now)
	}
	hb := workflowActiveAttemptHeartbeat(run, attempts)
	if hb.IsZero() {
		return TUIDimStyle.Render("last heartbeat: none")
	}
	ago := now.Sub(hb)
	if ago < 0 {
		ago = 0
	}
	line := "last heartbeat: " + FormatDuration(ago) + " ago"
	if workflowHeartbeatFresh(hb, now, workflowHeartbeatFreshWindow) {
		return tuiInfoStyle.Render(line)
	}
	return TUIErrorStyle.Render(line + " · stale")
}

// workflowDeliveryClaimLine renders the delivery liveness header line for a
// delivery_pending run from its execution claim. The claim lease is the
// ledger's own definition of alive: a claim inside DefaultClaimLease is a
// live delivery attempt, one past it (recovery clears these later) is a
// crashed delivery. A zero claim reads as waiting for a delivery.
func workflowDeliveryClaimLine(claim workflowRunDeliveryClaim, now time.Time) string {
	if !claim.ok {
		return TUIDimStyle.Render("delivery: waiting")
	}
	ago := now.Sub(claim.at)
	if ago < 0 {
		ago = 0
	}
	line := "delivery: in flight · claim " + FormatDuration(ago) + " ago"
	if workflowHeartbeatFresh(claim.at, now, workflowledger.DefaultClaimLease) {
		return tuiInfoStyle.Render(line)
	}
	return TUIErrorStyle.Render(line + " · stale")
}

// buildWorkflowRunSteps maps each compiled step (declaration order) to its
// live state. The latest attempt per step wins; a gate step with a pending
// approval reads waiting; run.ActiveStepID marks the "here" position when no
// attempt row exists yet.
func buildWorkflowRunSteps(compiled *definition.CompiledWorkflow, run workflowledger.RunSnapshot, attempts []workflowledger.StepAttempt, approvals []workflowledger.ApprovalRecord) []workflowStepRow {
	latest := make(map[string]workflowledger.StepAttempt, len(attempts))
	for i := range attempts {
		a := attempts[i]
		if cur, ok := latest[a.StepID]; !ok || a.StartedAt.After(cur.StartedAt) {
			latest[a.StepID] = a
		}
	}
	rows := make([]workflowStepRow, 0, len(compiled.Steps))
	for _, s := range compiled.Steps {
		var attempt *workflowledger.StepAttempt
		if a, ok := latest[s.ID]; ok {
			attempt = &a
		}
		rows = append(rows, workflowStepRow{
			id: s.ID, kind: s.Kind, actor: workflowStepActorLabel(s),
			state:  stepState(s, run, attempt, approvals),
			active: s.ID == run.ActiveStepID,
		})
	}
	return rows
}

// stepState derives one step's live state. Priority: a pending approval on a
// gate step is waiting; a terminal attempt names its outcome; a running
// attempt and the run's ActiveStepID (no attempt row yet) read active while
// the run is genuinely executing; anything else is pending. The ActiveStepID
// fallback is suppressed while the run is parked at an approval gate, so a
// non-gate step never claims "active" when the run waits on a person.
func stepState(s definition.Step, run workflowledger.RunSnapshot, attempt *workflowledger.StepAttempt, approvals []workflowledger.ApprovalRecord) workflowStepState {
	if isWorkflowGateKind(s.Kind) {
		for i := range approvals {
			if approvals[i].StepID == s.ID && approvals[i].Status == "pending" {
				return workflowStepWaiting
			}
		}
	}
	if attempt != nil {
		switch attempt.Status {
		case workflowledger.AttemptStatusSucceeded:
			return workflowStepDone
		case workflowledger.AttemptStatusFailed:
			return workflowStepFailed
		case workflowledger.AttemptStatusCanceled:
			return workflowStepCanceled
		case workflowledger.AttemptStatusTimedOut:
			return workflowStepTimedOut
		case workflowledger.AttemptStatusInterrupted:
			return workflowStepInterrupted
		default:
			return workflowStepActive
		}
	}
	if run.ActiveStepID == s.ID && !workflowRunParkedForApproval(run, approvals) {
		return workflowStepActive
	}
	return workflowStepPending
}

// workflowRunParkedForApproval reports whether the run is parked at an
// approval gate: a pending ApprovalRecord exists and the ledger status does
// not place the run in flight (pending/running). While parked, no step is
// actively executing, so the ActiveStepID fallback must not read "active".
func workflowRunParkedForApproval(run workflowledger.RunSnapshot, approvals []workflowledger.ApprovalRecord) bool {
	pending := false
	for i := range approvals {
		if approvals[i].Status == "pending" {
			pending = true
			break
		}
	}
	if !pending {
		return false
	}
	return run.Status != workflowledger.RunStatusPending && run.Status != workflowledger.RunStatusRunning
}

// isWorkflowGateKind reports whether the step kind parks a run for a person
// (an approval record can park it at waiting_approval).
func isWorkflowGateKind(kind string) bool {
	return kind == "human_gate" || kind == "agent_gate"
}

// workflowStepActorLabel names the step's executing principal: the agent for
// agent/agent_gate steps, the panel for agent_panel, the verifier (or
// sandboxed program) for evidence_gate, and "human" for human_gate.
func workflowStepActorLabel(s definition.Step) string {
	switch s.Kind {
	case "agent", "agent_gate":
		return s.Agent
	case "agent_panel":
		if s.Panel != nil {
			if len(s.Panel.Members) > 0 {
				return fmt.Sprintf("%d panel agents", len(s.Panel.Members))
			}
			return "panel"
		}
	case "evidence_gate":
		if s.Verifier != "" {
			return s.Verifier
		}
		if s.Command != nil {
			return s.Command.Program
		}
	case "human_gate":
		return "human"
	}
	return ""
}

// workflowRunActions returns the actions valid for one run status. Approve
// and reject additionally require a pending approval record (the availability
// check the plan pins: waiting_approval alone is not enough). Deliver covers
// delivery_failed as the run's only recovery surface (resume refuses it).
func workflowRunActions(status workflowledger.RunStatus, pendingApproval bool) []workflowDialogAction {
	var actions []workflowDialogAction
	if workflowledger.IsResumableRunStatus(status) {
		actions = append(actions,
			workflowDialogAction{key: "c", label: "cancel", confirm: workflowConfirmCancel, needsEngine: true},
			workflowDialogAction{key: "r", label: "resume", confirm: workflowConfirmResume, needsEngine: true},
		)
	}
	if status == workflowledger.RunStatusDeliveryPending || status == workflowledger.RunStatusDeliveryFailed {
		actions = append(actions, workflowDialogAction{key: "d", label: "deliver", confirm: workflowConfirmDeliver, needsEngine: true})
	}
	if status == workflowledger.RunStatusWaitingApproval && pendingApproval {
		actions = append(actions,
			workflowDialogAction{key: "a", label: "approve", confirm: workflowConfirmApprove},
			workflowDialogAction{key: "x", label: "reject", confirm: workflowConfirmReject},
		)
	}
	if workflowledger.IsDeletableRunStatus(status) {
		actions = append(actions, workflowDialogAction{key: "D", label: "delete", confirm: workflowConfirmDelete, needsEngine: true})
	}
	if workflowledger.IsTerminalRunStatus(status) || status == workflowledger.RunStatusDeliveryPending {
		actions = append(actions, workflowDialogAction{key: "u", label: "cleanup", confirm: workflowConfirmCleanup})
	}
	return actions
}

// renderWorkflowStepRow renders one step row: the active marker, the state
// tag, the step id, and the kind with its executing principal.
func renderWorkflowStepRow(s workflowStepRow) string {
	marker := "  "
	if s.active {
		marker = tuiAccentStyle.Render("▶ ")
	}
	state := workflowStepStateStyle(s.state).Render("[" + string(s.state) + "]")
	line := marker + state + " " + s.id
	detail := s.kind
	if s.actor != "" {
		detail += ": " + s.actor
	}
	return line + TUIDimStyle.Render(" · "+detail)
}

func workflowStepStateStyle(state workflowStepState) lipgloss.Style {
	switch state {
	case workflowStepActive:
		return tuiAccentStyle
	case workflowStepFailed, workflowStepCanceled, workflowStepTimedOut:
		return TUIErrorStyle
	case workflowStepWaiting:
		return tuiInfoStyle
	default:
		return TUIDimStyle
	}
}

// workflowConfirmAction identifies one armed run-control confirmation.
type workflowConfirmAction int

const (
	workflowConfirmNone workflowConfirmAction = iota
	workflowConfirmCancel
	workflowConfirmResume
	workflowConfirmDeliver
	workflowConfirmApprove
	workflowConfirmReject
	workflowConfirmDelete
	workflowConfirmCleanup
)
