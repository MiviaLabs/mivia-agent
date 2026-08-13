package cli

import (
	"reflect"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/tasks"
)

// reconcileCase drives the pure §5a reconciler.
func reconcileCase(t *testing.T, task tasks.Task, run RunInfo, merged bool, runPushed bool, maxAttempts int) ReconcileAction {
	t.Helper()
	return reconcileTask(task, run, merged, runPushed, maxAttempts)
}

// --- Reconciliation: restart mid-stack (recovery must be a no-op) --------

func TestReconcileRestartMidStackNoop(t *testing.T) {
	// A stack that was driving when the driver died: a merged chunk, an
	// in-flight chunk with a live run, and a planned dependent.
	a := tasks.Task{ID: "a", Status: stackStatusMerged}
	b := tasks.Task{ID: "b", Status: stackStatusRunning, Deps: []string{"a"}}
	c := tasks.Task{ID: "c", Status: stackStatusPlanned, Deps: []string{"b"}}

	if act := reconcileCase(t, a, RunInfo{Present: true, Status: runStatusDeliveryPending}, true, true, 3); act.Action != stackActionLeave {
		t.Fatalf("merged task a: action = %q, want leave", act.Action)
	}
	if act := reconcileCase(t, b, RunInfo{Present: true, Status: runStatusRunning}, false, true, 3); act.Action != stackActionLeave {
		t.Fatalf("running task b: action = %q, want leave (run is active)", act.Action)
	}
	if act := reconcileCase(t, c, RunInfo{Present: false}, false, true, 3); act.Action != stackActionLeave {
		t.Fatalf("planned task c: action = %q, want leave (waiting for admission)", act.Action)
	}
}

// --- Reconciliation: run died mid-flight (bounded reopen, then halt) -----

func TestReconcileRunDiedMidFlightReopens(t *testing.T) {
	// The chunk's run failed while the task says running: reopen with a
	// bounded retry and a durable attempt count.
	task := tasks.Task{ID: "b", Status: stackStatusRunning, Attempts: 0}
	act := reconcileCase(t, task, RunInfo{Present: true, Status: runStatusFailed}, false, true, 3)
	if act.Action != stackActionReopen {
		t.Fatalf("action = %q, want reopen", act.Action)
	}
	if act.NewStatus != stackStatusReopened {
		t.Fatalf("new status = %q, want reopened", act.NewStatus)
	}
	if act.Attempts != 1 {
		t.Fatalf("attempts = %d, want 1", act.Attempts)
	}
}

func TestReconcileReopenBoundedThenHalt(t *testing.T) {
	// Past the bound the chunk is marked failed and the stack halts.
	task := tasks.Task{ID: "b", Status: stackStatusReopened, Attempts: 3}
	act := reconcileCase(t, task, RunInfo{Present: true, Status: runStatusTimedOut}, false, true, 3)
	if act.Action != stackActionMarkFailed {
		t.Fatalf("action = %q, want mark_failed", act.Action)
	}
	if act.NewStatus != stackStatusFailed {
		t.Fatalf("new status = %q, want failed", act.NewStatus)
	}
}

// --- Reconciliation: interrupted merge -----------------------------------

func TestReconcileInterruptedMerge(t *testing.T) {
	// A chunk reached delivery and its PR merged, but the task ledger never
	// learned: git merge state decides mark_merged, unblocking dependents.
	task := tasks.Task{ID: "b", Status: stackStatusReviewed, Deps: []string{"a"}}
	act := reconcileCase(t, task, RunInfo{Present: true, Status: runStatusDeliveryPending}, true, true, 3)
	if act.Action != stackActionMarkMerged {
		t.Fatalf("action = %q, want mark_merged", act.Action)
	}
	if act.NewStatus != stackStatusMerged {
		t.Fatalf("new status = %q, want merged", act.NewStatus)
	}
}

func TestReconcileDeliveryPendingNotesPublish(t *testing.T) {
	// Succeeded + delivery_pending -> deliver (publish grant note), NOT
	// merged: the human checkpoint (D1 policy A) still owns publication.
	task := tasks.Task{ID: "b", Status: stackStatusRunning}
	act := reconcileCase(t, task, RunInfo{Present: true, Status: runStatusDeliveryPending}, false, true, 3)
	if act.Action != stackActionDeliver {
		t.Fatalf("action = %q, want deliver", act.Action)
	}
}

