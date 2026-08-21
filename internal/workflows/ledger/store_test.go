package ledger

import (
	"reflect"
	"sync"
	"testing"
	"time"
)

// fixedClock and requireErr are declared once for the package, in
// storage_test.go; every test file in this package shares them.

func TestStorePlanAndReadBack(t *testing.T) {
	s := NewMemoryStore()
	plan := Plan{
		ID:         "plan-1",
		Scope:      Scope{Type: ScopeRun, ID: "run-1"},
		Schema:     "chunk-plan-v1",
		PayloadRef: "ref:plan:abc",
		CreatedAt:  fixedClock,
	}
	ref, err := s.StorePlan(plan)
	requireErr(t, err, nil, "StorePlan")
	if ref != plan.ID {
		t.Fatalf("ref = %q, want %q", ref, plan.ID)
	}
	got, err := s.ReadBackPlan(ref)
	requireErr(t, err, nil, "ReadBackPlan")
	if !reflect.DeepEqual(got, plan) {
		t.Fatalf("read-back mismatch:\n got %+v\nwant %+v", got, plan)
	}
	_, err = s.ReadBackPlan("no-such-plan")
	requireErr(t, err, ErrPlanNotFound, "ReadBackPlan unknown")
}

func TestStorePlanRejectsInvalidInput(t *testing.T) {
	s := NewMemoryStore()
	_, err := s.StorePlan(Plan{Scope: Scope{Type: ScopeRun, ID: "run-1"}})
	requireErr(t, err, ErrInvalidPlan, "empty plan ID")
	_, err = s.StorePlan(Plan{ID: "plan-1", Scope: Scope{Type: "unknown", ID: "x"}})
	requireErr(t, err, ErrInvalidScope, "invalid scope type")
}

func TestStorePlanDuplicate(t *testing.T) {
	s := NewMemoryStore()
	plan := Plan{ID: "plan-1", Scope: Scope{Type: ScopeRun, ID: "run-1"}, Schema: "chunk-plan-v1"}
	ref, err := s.StorePlan(plan)
	requireErr(t, err, nil, "StorePlan")
	// Identical re-store is an idempotent no-op (recovery re-entry).
	ref2, err := s.StorePlan(plan)
	requireErr(t, err, nil, "identical re-store")
	if ref2 != ref {
		t.Fatalf("re-store ref = %q, want %q", ref2, ref)
	}
	// Same ID with different content is a duplicate.
	other := plan
	other.Schema = "other-schema-v1"
	_, err = s.StorePlan(other)
	requireErr(t, err, ErrTaskDuplicate, "different content same ID")
}

func TestBindPlanToScope(t *testing.T) {
	s := NewMemoryStore()
	ref, err := s.StorePlan(Plan{ID: "plan-1", Scope: Scope{Type: ScopeSession, ID: "sess-1"}})
	requireErr(t, err, nil, "StorePlan")

	// Re-bind to a different scope of a different type.
	scope := Scope{Type: ScopeWorkflow, ID: "feature-delivery"}
	requireErr(t, s.BindPlanToScope(ref, scope), nil, "BindPlanToScope")
	got, err := s.ReadBackPlan(ref)
	requireErr(t, err, nil, "ReadBackPlan")
	if got.Scope != scope {
		t.Fatalf("plan scope = %+v, want %+v", got.Scope, scope)
	}

	// Binding the same scope again is an idempotent no-op.
	requireErr(t, s.BindPlanToScope(ref, scope), nil, "re-bind same scope")

	// Error cases.
	requireErr(t, s.BindPlanToScope("no-such-plan", scope), ErrPlanNotFound, "unknown plan")
	requireErr(t, s.BindPlanToScope(ref, Scope{Type: "bogus", ID: "x"}), ErrInvalidScope, "invalid scope type")
}

