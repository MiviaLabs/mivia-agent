package ledger

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/contentref"
)

// ---------------------------------------------------------------------------
// 5. CreateStepAttempt
// ---------------------------------------------------------------------------

func TestStorageRepository_CreateStepAttempt(t *testing.T) {
	ctx := context.Background()
	for name, repo := range repos(t) {
		t.Run(name, func(t *testing.T) {
			run := runID(t)
			snap, json := newRun(t, run)
			requireErr(t, repo.CreateRun(ctx, snap, json), nil, "CreateRun")

			a1 := StepAttempt{AttemptID: "att-1", RunID: run, StepID: "plan", AttemptNo: 1}
			a2 := StepAttempt{AttemptID: "att-2", RunID: run, StepID: "plan", AttemptNo: 2}
			requireErr(t, repo.CreateStepAttempt(ctx, a1), nil, "create attempt 1")
			requireErr(t, repo.CreateStepAttempt(ctx, a2), nil, "create attempt 2")

			// The (runID, stepID, attemptNo) triple is unique: a second
			// create for the same triple is ErrDuplicate (no-double-dispatch)
			// even with a different attempt ID. (This is the repository-level
			// logical duplicate, not a sequence collision — the memory
			// backend must honor it too.)
			dup := a1
			dup.AttemptID = "att-1-dup"
			requireErr(t, repo.CreateStepAttempt(ctx, dup), ErrDuplicate, "duplicate (runID, stepID, attemptNo)")

			// Recorded attempt: running, version 1, StartedAt stamped.
			got, err := repo.GetStepAttempt(ctx, run, "att-1")
			if err != nil {
				t.Fatalf("GetStepAttempt: %v", err)
			}
			if got.RunID != run || got.StepID != "plan" || got.AttemptNo != 1 {
				t.Fatalf("attempt identity = %+v, want run=%q step=plan no=1", got, run)
			}
			if got.Status != AttemptStatusRunning {
				t.Fatalf("attempt Status = %q, want %q", got.Status, AttemptStatusRunning)
			}
			if got.Version != 1 {
				t.Fatalf("attempt Version = %d, want 1", got.Version)
			}
			if !got.StartedAt.Equal(fixedClock) {
				t.Fatalf("attempt StartedAt = %v, want %v (clock-stamped)", got.StartedAt, fixedClock)
			}

			// Unknown attempt -> ErrNotFound.
			_, err = repo.GetStepAttempt(ctx, run, "att-missing")
			requireErr(t, err, ErrNotFound, "GetStepAttempt unknown")

			// Ordered by event sequence: att-1 before att-2.
			list, err := repo.ListStepAttempts(ctx, run)
			if err != nil {
				t.Fatalf("ListStepAttempts: %v", err)
			}
			if len(list) != 2 {
				t.Fatalf("ListStepAttempts = %d attempts, want 2", len(list))
			}
			if list[0].AttemptID != "att-1" || list[1].AttemptID != "att-2" {
				t.Fatalf("ListStepAttempts order = [%s, %s], want [att-1, att-2]",
					list[0].AttemptID, list[1].AttemptID)
			}
		})
	}
}