// --- Reconciliation: pushed evidence gates merge marking -------------------

func TestReconcileDeliveryPendingNeverPushedNotMerged(t *testing.T) {
	// A delivery_pending run whose branch was never pushed (its delivery
	// record never reached pushed/succeeded) must NOT be marked merged even
	// when git reports ref absence: the PR was never created, and marking the
	// chunk merged would complete the stack with a silent PR loss.
	task := tasks.Task{ID: "b", Status: stackStatusRunning, Deps: []string{"a"}}
	act := reconcileCase(t, task, RunInfo{Present: true, Status: runStatusDeliveryPending}, true, false, 3)
	if act.Action == stackActionMarkMerged {
		t.Fatalf("never-pushed delivery_pending run was marked merged (action=%q); the chunk must wait for its publish grant", act.Action)
	}
	if act.Action != stackActionDeliver {
		t.Fatalf("action = %q, want deliver (publish grant)", act.Action)
	}
}

func TestReconcileMergedTaskNeverReopens(t *testing.T) {
	// Terminal task statuses are untouched even when the run looks failed.
	task := tasks.Task{ID: "a", Status: stackStatusMerged}
	act := reconcileCase(t, task, RunInfo{Present: true, Status: runStatusFailed}, false, true, 3)
	if act.Action != stackActionLeave || act.NewStatus != "" {
		t.Fatalf("merged task with failed run: action=%q new=%q, want leave/no-op", act.Action, act.NewStatus)
	}
}

// --- Topological admission order -----------------------------------------

func TestTopologicalOrderDependenciesFirst(t *testing.T) {
	chunks := []ChunkPlan{
		{ID: "c", DependsOn: []string{"a"}},
		{ID: "b"},
		{ID: "a"},
	}
	order, err := stackTopologicalOrder(chunks)
	if err != nil {
		t.Fatalf("stackTopologicalOrder: %v", err)
	}
	want := []string{"a", "b", "c"}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("order = %v, want %v", order, want)
	}
}

func TestTopologicalOrderCycleRejected(t *testing.T) {
	chunks := []ChunkPlan{
		{ID: "a", DependsOn: []string{"b"}},
		{ID: "b", DependsOn: []string{"a"}},
	}
	if _, err := stackTopologicalOrder(chunks); err == nil {
		t.Fatal("stackTopologicalOrder accepted a cycle")
	}
}

func TestTopologicalOrderUnknownDepRejected(t *testing.T) {
	chunks := []ChunkPlan{{ID: "a", DependsOn: []string{"ghost"}}}
	if _, err := stackTopologicalOrder(chunks); err == nil {
		t.Fatal("stackTopologicalOrder accepted an unknown dependency")
	}
}

// --- Stable admission key derivation (F15) -------------------------------

func TestStackAdmissionKeyStable(t *testing.T) {
	key1, err := stackAdmissionKey("stack-1", "chunk-a")
	if err != nil {
		t.Fatalf("stackAdmissionKey: %v", err)
	}
	key2, _ := stackAdmissionKey("stack-1", "chunk-a")
	if key1 != "stack-1:chunk-a" || key1 != key2 {
		t.Fatalf("key = %q, want stable %q", key1, "stack-1:chunk-a")
	}
	if _, err := stackAdmissionKey("", "chunk-a"); err == nil {
		t.Fatal("stackAdmissionKey accepted an empty stack id")
	}
	if _, err := stackAdmissionKey("stack-1", "chunk:a"); err == nil {
		t.Fatal("stackAdmissionKey accepted a colon inside a chunk id (ambiguous key)")
	}
}

// --- Next admission wave (schedule after deps merge) ---------------------

func TestNextAdmissionWaveDepsMerged(t *testing.T) {
	byID := map[string]tasks.Task{
		"a": {ID: "a", Status: stackStatusMerged},
		"b": {ID: "b", Status: stackStatusPlanned, Deps: []string{"a"}},
		"c": {ID: "c", Status: stackStatusBlocked, Deps: []string{"a"}},
		"d": {ID: "d", Status: stackStatusPlanned, Deps: []string{"b"}},
	}
	wave := nextAdmissionWave(byID, map[string]bool{"a": true}, []string{"a", "b", "c", "d"})
	if !reflect.DeepEqual(wave, []string{"b", "c"}) {
		t.Fatalf("wave = %v, want [b c]", wave)
	}
}

