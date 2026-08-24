package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// Dedup integration tests: the runtime Dispatcher dedups identical Tool
// invocations within one logical turn keyed by (TurnID, ParentID, Step),
// where the loop stamps Step>0 on every step's tool dispatch (loop.runStep).
// The step component makes the behavior "same-step collapse, cross-step
// fresh": identical calls in ONE step dedup to a single execution, while an
// identical call re-issued in a LATER step of the same turn re-runs. These
// tests exercise that behavior end to end through the real agent loop, real
// httptest provider, and a real run_command against the temp workspace.
//
// The run_command argument shape is the registry's real one: argv is the
// required array ({"command":...} is NOT a run_command shape); a redirect
// needs a shell, so argv[0] is "sh" (allowlisted via newIntegrationHelperWithOpts).
//
// The loop shares ONE dispatcher per turn only when Options.Dispatcher is set
// (the production wiring: NewSessionDispatcher -> agent task handler ->
// Options.Dispatcher). With it nil, executeToolsParallel builds a FRESH
// dispatcher per tool batch, so the per-turn dedup buckets could never survive
// across steps and the cross-step cases below would pass for the wrong reason
// (a brand-new dispatcher has no recorded results, so every cross-step call
// looks fresh regardless of the step key). Each test therefore passes a shared
// dispatcher built over the same registry, mirroring how production runs the
// loop, so the step-stamped bucket is what decides same-step collapse vs
// cross-step re-run.

// dedupSharedDispatcher builds the dispatcher the loop must share across all
// steps of a turn, exactly as the production session wiring does.
func dedupSharedDispatcher(t *testing.T, h *integrationHelper) *runtime.Dispatcher {
	t.Helper()
	d, err := runtime.NewToolDispatcher(h.reg, runtime.Policy{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Close)
	return d
}

const dedupAppendLineArgs = `{"argv":["sh","-c","echo x >> out.txt"]}`

// dedupToolCall builds a run_command provider.ToolCall with the given ID and
// the exact same arguments as its sibling calls.
func dedupToolCall(id string) provider.ToolCall {
	return tc(id, tools.RunCommandToolName, dedupAppendLineArgs)
}

// dedupReadLineCount returns the number of lines in out.txt in the helper's
// workspace, failing the test if the file is missing.
func dedupReadLineCount(t *testing.T, h *integrationHelper) int {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(h.ws.Abs, "out.txt"))
	if err != nil {
		t.Fatalf("read out.txt: %v", err)
	}
	trimmed := strings.TrimRight(string(data), "\n")
	if trimmed == "" {
		return 0
	}
	return strings.Count(trimmed, "\n") + 1
}

