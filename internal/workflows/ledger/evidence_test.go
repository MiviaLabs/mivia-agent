package ledger

import (
	"bytes"
	"context"
	"testing"
)

func TestStepAttemptPersistsBoundedEvidenceSelection(t *testing.T) {
	repo := NewMemoryRepository()
	ctx := context.Background()
	run := runID(t)
	if err := repo.CreateRun(ctx, RunSnapshot{RunID: run, Status: RunStatusPending, ActiveStepID: "step"}, makeSnapshotJSON(t)); err != nil {
		t.Fatal(err)
	}
	if err := repo.CreateStepAttempt(ctx, StepAttempt{AttemptID: "a1", RunID: run, StepID: "step", AttemptNo: 1}); err != nil {
		t.Fatal(err)
	}
	evidence := []byte(`[{"name":"task","source":"input","bytes":5,"digest":"sha256:test"}]`)
	if err := repo.CompleteStepAttempt(ctx, run, "a1", 1, AttemptOutcome{Status: AttemptStatusSucceeded, EvidenceJSON: evidence}); err != nil {
		t.Fatal(err)
	}
	got, err := repo.GetStepAttempt(ctx, run, "a1")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got.EvidenceJSON, evidence) {
		t.Fatalf("evidence = %s, want %s", got.EvidenceJSON, evidence)
	}
}

func TestStepAttemptRejectsOversizedEvidence(t *testing.T) {
	repo := NewMemoryRepository()
	ctx := context.Background()
	run := runID(t)
	if err := repo.CreateRun(ctx, RunSnapshot{RunID: run, Status: RunStatusPending, ActiveStepID: "step"}, makeSnapshotJSON(t)); err != nil {
		t.Fatal(err)
	}
	if err := repo.CreateStepAttempt(ctx, StepAttempt{AttemptID: "a1", RunID: run, StepID: "step", AttemptNo: 1}); err != nil {
		t.Fatal(err)
	}
	if err := repo.CompleteStepAttempt(ctx, run, "a1", 1, AttemptOutcome{Status: AttemptStatusSucceeded, EvidenceJSON: bytes.Repeat([]byte{'x'}, MaxEvidenceBytes+1)}); err == nil {
		t.Fatal("oversized evidence was accepted")
	}
}
