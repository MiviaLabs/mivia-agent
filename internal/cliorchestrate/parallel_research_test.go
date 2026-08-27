package cliorchestrate

import (
	"context"
	"encoding/json"
	"fmt"
	cliagents "github.com/MiviaLabs/mivia-agent/internal/cliagents"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
)

// Phase 4 of the agent model routing plan: a parallel research fan-out must be
// deterministic to aggregate, honest about partial failure, and carry enough
// provenance and typed cause that a caller can act on a mixed outcome.

func researchSnapshots(ids ...string) []ledger.TaskSnapshot {
	out := make([]ledger.TaskSnapshot, len(ids))
	for i, id := range ids {
		out[i] = ledger.TaskSnapshot{TaskID: id, AgentName: "researcher-" + id}
	}
	return out
}

// One slow or failed researcher must not strand the run: every task reports,
// and the successful results survive alongside the failures.
func TestFanoutPreservesPartialResults(t *testing.T) {
	tool := &dispatchTasksTool{}
	snaps := researchSnapshots("a", "b", "c")
	results := []subagents.Result{
		{TaskID: "a", Status: "completed", Output: json.RawMessage(`{"output":"evidence A"}`)},
		{TaskID: "b", Status: "failed", Err: fmt.Errorf("provider refused")},
		{TaskID: "c", Status: "completed", Output: json.RawMessage(`{"output":"evidence C"}`)},
	}
	var decoded []map[string]any
	if err := json.Unmarshal([]byte(tool.encodeResults(snaps, results)), &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded) != 3 {
		t.Fatalf("every task must report, got %d", len(decoded))
	}
	if decoded[0]["output"] == nil || decoded[2]["output"] == nil {
		t.Fatal("a failed sibling must not discard successful results")
	}
	if decoded[1]["error"] == nil {
		t.Fatal("the failed task must report its failure")
	}
}

// Aggregation follows the caller's declared task order, not completion order,
// so the same fan-out produces the same report every run.
func TestFanoutAggregationIsDeterministic(t *testing.T) {
	tool := &dispatchTasksTool{}
	snaps := researchSnapshots("a", "b", "c")
	// Results arrive in completion order, which is not declaration order.
	results := []subagents.Result{
		{TaskID: "a", Status: "completed", Output: json.RawMessage(`{"output":"A"}`)},
		{TaskID: "b", Status: "completed", Output: json.RawMessage(`{"output":"B"}`)},
		{TaskID: "c", Status: "completed", Output: json.RawMessage(`{"output":"C"}`)},
	}
	first := tool.encodeResults(snaps, results)
	for i := 0; i < 5; i++ {
		if got := tool.encodeResults(snaps, results); got != first {
			t.Fatalf("aggregation is not stable:\n%s\n%s", first, got)
		}
	}
	var decoded []map[string]any
	if err := json.Unmarshal([]byte(first), &decoded); err != nil {
		t.Fatal(err)
	}
	for i, want := range []string{"a", "b", "c"} {
		if decoded[i]["task_id"] != want {
			t.Fatalf("position %d = %v, want %s", i, decoded[i]["task_id"], want)
		}
	}
}

// Every aggregated result names the agent that produced it, so evidence from a
// fan-out is attributable.
func TestFanoutResultsCarryAgentProvenance(t *testing.T) {
	tool := &dispatchTasksTool{}
	out := tool.encodeResults(researchSnapshots("a"), []subagents.Result{
		{TaskID: "a", Status: "completed", Output: json.RawMessage(`{"output":"A"}`)},
	})
	var decoded []map[string]any
	if err := json.Unmarshal([]byte(out), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded[0]["agent"] != "researcher-a" {
		t.Fatalf("agent provenance = %v", decoded[0]["agent"])
	}
}

// Status collapses distinct outcomes; reason must keep them apart so a caller
// can tell a cancel from a deadline from an agent's own ceiling.
func TestTerminationReasonsAreTyped(t *testing.T) {
	for name, tc := range map[string]struct {
		result subagents.Result
		want   string
	}{
		"agent ceiling": {subagents.Result{TaskID: "x", Err: fmt.Errorf("wrap: %w", cliagents.ErrAgentWallClockExceeded)}, "agent_wall_clock_exceeded"},
		"deadline":      {subagents.Result{TaskID: "x", Err: fmt.Errorf("wrap: %w", context.DeadlineExceeded)}, "deadline_exceeded"},
		"canceled":      {subagents.Result{TaskID: "x", Err: fmt.Errorf("wrap: %w", context.Canceled)}, "canceled"},
		"failed":        {subagents.Result{TaskID: "x", Err: fmt.Errorf("provider refused")}, "failed"},
		"never started": {subagents.Result{TaskID: "x", Status: "missing"}, "never_started"},
		"success":       {subagents.Result{TaskID: "x", Status: "completed"}, ""},
	} {
		t.Run(name, func(t *testing.T) {
			if got := terminationReason(tc.result); got != tc.want {
				t.Fatalf("reason = %q, want %q", got, tc.want)
			}
		})
	}
}

// The reason field is model-visible and aggregated across a fan-out, so it
// must stay a fixed vocabulary and never echo error text - otherwise it
// becomes a second channel for prompt or payload content.
func TestTerminationReasonNeverLeaksErrorText(t *testing.T) {
	secret := "sk-live-DEADBEEF-do-not-echo"
	reason := terminationReason(subagents.Result{
		TaskID: "x", Err: fmt.Errorf("provider rejected credential %s", secret),
	})
	if strings.Contains(reason, secret) || strings.Contains(reason, "credential") {
		t.Fatalf("reason leaked error text: %q", reason)
	}
	if reason != "failed" {
		t.Fatalf("reason = %q, want the fixed vocabulary value", reason)
	}
}