// TestLoopDedupAllowsReissueAcrossSteps pins the post-Wave-B step-stamped
// dedup: the loop stamps Step>0 per step (loop.runStep), so the per-turn dedup
// key (TurnID+ParentID+Step) does NOT collapse an identical run_command
// re-issued in a LATER step into the recorded result - the re-issue re-runs
// and out.txt has 2 lines. Same-step collapse, cross-step fresh.
func TestLoopDedupAllowsReissueAcrossSteps(t *testing.T) {
	h := newIntegrationHelperWithOpts(t, []scriptedStep{
		{
			content:   "run it",
			toolCalls: []provider.ToolCall{dedupToolCall("call_reissue_1")},
		},
		{
			content:   "run it again",
			toolCalls: []provider.ToolCall{dedupToolCall("call_reissue_2")},
		},
		{
			content: "done",
		},
	}, tools.DefaultOptions{RunAllowlist: []string{"sh"}})

	loop := h.newLoop()
	ctx := context.Background()
	text, err := loop.Run(ctx, "append x to out.txt twice", Options{
		Model:              "integration-model",
		TurnID:             "turn:1",
		ParentID:           "session",
		MaxSteps:           10,
		RequireFinalText:   true,
		MaxConcurrentTools: 2,
		ToolTimeout:        5 * time.Second,
		Dispatcher:         dedupSharedDispatcher(t, h),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "done") {
		t.Fatalf("expected final answer mentioning done, got %q", text)
	}
	if got := dedupReadLineCount(t, h); got != 2 {
		t.Fatalf("out.txt has %d line(s), want exactly 2: an identical run_command re-issued in a later step must re-run (loop must stamp Step>0)", got)
	}
}

// TestLoopDedupCollapsesIdenticalCallsInOneBatch pins the same-batch case:
// two identical run_command calls in ONE provider response must execute only
// once (in-flight + turn-scoped dedup), so out.txt has exactly 1 line. With
// Wave-A dedup landed this passes immediately; it is a regression pin, not RED.
func TestLoopDedupCollapsesIdenticalCallsInOneBatch(t *testing.T) {
	h := newIntegrationHelperWithOpts(t, []scriptedStep{
		{
			content: "both",
			toolCalls: []provider.ToolCall{
				dedupToolCall("call_a"),
				dedupToolCall("call_b"),
			},
		},
		{
			content: "done",
		},
	}, tools.DefaultOptions{RunAllowlist: []string{"sh"}})

	loop := h.newLoop()
	ctx := context.Background()
	text, err := loop.Run(ctx, "append x to out.txt once", Options{Model: "integration-model",
		TurnID:             "turn:2",
		ParentID:           "session",
		MaxSteps:           10,
		RequireFinalText:   true,
		MaxConcurrentTools: 2,
		ToolTimeout:        5 * time.Second,
		Dispatcher:         dedupSharedDispatcher(t, h),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "done") {
		t.Fatalf("expected final answer mentioning done, got %q", text)
	}
	if got := dedupReadLineCount(t, h); got != 1 {
		t.Fatalf("out.txt has %d line(s), want exactly 1: identical same-batch calls must collapse to one execution", got)
	}
}

// TestLoopDedupRetryAfterFailureRerunsInLaterStep is a regression pin for the
// cross-step failure retry. The failing command writes a side effect
// (echo fail >> out.txt) and THEN exits 1: a dedup-replayed recorded failure
// is byte-identical to a fresh one, so the side effect is the discriminator
// that proves the later-step re-issue re-executed instead of silently
// replaying step 1's recorded failure. The loop must complete with the final
// answer, record two run_command tool results each reporting exit=1, and
// out.txt must have exactly 2 lines - one per actual execution.
func TestLoopDedupRetryAfterFailureRerunsInLaterStep(t *testing.T) {
	failArgs := `{"argv":["sh","-c","echo fail >> out.txt; exit 1"]}`
	h := newIntegrationHelperWithOpts(t, []scriptedStep{
		{
			content:   "try",
			toolCalls: []provider.ToolCall{tc("call_fail_1", tools.RunCommandToolName, failArgs)},
		},
		{
			content:   "try again",
			toolCalls: []provider.ToolCall{tc("call_fail_2", tools.RunCommandToolName, failArgs)},
		},
		{
			content: "done",
		},
	}, tools.DefaultOptions{RunAllowlist: []string{"sh"}})

	loop := h.newLoop()
	ctx := context.Background()
	text, err := loop.Run(ctx, "run a failing command, then try it again", Options{Model: "integration-model",
		TurnID:             "turn:3",
		ParentID:           "session",
		MaxSteps:           10,
		RequireFinalText:   true,
		MaxConcurrentTools: 2,
		ToolTimeout:        5 * time.Second,
		Dispatcher:         dedupSharedDispatcher(t, h),
	})
	if err != nil {
		t.Fatalf("loop must complete despite the failing command being re-issued: %v", err)
	}
	if !strings.Contains(text, "done") {
		t.Fatalf("expected final answer mentioning done, got %q", text)
	}
	if got := dedupReadLineCount(t, h); got != 2 {
		t.Fatalf("out.txt has %d line(s), want exactly 2: the later-step re-issue must re-execute (the side effect proves a re-run, not a replayed recorded failure)", got)
	}
	var toolResults int
	for _, msg := range loop.Messages {
		if msg.Role == provider.RoleTool && msg.Name == tools.RunCommandToolName {
			toolResults++
			if !strings.Contains(msg.Content, "exit=1") {
				t.Fatalf("run_command tool result missing exit=1: %q", msg.Content)
			}
		}
	}
	if toolResults != 2 {
		t.Fatalf("expected 2 run_command tool results in history, got %d", toolResults)
	}
}

// countingReadTool is a fake ExecutionRead-class tool that counts every
// handler execution. Declaring ExecutionRead stamps SkipDedup on every
// dispatch (prepareToolTasks), so identical calls - even two in ONE batch -
// must each execute fresh instead of being collapsed by the per-turn dedup.
type countingReadTool struct {
	mu    sync.Mutex
	calls int
}

func (t *countingReadTool) Name() string        { return "counted_read" }
func (t *countingReadTool) Description() string { return "counted read tool (integration test)" }
func (t *countingReadTool) Parameters() map[string]any {
	return map[string]any{
		"type":                 "object",
		"properties":           map[string]any{},
		"additionalProperties": false,
	}
}
func (t *countingReadTool) Execute(_ context.Context, _ json.RawMessage) (string, error) {
	t.mu.Lock()
	t.calls++
	n := t.calls
	t.mu.Unlock()
	return fmt.Sprintf("counted read execution #%d", n), nil
}
func (t *countingReadTool) Capability(_ json.RawMessage) tools.Capability {
	return tools.Capability{Class: tools.ExecutionRead}
}

// TestLoopReadToolAlwaysExecutesFreshAcrossSteps pins the Wave-B read-class
// freshness contract end to end: an ExecutionRead-class tool is stamped
// SkipDedup at dispatch, so an identical call re-issued in a LATER step - and
// even a same-batch twin - always executes fresh rather than being answered
// from the dedup cache. The counting handler proves it ran 3 times for 3
// calls (2 in one batch + 1 later step), and history carries 3 RoleTool
// messages.
func TestLoopReadToolAlwaysExecutesFreshAcrossSteps(t *testing.T) {
	h := newIntegrationHelperWithOpts(t, []scriptedStep{
		{
			content: "read it twice",
			toolCalls: []provider.ToolCall{
				tc("call_read_batch_1", "counted_read", `{}`),
				tc("call_read_batch_2", "counted_read", `{}`),
			},
		},
		{
			content:   "read it again",
			toolCalls: []provider.ToolCall{tc("call_read_later_1", "counted_read", `{}`)},
		},
		{
			content: "done",
		},
	}, tools.DefaultOptions{})

	// Register the custom tool AFTER helper construction but BEFORE the shared
	// dispatcher is built: NewToolDispatcher snapshots the registry's tools at
	// build time, so the dispatcher must be created after registration to
	// install the handler (the same ordering a session uses for
	// generation-owned tools). dedupSharedDispatcher runs inside the Options
	// literal below, i.e. after this Register.
	counting := &countingReadTool{}
	h.reg.Register(counting)

	loop := h.newLoop()
	ctx := context.Background()
	text, err := loop.Run(ctx, "run the counted read tool three times", Options{Model: "integration-model",
		TurnID:             "turn:read-fresh",
		ParentID:           "session",
		MaxSteps:           10,
		RequireFinalText:   true,
		MaxConcurrentTools: 2,
		ToolTimeout:        5 * time.Second,
		Dispatcher:         dedupSharedDispatcher(t, h),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "done") {
		t.Fatalf("expected final answer mentioning done, got %q", text)
	}
	if counting.calls != 3 {
		t.Fatalf("counted_read handler executed %d time(s), want exactly 3: an ExecutionRead-class call must run fresh for same-batch twins AND cross-step re-issues", counting.calls)
	}
	var toolMsgs int
	for _, msg := range loop.Messages {
		if msg.Role == provider.RoleTool && msg.Name == "counted_read" {
			toolMsgs++
		}
	}
	if toolMsgs != 3 {
		t.Fatalf("expected 3 counted_read tool messages in history, got %d", toolMsgs)
	}
}

// TestLoopDuplicateDeliveryMarkedInHistory pins the Wave-C duplicate notice
// through the REAL loop with the batch budget active, so history bodies come
// from the cappedBody shaping path. Of step 1's two identical run_command
// calls, exactly one executes and exactly one carries the fixed notice; the
// later-step re-issue re-executes and carries no notice. Ownership is racy,
// so assertions are order-agnostic counts, never indices.
func TestLoopDuplicateDeliveryMarkedInHistory(t *testing.T) {
	h := newIntegrationHelperWithOpts(t, []scriptedStep{
		{
			content: "run both",
			toolCalls: []provider.ToolCall{
				dedupToolCall("call_dup_a"),
				dedupToolCall("call_dup_b"),
			},
		},
		{
			content:   "run it again",
			toolCalls: []provider.ToolCall{dedupToolCall("call_dup_c")},
		},
		{
			content: "done",
		},
	}, tools.DefaultOptions{RunAllowlist: []string{"sh"}})

	loop := h.newLoop()
	ctx := context.Background()
	text, err := loop.Run(ctx, "append x to out.txt once, then append again", Options{Model: "integration-model",
		TurnID:                 "turn:dup-marked",
		ParentID:               "session",
		MaxSteps:               10,
		RequireFinalText:       true,
		MaxConcurrentTools:     2,
		ToolTimeout:            5 * time.Second,
		Dispatcher:             dedupSharedDispatcher(t, h),
		BatchResultBudgetBytes: 32 << 10, // >0: history bodies come from the cappedBody shaping path
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "done") {
		t.Fatalf("expected final answer mentioning done, got %q", text)
	}
	if got := dedupReadLineCount(t, h); got != 2 {
		t.Fatalf("out.txt has %d line(s), want exactly 2: the identical call re-issued in a later step must re-execute", got)
	}

	var step1Marked, step1Full, step2Marked, totalMarked int
	for _, msg := range loop.Messages {
		if msg.Role != provider.RoleTool || msg.Name != tools.RunCommandToolName {
			continue
		}
		marked := strings.Contains(msg.Content, EXPECTED_NOTICE)
		full := strings.Contains(msg.Content, "exit=0")
		if marked {
			totalMarked++
		}
		switch msg.ToolCallID {
		case "call_dup_a", "call_dup_b":
			if marked {
				step1Marked++
			}
			if full {
				step1Full++
			}
		case "call_dup_c":
			if marked {
				step2Marked++
			}
			if !full {
				t.Fatalf("the later-step re-issued run_command must carry its full output (exit=0), got %q", msg.Content)
			}
		}
	}
	if step1Marked != 1 || step1Full != 1 {
		t.Fatalf("step 1 must have exactly one duplicate-marked and one full-output message (marked=%d full=%d)", step1Marked, step1Full)
	}
	if step2Marked != 0 {
		t.Fatal("the later-step re-issued run_command must not carry the duplicate notice")
	}
	if totalMarked != 1 {
		t.Fatalf("expected exactly 1 duplicate-marked run_command message in the whole history, got %d", totalMarked)
	}
}
