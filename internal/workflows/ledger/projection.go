package ledger

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/storage"
)

// Projection is the rebuilt in-memory state of one workflow run.
type Projection struct {
	// Run is the current run snapshot. ActiveStepID is the DERIVED active
	// step (see below); all other fields replay the wf_run_created payload
	// plus status changes. nil when no wf events exist for the run.
	Run *RunSnapshot
	// SnapshotJSON is the canonical snapshot blob from wf_run_created.
	SnapshotJSON []byte
	// Attempts is ordered by event sequence.
	Attempts []StepAttempt
	// Transitions is derived from completed attempts that carried a route,
	// ordered by event sequence.
	Transitions  []TransitionRecord
	LoopCounters []LoopCounter
	Approvals    []ApprovalRecord
	Deliveries   []DeliveryRecord
	// ActiveStepID is the transition target of the NEWEST step-bearing event:
	// a completion's to_step_id, else an attempt's step_id, else the initial
	// step from wf_run_created. Loop/approval/delivery/status events carry no
	// step and are skipped. When the newest target is a reserved terminal step
	// ("success"/"failure") the workflow is done even if the run status CAS
	// was never recorded.
	ActiveStepID string
	// HasRun reports whether any wf_run_created event was seen.
	HasRun bool
}

// loopKey identifies one loop counter within a run. A struct key keeps the
// (run, loop_name) pairing injective regardless of loop name contents.
type loopKey struct {
	runID    string
	loopName string
}

// upsert adds v under key, or replaces the entry previously added under key,
// keeping the slice position of the first insertion so result slices stay in
// first-seen (replay) order.
func upsert[T any, K comparable](items []T, index map[K]int, key K, v T) []T {
	if i, ok := index[key]; ok {
		items[i] = v
		return items
	}
	index[key] = len(items)
	return append(items, v)
}

// cloneTime returns a deep copy of t (nil-safe).
func cloneTime(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	c := *t
	return &c
}

// rebuildState carries the per-kind merge indexes and the step-candidate
// list threaded through RebuildProjection's per-kind apply helpers.
type rebuildState struct {
	attemptIdx     map[string]int
	loopIdx        map[loopKey]int
	approvalIdx    map[string]int
	deliveryIdx    map[string]int
	initialStep    string
	stepCandidates []string // step IDs of step-bearing events, in replay order
}

// RebuildProjection deterministically replays wf events in store order
// (sorted by RowID, then Sequence) into a Projection. Unknown kinds are
// ignored (foreign coordinator events). All timestamps come from event
// payloads — never derived at read time. Returns an error only for
// undecodable payloads of known kinds.
func RebuildProjection(events []storage.Event) (Projection, error) {
	ordered := make([]storage.Event, len(events))
	copy(ordered, events)
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].RowID != ordered[j].RowID {
			return ordered[i].RowID < ordered[j].RowID
		}
		return ordered[i].Sequence < ordered[j].Sequence
	})

	var proj Projection
	st := &rebuildState{
		attemptIdx:  make(map[string]int),
		loopIdx:     make(map[loopKey]int),
		approvalIdx: make(map[string]int),
		deliveryIdx: make(map[string]int),
	}
	for _, ev := range ordered {
		var err error
		switch ev.Kind {
		case eventKindRunCreated:
			err = applyRunCreated(&proj, st, ev)
		case eventKindRunStatusChanged:
			err = applyRunStatusChanged(&proj, ev)
		case eventKindAttemptStarted:
			err = applyAttemptStarted(&proj, st, ev)
		case eventKindAttemptPrompt:
			err = applyAttemptPrompt(&proj, ev)
		case eventKindAttemptExecution:
			err = applyAttemptExecution(&proj, ev)
		case eventKindAttemptCompleted:
			err = applyAttemptCompleted(&proj, st, ev)
		case eventKindPanelPhaseSet:
			err = applyPanelPhase(&proj, ev)
		default:
			if strings.HasPrefix(ev.Kind, "wf_panel_") {
				err = fmt.Errorf("unknown panel event %q", ev.Kind)
			}
		case eventKindLoopIncremented:
			err = applyLoopIncremented(&proj, st, ev)
		case eventKindApprovalCreated:
			err = applyApprovalCreated(&proj, st, ev)
		case eventKindApprovalResolved:
			err = applyApprovalResolved(&proj, st, ev)
		case eventKindDeliveryUpserted:
			err = applyDeliveryUpserted(&proj, st, ev)
		case eventKindRunDeleted:
			err = applyRunDeleted(&proj)
		}
		if err != nil {
			return Projection{}, err
		}
	}

	if n := len(st.stepCandidates); n > 0 {
		proj.ActiveStepID = st.stepCandidates[n-1]
	} else {
		proj.ActiveStepID = st.initialStep
	}
	return proj, nil
}

