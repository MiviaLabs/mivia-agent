package cli

// Direct unit tests for cancelStackDependents and haltStackForFailedChunk.
// stack_canceled_status_test.go pins the reconciler's own terminal-status
// handling of an already-canceled chunk; these tests exercise the
// cancellation closure and the halt-error wrapping in stack_merge_cancel.go
// directly, including the branches no seed data reachable through the
// ledger's normal API can trigger (a TransitionTask failure, a
// stackTaskMap read failure), which need a storage.Store fake that injects
// the failure at the right call.

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/storage"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/tasks"
)

// newCancelTestLedger builds a fresh in-memory ledger with one stored plan
// for stackID, ready for tasks.Task seeding.
func newCancelTestLedger(t *testing.T, stackID string) *tasks.Store {
	t.Helper()
	ledger := tasks.NewMemoryStore()
	if _, err := ledger.StorePlan(tasks.Plan{ID: stackID, Scope: stackScope(stackID), Schema: stackPlanSchema}); err != nil {
		t.Fatal(err)
	}
	return ledger
}

// createCancelTestTask seeds one task under stackID's plan and scope.
func createCancelTestTask(t *testing.T, ledger *tasks.Store, stackID string, task tasks.Task) {
	t.Helper()
	task.PlanRef = stackID
	task.Scope = stackScope(stackID)
	if err := ledger.CreateTask(task); err != nil {
		t.Fatal(err)
	}
}

// TestCancelStackDependentsDiamondCancelsBothBranches pins that EVERY
// dependent of a failed chunk transitions to canceled in one call, not just
// the first dependent the map iteration happens to visit.
func TestCancelStackDependentsDiamondCancelsBothBranches(t *testing.T) {
	stackID := "stack-cancel-diamond"
	ledger := newCancelTestLedger(t, stackID)
	createCancelTestTask(t, ledger, stackID, tasks.Task{ID: "a", Status: stackStatusFailed})
	createCancelTestTask(t, ledger, stackID, tasks.Task{ID: "b", Status: stackStatusPlanned, Deps: []string{"a"}})
	createCancelTestTask(t, ledger, stackID, tasks.Task{ID: "c", Status: stackStatusPlanned, Deps: []string{"a"}})

	if err := cancelStackDependents(ledger, stackID); err != nil {
		t.Fatalf("cancelStackDependents() error = %v", err)
	}
	for _, id := range []string{"b", "c"} {
		if got := mustTaskStatus(t, ledger, stackID, id); got != stackStatusCanceled {
			t.Fatalf("task %s status = %q, want %q", id, got, stackStatusCanceled)
		}
	}
}

// TestCancelStackDependentsTransitiveChainCancelsAll pins the fixed-point
// loop: a dependency chain (C depends on B depends on A) must fully unwind
// in one call, not just cancel the immediate dependent.
func TestCancelStackDependentsTransitiveChainCancelsAll(t *testing.T) {
	stackID := "stack-cancel-transitive"
	ledger := newCancelTestLedger(t, stackID)
	createCancelTestTask(t, ledger, stackID, tasks.Task{ID: "a", Status: stackStatusFailed})
	createCancelTestTask(t, ledger, stackID, tasks.Task{ID: "b", Status: stackStatusPlanned, Deps: []string{"a"}})
	createCancelTestTask(t, ledger, stackID, tasks.Task{ID: "c", Status: stackStatusPlanned, Deps: []string{"b"}})

	if err := cancelStackDependents(ledger, stackID); err != nil {
		t.Fatalf("cancelStackDependents() error = %v", err)
	}
	for _, id := range []string{"b", "c"} {
		if got := mustTaskStatus(t, ledger, stackID, id); got != stackStatusCanceled {
			t.Fatalf("task %s status = %q, want %q (transitive cancellation)", id, got, stackStatusCanceled)
		}
	}
}

// TestCancelStackDependentsSkipsDanglingDependency pins that a Deps entry
// naming an unknown task id is skipped (not a crash or a silent stall): the
// task still cancels once a REAL failed dependency is found among its Deps.
func TestCancelStackDependentsSkipsDanglingDependency(t *testing.T) {
	stackID := "stack-cancel-dangling"
	ledger := newCancelTestLedger(t, stackID)
	createCancelTestTask(t, ledger, stackID, tasks.Task{ID: "a", Status: stackStatusFailed})
	createCancelTestTask(t, ledger, stackID, tasks.Task{ID: "b", Status: stackStatusPlanned, Deps: []string{"missing", "a"}})

	if err := cancelStackDependents(ledger, stackID); err != nil {
		t.Fatalf("cancelStackDependents() error = %v", err)
	}
	if got := mustTaskStatus(t, ledger, stackID, "b"); got != stackStatusCanceled {
		t.Fatalf("task b status = %q, want %q despite a dangling dependency reference", got, stackStatusCanceled)
	}
}

