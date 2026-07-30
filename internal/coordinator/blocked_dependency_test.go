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

// blockedDepRun spawns parent(fails) -> child(depends on parent) and joins.
func blockedDepRun(t *testing.T, partial ...bool) *RunResult {
	t.Helper()
	repo := ledger.NewMemoryLedgerRepository()
	d := runtime.New(runtime.Policy{})
	_ = d.Register(runtime.Subagent, "fail", staticHandler{err: errors.New("intentional failure")})
	_ = d.Register(runtime.Subagent, "child", staticHandler{out: json.RawMessage(`{"ok":true}`)})
	c := New(repo, subagents.New(d, subagents.Policy{Workers: 1}))

	h, err := c.Spawn(context.Background(), []subagents.Task{
		{ID: "parent", Name: "fail"},
		{ID: "child", Name: "child", DependsOn: []string{"parent"}},
	}, "", partial...)
	if err != nil {
		t.Fatal(err)
	}
	result, err := c.Join(context.Background(), h)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

// TestSpawnNonPartialRunErrorsOnBlockedDependency makes partial_results mean
// something in the only scheduler that runs in production. The knob reached
// exactly one site, dag.go's RunWithPartial call, and buildBatch nils DependsOn
// before handing the batch over, so the pool's own non-partial branch could never
// fire and the flag had no observable effect in either position. collectReady
// decides blocked-ness and never consulted it.
//
// The run-level error must name both the blocked task and the dependency that
// failed, so the report is actionable without re-deriving the graph.
func TestSpawnNonPartialRunErrorsOnBlockedDependency(t *testing.T) {
	result := blockedDepRun(t) // no variadic arg => partial=false, the default
	if result.Err == nil {
		t.Fatal("a non-partial run whose dependency failed must report a run-level error")
	}
	msg := result.Err.Error()
	for _, want := range []string{"child", "parent"} {
		if !strings.Contains(msg, want) {
			t.Errorf("run error %q must name %q", msg, want)
		}
	}

	// The ledger transition must still happen: returning before it would leave the
	// task queued forever, and tasksFromSnapshots would re-dispatch it on resume.
	var child *subagents.Result
	for i := range result.Results {
		if result.Results[i].TaskID == "child" {
			child = &result.Results[i]
		}
	}
	if child == nil {
		t.Fatal("blocked task missing from results")
	}
	if child.Status != "blocked" {
		t.Fatalf("child status = %q, want blocked", child.Status)
	}
	if child.Err == nil || !strings.Contains(child.Err.Error(), "parent") {
		t.Errorf("recorded task error must name the failed dependency, got %v", child.Err)
	}
}

// TestSpawnPartialRunToleratesBlockedDependency is the counterweight: with
// partial results requested, a blocked dependency is an expected outcome and must
// not become a run-level failure.
func TestSpawnPartialRunToleratesBlockedDependency(t *testing.T) {
	result := blockedDepRun(t, true)
	if result.Err != nil {
		t.Fatalf("a partial run must tolerate a blocked dependency, got %v", result.Err)
	}
}