// applyRunDeleted clears a deleted run's projection. The wf_run_deleted
// tombstone is the durable deletion marker: folding it empties every read
// surface (GetRun/ListRuns/ListEvents report the run absent), and a later
// incarnation of the same run ID replays from an empty projection.
func applyRunDeleted(proj *Projection) error {
	*proj = Projection{}
	return nil
}

func applyPanelPhase(proj *Projection, ev storage.Event) error {
	p, err := unmarshalPanelPhase(ev.Payload)
	if err != nil {
		return fmt.Errorf("decode %s payload: %w", ev.Kind, err)
	}
	if p.AttemptID == "" || p.Version == 0 {
		return fmt.Errorf("invalid panel phase payload")
	}
	for i := range proj.Attempts {
		a := &proj.Attempts[i]
		if a.AttemptID != p.AttemptID {
			continue
		}
		if a.PanelExecution == nil {
			return fmt.Errorf("panel phase has no panel attempt")
		}
		if IsTerminalAttemptStatus(a.Status) || p.Version != a.Version+1 || !validPanelTransitionWithWork(a.PanelExecution.Phase, p.Phase, p.Synthesis, true) {
			return fmt.Errorf("invalid panel phase transition")
		}
		a.PanelExecution.Phase = p.Phase
		if p.Phase == PanelPhaseSynthesisAdmitted {
			a.PanelExecution.Synthesis = p.Synthesis.clone()
		}
		a.Version = p.Version
		return nil
	}
	return fmt.Errorf("panel phase references unknown attempt %q", p.AttemptID)
}

func applyAttemptExecution(proj *Projection, ev storage.Event) error {
	p, err := unmarshalAttemptExecution(ev.Payload)
	if err != nil {
		return fmt.Errorf("decode %s payload: %w", ev.Kind, err)
	}
	for i := range proj.Attempts {
		if proj.Attempts[i].AttemptID == p.AttemptID {
			a := &proj.Attempts[i]
			if len(a.Executions) == 0 && a.CoordinatorRunID != "" && a.TaskID != "" {
				a.Executions = append(a.Executions, StepExecution{
					ExecutionNo: 1, CoordinatorRunID: a.CoordinatorRunID, TaskID: a.TaskID, StartedAt: a.StartedAt,
				})
			}
			executionNo := p.ExecutionNo
			if executionNo == 0 {
				executionNo = len(a.Executions) + 1
			}
			a.Executions = append(a.Executions, StepExecution{
				ExecutionNo: executionNo, CoordinatorRunID: p.CoordinatorRunID, TaskID: p.TaskID, StartedAt: p.CreatedAt,
			})
			a.CoordinatorRunID = p.CoordinatorRunID
			a.TaskID = p.TaskID
			return nil
		}
	}
	return nil
}