// TestCancelStackDependentsEmptyStackReturnsImmediately pins the maxRounds=0
// path: a stack with no tasks at all must return nil without ever entering
// the fixed-point loop.
func TestCancelStackDependentsEmptyStackReturnsImmediately(t *testing.T) {
	stackID := "stack-cancel-empty"
	ledger := newCancelTestLedger(t, stackID)
	if err := cancelStackDependents(ledger, stackID); err != nil {
		t.Fatalf("cancelStackDependents() on an empty stack error = %v, want nil", err)
	}
}

// failingChangesStore wraps storage.NewMemory() and always fails Changes,
// the read catchUp uses first: it lets a test reach stackTaskMap's error
// path (cancelStackDependents' line "return err" right after the
// stackTaskMap call) without any way to trigger that through normal ledger
// usage, since a valid stack scope never itself produces a ListTasksByScope
// error.
type failingChangesStore struct {
	*storage.Memory
}

func (f *failingChangesStore) Changes(ctx context.Context, afterCursor uint64) (map[string]int, uint64, error) {
	return nil, 0, errors.New("injected changes failure")
}

// TestCancelStackDependentsPropagatesTaskMapError pins that a stackTaskMap
// read failure propagates as-is instead of being swallowed.
func TestCancelStackDependentsPropagatesTaskMapError(t *testing.T) {
	ledger := tasks.NewStore(&failingChangesStore{Memory: storage.NewMemory()})
	err := cancelStackDependents(ledger, "stack-cancel-taskmap-error")
	if err == nil || !strings.Contains(err.Error(), "injected changes failure") {
		t.Fatalf("cancelStackDependents() error = %v, want it to propagate stackTaskMap's read failure", err)
	}
}

// failingTransitionStore wraps storage.NewMemory() and fails AppendBatch for
// any batch whose payload contains failSubstr, so a test can inject a
// TransitionTask failure at a specific transition (e.g. "to canceled")
// without disturbing the setup transitions that precede it.
type failingTransitionStore struct {
	*storage.Memory
	failSubstr string
}

func (f *failingTransitionStore) AppendBatch(ctx context.Context, events []storage.Event) error {
	if f.failSubstr != "" {
		for _, e := range events {
			if strings.Contains(string(e.Payload), f.failSubstr) {
				return errors.New("injected transition failure")
			}
		}
	}
	return f.Memory.AppendBatch(ctx, events)
}

// TestCancelStackDependentsPropagatesTransitionFailure pins that a durable
// ledger.TransitionTask failure while canceling a dependent is wrapped and
// returned, not swallowed.
func TestCancelStackDependentsPropagatesTransitionFailure(t *testing.T) {
	stackID := "stack-cancel-transition-fail"
	backing := &failingTransitionStore{Memory: storage.NewMemory(), failSubstr: `"to_status":"canceled"`}
	ledger := tasks.NewStore(backing)
	if _, err := ledger.StorePlan(tasks.Plan{ID: stackID, Scope: stackScope(stackID), Schema: stackPlanSchema}); err != nil {
		t.Fatal(err)
	}
	createCancelTestTask(t, ledger, stackID, tasks.Task{ID: "a", Status: stackStatusFailed})
	createCancelTestTask(t, ledger, stackID, tasks.Task{ID: "b", Status: stackStatusPlanned, Deps: []string{"a"}})

	err := cancelStackDependents(ledger, stackID)
	if err == nil || !strings.Contains(err.Error(), "cancel dependent chunk b") {
		t.Fatalf("cancelStackDependents() error = %v, want it to wrap the ledger's transition failure", err)
	}
}

// TestHaltStackForFailedChunkWrapsCancelError pins haltStackForFailedChunk's
// two cancel-error wrapping branches (note == "" and note != ""): both must
// carry the cancel-dependents failure, and the note variant must also carry
// the note text.
func TestHaltStackForFailedChunkWrapsCancelError(t *testing.T) {
	stackID := "stack-halt-cancel-fail"
	backing := &failingTransitionStore{Memory: storage.NewMemory(), failSubstr: `"to_status":"canceled"`}
	ledger := tasks.NewStore(backing)
	if _, err := ledger.StorePlan(tasks.Plan{ID: stackID, Scope: stackScope(stackID), Schema: stackPlanSchema}); err != nil {
		t.Fatal(err)
	}
	createCancelTestTask(t, ledger, stackID, tasks.Task{ID: "a", Status: stackStatusFailed})
	createCancelTestTask(t, ledger, stackID, tasks.Task{ID: "b", Status: stackStatusPlanned, Deps: []string{"a"}})

	withNote := haltStackForFailedChunk(ledger, stackID, "a", "run failed after 3 attempts")
	if withNote == nil || !strings.Contains(withNote.Error(), "run failed after 3 attempts") || !strings.Contains(withNote.Error(), "cancel dependents") {
		t.Fatalf("halt error with a note = %v, want it to carry both the note and the cancel-dependents wrap", withNote)
	}

	withoutNote := haltStackForFailedChunk(ledger, stackID, "a", "")
	if withoutNote == nil || !strings.Contains(withoutNote.Error(), "cancel dependents") {
		t.Fatalf("halt error without a note = %v, want the cancel-dependents wrap", withoutNote)
	}
}
