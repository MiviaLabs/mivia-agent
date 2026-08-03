package coordinator

import (
	"context"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agentmsg"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
)

// TestContextForTaskStampsMailboxAccess verifies that contextForTask stamps
// the run's shared mailbox access (plan 54, W2c validation): Drain, Interrupt,
// and Pending hooks are wired to the run mailboxes for the task's ID, and a
// delivered interrupt steer is visible via Pending/Interrupt/Drain.
func TestContextForTaskStampsMailboxAccess(t *testing.T) {
	tasks := []subagents.Task{{ID: "t1"}}
	mailboxes := newRunMailboxes(8)
	ctx := contextWithRunExec(context.Background(), "run-1", tasks, mailboxes)

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
