package ledger

import "testing"

func TestAwaitingInputTransitions(t *testing.T) {
	// running → awaiting_input
	if !ValidTaskTransition(string(TaskStatusRunning), string(TaskStatusAwaitingInput)) {
		t.Fatal("running → awaiting_input must be valid")
	}
	// awaiting_input → running (first return-to-running in the codebase)
	if !ValidTaskTransition(string(TaskStatusAwaitingInput), string(TaskStatusRunning)) {
		t.Fatal("awaiting_input → running must be valid")
	}
	for _, next := range []TaskStatus{
		TaskStatusCancelRequested, TaskStatusCanceled, TaskStatusTimedOut, TaskStatusFailed,
	} {
		if !ValidTaskTransition(string(TaskStatusAwaitingInput), string(next)) {
			t.Fatalf("awaiting_input → %s must be valid", next)
		}
	}
	// Must not go to completed directly from awaiting_input
	if ValidTaskTransition(string(TaskStatusAwaitingInput), string(TaskStatusCompleted)) {
		t.Fatal("awaiting_input → completed must be invalid")
	}
	// blocked remains terminal and distinct
	if ValidTaskTransition(string(TaskStatusBlocked), string(TaskStatusRunning)) {
		t.Fatal("blocked is terminal")
	}
	if ValidTaskTransition(string(TaskStatusAwaitingInput), string(TaskStatusBlocked)) {
		t.Fatal("awaiting_input → blocked is not a defined path")
	}
}

func TestAwaitingInputCASRace(t *testing.T) {
	repo := NewMemoryLedgerRepository()
	ctx := t.Context()
	if err := repo.CreateRun(ctx, "", RunSnapshot{RunID: "r1", Status: RunStatusRunning}); err != nil {
		t.Fatal(err)
	}
	if err := repo.CreateTask(ctx, TaskSnapshot{
		RunID: "r1", TaskID: "t1", Status: string(TaskStatusRunning), Version: 1,
	}); err != nil {
		t.Fatal(err)
	}
	// Park
	if err := repo.CompareAndSetTaskStatus(ctx, "r1", "t1", 1, string(TaskStatusAwaitingInput)); err != nil {
		t.Fatal(err)
	}
	// Answer wins: awaiting → running
	if err := repo.CompareAndSetTaskStatus(ctx, "r1", "t1", 2, string(TaskStatusRunning)); err != nil {
		t.Fatal(err)
	}
	// Stale cancel from version 2 loses
	if err := repo.CompareAndSetTaskStatus(ctx, "r1", "t1", 2, string(TaskStatusCancelRequested)); err != ErrConflict {
		t.Fatalf("stale CAS: %v, want ErrConflict", err)
	}
	// From running again, cancel works
	if err := repo.CompareAndSetTaskStatus(ctx, "r1", "t1", 3, string(TaskStatusCancelRequested)); err != nil {
		t.Fatal(err)
	}
}