func TestNextAdmissionWaveNoopWhenConsistent(t *testing.T) {
	// Every chunk merged: no admission wave (the stack is done).
	byID := map[string]tasks.Task{
		"a": {ID: "a", Status: stackStatusMerged},
		"b": {ID: "b", Status: stackStatusMerged, Deps: []string{"a"}},
	}
	merged := map[string]bool{"a": true, "b": true}
	if wave := nextAdmissionWave(byID, merged, []string{"a", "b"}); len(wave) != 0 {
		t.Fatalf("wave = %v, want empty", wave)
	}
}

// --- Durable attempt counting via the transition journal (D8) ------------

func TestStackAttemptCountFromJournal(t *testing.T) {
	store := tasks.NewMemoryStore()
	stackID := "stack-1"
	if _, err := store.StorePlan(tasks.Plan{ID: stackID, Scope: stackScope(stackID), Schema: stackPlanSchema}); err != nil {
		t.Fatalf("StorePlan: %v", err)
	}
	task := tasks.Task{ID: "b", PlanRef: stackID, Scope: stackScope(stackID), Status: stackStatusRunning}
	if err := store.CreateTask(task); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if n := stackAttemptCount(store, stackID, "b"); n != 0 {
		t.Fatalf("attempts before reopen = %d, want 0", n)
	}
	if err := store.TransitionTask(stackID, "b", stackStatusReopened); err != nil {
		t.Fatalf("TransitionTask reopen: %v", err)
	}
	if n := stackAttemptCount(store, stackID, "b"); n != 1 {
		t.Fatalf("attempts after one reopen = %d, want 1 (durable journal)", n)
	}
}

// --- Plan output parsing --------------------------------------------------

func TestParseStackPlanOutput(t *testing.T) {
	mode, chunks, err := parseStackPlanOutput([]byte(`{"stack_mode":"multi","chunk_plan":{"chunks":[{"id":"a","title":"t","files":["x.go"],"est_diff_lines":10,"tests":true},{"id":"b","title":"u","files":["y.go"],"est_diff_lines":5,"tests":true,"depends_on":["a"]}]}}`))
	if err != nil {
		t.Fatalf("parseStackPlanOutput: %v", err)
	}
	if mode != "multi" || len(chunks) != 2 || chunks[1].DependsOn[0] != "a" {
		t.Fatalf("parsed mode=%q chunks=%+v", mode, chunks)
	}
}

func TestParseStackPlanOutputSingle(t *testing.T) {
	mode, chunks, err := parseStackPlanOutput([]byte(`{"stack_mode":"single"}`))
	if err != nil {
		t.Fatalf("parseStackPlanOutput: %v", err)
	}
	if mode != "single" || len(chunks) != 0 {
		t.Fatalf("mode=%q chunks=%v, want single with no chunks", mode, chunks)
	}
}

func TestParseStackPlanOutputMalformed(t *testing.T) {
	if _, _, err := parseStackPlanOutput([]byte(`{"stack_mode":"bogus"}`)); err == nil {
		t.Fatal("parseStackPlanOutput accepted an invalid stack_mode")
	}
	if _, _, err := parseStackPlanOutput([]byte(`not json`)); err == nil {
		t.Fatal("parseStackPlanOutput accepted malformed JSON")
	}
}

// --- PR number extraction -------------------------------------------------

func TestStackPRNumber(t *testing.T) {
	if got := stackPRNumber("https://github.com/acme/repo/pull/42"); got != "42" {
		t.Fatalf("pr number = %q, want 42", got)
	}
	if got := stackPRNumber("https://github.com/acme/repo/pull/42/"); got != "42" {
		t.Fatalf("pr number with trailing slash = %q, want 42", got)
	}
	if got := stackPRNumber(""); got != "" {
		t.Fatalf("pr number for empty url = %q, want empty", got)
	}
}
