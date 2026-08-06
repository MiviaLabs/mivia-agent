package ledger

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/storage"
)

// TestStorageRepository_CompleteStepAttemptErrorRef pins that a failed
// attempt's error reference round-trips through the ledger projection.
func TestStorageRepository_CompleteStepAttemptErrorRef(t *testing.T) {
	ctx := context.Background()
	for name, repo := range repos(t) {
		t.Run(name, func(t *testing.T) {
			run, raw := newRun(t, runID(t, name))
			if err := repo.CreateRun(ctx, run, raw); err != nil {
				t.Fatal(err)
			}
			attempt := StepAttempt{AttemptID: "wfa-step-1", RunID: run.RunID, StepID: "step", AttemptNo: 1}
			if err := repo.CreateStepAttempt(ctx, attempt); err != nil {
				t.Fatal(err)
			}
			started, err := repo.GetStepAttempt(ctx, run.RunID, attempt.AttemptID)
			if err != nil {
				t.Fatal(err)
			}
			outcome := AttemptOutcome{
				Status: AttemptStatusFailed, ErrorRef: "sha256:errorref",
				ToStepID: "failure", TransitionIndex: -1,
			}
			if err := repo.CompleteStepAttempt(ctx, run.RunID, attempt.AttemptID, started.Version, outcome); err != nil {
				t.Fatal(err)
			}
			done, err := repo.GetStepAttempt(ctx, run.RunID, attempt.AttemptID)
			if err != nil {
				t.Fatal(err)
			}
			if done.ErrorRef != "sha256:errorref" {
				t.Fatalf("GetStepAttempt ErrorRef = %q, want %q", done.ErrorRef, "sha256:errorref")
			}
			list, err := repo.ListStepAttempts(ctx, run.RunID)
			if err != nil {
				t.Fatal(err)
			}
			if len(list) != 1 || list[0].ErrorRef != "sha256:errorref" {
				t.Fatalf("ListStepAttempts ErrorRef = %q, want %q", list[0].ErrorRef, "sha256:errorref")
			}
		})
	}
}

// TestRebuildProjectionHydratesErrorRef pins that replaying the event log
// (restart) restores a completed attempt's error reference.
func TestRebuildProjectionHydratesErrorRef(t *testing.T) {
	run, _ := newRun(t, runID(t))
	attempt := StepAttempt{AttemptID: "wfa-step-1", RunID: run.RunID, StepID: "step", AttemptNo: 1}
	created, err := json.Marshal(runCreatedPayload{Run: run, SnapshotJSON: []byte("snapshot"), CreatedAt: fixedClock})
	if err != nil {
		t.Fatal(err)
	}
	started, err := json.Marshal(attemptStartedPayload{Attempt: attempt, CreatedAt: fixedClock})
	if err != nil {
		t.Fatal(err)
	}
	completed, err := json.Marshal(attemptCompletedPayload{
		AttemptID: attempt.AttemptID, Status: AttemptStatusFailed,
		ErrorRef: "sha256:errorref", FinishedAt: fixedClock, CreatedAt: fixedClock,
	})
	if err != nil {
		t.Fatal(err)
	}
	events := []storage.Event{
		{Kind: eventKindRunCreated, Payload: created},
		{Kind: eventKindAttemptStarted, Payload: started},
		{Kind: eventKindAttemptCompleted, Payload: completed},
	}
	proj, err := RebuildProjection(events)
	if err != nil {
		t.Fatal(err)
	}
	if len(proj.Attempts) != 1 {
		t.Fatalf("attempts = %d, want 1", len(proj.Attempts))
	}
	if proj.Attempts[0].ErrorRef != "sha256:errorref" {
		t.Fatalf("rebuilt ErrorRef = %q, want %q", proj.Attempts[0].ErrorRef, "sha256:errorref")
	}
}

// TestEventsSummaryIncludesErrorRef pins that the audit trail names the error
// reference of a failed attempt.
func TestEventsSummaryIncludesErrorRef(t *testing.T) {
	ctx := context.Background()
	for name, repo := range repos(t) {
		t.Run(name, func(t *testing.T) {
			run, raw := newRun(t, runID(t, name))
			if err := repo.CreateRun(ctx, run, raw); err != nil {
				t.Fatal(err)
			}
			attempt := StepAttempt{AttemptID: "wfa-step-1", RunID: run.RunID, StepID: "step", AttemptNo: 1}
			if err := repo.CreateStepAttempt(ctx, attempt); err != nil {
				t.Fatal(err)
			}
			started, err := repo.GetStepAttempt(ctx, run.RunID, attempt.AttemptID)
			if err != nil {
				t.Fatal(err)
			}
			outcome := AttemptOutcome{Status: AttemptStatusFailed, ErrorRef: "sha256:errorref"}
			if err := repo.CompleteStepAttempt(ctx, run.RunID, attempt.AttemptID, started.Version, outcome); err != nil {
				t.Fatal(err)
			}
			records, err := repo.ListEvents(ctx, run.RunID, 0, 0)
			if err != nil {
				t.Fatal(err)
			}
			var found string
			for _, rec := range records {
				if rec.Kind == eventKindAttemptCompleted {
					found = rec.Summary
				}
			}
			if found == "" {
				t.Fatal("no wf_attempt_completed event found")
			}
			if !strings.Contains(found, "error sha256:errorref") {
				t.Fatalf("summary %q does not name the error ref", found)
			}
		})
	}
}
