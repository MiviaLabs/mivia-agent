package coordinator

import (
	"context"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agentmsg"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
)

// TestContextForTaskInstallsToolCallSink verifies that contextForTask wires
// the run's per-task tool-call sink onto the task's context (plan Part B,
// chunk 4): when runExecInfo.toolCalls is non-nil, ToolCallSinkFrom on the
// returned context reports ok=true, and invoking the sink buffers the step
// under the task's own ID for later flush.
func TestContextForTaskInstallsToolCallSink(t *testing.T) {
	tasks := []subagents.Task{{ID: "t1"}}
	toolCalls := newRunToolCallBuffer()
	ctx := contextWithRunExec(context.Background(), "run-1", tasks, nil, toolCalls)

	childCtx := contextForTask(ctx, "t1")

	sink, ok := subagents.ToolCallSinkFrom(childCtx)
	if !ok {
		t.Fatal("ToolCallSinkFrom(childCtx) not ok")
	}
	step := subagents.ToolCallStep{ToolCallID: "call-1", Name: "read_file", Kind: "start"}
	sink(step)

	got := toolCalls.flush("t1")
	if len(got) != 1 {
		t.Fatalf("flush(t1) returned %d steps, want 1", len(got))
	}
	if got[0].ToolCallID != "call-1" || got[0].Name != "read_file" {
		t.Fatalf("flush(t1)[0] = %+v, want ToolCallID=call-1 Name=read_file", got[0])
	}
}

// TestContextForTaskResetsToolCallBufferOnRedispatch is a RED test for
// Finding 1 of the Part B hostile bug audit: runToolCallBuffer is keyed only
// by taskID and flushed only on the terminal result, so a task's discarded
// first-attempt tool-call steps must not survive into a retry's redispatch.
// contextForTask is confirmed (task_context.go, dag.go's retry-requeue path
// through pool.Run -> Pool.executeOne -> ContextForTask) to run fresh on
// every dispatch attempt including retries, so it is the right place to
// clear any leftover buffered steps for that taskID before installing the
// sink for the new attempt. This simulates: attempt 1 buffers steps (never
// flushed, because the failed attempt never reaches recordTaskResult), then
// contextForTask runs again for the retry redispatch and attempt 2 buffers
// its own steps; the final flush must contain ONLY attempt 2's steps.
func TestContextForTaskResetsToolCallBufferOnRedispatch(t *testing.T) {
	tasks := []subagents.Task{{ID: "t1"}}
	toolCalls := newRunToolCallBuffer()
	ctx := contextWithRunExec(context.Background(), "run-1", tasks, nil, toolCalls)

	// Attempt 1: contextForTask installs a sink, the attempt emits a step,
	// then fails without ever calling recordTaskResult/flush (the buffer is
	// never cleared for a retryable failure — see dag.go processResults).
	attempt1Ctx := contextForTask(ctx, "t1")
	sink1, ok := subagents.ToolCallSinkFrom(attempt1Ctx)
	if !ok {
		t.Fatal("ToolCallSinkFrom(attempt1Ctx) not ok")
	}
	sink1(subagents.ToolCallStep{ToolCallID: "attempt1-call", Name: "attempt1_tool", Kind: "start"})

	// Retry redispatch: contextForTask runs again for the same taskID.
	attempt2Ctx := contextForTask(ctx, "t1")
	sink2, ok := subagents.ToolCallSinkFrom(attempt2Ctx)
	if !ok {
		t.Fatal("ToolCallSinkFrom(attempt2Ctx) not ok")
	}
	sink2(subagents.ToolCallStep{ToolCallID: "attempt2-call", Name: "attempt2_tool", Kind: "start"})

	got := toolCalls.flush("t1")
	for _, step := range got {
		if step.ToolCallID == "attempt1-call" {
			t.Fatalf("flush(t1) contains discarded attempt-1 step %+v; retry must not bleed into the final trace", step)
		}
	}
	if len(got) != 1 || got[0].ToolCallID != "attempt2-call" {
		t.Fatalf("flush(t1) = %+v, want only attempt2-call", got)
	}
}

// TestContextForTaskStampsMailboxAccess verifies that contextForTask stamps
// the run's shared mailbox access (plan 54, W2c validation): Drain, Interrupt,
// and Pending hooks are wired to the run mailboxes for the task's ID, and a
// delivered interrupt steer is visible via Pending/Interrupt/Drain.
func TestContextForTaskStampsMailboxAccess(t *testing.T) {
	tasks := []subagents.Task{{ID: "t1"}}
	mailboxes := newRunMailboxes(8)
	ctx := contextWithRunExec(context.Background(), "run-1", tasks, mailboxes, nil)

	childCtx := contextForTask(ctx, "t1")

	access, ok := runtime.MailboxAccessFrom(childCtx)
	if !ok {
		t.Fatal("MailboxAccessFrom(childCtx) not ok")
	}
	if access.Drain == nil || access.Interrupt == nil || access.Pending == nil {
		t.Fatal("expected all mailbox hooks non-nil (Drain/Interrupt/Pending)")
	}
	if access.Pending() {
		t.Fatal("Pending() should be false before any send")
	}

	msg := agentmsg.Message{
		ID:        "m",
		Kind:      agentmsg.KindSteer,
		From:      agentmsg.Party{},
		To:        agentmsg.Party{TaskID: "t1"},
		Body:      "hi",
		Interrupt: true,
	}
	if err := mailboxes.Send("t1", msg); err != nil {
		t.Fatal(err)
	}

	if !access.Pending() {
		t.Fatal("Pending() should be true after send")
	}

	select {
	case <-access.Interrupt():
	default:
		t.Fatal("Interrupt() channel should be readable after interrupt steer")
	}

	got := access.Drain()
	if len(got) != 1 {
		t.Fatalf("Drain() returned %d messages, want 1", len(got))
	}
	if got[0].Body != "hi" {
		t.Fatalf("Drain() message body = %q, want %q", got[0].Body, "hi")
	}
}