func TestCreateTaskAndGetTask(t *testing.T) {
	s := NewMemoryStore()
	ref, err := s.StorePlan(Plan{ID: "plan-1", Scope: Scope{Type: ScopeRun, ID: "run-1"}})
	requireErr(t, err, nil, "StorePlan")
	task := Task{
		ID:        "task-1",
		PlanRef:   ref,
		Scope:     Scope{Type: ScopeRun, ID: "run-1"},
		Status:    "planned",
		RunRef:    "wfr-1",
		PRNumber:  "42",
		Deps:      []string{"task-0"},
		Attempts:  2,
		LastError: "transient",
	}
	requireErr(t, s.CreateTask(task), nil, "CreateTask")
	got, err := s.GetTask(ref, task.ID)
	requireErr(t, err, nil, "GetTask")
	if !reflect.DeepEqual(got, task) {
		t.Fatalf("task mismatch:\n got %+v\nwant %+v", got, task)
	}
	// The returned copy must not alias the caller's Deps slice.
	got.Deps[0] = "mutated"
	again, err := s.GetTask(ref, task.ID)
	requireErr(t, err, nil, "GetTask again")
	if again.Deps[0] != "task-0" {
		t.Fatalf("returned copy aliases internal state: %v", again.Deps)
	}
	// Error cases.
	_, err = s.GetTask(ref, "no-such-task")
	requireErr(t, err, ErrTaskNotFound, "unknown task")
	_, err = s.GetTask("no-such-plan", "task-1")
	requireErr(t, err, ErrPlanNotFound, "unknown plan")
}

func TestCreateTaskRejectsInvalidInput(t *testing.T) {
	s := NewMemoryStore()
	ref, err := s.StorePlan(Plan{ID: "plan-1", Scope: Scope{Type: ScopeRun, ID: "run-1"}})
	requireErr(t, err, nil, "StorePlan")

	valid := Task{ID: "task-1", PlanRef: ref, Scope: Scope{Type: ScopeRun, ID: "run-1"}, Status: "planned"}
	requireErr(t, s.CreateTask(valid), nil, "valid task")

	cases := []struct {
		name string
		task Task
		want error
	}{
		{"empty ID", Task{PlanRef: ref, Scope: valid.Scope, Status: "planned"}, ErrInvalidTask},
		{"empty plan ref", Task{ID: "task-2", Scope: valid.Scope, Status: "planned"}, ErrInvalidTask},
		{"invalid scope", Task{ID: "task-2", PlanRef: ref, Scope: Scope{Type: "bogus", ID: "x"}, Status: "planned"}, ErrInvalidScope},
		{"empty status", Task{ID: "task-2", PlanRef: ref, Scope: valid.Scope}, ErrEmptyStatus},
		{"unknown plan", Task{ID: "task-2", PlanRef: "no-such-plan", Scope: valid.Scope, Status: "planned"}, ErrPlanNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			requireErr(t, s.CreateTask(tc.task), tc.want, "CreateTask")
		})
	}
}

func TestCreateTaskDuplicate(t *testing.T) {
	s := NewMemoryStore()
	ref, err := s.StorePlan(Plan{ID: "plan-1", Scope: Scope{Type: ScopeRun, ID: "run-1"}})
	requireErr(t, err, nil, "StorePlan")
	task := Task{ID: "task-1", PlanRef: ref, Scope: Scope{Type: ScopeRun, ID: "run-1"}, Status: "planned"}
	requireErr(t, s.CreateTask(task), nil, "CreateTask")
	// Identical re-create is an idempotent no-op.
	requireErr(t, s.CreateTask(task), nil, "identical re-create")
	// Same ID under the same plan with different content is a duplicate.
	other := task
	other.Status = "queued"
	requireErr(t, s.CreateTask(other), ErrTaskDuplicate, "different content same ID")
}

