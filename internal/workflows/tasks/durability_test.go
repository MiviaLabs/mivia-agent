package tasks

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/storage"
)

// TestAtomicTransitionOrderingMatchesCallOrder pins D8's journal guarantee:
// the append-only log of status transitions keeps call order, each entry is
// timestamped by the store clock, and the current status derives from the
// last entry.
func TestAtomicTransitionOrderingMatchesCallOrder(t *testing.T) {
	s := NewMemoryStore()
	base := time.Unix(0, 0)
	ticks := 0
	s.SetTimeSource(func() time.Time {
		ticks++
		return base.Add(time.Duration(ticks) * time.Millisecond)
	})

	ref, err := s.StorePlan(Plan{ID: "plan-1", Scope: Scope{Type: ScopeRun, ID: "run-1"}})
	requireErr(t, err, nil, "StorePlan")
	requireErr(t, s.CreateTask(Task{ID: "task-1", PlanRef: ref, Scope: Scope{Type: ScopeRun, ID: "run-1"}, Status: "planned"}), nil, "CreateTask")

	statuses := []string{"queued", "running", "implemented", "reviewed", "published", "merged"}
	for _, st := range statuses {
		requireErr(t, s.TransitionTask(ref, "task-1", st), nil, "transition "+st)
	}

	trans, err := s.ListTransitions(ref)
	requireErr(t, err, nil, "ListTransitions")
	if len(trans) != len(statuses) {
		t.Fatalf("journal length = %d, want %d", len(trans), len(statuses))
	}
	for i, tr := range trans {
		wantFrom := "planned"
		if i > 0 {
			wantFrom = statuses[i-1]
		}
		if tr.ToStatus != statuses[i] || tr.FromStatus != wantFrom {
			t.Fatalf("journal[%d] = %+v, want %s -> %s", i, tr, wantFrom, statuses[i])
		}
		if i > 0 && !tr.At.After(trans[i-1].At) {
			t.Fatalf("journal[%d].At (%v) not after journal[%d].At (%v)", i, tr.At, i-1, trans[i-1].At)
		}
		if i > 0 && tr.At.Sub(trans[i-1].At) != time.Millisecond {
			t.Fatalf("journal[%d].At spacing = %v, want 1ms (one clock tick per transition)", i, tr.At.Sub(trans[i-1].At))
		}
	}

	got, err := s.GetTask(ref, "task-1")
	requireErr(t, err, nil, "GetTask")
	if got.Status != "merged" {
		t.Fatalf("task status = %q, want %q", got.Status, "merged")
	}
}

// TestDurabilityAcrossRestart proves the ledger survives a fresh instance
// over the same store: plans, bindings, tasks, statuses and the journal are
// rebuilt from the event log, and writes from the new instance are visible to
// the old one (incremental catch-up).
func TestDurabilityAcrossRestart(t *testing.T) {
	under := storage.NewMemory()
	s1 := NewStore(under)

	plan := Plan{ID: "plan-1", Scope: Scope{Type: ScopeStep, ID: "run-1:decompose"}, Schema: "chunk-plan-v1", PayloadRef: "ref:plan:xyz"}
	ref, err := s1.StorePlan(plan)
	requireErr(t, err, nil, "StorePlan")
	requireErr(t, s1.BindPlanToScope(ref, Scope{Type: ScopeAgent, ID: "workflow-engineer"}), nil, "BindPlanToScope")
	requireErr(t, s1.CreateTask(Task{ID: "task-1", PlanRef: ref, Scope: Scope{Type: ScopeAgent, ID: "workflow-engineer"}, Status: "planned"}), nil, "CreateTask")
	for _, st := range []string{"queued", "running", "implemented"} {
		requireErr(t, s1.TransitionTask(ref, "task-1", st), nil, "transition "+st)
	}

	// "Restart": a fresh instance over the SAME store, rebuilt from events.
	s2 := NewStore(under)
	gotPlan, err := s2.ReadBackPlan(ref)
	requireErr(t, err, nil, "ReadBackPlan after restart")
	if gotPlan.ID != plan.ID || gotPlan.Scope != (Scope{Type: ScopeAgent, ID: "workflow-engineer"}) {
		t.Fatalf("plan after restart = %+v", gotPlan)
	}
	gotTask, err := s2.GetTask(ref, "task-1")
	requireErr(t, err, nil, "GetTask after restart")
	if gotTask.Status != "implemented" {
		t.Fatalf("task status after restart = %q, want %q", gotTask.Status, "implemented")
	}
	tasks, err := s2.ListTasksByScope(Scope{Type: ScopeAgent, ID: "workflow-engineer"})
	requireErr(t, err, nil, "ListTasksByScope after restart")
	if len(tasks) != 1 || tasks[0].ID != "task-1" {
		t.Fatalf("tasks after restart = %+v", tasks)
	}
	trans, err := s2.ListTransitions(ref)
	requireErr(t, err, nil, "ListTransitions after restart")
	if len(trans) != 3 || trans[0].ToStatus != "queued" || trans[2].ToStatus != "implemented" {
		t.Fatalf("journal after restart = %+v", trans)
	}

	// The new instance's write is visible to the old one.
	requireErr(t, s2.TransitionTask(ref, "task-1", "reviewed"), nil, "transition after restart")
	gotTask, err = s1.GetTask(ref, "task-1")
	requireErr(t, err, nil, "GetTask on old instance")
	if gotTask.Status != "reviewed" {
		t.Fatalf("old instance status = %q, want %q", gotTask.Status, "reviewed")
	}
	trans, err = s1.ListTransitions(ref)
	requireErr(t, err, nil, "ListTransitions on old instance")
	if len(trans) != 4 || trans[3].ToStatus != "reviewed" {
		t.Fatalf("old instance journal = %+v", trans)
	}
}

