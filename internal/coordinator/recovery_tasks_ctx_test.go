package coordinator

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
)

// buildTestCoordinator creates a coordinator with a real (fast) repo for
// tests that exercise the tasksFromSnapshots / terminalTaskResultWithOutput
// path without needing slow-load cancellation behaviour.
func buildTestCoordinator(t *testing.T) (*coordinator, *ledger.MemoryLedgerRepository) {
	t.Helper()
	repo := ledger.NewMemoryLedgerRepository()
	d := runtime.New(runtime.Policy{})
	_ = d.Register(runtime.Subagent, "worker", staticHandler{out: json.RawMessage(`{"ok":true}`)})
	p := subagents.New(d, subagents.Policy{Workers: 1})
	c := New(repo, p)
	return c.(*coordinator), repo
}

// TestTasksFromSnapshots_CompletedWithOutputRef (TC-A success): a completed
// snapshot carrying a non-empty OutputRef should land in the done map with
// the loaded output content. This exercises the ctx pass-through from
// tasksFromSnapshots → tasksFromSnapshotsWithAuthority → terminalTaskResultWithOutput
// → repo.LoadContent.
func TestTasksFromSnapshots_CompletedWithOutputRef(t *testing.T) {
	c, repo := buildTestCoordinator(t)
	ctx := context.Background()

	// Store output content so the reference resolves.
	outputRef := "ref:output:tc-a-123"
	storedContent := []byte(`{"result":"hello"}`)
	if err := repo.StoreContent(ctx, outputRef, storedContent); err != nil {
		t.Fatal(err)
	}

	snaps := []ledger.TaskSnapshot{
		{
			TaskID:      "task-a",
			Status:      string(ledger.TaskStatusCompleted),
			OutputRef:   outputRef,
			HandlerName: "worker",
			AgentName:   "worker",
			AgentDigest: "test-digest",
			Input:       json.RawMessage(`{"prompt":"work"}`),
		},
	}

	tasks, done, err := c.tasksFromSnapshots(ctx, snaps)
	if err != nil {
		t.Fatalf("tasksFromSnapshots returned error: %v", err)
	}
	if len(tasks) != 0 {
		t.Fatalf("expected 0 non-terminal tasks, got %d", len(tasks))
	}
	result, ok := done["task-a"]
	if !ok {
		t.Fatal("expected completed task in done map")
	}
	if result.Status != string(ledger.TaskStatusCompleted) {
		t.Fatalf("expected status completed, got %s", result.Status)
	}
	if string(result.Output) != string(storedContent) {
		t.Fatalf("Output = %s, want %s", result.Output, storedContent)
	}
}

// TestTerminalTaskResultWithOutput_CanceledCtx (TC-B canceled context):
// when LoadContent is slow and ctx is already canceled, the load should
// fail immediately (returning an error) so the output remains nil.
func TestTerminalTaskResultWithOutput_CanceledCtx(t *testing.T) {
	slowRepo := &slowLoadContentRepo{
		MemoryLedgerRepository: ledger.NewMemoryLedgerRepository(),
		loadDelay:              5 * time.Second,
	}

	// Store output content so the ref is valid.
	outputRef := "ref:output:tc-b-456"
	_ = slowRepo.StoreContent(context.Background(), outputRef, []byte(`{"result":"slow"}`))

	c := &coordinator{repo: slowRepo}

	snap := ledger.TaskSnapshot{
		TaskID:    "task-b",
		Status:    string(ledger.TaskStatusCompleted),
		OutputRef: outputRef,
	}

	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	result, terminal := c.terminalTaskResultWithOutput(canceledCtx, snap)
	if !terminal {
		t.Fatal("expected terminal=true for completed task")
	}
	if result.Output != nil {
		t.Errorf("Output = %s, want nil (LoadContent should fail with canceled context)", result.Output)
	}
}