// applyRunCreated folds one wf_run_created event into the projection. The
// caller-provided StartedAt persisted in the payload is honored (CreateRun
// stamps it with the current clock only when absent); the event's CreatedAt
// is the fallback only when the payload carries no StartedAt, so the rebuild
// agrees with the live projection across repository instances over one store.
func applyRunCreated(proj *Projection, st *rebuildState, ev storage.Event) error {
	p, err := unmarshalRunCreated(ev.Payload)
	if err != nil {
		return fmt.Errorf("decode %s payload: %w", ev.Kind, err)
	}
	run := p.Run.Clone()
	if run.StartedAt.IsZero() {
		run.StartedAt = p.CreatedAt
	}
	proj.Run = &run
	proj.SnapshotJSON = append([]byte(nil), p.SnapshotJSON...)
	proj.HasRun = true
	st.initialStep = run.ActiveStepID
	return nil
}

// applyRunStatusChanged folds one wf_run_status_changed event into the
// projection. Status events carry no step and are skipped for the derived
// active step.
func applyRunStatusChanged(proj *Projection, ev storage.Event) error {
	p, err := unmarshalRunStatusChanged(ev.Payload)
	if err != nil {
		return fmt.Errorf("decode %s payload: %w", ev.Kind, err)
	}
	if proj.Run == nil {
		return nil
	}
	proj.Run.Status = p.Status
	proj.Run.Version = p.Version
	proj.Run.FinishedAt = cloneTime(p.FinishedAt)
	return nil
}

// applyAttemptStarted folds one wf_attempt_started event into the projection:
// the attempt is merged by attempt_id and its step becomes a candidate for
// the derived active step when non-empty.
func applyAttemptStarted(proj *Projection, st *rebuildState, ev storage.Event) error {
	p, err := unmarshalAttemptStarted(ev.Payload)
	if err != nil {
		return fmt.Errorf("decode %s payload: %w", ev.Kind, err)
	}
	a := p.Attempt.Clone()
	if err := a.PanelExecution.validateInitial(a.RunID, a.AttemptID); err != nil {
		return fmt.Errorf("invalid panel attempt: %w", err)
	}
	a.Status = AttemptStatusRunning
	a.Version = 1
	proj.Attempts = upsert(proj.Attempts, st.attemptIdx, a.AttemptID, a)
	if a.StepID != "" {
		st.stepCandidates = append(st.stepCandidates, a.StepID)
	}
	return nil
}

// applyAttemptPrompt folds one wf_attempt_prompt event into the projection:
// the prompt reference is recorded on the matching attempt by attempt_id. It
// carries no step, so it contributes no step candidate and never changes the
// derived active step (mirroring the loop/approval/delivery apply functions).
// A prompt for an unknown attempt is ignored — no placeholder is created.
func applyAttemptPrompt(proj *Projection, ev storage.Event) error {
	p, err := unmarshalAttemptPrompt(ev.Payload)
	if err != nil {
		return fmt.Errorf("decode %s payload: %w", ev.Kind, err)
	}
	for i := range proj.Attempts {
		if proj.Attempts[i].AttemptID == p.AttemptID {
			proj.Attempts[i].PromptRef = p.PromptRef
			return nil
		}
	}
	return nil // unknown attempt: ignore
}