// TestDurabilityAcrossSQLiteRestart proves real durability: the ledger is
// written to SQLite, the database is closed and reopened, and every artifact
// reads back.
func TestDurabilityAcrossSQLiteRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tasks.db")
	store1, err := storage.OpenSQLite(path)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	s1 := NewStore(store1)
	ref, err := s1.StorePlan(Plan{ID: "plan-1", Scope: Scope{Type: ScopeRun, ID: "run-1"}, Schema: "chunk-plan-v1"})
	requireErr(t, err, nil, "StorePlan")
	requireErr(t, s1.CreateTask(Task{ID: "task-1", PlanRef: ref, Scope: Scope{Type: ScopeRun, ID: "run-1"}, Status: "planned"}), nil, "CreateTask")
	for _, st := range []string{"queued", "running"} {
		requireErr(t, s1.TransitionTask(ref, "task-1", st), nil, "transition "+st)
	}
	if err := store1.Close(); err != nil {
		t.Fatalf("close sqlite: %v", err)
	}

	store2, err := storage.OpenSQLite(path)
	if err != nil {
		t.Fatalf("reopen sqlite: %v", err)
	}
	s2 := NewStore(store2)
	gotPlan, err := s2.ReadBackPlan(ref)
	requireErr(t, err, nil, "ReadBackPlan after sqlite restart")
	if gotPlan.ID != ref || gotPlan.Schema != "chunk-plan-v1" {
		t.Fatalf("plan after sqlite restart = %+v", gotPlan)
	}
	gotTask, err := s2.GetTask(ref, "task-1")
	requireErr(t, err, nil, "GetTask after sqlite restart")
	if gotTask.Status != "running" {
		t.Fatalf("task status after sqlite restart = %q, want %q", gotTask.Status, "running")
	}
	trans, err := s2.ListTransitions(ref)
	requireErr(t, err, nil, "ListTransitions after sqlite restart")
	if len(trans) != 2 || trans[1].ToStatus != "running" {
		t.Fatalf("journal after sqlite restart = %+v", trans)
	}
}