// TestTasksFromSnapshots_MixedTerminalAndNonTerminal (TC-C mixed): a completed
// snapshot with OutputRef goes to done, a running snapshot goes to out.
func TestTasksFromSnapshots_MixedTerminalAndNonTerminal(t *testing.T) {
	c, repo := buildTestCoordinator(t)
	ctx := context.Background()

	outputRef := "ref:output:tc-c-789"
	_ = repo.StoreContent(ctx, outputRef, []byte(`{"result":"mixed"}`))

	snaps := []ledger.TaskSnapshot{
		{
			TaskID:      "task-c-done",
			Status:      string(ledger.TaskStatusCompleted),
			OutputRef:   outputRef,
			HandlerName: "worker",
			AgentName:   "worker",
			AgentDigest: "test-digest",
			Input:       json.RawMessage(`{"prompt":"work"}`),
		},
		{
			TaskID:      "task-c-running",
			Status:      string(ledger.TaskStatusRunning),
			HandlerName: "worker",
			AgentName:   "worker",
			AgentDigest: "test-digest",
			Input:       json.RawMessage(`{"prompt":"still-going"}`),
		},
	}

	tasks, done, err := c.tasksFromSnapshots(ctx, snaps)
	if err != nil {
		t.Fatalf("tasksFromSnapshots returned error: %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("expected 1 non-terminal task, got %d", len(tasks))
	}
	if tasks[0].ID != "task-c-running" {
		t.Fatalf("expected non-terminal task ID task-c-running, got %s", tasks[0].ID)
	}
	if _, ok := done["task-c-done"]; !ok {
		t.Fatal("expected completed task-c-done in done map")
	}
	if _, ok := done["task-c-running"]; ok {
		t.Fatal("running task should not be in done map")
	}
}

// TestTasksFromSnapshots_CompletedEmptyOutputRef (TC-D): a completed
// snapshot with an empty OutputRef lands in done with nil Output, no
// LoadContent call, no error.
func TestTasksFromSnapshots_CompletedEmptyOutputRef(t *testing.T) {
	c, _ := buildTestCoordinator(t)
	ctx := context.Background()

	snaps := []ledger.TaskSnapshot{
		{
			TaskID:      "task-d",
			Status:      string(ledger.TaskStatusCompleted),
			OutputRef:   "", // empty output ref
			HandlerName: "worker",
			AgentName:   "worker",
			AgentDigest: "test-digest",
			Input:       json.RawMessage(`{"prompt":"work"}`),
		},
	}

	tasks, done, err := c.tasksFromSnapshots(ctx, snaps)
	if err != nil {
		t.Fatalf("tasksFromSnapshots returned error: %v", err)
	}
	if len(tasks) != 0 {
		t.Fatalf("expected 0 non-terminal tasks, got %d", len(tasks))
	}
	result, ok := done["task-d"]
	if !ok {
		t.Fatal("expected completed task in done map")
	}
	if result.Output != nil {
		t.Fatalf("Output = %s, want nil for empty OutputRef", result.Output)
	}
}

// TestTasksFromSnapshots_CanceledTerminalStatus (TC-E): a canceled task
// is terminal and should land in done with error status set.
func TestTasksFromSnapshots_CanceledTerminalStatus(t *testing.T) {
	c, _ := buildTestCoordinator(t)
	ctx := context.Background()

	snaps := []ledger.TaskSnapshot{
		{
			TaskID:      "task-e",
			Status:      string(ledger.TaskStatusCanceled),
			OutputRef:   "",
			HandlerName: "worker",
			AgentName:   "worker",
			AgentDigest: "test-digest",
			Input:       json.RawMessage(`{"prompt":"work"}`),
		},
	}

	tasks, done, err := c.tasksFromSnapshots(ctx, snaps)
	if err != nil {
		t.Fatalf("tasksFromSnapshots returned error: %v", err)
	}
	if len(tasks) != 0 {
		t.Fatalf("expected 0 non-terminal tasks, got %d", len(tasks))
	}
	result, ok := done["task-e"]
	if !ok {
		t.Fatal("expected canceled task in done map")
	}
	if result.Status != string(ledger.TaskStatusCanceled) {
		t.Fatalf("expected status canceled, got %s", result.Status)
	}
	if result.Err == nil {
		t.Fatal("expected non-nil Err for canceled task")
	}
}
