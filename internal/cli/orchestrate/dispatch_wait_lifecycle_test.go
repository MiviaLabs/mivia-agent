package orchestrate

// Dispatch-tasks wait-mode and dedup lifecycle tests, split out of
// orchestrate_lifecycle_test.go to stay under the 800-line file ceiling
// enforced by internal/cli's structure gate.

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	sdkagentloop "github.com/MiviaLabs/mivia-ai-sdk/agentloop"
	"github.com/MiviaLabs/mivia-ai-sdk/provider"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
)

func TestTaskDepthPropagates(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	dispatcher := runtime.New(runtime.Policy{MaxDepth: 8})
	depths := make(chan int, 1)
	if err := dispatcher.Register(runtime.Subagent, "oneshot", handlerFunc(func(_ context.Context, req runtime.Request) (json.RawMessage, error) {
		depths <- req.Depth
		return json.RawMessage(`{}`), nil
	})); err != nil {
		t.Fatal(err)
	}
	ctx := runtime.ContextWithCaller(context.Background(), runtime.Caller{SessionID: "depth-session", TurnID: "turn-1", Depth: 1})
	_, err := NewDispatchTasksToolConfigured(dispatcher, config.DefaultSubagentConfig, repo, testAgentRegistry(t, "oneshot")).Execute(ctx, json.RawMessage(`{"tasks":[{"id":"t1","agent":"oneshot","prompt":"work"}],"wait":"run"}`))
	if err != nil {
		t.Fatal(err)
	}
	if depth := <-depths; depth != 2 {
		t.Fatalf("task depth = %d, want 2", depth)
	}
}

// TestDispatchTasksWaitNoneReturnsRunID guards dispatch_tasks absorbing
// spawn_agent's async wait modes: wait="none" must return immediately with
// a run_id/status envelope, not block for the run to finish.
func TestDispatchTasksWaitNoneReturnsRunID(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	dispatcher := runtime.New(runtime.Policy{})
	block := make(chan struct{})
	if err := dispatcher.Register(runtime.Subagent, "worker", handlerFunc(func(ctx context.Context, req runtime.Request) (json.RawMessage, error) {
		<-block
		return json.RawMessage(`{"ok":true}`), nil
	})); err != nil {
		t.Fatal(err)
	}
	defer close(block)
	tool := NewDispatchTasksToolConfigured(dispatcher, config.DefaultSubagentConfig, repo, testAgentRegistry(t, "worker"))
	ctx := runtime.ContextWithCaller(context.Background(), runtime.Caller{SessionID: "session-wait-none"})
	out, err := tool.Execute(ctx, json.RawMessage(`{"tasks":[{"id":"t1","agent":"worker","prompt":"work"}],"wait":"none"}`))
	if err != nil {
		t.Fatalf("Execute error = %v, want nil (must not block on the still-running task)", err)
	}
	var resp struct {
		RunID string `json:"run_id"`
	}
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		t.Fatalf("Execute output %q did not decode: %v", out, err)
	}
	if resp.RunID == "" {
		t.Fatalf("Execute output %q missing run_id", out)
	}
}

func TestDispatchTasksOmittedWaitReturnsRunID(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	dispatcher := runtime.New(runtime.Policy{})
	block := make(chan struct{})
	if err := dispatcher.Register(runtime.Subagent, "worker", handlerFunc(func(context.Context, runtime.Request) (json.RawMessage, error) {
		<-block
		return json.RawMessage(`{"ok":true}`), nil
	})); err != nil {
		t.Fatal(err)
	}
	defer close(block)
	tool := NewDispatchTasksToolConfigured(dispatcher, config.DefaultSubagentConfig, repo, testAgentRegistry(t, "worker"))
	started := time.Now()
	out, err := tool.Execute(runtime.ContextWithCaller(context.Background(), runtime.Caller{SessionID: "session-default-wait"}), json.RawMessage(`{"tasks":[{"id":"t1","agent":"worker","prompt":"work"}]}`))
	if err != nil {
		t.Fatalf("Execute error = %v", err)
	}
	if time.Since(started) > time.Second {
		t.Fatalf("omitted wait blocked for %s; want detached dispatch", time.Since(started))
	}
	var resp struct {
		RunID string `json:"run_id"`
	}
	if err := json.Unmarshal([]byte(out), &resp); err != nil || resp.RunID == "" {
		t.Fatalf("output %q missing run_id: %v", out, err)
	}
}

func TestNormalizedDispatchWaitDefaultsToNone(t *testing.T) {
	got, err := normalizedDispatchWait("", "")
	if err != nil {
		t.Fatalf("normalizedDispatchWait: %v", err)
	}
	if got != "none" {
		t.Fatalf("default wait = %q, want none", got)
	}
	for _, mode := range []string{"none", "task", "run"} {
		if mode == "task" {
			got, err = normalizedDispatchWait(mode, "task-1")
		} else {
			got, err = normalizedDispatchWait(mode, "")
		}
		if err != nil || got != mode {
			t.Fatalf("explicit wait %q normalized to %q, err=%v", mode, got, err)
		}
	}
}

