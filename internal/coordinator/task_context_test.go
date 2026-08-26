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