// TestStoreBindQueryEachScopeType covers the locked scope vocabulary:
// store + bind + query for session, step, agent, workflow and run.
func TestStoreBindQueryEachScopeType(t *testing.T) {
	scopes := []Scope{
		{Type: ScopeSession, ID: "sess-1"},
		{Type: ScopeStep, ID: "run-1:decompose"},
		{Type: ScopeAgent, ID: "workflow-engineer"},
		{Type: ScopeWorkflow, ID: "feature-delivery"},
		{Type: ScopeRun, ID: "run-1"},
	}
	for _, sc := range scopes {
		t.Run(sc.Type, func(t *testing.T) {
			s := NewMemoryStore()
			ref, err := s.StorePlan(Plan{ID: "plan-" + sc.Type, Scope: sc, Schema: "chunk-plan-v1"})
			requireErr(t, err, nil, "StorePlan")
			if ref != "plan-"+sc.Type {
				t.Fatalf("ref = %q", ref)
			}

			// Bind away and back; the query follows the final binding.
			other := Scope{Type: sc.Type, ID: sc.ID + "-other"}
			requireErr(t, s.BindPlanToScope(ref, other), nil, "bind other")
			requireErr(t, s.BindPlanToScope(ref, sc), nil, "bind back")

			// A task bound to the scope is returned by the scope query.
			task := Task{ID: "task-1", PlanRef: ref, Scope: sc, Status: "planned"}
			requireErr(t, s.CreateTask(task), nil, "CreateTask")
			// A task bound to a different scope of the same type must not leak.
			requireErr(t, s.CreateTask(Task{ID: "task-2", PlanRef: ref, Scope: other, Status: "planned"}), nil, "CreateTask other")

			tasks, err := s.ListTasksByScope(sc)
			requireErr(t, err, nil, "ListTasksByScope")
			if len(tasks) != 1 || tasks[0].ID != "task-1" {
				t.Fatalf("tasks by scope = %+v, want exactly [task-1]", tasks)
			}
			otherTasks, err := s.ListTasksByScope(other)
			requireErr(t, err, nil, "ListTasksByScope other")
			if len(otherTasks) != 1 || otherTasks[0].ID != "task-2" {
				t.Fatalf("tasks by other scope = %+v, want exactly [task-2]", otherTasks)
			}

			// The plan itself reads back with the final binding.
			got, err := s.ReadBackPlan(ref)
			requireErr(t, err, nil, "ReadBackPlan")
			if got.Scope != sc {
				t.Fatalf("plan scope = %+v, want %+v", got.Scope, sc)
			}
		})
	}
	// Invalid scope type is rejected by the query.
	s := NewMemoryStore()
	_, err := s.ListTasksByScope(Scope{Type: "bogus", ID: "x"})
	requireErr(t, err, ErrInvalidScope, "ListTasksByScope invalid scope")
}

func TestTransitionTask(t *testing.T) {
	s := NewMemoryStore()
	s.SetTimeSource(func() time.Time { return fixedClock })
	ref, err := s.StorePlan(Plan{ID: "plan-1", Scope: Scope{Type: ScopeRun, ID: "run-1"}})
	requireErr(t, err, nil, "StorePlan")
	requireErr(t, s.CreateTask(Task{ID: "task-1", PlanRef: ref, Scope: Scope{Type: ScopeRun, ID: "run-1"}, Status: "planned"}), nil, "CreateTask")

	requireErr(t, s.TransitionTask(ref, "task-1", "queued"), nil, "transition queued")
	requireErr(t, s.TransitionTask(ref, "task-1", "running"), nil, "transition running")

	got, err := s.GetTask(ref, "task-1")
	requireErr(t, err, nil, "GetTask")
	if got.Status != "running" {
		t.Fatalf("task status = %q, want %q", got.Status, "running")
	}

	trans, err := s.ListTransitions(ref)
	requireErr(t, err, nil, "ListTransitions")
	if len(trans) != 2 {
		t.Fatalf("journal length = %d, want 2", len(trans))
	}
	if trans[0].FromStatus != "planned" || trans[0].ToStatus != "queued" {
		t.Fatalf("journal[0] = %+v, want planned -> queued", trans[0])
	}
	if trans[1].FromStatus != "queued" || trans[1].ToStatus != "running" {
		t.Fatalf("journal[1] = %+v, want queued -> running", trans[1])
	}
	if !trans[0].At.Equal(fixedClock) || !trans[1].At.Equal(fixedClock) {
		t.Fatalf("journal timestamps = %v, %v; want fixedClock %v", trans[0].At, trans[1].At, fixedClock)
	}

	// Error cases.
	requireErr(t, s.TransitionTask(ref, "task-1", ""), ErrEmptyStatus, "empty status")
	requireErr(t, s.TransitionTask(ref, "no-such-task", "running"), ErrTaskNotFound, "unknown task")
	requireErr(t, s.TransitionTask("no-such-plan", "task-1", "running"), ErrPlanNotFound, "unknown plan")
	_, err = s.ListTransitions("no-such-plan")
	requireErr(t, err, ErrPlanNotFound, "ListTransitions unknown plan")
}

