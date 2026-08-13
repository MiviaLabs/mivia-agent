package cli

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
)

// hangingBatchTool builds a dispatch_tasks tool whose "oneshot" handler answers
// immediately unless the prompt says "block", in which case it hangs until its
// context is cancelled - one fast sibling and one hanging task in the same batch.
func hangingBatchTool(t *testing.T) *dispatchTasksTool {
	t.Helper()
	d := runtime.New(runtime.Policy{MaxDepth: 3})
	err := d.Register(runtime.Subagent, "oneshot", handlerFunc(func(ctx context.Context, req runtime.Request) (json.RawMessage, error) {
		if strings.Contains(string(req.Input), "block") {
			<-ctx.Done()
			return nil, ctx.Err()
		}
		return json.RawMessage(`{"output":"FAST_RESULT"}`), nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	return &dispatchTasksTool{
		dispatcher: d,
		// A finite DefaultTimeout keeps the orchestration budget (and the
		// Capability-derived parent clock) observable in-test: under the
		// raise-only floor a model timeout_seconds:1 cannot shrink the
		// default 0 -> 12h ceiling, so the hanging task would block ~12h.
		cfg:      config.SubagentConfig{DefaultTimeout: 1, InlineOutputBytes: config.DefaultSubagentConfig.InlineOutputBytes},
		repo:     ledger.NewMemoryLedgerRepository(),
		agentReg: testAgentRegistry(t, "oneshot"),
	}
}

// TestDispatchTasksHangingTaskKeepsSiblingResults is the regression the earlier
// timeout test could not catch. TestDispatchTasksTimeoutReturnsStructuredStatus
// passes context.Background(), so the tool-call context never expires and each
// task times out on its own clock. The real agent loop bounds the call with
// Capability(args).Timeout (loop_tools.go), and dispatch_tasks derives that budget
// from the same EffectiveTimeoutSec inputs as each task's own budget - so the two
// deadlines are equal and the outer clock, started first, always fires first.
//
// Join then returns ctx.Err() and discards the run result, runThroughCoordinator
// propagates a nil *RunResult, and dispatch_tasks - having no results - emitted a
// bare {"error":"context deadline exceeded","status":"timed_out"}. Every completed
// sibling's result vanished from the payload even though the ledger still held it.
//
// That is exactly the loss docs/product/agent.md and the ADLC rule now promise
// cannot happen, so it has to be true.
func TestDispatchTasksHangingTaskKeepsSiblingResults(t *testing.T) {
	tool := hangingBatchTool(t)
	args := json.RawMessage(`{
		"timeout_seconds": 1,
		"tasks": [
			{"id":"fast","agent":"oneshot","prompt":"answer now"},
			{"id":"slow","agent":"oneshot","prompt":"block forever"}
		]
	}`)

	// Bound the call exactly as the agent loop does.
	ctx, cancel := context.WithTimeout(context.Background(), tool.Capability(args).Timeout)
	defer cancel()

	start := time.Now()
	body, err := tool.Execute(ctx, args)
	if elapsed := time.Since(start); elapsed > 30*time.Second {
		t.Fatalf("dispatch hung for %s", elapsed)
	}
	if err != nil {
		t.Fatalf("run outcomes travel in the payload, not as a Go error: %v", err)
	}

	if !strings.Contains(body, "FAST_RESULT") && !strings.Contains(body, `"fast"`) {
		t.Fatalf("the completed sibling's result was dropped from the payload:\n%s", body)
	}

	// The payload must account for both tasks, not just report a run-level error.
	var results []map[string]any
	if jsonErr := json.Unmarshal([]byte(body), &results); jsonErr != nil {
		t.Fatalf("payload is not the per-task array (a bare error envelope loses every sibling): %v\n%s", jsonErr, body)
	}
	seen := map[string]string{}
	for _, r := range results {
		id, _ := r["task_id"].(string)
		status, _ := r["status"].(string)
		seen[id] = status
	}
	if _, ok := seen["fast"]; !ok {
		t.Errorf("task 'fast' missing from results: %v", seen)
	}
	if _, ok := seen["slow"]; !ok {
		t.Errorf("task 'slow' missing from results: %v", seen)
	}
	if got := seen["fast"]; got != "completed" {
		t.Errorf("fast task status = %q, want completed", got)
	}
}

// TestDispatchOrchestrationBudgetOutlivesTaskBudget pins the cause rather than the
// symptom: the wall-clock budget for the whole call must exceed the budget of any
// single task in it, or the outer deadline is guaranteed to fire first and the
// salvage path becomes the normal path instead of the exception.
func TestDispatchOrchestrationBudgetOutlivesTaskBudget(t *testing.T) {
	cases := []struct {
		name string
		args string
		task int
	}{
		{"batch level", `{"timeout_seconds":30,"tasks":[{"id":"a","prompt":"x"}]}`, 30},
		{"per task", `{"tasks":[{"id":"a","prompt":"x","timeout_seconds":45}]}`, 45},
		{"max of several", `{"tasks":[{"id":"a","prompt":"x","timeout_seconds":10},{"id":"b","prompt":"y","timeout_seconds":45}]}`, 45},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			got := dispatchOrchestrationSec(config.DefaultSubagentConfig.DefaultTimeout, json.RawMessage(tt.args))
			if got <= tt.task {
				t.Fatalf("orchestration budget %ds must exceed the %ds task budget", got, tt.task)
			}
		})
	}
}

// TestDispatchOrchestrationSecClampsHugeOverride pins the overflow guard at the
// whole-call budget: a huge model timeout_seconds must clamp to
// MaxTimeoutSeconds so dispatchOrchestrationSec (and its +15 slack) stays
// positive and never drops below the orchestration floor. The agent loop arms
// the call with Capability(args).Timeout, so that path must be positive too.
func TestDispatchOrchestrationSecClampsHugeOverride(t *testing.T) {
	args := json.RawMessage(`{"timeout_seconds":10000000000,"tasks":[{"id":"a","prompt":"x"}]}`)
	got := dispatchOrchestrationSec(config.DefaultSubagentConfig.DefaultTimeout, args)
	if got <= 0 {
		t.Fatalf("orchestration budget %ds must stay positive after a huge override", got)
	}
	if got < config.DefaultOrchestrationTimeoutSec {
		t.Fatalf("orchestration budget %ds dropped below the %ds floor", got, config.DefaultOrchestrationTimeoutSec)
	}
	if d := time.Duration(got) * time.Second; d <= 0 {
		t.Fatalf("orchestration budget %ds overflows time.Duration (got %v)", got, d)
	}
	cap := hangingBatchTool(t).Capability(args)
	if cap.Timeout <= 0 {
		t.Fatalf("Capability Timeout %v must stay positive after a huge override", cap.Timeout)
	}
	if cap.Timeout < time.Duration(config.DefaultOrchestrationTimeoutSec)*time.Second {
		t.Fatalf("Capability Timeout %v dropped below the %s floor", cap.Timeout, time.Duration(config.DefaultOrchestrationTimeoutSec)*time.Second)
	}
}

// TestDispatchOrchestrationSecHonorsExplicitTimeout is the regression test for
// the root cause: an explicit batch-level timeout_seconds must actually bound
// the call. Before the fix, EffectiveTimeoutSec(43200, 600) returned 43200
// (raise-only), silently ignoring the requested 600s and giving every
// dispatch_tasks call a 12h budget. Now RequestedTimeoutSec(43200, 600)
// returns 600, so the call budget is 600+slack = 615s — not 43215s.
func TestDispatchOrchestrationSecHonorsExplicitTimeout(t *testing.T) {
	// With the 12h default configured (as in production), an explicit 600s
	// must produce a ~615s call budget, not ~43215s.
	args := json.RawMessage(`{"timeout_seconds":600,"tasks":[{"id":"a","prompt":"x"}]}`)
	got := dispatchOrchestrationSec(config.DefaultSubagentConfig.DefaultTimeout, args)
	if got != 600+dispatchOrchestrationSlackSec {
		t.Fatalf("explicit 600s timeout: orchestration budget = %ds, want %ds (was silently floored to %ds before the fix)",
			got, 600+dispatchOrchestrationSlackSec, config.DefaultOrchestrationTimeoutSec+dispatchOrchestrationSlackSec)
	}
	// Sanity: omitting timeout_seconds still yields the 12h default + slack.
	argsNoTimeout := json.RawMessage(`{"tasks":[{"id":"a","prompt":"x"}]}`)
	gotDefault := dispatchOrchestrationSec(config.DefaultSubagentConfig.DefaultTimeout, argsNoTimeout)
	if gotDefault != config.DefaultOrchestrationTimeoutSec+dispatchOrchestrationSlackSec {
		t.Fatalf("no explicit timeout: orchestration budget = %ds, want %ds", gotDefault, config.DefaultOrchestrationTimeoutSec+dispatchOrchestrationSlackSec)
	}
	// Per-task timeout can still raise above the batch level.
	argsRaised := json.RawMessage(`{"timeout_seconds":600,"tasks":[{"id":"a","prompt":"x","timeout_seconds":900}]}`)
	gotRaised := dispatchOrchestrationSec(config.DefaultSubagentConfig.DefaultTimeout, argsRaised)
	if gotRaised != 900+dispatchOrchestrationSlackSec {
		t.Fatalf("per-task 900s raises above batch 600s: orchestration budget = %ds, want %ds", gotRaised, 900+dispatchOrchestrationSlackSec)
	}
}

func TestRequestTimeout(t *testing.T) {
	effectiveDefault := time.Duration(config.DefaultOrchestrationTimeoutSec) * time.Second
	// configured = 0 (default) and fallback = 0: effective orchestration default.
	if got := requestTimeout(0, 0); got != effectiveDefault {
		t.Errorf("requestTimeout(0, 0) = %v, want %v", got, effectiveDefault)
	}
	// configured > 0 wins over the fallback.
	if got := requestTimeout(60, 0); got != 60*time.Second {
		t.Errorf("requestTimeout(60, 0) = %v, want 60s", got)
	}
	// negative configured treated as zero; fallback applies.
	if got := requestTimeout(-1, 0); got != effectiveDefault {
		t.Errorf("requestTimeout(-1, 0) = %v, want %v (negative treated as zero)", got, effectiveDefault)
	}
	// configured = 0 falls back to the supplied fallback.
	if got := requestTimeout(0, 45); got != 45*time.Second {
		t.Errorf("requestTimeout(0, 45) = %v, want 45s", got)
	}
	// configured = 0 and non-positive fallback: effective orchestration default.
	if got := requestTimeout(0, -1); got != effectiveDefault {
		t.Errorf("requestTimeout(0, -1) = %v, want %v", got, effectiveDefault)
	}
}
