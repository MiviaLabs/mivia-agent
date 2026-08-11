package ledger

import (
	"context"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// 19. SetStepAttemptPrompt
// ---------------------------------------------------------------------------

// TestStorageRepository_SetStepAttemptPrompt covers the round trip: the prompt
// ref is recorded on a RUNNING attempt (no terminal status required — a prompt
// is persisted at dispatch time, before completion) and surfaces through
// GetStepAttempt and ListStepAttempts; the wf_attempt_prompt event lands in
// the audit trail.
func TestStorageRepository_SetStepAttemptPrompt(t *testing.T) {
	ctx := context.Background()
	for name, repo := range repos(t) {
		t.Run(name, func(t *testing.T) {
			run := runID(t)
			snap, json := newRun(t, run)
			requireErr(t, repo.CreateRun(ctx, snap, json), nil, "CreateRun")

			a1 := StepAttempt{AttemptID: "att-1", RunID: run, StepID: "plan", AttemptNo: 1}
			requireErr(t, repo.CreateStepAttempt(ctx, a1), nil, "create attempt")

			// The attempt is Running when the prompt is set.
			got, err := repo.GetStepAttempt(ctx, run, "att-1")
			if err != nil {
				t.Fatalf("GetStepAttempt before prompt: %v", err)
			}
			if got.Status != AttemptStatusRunning {
				t.Fatalf("attempt Status = %q, want %q before prompt", got.Status, AttemptStatusRunning)
			}

			const promptRef = "refs/prompts/p-1"
			requireErr(t, repo.SetStepAttemptPrompt(ctx, run, "att-1", promptRef), nil, "set prompt")

			got, err = repo.GetStepAttempt(ctx, run, "att-1")
			if err != nil {
				t.Fatalf("GetStepAttempt after prompt: %v", err)
			}
			if got.PromptRef != promptRef {
				t.Fatalf("attempt PromptRef = %q, want %q", got.PromptRef, promptRef)
			}
			if got.Status != AttemptStatusRunning || got.Version != 1 {
				t.Fatalf("attempt after prompt = (%q, v%d), want (running, v1) untouched", got.Status, got.Version)
			}

			list, err := repo.ListStepAttempts(ctx, run)
			if err != nil {
				t.Fatalf("ListStepAttempts: %v", err)
			}
			if len(list) != 1 || list[0].PromptRef != promptRef {
				t.Fatalf("ListStepAttempts = %+v, want one attempt with PromptRef %q", list, promptRef)
			}

			// The wf_attempt_prompt event is in the audit trail.
			events, err := repo.ListEvents(ctx, run, 0, 0)
			if err != nil {
				t.Fatalf("ListEvents: %v", err)
			}
			promptEvents := 0
			for _, ev := range events {
				if ev.Kind == eventKindAttemptPrompt {
					promptEvents++
					if ev.Summary == "" {
						t.Fatalf("prompt event summary empty: %+v", ev)
					}
				}
			}
			if promptEvents != 1 {
				t.Fatalf("ListEvents has %d wf_attempt_prompt events, want 1", promptEvents)
			}
		})
	}
}

// TestStorageRepository_SetStepAttemptPromptNotFound covers ErrNotFound for an
// absent run or an unknown attempt.
func TestStorageRepository_SetStepAttemptPromptNotFound(t *testing.T) {
	ctx := context.Background()
	for name, repo := range repos(t) {
		t.Run(name, func(t *testing.T) {
			run := runID(t)
			snap, json := newRun(t, run)
			requireErr(t, repo.CreateRun(ctx, snap, json), nil, "CreateRun")
			requireErr(t, repo.CreateStepAttempt(ctx, StepAttempt{AttemptID: "att-1", RunID: run, StepID: "plan", AttemptNo: 1}),
				nil, "create attempt")

			requireErr(t, repo.SetStepAttemptPrompt(ctx, run, "att-missing", "refs/prompts/p-1"),
				ErrNotFound, "unknown attempt")
			requireErr(t, repo.SetStepAttemptPrompt(ctx, "wfr-no-such-run", "att-1", "refs/prompts/p-1"),
				ErrNotFound, "unknown run")
		})
	}
}

// TestStorageRepository_SetStepAttemptPromptEmptyRef covers rejection of an
// empty prompt ref: the attempt is left without a prompt and no event is
// appended.
func TestStorageRepository_SetStepAttemptPromptEmptyRef(t *testing.T) {
	ctx := context.Background()
	for name, repo := range repos(t) {
		t.Run(name, func(t *testing.T) {
			run := runID(t)
			snap, json := newRun(t, run)
			requireErr(t, repo.CreateRun(ctx, snap, json), nil, "CreateRun")
			requireErr(t, repo.CreateStepAttempt(ctx, StepAttempt{AttemptID: "att-1", RunID: run, StepID: "plan", AttemptNo: 1}),
				nil, "create attempt")

			if err := repo.SetStepAttemptPrompt(ctx, run, "att-1", ""); err == nil {
				t.Fatal("SetStepAttemptPrompt accepted an empty prompt ref")
			}
			got, err := repo.GetStepAttempt(ctx, run, "att-1")
			if err != nil {
				t.Fatalf("GetStepAttempt: %v", err)
			}
			if got.PromptRef != "" {
				t.Fatalf("attempt PromptRef = %q after rejected empty ref, want empty", got.PromptRef)
			}
		})
	}
}

// TestStorageRepository_SetStepAttemptPromptIdempotent covers the idempotent
// retry contract: setting the SAME ref twice is a no-op and appends only one
// wf_attempt_prompt event.
func TestStorageRepository_SetStepAttemptPromptIdempotent(t *testing.T) {
	ctx := context.Background()
	for name, repo := range repos(t) {
		t.Run(name, func(t *testing.T) {
			run := runID(t)
			snap, json := newRun(t, run)
			requireErr(t, repo.CreateRun(ctx, snap, json), nil, "CreateRun")
			requireErr(t, repo.CreateStepAttempt(ctx, StepAttempt{AttemptID: "att-1", RunID: run, StepID: "plan", AttemptNo: 1}),
				nil, "create attempt")

			const promptRef = "refs/prompts/p-1"
			requireErr(t, repo.SetStepAttemptPrompt(ctx, run, "att-1", promptRef), nil, "first set")
			requireErr(t, repo.SetStepAttemptPrompt(ctx, run, "att-1", promptRef), nil, "retry same ref")

			events, err := repo.ListEvents(ctx, run, 0, 0)
			if err != nil {
				t.Fatalf("ListEvents: %v", err)
			}
			n := 0
			for _, ev := range events {
				if ev.Kind == eventKindAttemptPrompt {
					n++
				}
			}
			if n != 1 {
				t.Fatalf("ListEvents has %d wf_attempt_prompt events after idempotent retry, want 1", n)
			}
		})
	}
}

// TestStorageRepository_SetStepAttemptPromptConflict covers attempt
// immutability: once an attempt carries a prompt ref, a DIFFERENT ref is
// rejected (attempts are immutable after dispatch) and the original ref is
// preserved.
func TestStorageRepository_SetStepAttemptPromptConflict(t *testing.T) {
	ctx := context.Background()
	for name, repo := range repos(t) {
		t.Run(name, func(t *testing.T) {
			run := runID(t)
			snap, json := newRun(t, run)
			requireErr(t, repo.CreateRun(ctx, snap, json), nil, "CreateRun")
			requireErr(t, repo.CreateStepAttempt(ctx, StepAttempt{AttemptID: "att-1", RunID: run, StepID: "plan", AttemptNo: 1}),
				nil, "create attempt")

			const promptRef = "refs/prompts/p-1"
			requireErr(t, repo.SetStepAttemptPrompt(ctx, run, "att-1", promptRef), nil, "set prompt")

			requireErr(t, repo.SetStepAttemptPrompt(ctx, run, "att-1", "refs/prompts/p-2"),
				ErrConflict, "different ref on prompted attempt")

			got, err := repo.GetStepAttempt(ctx, run, "att-1")
			if err != nil {
				t.Fatalf("GetStepAttempt after conflict: %v", err)
			}
			if got.PromptRef != promptRef {
				t.Fatalf("attempt PromptRef = %q after conflict, want %q unchanged", got.PromptRef, promptRef)
			}
		})
	}
}

// TestStorageRepository_SetStepAttemptPromptSurvivesCompletion covers the
// invariant that completing an attempt does NOT wipe its prompt ref: the
// wf_attempt_completed mutation must leave PromptRef intact.
func TestStorageRepository_SetStepAttemptPromptSurvivesCompletion(t *testing.T) {
	ctx := context.Background()
	for name, repo := range repos(t) {
		t.Run(name, func(t *testing.T) {
			run := runID(t)
			snap, json := newRun(t, run)
			requireErr(t, repo.CreateRun(ctx, snap, json), nil, "CreateRun")
			requireErr(t, repo.CreateStepAttempt(ctx, StepAttempt{AttemptID: "att-1", RunID: run, StepID: "plan", AttemptNo: 1}),
				nil, "create attempt")

			const promptRef = "refs/prompts/p-1"
			requireErr(t, repo.SetStepAttemptPrompt(ctx, run, "att-1", promptRef), nil, "set prompt")
			requireErr(t, repo.CompleteStepAttempt(ctx, run, "att-1", 1, completionOutcome()), nil, "complete attempt")

			got, err := repo.GetStepAttempt(ctx, run, "att-1")
			if err != nil {
				t.Fatalf("GetStepAttempt after completion: %v", err)
			}
			if got.PromptRef != promptRef {
				t.Fatalf("attempt PromptRef = %q after completion, want %q (completion must not wipe it)", got.PromptRef, promptRef)
			}
			if got.Status != AttemptStatusSucceeded {
				t.Fatalf("attempt Status = %q, want %q", got.Status, AttemptStatusSucceeded)
			}
		})
	}
}

// TestStorageRepository_SetStepAttemptPromptReplay covers event-sourced
// durability across repository instances: a legacy run whose log has no
// wf_attempt_prompt event replays with an empty PromptRef, and a prompt set on
// one instance is visible to the other after catch-up.
func TestStorageRepository_SetStepAttemptPromptReplay(t *testing.T) {
	ctx := context.Background()
	for _, pair := range repoPairs() {
		t.Run(pair.name, func(t *testing.T) {
			a, b, done := pair.new(t)
			defer done()

			run := runID(t)
			snap, json := newRun(t, run)
			requireErr(t, a.CreateRun(ctx, snap, json), nil, "CreateRun on A")
			requireErr(t, a.CreateStepAttempt(ctx, StepAttempt{AttemptID: "att-1", RunID: run, StepID: "plan", AttemptNo: 1}),
				nil, "create attempt on A")

			// Legacy replay: no prompt event in the log -> empty PromptRef.
			got, err := b.GetStepAttempt(ctx, run, "att-1")
			if err != nil {
				t.Fatalf("GetStepAttempt on B (legacy): %v", err)
			}
			if got.PromptRef != "" {
				t.Fatalf("legacy attempt PromptRef = %q, want empty", got.PromptRef)
			}

			// A records the prompt; B catches up and sees it.
			const promptRef = "refs/prompts/p-1"
			requireErr(t, a.SetStepAttemptPrompt(ctx, run, "att-1", promptRef), nil, "set prompt on A")
			got, err = b.GetStepAttempt(ctx, run, "att-1")
			if err != nil {
				t.Fatalf("GetStepAttempt on B after prompt: %v", err)
			}
			if got.PromptRef != promptRef {
				t.Fatalf("B PromptRef = %q after catch-up, want %q", got.PromptRef, promptRef)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// SetStepAttemptHeartbeat
// ---------------------------------------------------------------------------

// TestStorageRepository_SetStepAttemptHeartbeatCrossInstance covers replay
// across repository instances: a heartbeat written on one instance is visible
// to a second instance over the shared store after catch-up.
func TestStorageRepository_SetStepAttemptHeartbeatCrossInstance(t *testing.T) {
	ctx := context.Background()
	for _, pair := range repoPairs() {
		t.Run(pair.name, func(t *testing.T) {
			a, b, done := pair.new(t)
			defer done()

			run := runID(t)
			snap, json := newRun(t, run)
			requireErr(t, a.CreateRun(ctx, snap, json), nil, "CreateRun on A")
			requireErr(t, a.CreateStepAttempt(ctx, StepAttempt{AttemptID: "att-1", RunID: run, StepID: "plan", AttemptNo: 1}), nil, "create attempt on A")

			// Legacy replay: no heartbeat event -> zero LastHeartbeatAt.
			got, err := b.GetStepAttempt(ctx, run, "att-1")
			if err != nil {
				t.Fatalf("GetStepAttempt on B (legacy): %v", err)
			}
			if !got.LastHeartbeatAt.IsZero() {
				t.Fatalf("legacy attempt LastHeartbeatAt = %v, want zero", got.LastHeartbeatAt)
			}

			// A records heartbeats; B catches up and sees the latest.
			requireErr(t, a.SetStepAttemptHeartbeat(ctx, run, "att-1", fixedClock), nil, "heartbeat t1 on A")
			requireErr(t, a.SetStepAttemptHeartbeat(ctx, run, "att-1", fixedClock.Add(30*time.Second)), nil, "heartbeat t2 on A")
			got, err = b.GetStepAttempt(ctx, run, "att-1")
			if err != nil {
				t.Fatalf("GetStepAttempt on B after heartbeats: %v", err)
			}
			if !got.LastHeartbeatAt.Equal(fixedClock.Add(30 * time.Second)) {
				t.Fatalf("B LastHeartbeatAt = %v after catch-up, want %v", got.LastHeartbeatAt, fixedClock.Add(30*time.Second))
			}
		})
	}
}

// ---------------------------------------------------------------------------
// RecordStepAttemptOutcome
// ---------------------------------------------------------------------------

// TestStorageRepository_RecordStepAttemptOutcome pins the atomic re-entry
// API contract: the attempt and its TERMINAL outcome land in exactly ONE
// wf_attempt_completed event, the recorded attempt carries its full identity
// and route, and the duplicate/transition guards mirror CreateStepAttempt and
// CompleteStepAttempt.
func TestStorageRepository_RecordStepAttemptOutcome(t *testing.T) {
	ctx := context.Background()
	for name, repo := range repos(t) {
		t.Run(name, func(t *testing.T) {
			run := runID(t)
			snap, json := newRun(t, run)
			requireErr(t, repo.CreateRun(ctx, snap, json), nil, "CreateRun")

			attempt := StepAttempt{AttemptID: "wfa-wf-delivery-1", RunID: run, StepID: "wf-delivery", AttemptNo: 1}
			outcome := AttemptOutcome{Status: AttemptStatusFailed, ErrorRef: "sha256:repair-hint", ToStepID: "repair"}
			requireErr(t, repo.RecordStepAttemptOutcome(ctx, attempt, outcome), nil, "record")

			// Exactly ONE durable event of kind wf_attempt_completed: the
			// attempt is never observable in a non-terminal state.
			events, err := repo.ListEvents(ctx, run, 0, 0)
			if err != nil {
				t.Fatalf("ListEvents: %v", err)
			}
			completed := 0
			for _, ev := range events {
				if ev.Kind == eventKindAttemptCompleted {
					completed++
				}
			}
			if completed != 1 {
				t.Fatalf("wf_attempt_completed events = %d, want exactly 1 (a fresh attempt + its terminal outcome is one write)", completed)
			}

			// The attempt is terminal from its first observable instant, with
			// identity, outcome and timestamps intact.
			got, err := repo.GetStepAttempt(ctx, run, attempt.AttemptID)
			if err != nil {
				t.Fatalf("GetStepAttempt: %v", err)
			}
			if got.Status != AttemptStatusFailed {
				t.Fatalf("attempt Status = %q, want %q", got.Status, AttemptStatusFailed)
			}
			if got.StepID != "wf-delivery" || got.AttemptNo != 1 {
				t.Fatalf("attempt identity = (%q, %d), want (wf-delivery, 1)", got.StepID, got.AttemptNo)
			}
			if got.ErrorRef != outcome.ErrorRef || got.ToStepID != "repair" {
				t.Fatalf("attempt outcome = (ErrorRef %q, ToStepID %q), want (%q, repair)", got.ErrorRef, got.ToStepID, outcome.ErrorRef)
			}
			if got.Version != 1 {
				t.Fatalf("attempt Version = %d, want 1", got.Version)
			}
			if got.FinishedAt == nil || !got.FinishedAt.Equal(fixedClock) {
				t.Fatalf("attempt FinishedAt = %v, want %v", got.FinishedAt, fixedClock)
			}
			if !got.StartedAt.Equal(fixedClock) {
				t.Fatalf("attempt StartedAt = %v, want %v", got.StartedAt, fixedClock)
			}

			// The (runID, stepID, attemptNo) triple is unique, mirroring
			// CreateStepAttempt: a second record for the same triple is
			// ErrDuplicate even with a different AttemptID.
			dup := attempt
			dup.AttemptID = "wfa-wf-delivery-1-dup"
			requireErr(t, repo.RecordStepAttemptOutcome(ctx, dup, outcome), ErrDuplicate, "duplicate (runID, stepID, attemptNo)")
			// A second record reusing the same AttemptID is also a duplicate.
			requireErr(t, repo.RecordStepAttemptOutcome(ctx, attempt, outcome), ErrDuplicate, "duplicate AttemptID")

			// A non-terminal outcome is refused (ErrInvalidTransition).
			bad := StepAttempt{AttemptID: "wfa-wf-delivery-2", RunID: run, StepID: "wf-delivery", AttemptNo: 2}
			requireErr(t, repo.RecordStepAttemptOutcome(ctx, bad, AttemptOutcome{Status: AttemptStatusRunning, ToStepID: "repair"}),
				ErrInvalidTransition, "non-terminal outcome")

			// A route on an interrupted outcome is refused (the
			// no-route-on-interrupted/canceled/timed_out rule).
			bad = StepAttempt{AttemptID: "wfa-wf-delivery-3", RunID: run, StepID: "wf-delivery", AttemptNo: 3}
			requireErr(t, repo.RecordStepAttemptOutcome(ctx, bad, AttemptOutcome{Status: AttemptStatusInterrupted, ToStepID: "repair"}),
				ErrInvalidTransition, "route on interrupted")
		})
	}
}

// TestStorageRepository_RecordStepAttemptOutcomeReplay covers the replay
// contract the delivery re-entry fix depends on: a completed-only attempt (no
// wf_attempt_started event) replays with its StepID/AttemptNo/StartedAt
// identity across a fresh repository instance over the shared store, so
// LatestFailureText and the repair budget keep working after a rebuild.
func TestStorageRepository_RecordStepAttemptOutcomeReplay(t *testing.T) {
	ctx := context.Background()
	for _, pair := range repoPairs() {
		t.Run(pair.name, func(t *testing.T) {
			a, b, done := pair.new(t)
			defer done()

			run := runID(t)
			snap, json := newRun(t, run)
			requireErr(t, a.CreateRun(ctx, snap, json), nil, "CreateRun on A")
			attempt := StepAttempt{AttemptID: "wfa-wf-delivery-1", RunID: run, StepID: "wf-delivery", AttemptNo: 1}
			requireErr(t, a.RecordStepAttemptOutcome(ctx, attempt, AttemptOutcome{Status: AttemptStatusFailed, ErrorRef: "sha256:repair-hint", ToStepID: "repair"}), nil, "record on A")

			// B replays the completed-only attempt with its identity intact.
			got, err := b.GetStepAttempt(ctx, run, attempt.AttemptID)
			if err != nil {
				t.Fatalf("GetStepAttempt on B: %v", err)
			}
			if got.StepID != "wf-delivery" || got.AttemptNo != 1 {
				t.Fatalf("B replayed attempt identity = (%q, %d), want (wf-delivery, 1)", got.StepID, got.AttemptNo)
			}
			if got.Status != AttemptStatusFailed || got.ToStepID != "repair" || got.ErrorRef != "sha256:repair-hint" {
				t.Fatalf("B replayed attempt outcome = (%q, %q, %q), want (failed, repair, sha256:repair-hint)", got.Status, got.ToStepID, got.ErrorRef)
			}
			if got.StartedAt.IsZero() {
				t.Fatalf("B replayed attempt StartedAt is zero; LatestFailureText and the repair budget depend on it")
			}
			// The derived active step survives the rebuild: the completion's
			// route is the newest step-bearing event.
			runB, err := b.GetRun(ctx, run)
			if err != nil {
				t.Fatalf("GetRun on B: %v", err)
			}
			if runB.ActiveStepID != "repair" {
				t.Fatalf("B derived active step = %q, want repair", runB.ActiveStepID)
			}
		})
	}
}
