package coordinator

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
)

// blockedDepRun spawns solo(succeeds) + parent(fails) + child(depends on parent)
// and joins. Three tasks so the "every task is reported" assertion has a
// successful one available to lose.
func blockedDepRun(t *testing.T) *RunResult {
	t.Helper()
	repo := ledger.NewMemoryLedgerRepository()
	d := runtime.New(runtime.Policy{})
	_ = d.Register(runtime.Subagent, "fail", staticHandler{err: errors.New("intentional failure")})
	_ = d.Register(runtime.Subagent, "ok", staticHandler{out: json.RawMessage(`{"ok":true}`)})
	c := New(repo, subagents.New(d, subagents.Policy{Workers: 1}))

	h, err := c.Spawn(context.Background(), []subagents.Task{
		{ID: "solo", Name: "ok"},
		{ID: "parent", Name: "fail"},
		{ID: "child", Name: "ok", DependsOn: []string{"parent"}},
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	result, err := c.Join(context.Background(), h)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

// TestSpawnRunErrorsOnBlockedDependency - a run that could not do everything it
// was asked to says so. collectReady decides blocked-ness and reported nothing at
// run level, so a run that silently did less looked like a clean success.
//
// The error names both the blocked task and the dependency that failed, so the
// report is actionable without re-deriving the graph.
func TestSpawnRunErrorsOnBlockedDependency(t *testing.T) {
	result := blockedDepRun(t)
	if result.Err == nil {
		t.Fatal("a run whose dependency failed must report a run-level error")
	}
	msg := result.Err.Error()
	for _, want := range []string{"child", "parent"} {
		if !strings.Contains(msg, want) {
			t.Errorf("run error %q must name %q", msg, want)
		}
	}
}

// TestSpawnReturnsFullResultSetDespiteBlockedDependency is the guarantee that
// replaced the removed partial_results knob: reporting a run-level failure must
// never cost the caller the results. Every task is accounted for, each with its
// own status, including the one that succeeded next to the failure.
//
// The ledger transition to blocked also still happens - aborting the scheduler
// here (as the pool's ready() once did when partial results were off) would leave
// the task queued forever and tasksFromSnapshots would re-dispatch it on resume.
func TestSpawnReturnsFullResultSetDespiteBlockedDependency(t *testing.T) {
	result := blockedDepRun(t)

	byID := make(map[string]subagents.Result, len(result.Results))
	for _, r := range result.Results {
		byID[r.TaskID] = r
	}
	if len(byID) != 3 {
		t.Fatalf("expected one result per task, got %d: %v", len(byID), byID)
	}
	if got := byID["solo"].Status; got != "completed" {
		t.Errorf("the task that succeeded must still be reported: solo status = %q", got)
	}
	if got := byID["parent"].Status; got != "failed" {
		t.Errorf("parent status = %q, want failed", got)
	}
	child := byID["child"]
	if child.Status != "blocked" {
		t.Errorf("child status = %q, want blocked", child.Status)
	}
	if child.Err == nil || !strings.Contains(child.Err.Error(), "parent") {
		t.Errorf("recorded task error must name the failed dependency, got %v", child.Err)
	}
}