// TestTransitionTaskCAS pins the compare-and-swap admission guard: the
// transition only applies when the task's current status is one of the
// caller's expected preconditions, so two concurrent callers racing to admit
// the same task can never both win.
func TestTransitionTaskCAS(t *testing.T) {
	s := NewMemoryStore()
	s.SetTimeSource(func() time.Time { return fixedClock })
	ref, err := s.StorePlan(Plan{ID: "plan-1", Scope: Scope{Type: ScopeRun, ID: "run-1"}})
	requireErr(t, err, nil, "StorePlan")
	requireErr(t, s.CreateTask(Task{ID: "task-1", PlanRef: ref, Scope: Scope{Type: ScopeRun, ID: "run-1"}, Status: "planned"}), nil, "CreateTask")

	// A precondition that does not match the current status is a clean no-op,
	// not an error: the caller learns it lost the race and moves on.
	ok, err := s.TransitionTaskCAS(ref, "task-1", []string{"queued", "reopened"}, "running")
	requireErr(t, err, nil, "CAS mismatched precondition")
	if ok {
		t.Fatal("CAS with mismatched precondition should not apply")
	}
	got, err := s.GetTask(ref, "task-1")
	requireErr(t, err, nil, "GetTask after failed CAS")
	if got.Status != "planned" {
		t.Fatalf("task status = %q, want unchanged %q", got.Status, "planned")
	}

	// A matching precondition applies exactly once.
	ok, err = s.TransitionTaskCAS(ref, "task-1", []string{"planned", "reopened"}, "running")
	requireErr(t, err, nil, "CAS matching precondition")
	if !ok {
		t.Fatal("CAS with matching precondition should apply")
	}
	got, err = s.GetTask(ref, "task-1")
	requireErr(t, err, nil, "GetTask after successful CAS")
	if got.Status != "running" {
		t.Fatalf("task status = %q, want %q", got.Status, "running")
	}

	// The now-stale precondition ("planned") no longer matches, so a second
	// caller racing behind the first observes a clean loss, never a second win.
	ok, err = s.TransitionTaskCAS(ref, "task-1", []string{"planned", "reopened"}, "running")
	requireErr(t, err, nil, "CAS second racer")
	if ok {
		t.Fatal("second CAS racer should lose: task already transitioned")
	}

	// Error cases mirror TransitionTask.
	_, err = s.TransitionTaskCAS(ref, "task-1", []string{"running"}, "")
	requireErr(t, err, ErrEmptyStatus, "empty status")
	_, err = s.TransitionTaskCAS(ref, "no-such-task", []string{"planned"}, "running")
	requireErr(t, err, ErrTaskNotFound, "unknown task")
	_, err = s.TransitionTaskCAS("no-such-plan", "task-1", []string{"planned"}, "running")
	requireErr(t, err, ErrPlanNotFound, "unknown plan")
}

// TestTransitionTaskCAS_ConcurrentRace exercises the actual concurrency
// scenario the guard exists for: many goroutines racing to admit the same
// task must produce exactly one winner, never zero and never more than one.
func TestTransitionTaskCAS_ConcurrentRace(t *testing.T) {
	s := NewMemoryStore()
	ref, err := s.StorePlan(Plan{ID: "plan-1", Scope: Scope{Type: ScopeRun, ID: "run-1"}})
	requireErr(t, err, nil, "StorePlan")
	requireErr(t, s.CreateTask(Task{ID: "task-1", PlanRef: ref, Scope: Scope{Type: ScopeRun, ID: "run-1"}, Status: "planned"}), nil, "CreateTask")

	const racers = 32
	wins := make([]bool, racers)
	var wg sync.WaitGroup
	wg.Add(racers)
	for i := 0; i < racers; i++ {
		go func(i int) {
			defer wg.Done()
			ok, err := s.TransitionTaskCAS(ref, "task-1", []string{"planned"}, "running")
			if err != nil {
				t.Errorf("racer %d: unexpected error: %v", i, err)
				return
			}
			wins[i] = ok
		}(i)
	}
	wg.Wait()

	winCount := 0
	for _, w := range wins {
		if w {
			winCount++
		}
	}
	if winCount != 1 {
		t.Fatalf("winCount = %d, want exactly 1", winCount)
	}
	got, err := s.GetTask(ref, "task-1")
	requireErr(t, err, nil, "GetTask after race")
	if got.Status != "running" {
		t.Fatalf("task status = %q, want %q", got.Status, "running")
	}
	trans, err := s.ListTransitions(ref)
	requireErr(t, err, nil, "ListTransitions after race")
	if len(trans) != 1 {
		t.Fatalf("journal length = %d, want exactly 1 (no double-admission)", len(trans))
	}
}
