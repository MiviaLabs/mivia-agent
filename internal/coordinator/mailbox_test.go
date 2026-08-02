package coordinator

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/agentmsg"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
)

func TestMailboxSendDrainTerminal(t *testing.T) {
	mb := newRunMailboxes(2)
	msg, _ := agentmsg.NewMessage("r", agentmsg.KindSteer,
		agentmsg.Party{Role: agentmsg.ParentSentinel}, agentmsg.Party{TaskID: "t"},
		"steer-1", nil, agentmsg.Options{ID: "m1"})
	if err := mb.Send("t", msg); err != nil {
		t.Fatal(err)
	}
	got := mb.Drain("t")
	if len(got) != 1 || got[0].Body != "steer-1" {
		t.Fatalf("got %+v", got)
	}
	mb.MarkTerminal("t")
	if err := mb.Send("t", msg); err == nil {
		t.Fatal("terminal must reject send")
	}
}

func TestMailboxFull(t *testing.T) {
	mb := newRunMailboxes(1)
	msg, _ := agentmsg.NewMessage("r", agentmsg.KindSteer,
		agentmsg.Party{Role: agentmsg.ParentSentinel}, agentmsg.Party{TaskID: "t"},
		"a", nil, agentmsg.Options{ID: "m1"})
	if err := mb.Send("t", msg); err != nil {
		t.Fatal(err)
	}
	msg2 := msg
	msg2.ID = "m2"
	if err := mb.Send("t", msg2); err == nil {
		t.Fatal("full mailbox must reject")
	}
}

func TestSendToTaskSteerDelivered(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	d := runtime.New(runtime.Policy{})
	// Multi-step-like: hang until steers can arrive then finish.
	// Simpler: static handler that doesn't need multi-step; just test Send API.
	_ = d.Register(runtime.Subagent, "worker", staticHandler{out: json.RawMessage(`{"ok":true}`)})
	c := New(repo, subagents.New(d, subagents.Policy{Workers: 1}))
	h, err := c.Spawn(context.Background(), []subagents.Task{{ID: "t1", Name: "worker"}}, "")
	if err != nil {
		t.Fatal(err)
	}
	// Join first so task is terminal - send should persist undelivered.
	if _, err := c.Join(context.Background(), h); err != nil {
		t.Fatal(err)
	}
	msg, _ := agentmsg.NewMessage(h.runID, agentmsg.KindSteer,
		agentmsg.Party{Role: agentmsg.ParentSentinel}, agentmsg.Party{TaskID: "t1"},
		"late steer", nil, agentmsg.Options{ID: "steer-late"})
	// Mark terminal manually if not already
	h.MarkTaskMailboxTerminal("t1")
	delivered, err := c.SendToTask(context.Background(), h, "t1", msg)
	if err != nil {
		t.Fatal(err)
	}
	if delivered {
		t.Fatal("expected undelivered to terminal task")
	}
	// Durable in ledger
	list, err := c.ListRunMessages(context.Background(), h.runID, "t1")
	if err != nil || len(list) == 0 {
		t.Fatalf("list=%v err=%v", list, err)
	}
}

func TestSendToTaskWhileRunning(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	d := runtime.New(runtime.Policy{})
	started := make(chan struct{})
	done := make(chan struct{})
	_ = d.Register(runtime.Subagent, "worker", handlerFunc(func(ctx context.Context, _ runtime.Request) (json.RawMessage, error) {
		close(started)
		select {
		case <-done:
		case <-ctx.Done():
		case <-time.After(2 * time.Second):
		}
		return json.RawMessage(`{"ok":true}`), nil
	}))
	c := New(repo, subagents.New(d, subagents.Policy{Workers: 1}))
	h, err := c.Spawn(context.Background(), []subagents.Task{
		{ID: "t1", Name: "worker", Timeout: 5 * time.Second},
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("not started")
	}
	msg, _ := agentmsg.NewMessage(h.runID, agentmsg.KindSteer,
		agentmsg.Party{Role: agentmsg.ParentSentinel}, agentmsg.Party{TaskID: "t1"},
		"mid steer", nil, agentmsg.Options{ID: "steer-mid"})
	delivered, err := c.SendToTask(context.Background(), h, "t1", msg)
	if err != nil {
		t.Fatal(err)
	}
	if !delivered {
		t.Fatal("expected delivered while running")
	}
	// Drain should see it
	if h.mailboxes == nil {
		t.Fatal("no mailboxes")
	}
	got := h.mailboxes.Drain("t1")
	if len(got) != 1 || got[0].Body != "mid steer" {
		t.Fatalf("drain=%+v", got)
	}
	close(done)
	_, _ = c.Join(context.Background(), h)
}
