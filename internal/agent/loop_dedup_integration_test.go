package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
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
	text, err := loop.Run(ctx, "append x to out.txt once", Options{
		Model:              "integration-model",
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
	text, err := loop.Run(ctx, "run a failing command, then try it again", Options{
		Model:              "integration-model",
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