func TestStorageRepository_SetStepAttemptExecutionPersistsRetryIdentity(t *testing.T) {
	ctx := context.Background()
	for name, repo := range repos(t) {
		t.Run(name, func(t *testing.T) {
			run := runID(t)
			snap, raw := newRun(t, run)
			requireErr(t, repo.CreateRun(ctx, snap, raw), nil, "CreateRun")
			requireErr(t, repo.CreateStepAttempt(ctx, StepAttempt{AttemptID: "att-1", RunID: run, StepID: "plan", AttemptNo: 1, CoordinatorRunID: "coord-first", TaskID: "task-first"}), nil, "CreateStepAttempt")
			requireErr(t, repo.SetStepAttemptExecution(ctx, run, "att-1", "coord-retry", "task-retry", "provider overloaded: retry 1"), nil, "SetStepAttemptExecution")
			requireErr(t, repo.SetStepAttemptExecution(ctx, run, "att-1", "coord-final", "task-final", "provider overloaded: retry 2"), nil, "SetStepAttemptExecution")
			got, err := repo.GetStepAttempt(ctx, run, "att-1")
			if err != nil {
				t.Fatal(err)
			}
			if got.CoordinatorRunID != "coord-final" || got.TaskID != "task-final" {
				t.Fatalf("execution identity = %q/%q, want coord-final/task-final", got.CoordinatorRunID, got.TaskID)
			}
			if len(got.Executions) != 3 {
				t.Fatalf("execution history length = %d, want 3", len(got.Executions))
			}
			for i, want := range []struct{ run, task string }{{"coord-first", "task-first"}, {"coord-retry", "task-retry"}, {"coord-final", "task-final"}} {
				if got.Executions[i].ExecutionNo != i+1 || got.Executions[i].CoordinatorRunID != want.run || got.Executions[i].TaskID != want.task {
					t.Fatalf("execution %d = %+v, want %s/%s", i+1, got.Executions[i], want.run, want.task)
				}
			}

			// The transient-retry reasons are persisted in the audit trail:
			// each SetStepAttemptExecution wrote a wf_attempt_execution event
			// whose summary carries the reason text.
			events, err := repo.ListEvents(ctx, run, 0, 0)
			if err != nil {
				t.Fatalf("ListEvents: %v", err)
			}
			summaries := []string{}
			for _, ev := range events {
				if ev.Kind == eventKindAttemptExecution {
					summaries = append(summaries, ev.Summary)
				}
			}
			if len(summaries) != 2 {
				t.Fatalf("ListEvents has %d wf_attempt_execution events, want 2", len(summaries))
			}
			joined := strings.Join(summaries, " ")
			for _, want := range []string{"coord-retry", "task-retry", "provider overloaded: retry 1", "coord-final", "task-final", "provider overloaded: retry 2"} {
				if !strings.Contains(joined, want) {
					t.Fatalf("attempt_execution summaries %v, want one to contain %q", summaries, want)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// 6. CompleteStepAttempt
// ---------------------------------------------------------------------------

// completionOutcome builds the succeeded-with-route outcome used by the
// CompleteStepAttempt tests.
func completionOutcome() AttemptOutcome {
	evidence := []byte("evidence")
	return AttemptOutcome{
		Status:          AttemptStatusSucceeded,
		OutputRef:       contentref.Reference(contentref.KindOutput, evidence),
		OutputDigest:    DigestHex(evidence),
		ToStepID:        "implement",
		TransitionIndex: 0,
		MatchDigest:     "m",
		DecisionJSON:    []byte(`{"verdict":"approved"}`),
	}
}

// TestStorageRepository_CompleteStepAttempt covers the happy path: a legal
// running -> succeeded completion records status, route and output evidence,
// stamps FinishedAt, bumps the version and derives exactly one transition.
func TestStorageRepository_CompleteStepAttempt(t *testing.T) {
	ctx := context.Background()
	for name, repo := range repos(t) {
		t.Run(name, func(t *testing.T) {
			run := runID(t)
			snap, json := newRun(t, run)
			requireErr(t, repo.CreateRun(ctx, snap, json), nil, "CreateRun")

			a1 := StepAttempt{AttemptID: "att-1", RunID: run, StepID: "plan", AttemptNo: 1}
			requireErr(t, repo.CreateStepAttempt(ctx, a1), nil, "create attempt")

			outcome := completionOutcome()
			// running -> succeeded from version 1 is legal.
			requireErr(t, repo.CompleteStepAttempt(ctx, run, "att-1", 1, outcome),
				nil, "complete attempt running->succeeded")

			got, err := repo.GetStepAttempt(ctx, run, "att-1")
			if err != nil {
				t.Fatalf("GetStepAttempt: %v", err)
			}
			if got.Status != AttemptStatusSucceeded {
				t.Fatalf("attempt Status = %q, want %q", got.Status, AttemptStatusSucceeded)
			}
			if got.OutputRef != outcome.OutputRef {
				t.Fatalf("OutputRef = %q, want %q", got.OutputRef, outcome.OutputRef)
			}
			if got.OutputDigest != outcome.OutputDigest {
				t.Fatalf("OutputDigest = %q, want %q", got.OutputDigest, outcome.OutputDigest)
			}
			if got.ToStepID != "implement" || got.TransitionIndex != 0 || got.MatchDigest != "m" {
				t.Fatalf("route fields = (%q, %d, %q), want (implement, 0, m)",
					got.ToStepID, got.TransitionIndex, got.MatchDigest)
			}
			if !bytes.Equal(got.DecisionJSON, outcome.DecisionJSON) {
				t.Fatalf("DecisionJSON = %s, want %s", got.DecisionJSON, outcome.DecisionJSON)
			}
			if got.FinishedAt == nil {
				t.Fatalf("attempt FinishedAt not stamped")
			}
			if !got.FinishedAt.Equal(fixedClock) {
				t.Fatalf("attempt FinishedAt = %v, want %v", *got.FinishedAt, fixedClock)
			}
			if got.Version != 2 {
				t.Fatalf("attempt Version = %d, want 2", got.Version)
			}

			// Exactly one transition record carrying the outcome's fields.
			trans, err := repo.ListTransitions(ctx, run)
			if err != nil {
				t.Fatalf("ListTransitions: %v", err)
			}
			if len(trans) != 1 {
				t.Fatalf("ListTransitions = %d records, want 1", len(trans))
			}
			tr := trans[0]
			if tr.RunID != run {
				t.Fatalf("transition RunID = %q, want %q", tr.RunID, run)
			}
			if tr.FromAttemptID != "att-1" {
				t.Fatalf("transition FromAttemptID = %q, want att-1", tr.FromAttemptID)
			}
			if tr.ToStepID != "implement" || tr.TransitionIndex != 0 || tr.MatchDigest != "m" {
				t.Fatalf("transition route = (%q, %d, %q), want (implement, 0, m)",
					tr.ToStepID, tr.TransitionIndex, tr.MatchDigest)
			}
			if !bytes.Equal(tr.DecisionJSON, outcome.DecisionJSON) {
				t.Fatalf("transition DecisionJSON = %s, want %s", tr.DecisionJSON, outcome.DecisionJSON)
			}
			if !tr.CreatedAt.Equal(fixedClock) {
				t.Fatalf("transition CreatedAt = %v, want %v", tr.CreatedAt, fixedClock)
			}
		})
	}
}

// TestStorageRepository_CompleteStepAttemptConflicts covers the conflict
// cases: a stale expected version, a non-terminal outcome status, an illegal
// edge on an already-terminal attempt, and the absence of derived transitions
// from failed completions.
func TestStorageRepository_CompleteStepAttemptConflicts(t *testing.T) {
	ctx := context.Background()
	for name, repo := range repos(t) {
		t.Run(name, func(t *testing.T) {
			run := runID(t)
			snap, json := newRun(t, run)
			requireErr(t, repo.CreateRun(ctx, snap, json), nil, "CreateRun")

			a1 := StepAttempt{AttemptID: "att-1", RunID: run, StepID: "plan", AttemptNo: 1}
			requireErr(t, repo.CreateStepAttempt(ctx, a1), nil, "create attempt")
			outcome := completionOutcome()
			requireErr(t, repo.CompleteStepAttempt(ctx, run, "att-1", 1, outcome),
				nil, "complete attempt running->succeeded")

			// Stale expected version -> ErrConflict, attempt unchanged.
			requireErr(t, repo.CompleteStepAttempt(ctx, run, "att-1", 1, outcome),
				ErrConflict, "stale attempt version")
			got, err := repo.GetStepAttempt(ctx, run, "att-1")
			if err != nil {
				t.Fatalf("GetStepAttempt after conflict: %v", err)
			}
			if got.Status != AttemptStatusSucceeded || got.Version != 2 {
				t.Fatalf("attempt after conflict = (%q, v%d), want (succeeded, v2) unchanged", got.Status, got.Version)
			}

			// Outcome with a non-terminal status (running) is invalid.
			a2 := StepAttempt{AttemptID: "att-2", RunID: run, StepID: "plan", AttemptNo: 2}
			requireErr(t, repo.CreateStepAttempt(ctx, a2), nil, "create attempt 2")
			bad := AttemptOutcome{Status: AttemptStatusRunning}
			requireErr(t, repo.CompleteStepAttempt(ctx, run, "att-2", 1, bad),
				ErrInvalidTransition, "outcome status running")

			// Illegal edge: completing an already-terminal attempt.
			requireErr(t, repo.CompleteStepAttempt(ctx, run, "att-1", 2, outcome),
				ErrInvalidTransition, "complete terminal attempt")

			// Neither failed completion derived a transition.
			trans, err := repo.ListTransitions(ctx, run)
			if err != nil {
				t.Fatalf("ListTransitions: %v", err)
			}
			if len(trans) != 1 {
				t.Fatalf("ListTransitions after failures = %d records, want 1", len(trans))
			}
		})
	}
}

// ---------------------------------------------------------------------------
// 7. Interrupted completion derives no transition
// ---------------------------------------------------------------------------

func TestStorageRepository_InterruptedAttempt(t *testing.T) {
	ctx := context.Background()
	for name, repo := range repos(t) {
		t.Run(name, func(t *testing.T) {
			run := runID(t)
			snap, json := newRun(t, run)
			requireErr(t, repo.CreateRun(ctx, snap, json), nil, "CreateRun")

			a1 := StepAttempt{AttemptID: "att-1", RunID: run, StepID: "plan", AttemptNo: 1}
			requireErr(t, repo.CreateStepAttempt(ctx, a1), nil, "create attempt")

			outcome := AttemptOutcome{Status: AttemptStatusInterrupted}
			requireErr(t, repo.CompleteStepAttempt(ctx, run, "att-1", 1, outcome),
				nil, "interrupt attempt")

			got, err := repo.GetStepAttempt(ctx, run, "att-1")
			if err != nil {
				t.Fatalf("GetStepAttempt: %v", err)
			}
			if got.Status != AttemptStatusInterrupted {
				t.Fatalf("attempt Status = %q, want %q", got.Status, AttemptStatusInterrupted)
			}
			if got.FinishedAt == nil {
				t.Fatalf("attempt FinishedAt not stamped on interrupt")
			}
			if !got.FinishedAt.Equal(fixedClock) {
				t.Fatalf("attempt FinishedAt = %v, want %v", *got.FinishedAt, fixedClock)
			}

			// No transition is derived from an interrupted completion.
			trans, err := repo.ListTransitions(ctx, run)
			if err != nil {
				t.Fatalf("ListTransitions: %v", err)
			}
			if len(trans) != 0 {
				t.Fatalf("ListTransitions = %d records, want 0 for interrupted attempt", len(trans))
			}
		})
	}
}

// ---------------------------------------------------------------------------
// 18. Derived ActiveStepID stays fresh after in-place mutations
// ---------------------------------------------------------------------------

func TestStorageRepository_GetRunActiveStepDerived(t *testing.T) {
	ctx := context.Background()
	for name, repo := range repos(t) {
		t.Run(name, func(t *testing.T) {
			run := runID(t)
			snap, json := newRun(t, run)
			snap.ActiveStepID = "plan"
			requireErr(t, repo.CreateRun(ctx, snap, json), nil, "CreateRun")

			// The initial step is the derived value right after admission.
			got, err := repo.GetRun(ctx, run)
			if err != nil {
				t.Fatalf("GetRun: %v", err)
			}
			if got.ActiveStepID != "plan" {
				t.Fatalf("GetRun.ActiveStepID = %q, want %q (initial step)", got.ActiveStepID, "plan")
			}

			// Starting an attempt for the same step keeps the derived value.
			a1 := StepAttempt{AttemptID: "att-1", RunID: run, StepID: "plan", AttemptNo: 1}
			requireErr(t, repo.CreateStepAttempt(ctx, a1), nil, "create plan attempt")
			got, err = repo.GetRun(ctx, run)
			if err != nil {
				t.Fatalf("GetRun after attempt start: %v", err)
			}
			if got.ActiveStepID != "plan" {
				t.Fatalf("GetRun.ActiveStepID = %q after attempt start, want %q", got.ActiveStepID, "plan")
			}

			// Completing with a route moves the derived value ON THE SAME
			// instance (no reopen, no rebuild): the in-place mutation must
			// refresh ActiveStepID.
			outcome := AttemptOutcome{Status: AttemptStatusSucceeded, ToStepID: "implement", TransitionIndex: 0}
			requireErr(t, repo.CompleteStepAttempt(ctx, run, "att-1", 1, outcome), nil, "complete -> implement")
			got, err = repo.GetRun(ctx, run)
			if err != nil {
				t.Fatalf("GetRun after completion: %v", err)
			}
			if got.ActiveStepID != "implement" {
				t.Fatalf("GetRun.ActiveStepID = %q after route, want %q", got.ActiveStepID, "implement")
			}

			// ListRuns surfaces the same derived value.
			runs, err := repo.ListRuns(ctx)
			if err != nil {
				t.Fatalf("ListRuns: %v", err)
			}
			if len(runs) != 1 || runs[0].ActiveStepID != "implement" {
				t.Fatalf("ListRuns[0].ActiveStepID = %q, want %q", runs[0].ActiveStepID, "implement")
			}

			// An interrupted completion carries no route: the derived value
			// stays on the completed attempt's step (the completion event has
			// no step of its own).
			a2 := StepAttempt{AttemptID: "att-2", RunID: run, StepID: "implement", AttemptNo: 1}
			requireErr(t, repo.CreateStepAttempt(ctx, a2), nil, "create implement attempt")
			requireErr(t, repo.CompleteStepAttempt(ctx, run, "att-2", 1, AttemptOutcome{Status: AttemptStatusInterrupted}),
				nil, "interrupt implement attempt")
			got, err = repo.GetRun(ctx, run)
			if err != nil {
				t.Fatalf("GetRun after interrupt: %v", err)
			}
			if got.ActiveStepID != "implement" {
				t.Fatalf("GetRun.ActiveStepID = %q after interrupt, want %q", got.ActiveStepID, "implement")
			}
		})
	}
}
