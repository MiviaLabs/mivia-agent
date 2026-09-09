// should_skip_task_test.go isolates every leg of Pool.skipCanceledTask's
// guard (subagents.go): the hook being unset, the hook declining, the hook
// accepting, and the OnTaskDone finalize hook being unset while a task is
// skipped.
package subagents

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/runtime"
)

// skipProbePool builds a one-task pool whose handler records that it ran.
// ran is set only when the handler is actually invoked.
func skipProbePool(t *testing.T, ran *bool) *Pool {
	t.Helper()
	d := runtime.New(runtime.Policy{})
	if err := d.Register(runtime.Subagent, "work", handlerFunc(func(_ context.Context, _ runtime.Request) (json.RawMessage, error) {
		*ran = true
		return json.RawMessage(`"done"`), nil
	})); err != nil {
		t.Fatal(err)
	}
	return New(d, Policy{Workers: 1})
}

func runSkipProbe(t *testing.T, p *Pool) Result {
	t.Helper()
	results, err := p.Run(context.Background(), []Task{{ID: "t1", Name: "work"}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("Run returned %d results, want 1", len(results))
	}
	return results[0]
}

// TestSkipCanceledTaskUnsetHookRunsTask proves an unset ShouldSkipTask leaves
// execution exactly as it was: the handler runs and the task completes.
func TestSkipCanceledTaskUnsetHookRunsTask(t *testing.T) {
	var ran bool
	p := skipProbePool(t, &ran)

	r := runSkipProbe(t, p)
	if !ran {
		t.Fatal("the handler did not run with ShouldSkipTask unset")
	}
	if r.Status != "completed" {
		t.Fatalf("status = %q, want completed", r.Status)
	}
}

// TestSkipCanceledTaskDecliningHookRunsTask isolates the second leg of the
// guard: the hook IS set but reports false, so the task still runs.
func TestSkipCanceledTaskDecliningHookRunsTask(t *testing.T) {
	var ran bool
	p := skipProbePool(t, &ran)
	asked := 0
	p.ShouldSkipTask = func(_ context.Context, _ Task) bool {
		asked++
		return false
	}

	r := runSkipProbe(t, p)
	if asked != 1 {
		t.Fatalf("ShouldSkipTask consulted %d times, want exactly 1", asked)
	}
	if !ran {
		t.Fatal("the handler did not run although ShouldSkipTask reported false")
	}
	if r.Status != "completed" {
		t.Fatalf("status = %q, want completed", r.Status)
	}
}

// TestSkipCanceledTaskAcceptingHookSkipsHandler is the core case: an
// accepting hook settles the task as canceled and the handler never runs, so
// the task performs no side effects.
func TestSkipCanceledTaskAcceptingHookSkipsHandler(t *testing.T) {
	var ran bool
	p := skipProbePool(t, &ran)
	p.ShouldSkipTask = func(_ context.Context, _ Task) bool { return true }

	var doneResults []Result
	p.OnTaskDone = func(_ context.Context, _ Task, r Result) {
		doneResults = append(doneResults, r)
	}

	r := runSkipProbe(t, p)
	if ran {
		t.Fatal("the handler ran although ShouldSkipTask reported true")
	}
	if r.Status != "canceled" {
		t.Fatalf("status = %q, want canceled", r.Status)
	}
	if !errors.Is(r.Err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", r.Err)
	}
	if len(doneResults) != 1 || doneResults[0].Status != "canceled" {
		t.Fatalf("OnTaskDone saw %+v, want exactly one canceled result", doneResults)
	}
}

// TestSkipCanceledTaskAcceptingHookWithoutOnTaskDone isolates the
// `p.OnTaskDone != nil` leg: skipping must not depend on a finalize hook
// being installed.
func TestSkipCanceledTaskAcceptingHookWithoutOnTaskDone(t *testing.T) {
	var ran bool
	p := skipProbePool(t, &ran)
	p.ShouldSkipTask = func(_ context.Context, _ Task) bool { return true }

	r := runSkipProbe(t, p) // must not panic
	if ran {
		t.Fatal("the handler ran although ShouldSkipTask reported true")
	}
	if r.Status != "canceled" {
		t.Fatalf("status = %q, want canceled", r.Status)
	}
}