// TestForeignRunsAreIgnored pins the namespace contract: workflow (wfr-),
// coordinator (run-) and other foreign events in the shared store never leak
// into task results and never break catch-up.
func TestForeignRunsAreIgnored(t *testing.T) {
	under := storage.NewMemory()
	requireErr(t, under.Append(context.Background(), storage.Event{
		ID: "wfe:616263:wf_run_created", RunID: "wfr-1", Sequence: 1,
		Kind: "wf_run_created", Payload: []byte(`{"run":{"run_id":"wfr-1"}}`),
	}), nil, "append workflow event")
	requireErr(t, under.Append(context.Background(), storage.Event{
		ID: "se-1", RunID: "run-1", Sequence: 1,
		Kind: "agent", Payload: []byte(`{"task":"t1"}`),
	}), nil, "append coordinator event")

	s := NewStore(under)
	ref, err := s.StorePlan(Plan{ID: "plan-1", Scope: Scope{Type: ScopeRun, ID: "run-1"}})
	requireErr(t, err, nil, "StorePlan")
	requireErr(t, s.CreateTask(Task{ID: "task-1", PlanRef: ref, Scope: Scope{Type: ScopeRun, ID: "run-1"}, Status: "planned"}), nil, "CreateTask")

	tasks, err := s.ListTasksByScope(Scope{Type: ScopeRun, ID: "run-1"})
	requireErr(t, err, nil, "ListTasksByScope")
	if len(tasks) != 1 || tasks[0].ID != "task-1" {
		t.Fatalf("tasks = %+v, want exactly [task-1]", tasks)
	}
	all, err := s.ListTasksByScope(Scope{Type: ScopeRun, ID: ""})
	requireErr(t, err, nil, "ListTasksByScope empty ID")
	if len(all) != 1 {
		t.Fatalf("tasks across empty scope ID = %d, want 1 (foreign runs ignored)", len(all))
	}
}

func TestConcurrentTransitionsSameInstance(t *testing.T) {
	s := NewMemoryStore()
	ref, err := s.StorePlan(Plan{ID: "plan-1", Scope: Scope{Type: ScopeRun, ID: "run-1"}})
	requireErr(t, err, nil, "StorePlan")
	const n = 8
	for i := 0; i < n; i++ {
		requireErr(t, s.CreateTask(Task{
			ID: fmt.Sprintf("task-%d", i), PlanRef: ref,
			Scope: Scope{Type: ScopeRun, ID: "run-1"}, Status: "planned",
		}), nil, "CreateTask")
	}
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = s.TransitionTask(ref, fmt.Sprintf("task-%d", i), "running")
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		requireErr(t, err, nil, fmt.Sprintf("transition task-%d", i))
	}
	trans, err := s.ListTransitions(ref)
	requireErr(t, err, nil, "ListTransitions")
	if len(trans) != n {
		t.Fatalf("journal length = %d, want %d", len(trans), n)
	}
	seen := make(map[string]bool, n)
	for _, tr := range trans {
		seen[tr.TaskID] = true
	}
	if len(seen) != n {
		t.Fatalf("journal covers %d distinct tasks, want %d", len(seen), n)
	}
}

// transitionWithRetry re-runs a transition after ErrConflict: a concurrent
// writer took the sequence slot; the retry catches up and reappends.
func transitionWithRetry(s *Store, planRef, taskID, status string) error {
	for {
		err := s.TransitionTask(planRef, taskID, status)
		if err == nil || !errors.Is(err, ErrConflict) {
			return err
		}
	}
}

func TestConcurrentCrossInstanceTransitions(t *testing.T) {
	under := storage.NewMemory()
	s1 := NewStore(under)
	ref, err := s1.StorePlan(Plan{ID: "plan-1", Scope: Scope{Type: ScopeRun, ID: "run-1"}})
	requireErr(t, err, nil, "StorePlan")
	requireErr(t, s1.CreateTask(Task{ID: "task-1", PlanRef: ref, Scope: Scope{Type: ScopeRun, ID: "run-1"}, Status: "planned"}), nil, "CreateTask task-1")
	requireErr(t, s1.CreateTask(Task{ID: "task-2", PlanRef: ref, Scope: Scope{Type: ScopeRun, ID: "run-1"}, Status: "planned"}), nil, "CreateTask task-2")
	s2 := NewStore(under)

	errs := make([]error, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); errs[0] = transitionWithRetry(s1, ref, "task-1", "running") }()
	go func() { defer wg.Done(); errs[1] = transitionWithRetry(s2, ref, "task-2", "running") }()
	wg.Wait()
	for i, err := range errs {
		requireErr(t, err, nil, fmt.Sprintf("cross-instance transition %d", i))
	}
	// Both instances converge on the same journal.
	for _, s := range []*Store{s1, s2} {
		trans, err := s.ListTransitions(ref)
		requireErr(t, err, nil, "ListTransitions")
		if len(trans) != 2 {
			t.Fatalf("journal length = %d, want 2", len(trans))
		}
		tasks, err := s.ListTasksByScope(Scope{Type: ScopeRun, ID: "run-1"})
		requireErr(t, err, nil, "ListTasksByScope")
		if len(tasks) != 2 {
			t.Fatalf("tasks = %d, want 2", len(tasks))
		}
	}
}