// applyAttemptCompleted folds one wf_attempt_completed event into the
// projection: the attempt is merged by attempt_id, and a non-empty ToStepID
// derives a TransitionRecord and a step candidate for the derived active
// step.
func applyAttemptCompleted(proj *Projection, st *rebuildState, ev storage.Event) error {
	p, err := unmarshalAttemptCompleted(ev.Payload)
	if err != nil {
		return fmt.Errorf("decode %s payload: %w", ev.Kind, err)
	}
	i, ok := st.attemptIdx[p.AttemptID]
	if !ok {
		proj.Attempts = append(proj.Attempts, StepAttempt{AttemptID: p.AttemptID, RunID: ev.RunID})
		i = len(proj.Attempts) - 1
		st.attemptIdx[p.AttemptID] = i
	}
	a := &proj.Attempts[i]
	a.Status = p.Status
	a.CoordinatorRunID = p.CoordinatorRunID
	a.TaskID = p.TaskID
	a.OutputRef = p.OutputRef
	a.OutputDigest = p.OutputDigest
	a.ErrorRef = p.ErrorRef
	a.ToStepID = p.ToStepID
	a.TransitionIndex = p.TransitionIndex
	a.MatchDigest = p.MatchDigest
	a.DecisionJSON = append([]byte(nil), p.DecisionJSON...)
	a.EvidenceJSON = append([]byte(nil), p.EvidenceJSON...)
	a.FinishedAt = cloneTime(&p.FinishedAt)
	if p.Version > 0 {
		a.Version = p.Version
	} else {
		a.Version = 2
	}
	if p.ToStepID != "" {
		proj.Transitions = append(proj.Transitions, TransitionRecord{
			RunID:           ev.RunID,
			FromAttemptID:   p.AttemptID,
			ToStepID:        p.ToStepID,
			TransitionIndex: p.TransitionIndex,
			MatchDigest:     p.MatchDigest,
			DecisionJSON:    append([]byte(nil), p.DecisionJSON...),
			CreatedAt:       p.CreatedAt,
		})
		st.stepCandidates = append(st.stepCandidates, p.ToStepID)
	}
	return nil
}

// applyLoopIncremented folds one wf_loop_incremented event into the
// projection, merging by (run, loop name) with latest-iteration-wins.
func applyLoopIncremented(proj *Projection, st *rebuildState, ev storage.Event) error {
	p, err := unmarshalLoopIncremented(ev.Payload)
	if err != nil {
		return fmt.Errorf("decode %s payload: %w", ev.Kind, err)
	}
	lc := LoopCounter{RunID: ev.RunID, LoopName: p.LoopName, Iterations: p.Iterations}
	proj.LoopCounters = upsert(proj.LoopCounters, st.loopIdx, loopKey{runID: ev.RunID, loopName: p.LoopName}, lc)
	return nil
}

// applyApprovalCreated folds one wf_approval_created event into the
// projection, merging by approval ID.
func applyApprovalCreated(proj *Projection, st *rebuildState, ev storage.Event) error {
	p, err := unmarshalApprovalCreated(ev.Payload)
	if err != nil {
		return fmt.Errorf("decode %s payload: %w", ev.Kind, err)
	}
	proj.Approvals = upsert(proj.Approvals, st.approvalIdx, p.Approval.ApprovalID, p.Approval.Clone())
	return nil
}

// applyApprovalResolved folds one wf_approval_resolved event into the
// projection, merging by approval ID.
func applyApprovalResolved(proj *Projection, st *rebuildState, ev storage.Event) error {
	p, err := unmarshalApprovalResolved(ev.Payload)
	if err != nil {
		return fmt.Errorf("decode %s payload: %w", ev.Kind, err)
	}
	i, ok := st.approvalIdx[p.ApprovalID]
	if !ok {
		proj.Approvals = append(proj.Approvals, ApprovalRecord{ApprovalID: p.ApprovalID, RunID: ev.RunID})
		i = len(proj.Approvals) - 1
		st.approvalIdx[p.ApprovalID] = i
	}
	a := &proj.Approvals[i]
	a.Status = p.Status
	a.Actor = p.Actor
	a.Reason = p.Reason
	a.ResolvedAt = cloneTime(&p.ResolvedAt)
	return nil
}

// applyDeliveryUpserted folds one wf_delivery_upserted event into the
// projection, merging by idempotency key with latest-wins.
func applyDeliveryUpserted(proj *Projection, st *rebuildState, ev storage.Event) error {
	p, err := unmarshalDeliveryUpserted(ev.Payload)
	if err != nil {
		return fmt.Errorf("decode %s payload: %w", ev.Kind, err)
	}
	proj.Deliveries = upsert(proj.Deliveries, st.deliveryIdx, p.Delivery.IdempotencyKey, p.Delivery.Clone())
	return nil
}
