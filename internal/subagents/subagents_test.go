package subagents

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/runtime"
)

type h struct{}

func (h) Invoke(context.Context, runtime.Request) (json.RawMessage, error) {
	return json.RawMessage(`{"done":true}`), nil
}
func TestPoolDependencyOrderAndDeterminism(t *testing.T) {
	d := runtime.New(runtime.Policy{})
	_ = d.Register(runtime.Subagent, "a", h{})
	_ = d.Register(runtime.Subagent, "b", h{})
	p := New(d, Policy{Workers: 2})
	got, err := p.Run(context.Background(), []Task{{ID: "b", Name: "b", DependsOn: []string{"a"}}, {ID: "a", Name: "a"}})
	if err != nil || len(got) != 2 || got[0].TaskID != "b" {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestPoolUsesFreshOpaqueInvocationIDs(t *testing.T) {
	var mu sync.Mutex
	var ids []string
	d := runtime.New(runtime.Policy{})
	_ = d.Register(runtime.Subagent, "a", handlerFunc(func(_ context.Context, req runtime.Request) (json.RawMessage, error) {
		mu.Lock()
		ids = append(ids, req.ID)
		mu.Unlock()
		return json.RawMessage(`{"done":true}`), nil
	}))
	p := New(d, Policy{Workers: 2})
	if _, err := p.Run(context.Background(), []Task{{ID: "same", Name: "a"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Run(context.Background(), []Task{{ID: "same", Name: "a"}}); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(ids) != 2 || ids[0] == "same" || ids[1] == "same" || ids[0] == ids[1] {
		t.Fatalf("runtime invocation IDs = %q, want distinct opaque IDs", ids)
	}
}
func TestPoolRejectsCycles(t *testing.T) {
	d := runtime.New(runtime.Policy{})
	_ = d.Register(runtime.Subagent, "a", h{})
	p := New(d, Policy{})
	if _, err := p.Run(context.Background(), []Task{{ID: "a", Name: "a", DependsOn: []string{"a"}}}); err == nil {
		t.Fatal("cycle accepted")
	}
}

func TestPoolRejectsInvocationKeyCollision(t *testing.T) {
	d := runtime.New(runtime.Policy{})
	_ = d.Register(runtime.Subagent, "a", h{})
	p := New(d, Policy{})
	tasks := []Task{{ID: "a", Name: "a", InvocationKey: "same"}, {ID: "b", Name: "a", InvocationKey: "same"}}
	if _, err := p.Run(context.Background(), tasks); err == nil {
		t.Fatal("invocation key collision accepted")
	}
}

func TestPoolRejectsNegativeBudgetBeforeCumulativeAccounting(t *testing.T) {
	d := runtime.New(runtime.Policy{})
	p := New(d, Policy{MaxBudget: 3})

	_, err := p.validate([]Task{
		{ID: "negative", Budget: -2},
		{ID: "positive", Budget: 3},
		{ID: "overflow", Budget: 1},
	})
	if err == nil || err.Error() != "budget must be non-negative" {
		t.Fatalf("validate error = %v, want negative-budget rejection", err)
	}
}

func TestPoolRecordsBlockedTaskWhenDependencyFails(t *testing.T) {
	d := runtime.New(runtime.Policy{})
	_ = d.Register(runtime.Subagent, "fail", handlerFunc(func(context.Context, runtime.Request) (json.RawMessage, error) { return nil, context.Canceled }))
	_ = d.Register(runtime.Subagent, "next", h{})
	p := New(d, Policy{})
	got, err := p.Run(context.Background(), []Task{{ID: "next", Name: "next", DependsOn: []string{"fail"}}, {ID: "fail", Name: "fail"}})
	if err != nil || got[0].Status != "blocked" {
		t.Fatalf("%+v %v", got, err)
	}
}

type handlerFunc func(context.Context, runtime.Request) (json.RawMessage, error)

func (f handlerFunc) Invoke(ctx context.Context, req runtime.Request) (json.RawMessage, error) {
	return f(ctx, req)
}

func TestPoolTaskTimeoutSurfacesTimedOutStatus(t *testing.T) {
	d := runtime.New(runtime.Policy{})
	_ = d.Register(runtime.Subagent, "slow", handlerFunc(func(ctx context.Context, _ runtime.Request) (json.RawMessage, error) {
		select {
		case <-time.After(time.Second):
			return json.RawMessage(`{"ok":true}`), nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}))
	p := New(d, Policy{Workers: 1})
	start := time.Now()
	got, err := p.Run(context.Background(), []Task{{
		ID: "t1", Name: "slow", Timeout: 30 * time.Millisecond,
	}})
	if elapsed := time.Since(start); elapsed > 400*time.Millisecond {
		t.Fatalf("pool hang: %s", elapsed)
	}
	if err != nil {
		t.Fatalf("unexpected run err: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("results=%d", len(got))
	}
	if got[0].Status != "timed_out" {
		t.Fatalf("status=%q want timed_out err=%v", got[0].Status, got[0].Err)
	}
	if got[0].Err == nil {
		t.Fatal("expected error on timed_out task")
	}
}

func TestPoolCancelStillReportsEveryTask(t *testing.T) {
	d := runtime.New(runtime.Policy{})
	blockStarted := make(chan struct{})
	fastDone := make(chan struct{})
	_ = d.Register(runtime.Subagent, "block", handlerFunc(func(ctx context.Context, _ runtime.Request) (json.RawMessage, error) {
		close(blockStarted)
		<-ctx.Done()
		return nil, ctx.Err()
	}))
	_ = d.Register(runtime.Subagent, "fast", handlerFunc(func(context.Context, runtime.Request) (json.RawMessage, error) {
		close(fastDone)
		return json.RawMessage(`{"done":true}`), nil
	}))
	p := New(d, Policy{Workers: 2})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan []Result, 1)
	go func() {
		got, _ := p.Run(ctx, []Task{
			{ID: "a", Name: "fast"},
			{ID: "b", Name: "block"},
		})
		done <- got
	}()
	// Wait for both handlers (channel sync, no sleep).
	for _, ch := range []<-chan struct{}{blockStarted, fastDone} {
		select {
		case <-ch:
		case <-time.After(time.Second):
			t.Fatal("handler did not start/finish")
		}
	}
	cancel()
	select {
	case got := <-done:
		by := map[string]Result{}
		for _, r := range got {
			by[r.TaskID] = r
		}
		if by["a"].Status != "completed" {
			t.Fatalf("fast task status=%q want completed", by["a"].Status)
		}
		if by["b"].Status != "canceled" && by["b"].Status != "timed_out" && by["b"].Status != "failed" {
			t.Fatalf("block task status=%q want canceled-ish", by["b"].Status)
		}
	case <-time.After(time.Second):
		t.Fatal("pool.Run did not return after cancel")
	}
}

func TestPoolPolicyTimeoutFallback(t *testing.T) {
	d := runtime.New(runtime.Policy{})
	_ = d.Register(runtime.Subagent, "slow", handlerFunc(func(ctx context.Context, _ runtime.Request) (json.RawMessage, error) {
		select {
		case <-time.After(time.Second):
			return json.RawMessage(`{}`), nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}))
	p := New(d, Policy{Workers: 1, Timeout: 25 * time.Millisecond})
	start := time.Now()
	got, err := p.Run(context.Background(), []Task{{ID: "t1", Name: "slow"}}) // no per-task timeout
	if elapsed := time.Since(start); elapsed > 400*time.Millisecond {
		t.Fatalf("hang: %s", elapsed)
	}
	if err != nil {
		t.Fatalf("run err: %v", err)
	}
	if got[0].Status != "timed_out" {
		t.Fatalf("status=%q err=%v", got[0].Status, got[0].Err)
	}
	if !strings.Contains(got[0].Err.Error(), "deadline") && got[0].Err != context.DeadlineExceeded {
		// status is enough; err should be present
		if got[0].Err == nil {
			t.Fatal("expected timeout error")
		}
	}
}

func TestResultStatusWrappedErrors(t *testing.T) {
	tests := []struct {
		name       string
		handlerErr func() error
		wantStatus string
	}{
		{
			name:       "wrapped_deadline_returns_timed_out",
			handlerErr: func() error { return fmt.Errorf("wrapped: %w", context.DeadlineExceeded) },
			wantStatus: "timed_out",
		},
		{
			name:       "wrapped_canceled_returns_canceled",
			handlerErr: func() error { return fmt.Errorf("wrapped: %w", context.Canceled) },
			wantStatus: "canceled",
		},
		{
			name:       "generic_error_returns_failed",
			handlerErr: func() error { return fmt.Errorf("generic failure") },
			wantStatus: "failed",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d := runtime.New(runtime.Policy{})
			_ = d.Register(runtime.Subagent, "test", handlerFunc(func(_ context.Context, _ runtime.Request) (json.RawMessage, error) {
				return nil, tc.handlerErr()
			}))
			p := New(d, Policy{Workers: 1})
			got, err := p.Run(context.Background(), []Task{{
				ID: "t1", Name: "test", Timeout: 10 * time.Millisecond,
			}})
			if err != nil {
				t.Fatalf("unexpected run err: %v", err)
			}
			if len(got) != 1 {
				t.Fatalf("results=%d, want 1", len(got))
			}
			if got[0].Status != tc.wantStatus {
				t.Fatalf("status=%q want %q err=%v", got[0].Status, tc.wantStatus, got[0].Err)
			}
		})
	}
}

func TestResultStatusWrappedContextErrors(t *testing.T) {
	// Direct unit test of resultStatus with a context whose Err() is a deadline.
	// The handler error is also a wrapped deadline, testing both the ctx.Err()
	// branch and the err branch.
	taskCtx, cancel := context.WithTimeout(context.Background(), 1*time.Nanosecond)
	defer cancel()
	<-taskCtx.Done() // ensure deadline fires

	// Handler wraps the deadline error, simulating MultiStepHandler/OneShotHandler.
	wrappedErr := fmt.Errorf("multi-step subagent %q: %w", "test", context.DeadlineExceeded)

	got := resultStatus(taskCtx, context.Background(), wrappedErr)
	if got != "timed_out" {
		t.Fatalf("resultStatus=%q want timed_out", got)
	}

	// Also test wrapped canceled with a canceled context.
	cancelCtx, cancelCancel := context.WithCancel(context.Background())
	cancelCancel()
	<-cancelCtx.Done()

	wrappedCancelErr := fmt.Errorf("subagent %q: %w", "test", context.Canceled)
	gotCancel := resultStatus(cancelCtx, context.Background(), wrappedCancelErr)
	if gotCancel != "canceled" {
		t.Fatalf("resultStatus=%q want canceled", gotCancel)
	}

	// Verify the errors.Is path works: the wrapped error should be detected.
	if !errors.Is(wrappedErr, context.DeadlineExceeded) {
		t.Fatal("errors.Is(wrappedErr, context.DeadlineExceeded) should be true")
	}
	if !errors.Is(wrappedCancelErr, context.Canceled) {
		t.Fatal("errors.Is(wrappedCancelErr, context.Canceled) should be true")
	}
}

// TestPoolBlockedChainNeverReportsMissingOrCycle pins DC-9 (dishonest status and
// false error) in the standalone pool scheduler. With tasks a(fails),
// b(DependsOn a), and c(DependsOn b), the second ready() pass used to visit the
// pending map in random order. When c was visited before b, c saw no result for
// b, stayed pending, and run() reported the false "dependency cycle" error while
// collectResults reported c as "missing" instead of "blocked". The 50-iteration
// loop defeats Go's per-iteration map-order randomization, so the test fails
// before the fix with probability ~1.
func TestPoolBlockedChainNeverReportsMissingOrCycle(t *testing.T) {
	d := runtime.New(runtime.Policy{})
	_ = d.Register(runtime.Subagent, "fail", handlerFunc(func(context.Context, runtime.Request) (json.RawMessage, error) {
		return nil, errors.New("boom")
	}))
	_ = d.Register(runtime.Subagent, "ok", h{})
	p := New(d, Policy{Workers: 2})
	for i := 0; i < 50; i++ {
		got, err := p.Run(context.Background(), []Task{
			{ID: "a", Name: "fail"},
			{ID: "b", Name: "ok", DependsOn: []string{"a"}},
			{ID: "c", Name: "ok", DependsOn: []string{"b"}},
		})
		if err != nil {
			t.Fatalf("iteration %d: run error %v; a failed chain has no cycle", i, err)
		}
		if len(got) != 3 {
			t.Fatalf("iteration %d: got %d results, want 3", i, len(got))
		}
		by := map[string]Result{}
		for _, r := range got {
			by[r.TaskID] = r
		}
		if by["a"].Status != "failed" {
			t.Fatalf("iteration %d: a status=%q want failed", i, by["a"].Status)
		}
		if by["b"].Status != "blocked" || by["b"].Err == nil || !strings.Contains(by["b"].Err.Error(), "dependency a failed") {
			t.Fatalf("iteration %d: b status=%q err=%v want blocked naming a", i, by["b"].Status, by["b"].Err)
		}
		if by["c"].Status != "blocked" || by["c"].Err == nil || !strings.Contains(by["c"].Err.Error(), "dependency b failed") {
			t.Fatalf("iteration %d: c status=%q err=%v want blocked naming b", i, by["c"].Status, by["c"].Err)
		}
		for id, r := range by {
			if r.Status == "missing" {
				t.Fatalf("iteration %d: task %s reported missing", i, id)
			}
		}
	}
}

// FuzzPoolDependencyStatuses asserts the standalone pool scheduler settles
// every acyclic graph honestly. An acyclic graph never reports "missing" and
// never reports a false "dependency cycle": every task with a failed ancestor
// is blocked, and the blocked error names the failed dependency. A cyclic
// graph terminates, and a "dependency cycle" error appears exactly when some
// task has no result. The input bytes parse into a bounded task graph with
// random handler outcomes and random dependencies.
func FuzzPoolDependencyStatuses(f *testing.F) {
	f.Add([]byte{3, 'f', 0, 'o', 1, 0, 'o', 1, 1}) // chain a->b->c, a fails
	f.Add([]byte{2, 'o', 1, 1, 'o', 1, 0})         // cycle a<->b
	f.Add([]byte{0})                               // empty graph
	f.Add([]byte{1, 'f', 0})                       // single failing task
	f.Fuzz(func(t *testing.T, data []byte) {
		tasks, ok := parsePoolFuzzTasks(data)
		if !ok {
			return
		}
		d := runtime.New(runtime.Policy{})
		_ = d.Register(runtime.Subagent, "fail", handlerFunc(func(context.Context, runtime.Request) (json.RawMessage, error) {
			return nil, errors.New("boom")
		}))
		_ = d.Register(runtime.Subagent, "ok", h{})
		results, err := New(d, Policy{Workers: 4}).Run(context.Background(), tasks)
		cycleErr := err != nil && strings.Contains(err.Error(), "dependency cycle")
		if err != nil && !cycleErr {
			t.Fatalf("unexpected run error: %v", err)
		}
		missing := 0
		for _, r := range results {
			if r.Status == "missing" {
				missing++
			}
		}
		if (cycleErr && missing == 0) || (!cycleErr && missing > 0) {
			t.Fatalf("run error and results disagree: cycleErr=%v missing=%d", cycleErr, missing)
		}
		if poolGraphHasCycle(tasks) {
			return
		}
		if cycleErr {
			t.Fatalf("acyclic graph reported dependency cycle: %v", err)
		}
		assertPoolTransitiveBlocking(t, tasks, results)
	})
}

// parsePoolFuzzTasks decodes a bounded task graph from fuzz input bytes.
// Layout: byte[0] is the task count n (0..8). Then each task has one name byte
// ('f' means the fail handler, any other byte means the ok handler), one dep
// count byte, then that many dep bytes, each an index into the generated task
// IDs. A dep index may repeat or reference the task itself, which creates a
// cycle. Malformed or oversized input returns ok=false.
func parsePoolFuzzTasks(data []byte) ([]Task, bool) {
	if len(data) < 1 {
		return nil, false
	}
	n := int(data[0])
	if n > 8 {
		return nil, false
	}
	ids := make([]string, n)
	for i := range ids {
		ids[i] = fmt.Sprintf("t%d", i)
	}
	pos := 1
	tasks := make([]Task, 0, n)
	for i := 0; i < n; i++ {
		if pos >= len(data) {
			return nil, false
		}
		name := "ok"
		if data[pos] == 'f' {
			name = "fail"
		}
		pos++
		if pos >= len(data) {
			return nil, false
		}
		depCount := int(data[pos])
		pos++
		if depCount > n {
			return nil, false
		}
		t := Task{ID: ids[i], Name: name}
		for j := 0; j < depCount; j++ {
			if pos >= len(data) {
				return nil, false
			}
			dep := int(data[pos])
			pos++
			if dep >= n {
				return nil, false
			}
			t.DependsOn = append(t.DependsOn, ids[dep])
		}
		tasks = append(tasks, t)
	}
	return tasks, true
}

// poolGraphHasCycle reports whether the dependency graph contains a cycle.
func poolGraphHasCycle(tasks []Task) bool {
	byID := make(map[string]Task, len(tasks))
	for _, t := range tasks {
		byID[t.ID] = t
	}
	visiting := make(map[string]bool, len(tasks))
	visited := make(map[string]bool, len(tasks))
	var dfs func(id string) bool
	dfs = func(id string) bool {
		if visiting[id] {
			return true
		}
		if visited[id] {
			return false
		}
		visiting[id] = true
		for _, dep := range byID[id].DependsOn {
			if dfs(dep) {
				return true
			}
		}
		delete(visiting, id)
		visited[id] = true
		return false
	}
	for _, t := range tasks {
		if dfs(t.ID) {
			return true
		}
	}
	return false
}

// assertPoolTransitiveBlocking checks the acyclic-graph outcome. A task is
// blocked exactly when one of its dependencies carries an error, transitively.
// Each blocked error names a dependency whose own result carries an error.
func assertPoolTransitiveBlocking(t *testing.T, tasks []Task, results []Result) {
	t.Helper()
	byID := make(map[string]Result, len(results))
	for _, r := range results {
		byID[r.TaskID] = r
	}
	blocked := make(map[string]bool, len(tasks))
	for changed := true; changed; {
		changed = false
		for _, task := range tasks {
			if blocked[task.ID] {
				continue
			}
			for _, dep := range task.DependsOn {
				if r, done := byID[dep]; done && r.Err != nil {
					blocked[task.ID] = true
					changed = true
					break
				}
			}
		}
	}
	for _, task := range tasks {
		r := byID[task.ID]
		if !blocked[task.ID] {
			if r.Status == "missing" || r.Status == "blocked" {
				t.Fatalf("task %s: want executed outcome, got status=%q", task.ID, r.Status)
			}
			continue
		}
		if r.Status != "blocked" || r.Err == nil {
			t.Fatalf("task %s: want blocked, got status=%q err=%v", task.ID, r.Status, r.Err)
		}
		msg := r.Err.Error()
		if !strings.HasPrefix(msg, "dependency ") || !strings.HasSuffix(msg, " failed") {
			t.Fatalf("task %s: blocked error %q has the wrong shape", task.ID, msg)
		}
		dep := strings.TrimSuffix(strings.TrimPrefix(msg, "dependency "), " failed")
		depResult, done := byID[dep]
		if !done || depResult.Err == nil {
			t.Fatalf("task %s: blocked error names %q, which did not fail", task.ID, dep)
		}
	}
}
