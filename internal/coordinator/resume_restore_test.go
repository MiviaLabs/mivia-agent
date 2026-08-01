package coordinator

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
)

// echoInputHandler reports the input it was given so a test can prove the
// resumed task carried its original payload rather than an empty one.
type echoInputHandler struct{ seen chan json.RawMessage }

func (h echoInputHandler) Invoke(_ context.Context, req runtime.Request) (json.RawMessage, error) {
	select {
	case h.seen <- req.Input:
	default:
	}
	return json.RawMessage(`{"ok":true}`), nil
}

func resumeFixture(t *testing.T, snap ledger.TaskSnapshot) (*coordinator, *ledger.StorageLedgerRepository, chan json.RawMessage) {
	t.Helper()
	store := storage.NewMemory()
	now := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)
	ctx := context.Background()

	repo := ledger.NewStorageLedgerRepository(store)
	repo.SetTimeSource(func() time.Time { return now })
	if err := repo.CreateRun(ctx, "", ledger.RunSnapshot{RunID: "run-x", Status: ledger.RunStatusRunning}); err != nil {
		t.Fatal(err)
	}
	snap.RunID = "run-x"
	if snap.AgentName == "" {
		snap.AgentName = "worker"
		snap.AgentDigest = "test-digest"
	}
	snap.Status = string(ledger.TaskStatusQueued)
	snap.Version = 1
	if err := repo.CreateTask(ctx, snap); err != nil {
		t.Fatal(err)
	}
	// Left queued: the run was interrupted before this task started, so the DAG
	// picks it up on resume without needing a retry policy.
	repo.Close()

	fresh := ledger.NewStorageLedgerRepository(store)
	fresh.SetTimeSource(func() time.Time { return now })
	seen := make(chan json.RawMessage, 4)
	d := runtime.New(runtime.Policy{})
	if err := d.Register(runtime.Subagent, "worker", echoInputHandler{seen: seen}); err != nil {
		t.Fatal(err)
	}
	p := subagents.New(d, subagents.Policy{Workers: 1, MaxDepth: 3, MaxBudget: 1000, Timeout: 5 * time.Second})
	return New(fresh, p).(*coordinator), fresh, seen
}

// The defect: resume rebuilt Task{ID, Name, DependsOn} and dropped Input, so
// every resumed task failed immediately with "invalid task input".
func TestResumeRestoresTaskInput(t *testing.T) {
	c, _, seen := resumeFixture(t, ledger.TaskSnapshot{
		TaskID:      "t1",
		HandlerName: "worker",
		Input:       json.RawMessage(`{"prompt":"original work"}`),
	})
	ctx := context.Background()

	h, err := c.ResumeInterruptedRun(ctx, "run-x")
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if _, err := c.Join(ctx, h); err != nil {
		t.Fatalf("join: %v", err)
	}
	select {
	case got := <-seen:
		if !strings.Contains(string(got), "original work") {
			t.Fatalf("resumed task lost its input: %s", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("resumed task never executed — its input was dropped")
	}
}

// §3: the ledger restores work, never authority. A hand-edited ledger row must
// not be able to hand a resumed task a permission, scope, role or identity.
func TestResumeDoesNotRestoreAuthorityFields(t *testing.T) {
	// Hostile fixture: ParentTaskID is the one identity-shaped field the ledger
	// really carries (spawn.go derives it from Task.Owner), so an attacker
	// editing the store file controls it. Asserting against a fixture with no
	// such value set would compare zero to zero and pass no matter what the
	// restore does.
	c, _, _ := resumeFixture(t, ledger.TaskSnapshot{
		TaskID:       "t1",
		HandlerName:  "worker",
		Input:        json.RawMessage(`{"prompt":"x"}`),
		ParentTaskID: "task-attacker-controlled",
	})
	tasks, err := c.rebuildTasksForResume(context.Background(), "run-x")
	if err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(tasks))
	}
	got := tasks[0]
	for name, value := range map[string]string{
		"Permission": got.Permission,
		"Scope":      got.Scope,
		"Role":       got.Role,
		"SessionID":  got.SessionID,
		"TurnID":     got.TurnID,
		"Owner":      got.Owner,
	} {
		if value != "" {
			t.Errorf("%s was restored from the ledger (%q); authority must be re-derived, not persisted", name, value)
		}
	}
	if got.Owner == "task-attacker-controlled" {
		t.Error("Owner was restored from the persisted ParentTaskID; a workspace-writable file must not set run provenance")
	}
	if got.InvocationKey != "" || got.IdempotencyKey != "" {
		t.Errorf("idempotency scope restored from ledger: %q/%q", got.InvocationKey, got.IdempotencyKey)
	}
}

// Limits are restored but clamped: a run legitimately predates a config change,
// so a smaller value is honoured, but the ledger cannot raise the ceiling.
func TestResumeClampsLimitsToCurrentConfig(t *testing.T) {
	c, _, _ := resumeFixture(t, ledger.TaskSnapshot{
		TaskID:      "t1",
		HandlerName: "worker",
		Input:       json.RawMessage(`{"prompt":"x"}`),
		Depth:       99,
		Budget:      1_000_000,
		Timeout:     time.Hour,
	})
	tasks, err := c.rebuildTasksForResume(context.Background(), "run-x")
	if err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	got := tasks[0]
	if got.Depth > 3 {
		t.Errorf("Depth not clamped to pool max: %d", got.Depth)
	}
	if got.Budget > 1000 {
		t.Errorf("Budget not clamped to pool max: %d", got.Budget)
	}
	if got.Timeout > 5*time.Second {
		t.Errorf("Timeout not clamped to pool max: %s", got.Timeout)
	}
}

func TestResumeHonoursSmallerPersistedLimits(t *testing.T) {
	c, _, _ := resumeFixture(t, ledger.TaskSnapshot{
		TaskID:      "t1",
		HandlerName: "worker",
		Input:       json.RawMessage(`{"prompt":"x"}`),
		Depth:       1,
		Budget:      10,
		Timeout:     time.Second,
	})
	tasks, err := c.rebuildTasksForResume(context.Background(), "run-x")
	if err != nil {
		t.Fatal(err)
	}
	got := tasks[0]
	if got.Depth != 1 || got.Budget != 10 || got.Timeout != time.Second {
		t.Fatalf("smaller persisted limits not honoured: depth=%d budget=%d timeout=%s", got.Depth, got.Budget, got.Timeout)
	}
}

// A task persisted before this change has no Input. Resume must say so rather
// than failing later with the generic "invalid task input" from the handler.
func TestResumeOldTaskWithoutInputFailsClearly(t *testing.T) {
	c, _, _ := resumeFixture(t, ledger.TaskSnapshot{TaskID: "t1", HandlerName: "worker"})
	_, err := c.ResumeInterruptedRun(context.Background(), "run-x")
	if err == nil {
		t.Fatal("resuming a task with no persisted input must fail")
	}
	low := strings.ToLower(err.Error())
	if !strings.Contains(low, "input") || !strings.Contains(low, "t1") {
		t.Fatalf("error must name the task and the missing input, got: %v", err)
	}
}