// TestDispatchTasksSameToolCallIDDedupesRetry pins the harness-only
// idempotency-key redesign: dispatch_tasks no longer accepts a model-
// supplied idempotency_key (a model has no reliable way to construct a
// value that is stable across a genuine retry but distinct from every
// other call - see dispatchNamespace's doc comment). The harness derives
// one instead from the tool call's own ToolCallID, so a provider-level
// retry of the SAME assistant turn - which replays the SAME ToolCallID -
// still dedupes: the worker runs once, and both calls return the reused
// run's result.
func TestDispatchTasksSameToolCallIDDedupesRetry(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	dispatcher := runtime.New(runtime.Policy{})
	var calls atomic.Int32
	if err := dispatcher.Register(runtime.Subagent, "worker", handlerFunc(func(context.Context, runtime.Request) (json.RawMessage, error) {
		calls.Add(1)
		return json.RawMessage(`{"ok":true}`), nil
	})); err != nil {
		t.Fatal(err)
	}
	tool := NewDispatchTasksToolConfigured(dispatcher, config.DefaultSubagentConfig, repo, testAgentRegistry(t, "worker"))
	args := json.RawMessage(`{"tasks":[{"id":"task-1","agent":"worker","prompt":"requested work"}],"wait":"run"}`)
	callCtx := func() context.Context {
		base := runtime.ContextWithCaller(context.Background(), runtime.Caller{SessionID: "session"})
		return sdkagentloop.WithToolCall(base, provider.ToolCall{ID: "call_retry_1", Name: ToolDispatchTasks})
	}
	first, err := tool.Execute(callCtx(), args)
	if err != nil {
		t.Fatal(err)
	}
	second, err := tool.Execute(callCtx(), args)
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatalf("worker invoked %d times, want 1 (a replayed ToolCallID must reuse the run)", calls.Load())
	}
	if first != second {
		t.Fatalf("first = %q, second = %q; want the reused run's result both times", first, second)
	}
}

// TestDispatchTasksDifferentToolCallIDsDoNotDedupe is
// TestDispatchTasksSameToolCallIDDedupesRetry's negative: two calls with
// the SAME task shape but DIFFERENT ToolCallIDs are genuinely separate
// dispatches (two distinct turns asking for identical-looking work), not
// a replay, so both must run.
func TestDispatchTasksDifferentToolCallIDsDoNotDedupe(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	dispatcher := runtime.New(runtime.Policy{})
	var calls atomic.Int32
	if err := dispatcher.Register(runtime.Subagent, "worker", handlerFunc(func(context.Context, runtime.Request) (json.RawMessage, error) {
		calls.Add(1)
		return json.RawMessage(`{"ok":true}`), nil
	})); err != nil {
		t.Fatal(err)
	}
	tool := NewDispatchTasksToolConfigured(dispatcher, config.DefaultSubagentConfig, repo, testAgentRegistry(t, "worker"))
	args := json.RawMessage(`{"tasks":[{"id":"task-1","agent":"worker","prompt":"requested work"}],"wait":"run"}`)
	base := runtime.ContextWithCaller(context.Background(), runtime.Caller{SessionID: "session"})
	if _, err := tool.Execute(sdkagentloop.WithToolCall(base, provider.ToolCall{ID: "call_a", Name: ToolDispatchTasks}), args); err != nil {
		t.Fatal(err)
	}
	if _, err := tool.Execute(sdkagentloop.WithToolCall(base, provider.ToolCall{ID: "call_b", Name: ToolDispatchTasks}), args); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Fatalf("worker invoked %d times, want 2 (distinct ToolCallIDs must not dedupe)", calls.Load())
	}
}

// TestDispatchTasksDefaultWaitIsNoneEnvelope guards the interactive default:
// omitting "wait" returns a reachable run envelope instead of blocking for
// the full batch. The UI and transcript reconstruction paths also accept this
// wrapped shape.
func TestDispatchTasksDefaultWaitIsNoneEnvelope(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	dispatcher := runtime.New(runtime.Policy{})
	if err := dispatcher.Register(runtime.Subagent, "worker", handlerFunc(func(context.Context, runtime.Request) (json.RawMessage, error) {
		return json.RawMessage(`{"output":"done"}`), nil
	})); err != nil {
		t.Fatal(err)
	}
	tool := NewDispatchTasksToolConfigured(dispatcher, config.DefaultSubagentConfig, repo, testAgentRegistry(t, "worker"))
	ctx := runtime.ContextWithCaller(context.Background(), runtime.Caller{SessionID: "session-default-wait"})
	out, err := tool.Execute(ctx, json.RawMessage(`{"tasks":[{"id":"t1","agent":"worker","prompt":"work"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	var response struct {
		RunID       string `json:"run_id"`
		TaskResults []struct {
			TaskID string `json:"task_id"`
			Status string `json:"status"`
		} `json:"task_results"`
	}
	if err := json.Unmarshal([]byte(out), &response); err != nil {
		t.Fatalf("Execute output %q is not a run envelope: %v", out, err)
	}
	// The run is detached, so task_results may still be empty while the
	// worker is queued. The returned run ID is the durable handle used by
	// inspect_agents or join_run.
	if response.RunID == "" {
		t.Fatalf("response = %+v, want a reachable run envelope", response)
	}
}
