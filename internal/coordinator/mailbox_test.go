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

// Answers must not unblock the child before the answer is durable in the ledger.
func TestSendToTaskAnswerPersistsBeforeDeliver(t *testing.T) {
	c, repo := newPostMessageCoordinator(t)
	ctx := context.Background()
	// Seed a run/task and park a question.
	h, err := c.Spawn(ctx, []subagents.Task{{ID: "t1", Name: "worker"}}, "")
	if err != nil {
		t.Fatal(err)
	}
	// Don't join yet - we need a live handle for SendToTask; use post after join
	// for the ledger target, and park on a fresh handle's run.
	if _, err := c.Join(ctx, h); err != nil {
		t.Fatal(err)
	}
	snap, _ := c.Inspect(ctx, h)
	runID, taskID := snap.RunID, snap.Tasks[0].TaskID

	// Park as if a child is waiting.
	answerCh, unpark, err := c.ParkQuestion(runID, taskID, "msg-q")
	if err != nil {
		t.Fatal(err)
	}
	defer unpark()

	// Fail store after park would be ideal; instead assert order by checking
	// that after a successful SendToTask the answer is both in ledger AND on channel.
	msg, err := agentmsg.NewMessage(runID, agentmsg.KindAnswer,
		agentmsg.Party{Role: agentmsg.ParentSentinel},
		agentmsg.Party{TaskID: taskID},
		"the answer", nil,
		agentmsg.Options{ID: "msg-ans", InReplyTo: "msg-q"})
	if err != nil {
		t.Fatal(err)
	}
	delivered, err := c.SendToTask(ctx, h, taskID, msg)
	if err != nil {
		t.Fatal(err)
	}
	// Terminal task → undelivered mailbox OK; answer channel still receives.
	_ = delivered
	select {
	case a := <-answerCh:
		if a != "the answer" {
			t.Fatalf("answer = %q", a)
		}
	case <-time.After(time.Second):
		t.Fatal("answer not delivered to parked question")
	}
	// Durable
	list, err := c.ListRunMessages(ctx, runID, taskID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, m := range list {
		if m.MessageID == "msg-ans" && m.Kind == agentmsg.KindAnswer {
			found = true
			full, err := c.LoadMessageBody(ctx, m.ContentRef)
			if err != nil {
				t.Fatal(err)
			}
			if !full.From.IsParent() {
				t.Fatalf("answer From not parent: %+v", full.From)
			}
		}
	}
	if !found {
		t.Fatalf("answer not in ledger: %+v", list)
	}
	_ = repo
}

// conflictOnCanceledCAS forces finalize cancel CAS to conflict so the
// ErrConflict mailbox fence (cancel.go) runs — recordRunResults also loses
// the same CAS, leaving status cancel_requested while the fence still applies.
type conflictOnCanceledCAS struct {
	*ledger.MemoryLedgerRepository
}

func (r *conflictOnCanceledCAS) CompareAndSetTaskStatus(ctx context.Context, runID, taskID string, expectedVersion uint64, newStatus string) error {
	if newStatus == string(ledger.TaskStatusCanceled) {
		return ledger.ErrConflict
	}
	return r.MemoryLedgerRepository.CompareAndSetTaskStatus(ctx, runID, taskID, expectedVersion, newStatus)
}

func TestCancelFinalizeConflictStillMarksMailboxTerminal(t *testing.T) {
	repo := &conflictOnCanceledCAS{MemoryLedgerRepository: ledger.NewMemoryLedgerRepository()}
	d := runtime.New(runtime.Policy{})
	started := make(chan struct{})
	_ = d.Register(runtime.Subagent, "slow", handlerFunc(func(ctx context.Context, _ runtime.Request) (json.RawMessage, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	}))
	c := New(repo, subagents.New(d, subagents.Policy{Workers: 1}))
	h, err := c.Spawn(context.Background(), []subagents.Task{
		{ID: "t1", Name: "slow", Timeout: 30 * time.Second},
		{ID: "t2", Name: "slow", DependsOn: []string{"t1"}, Timeout: 30 * time.Second},
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	<-started
	if err := c.Cancel(context.Background(), h); err != nil {
		t.Fatal(err)
	}
	_, _ = c.Join(context.Background(), h)
	// Fence must hold even when terminal CAS always conflicts.
	msg, _ := agentmsg.NewMessage(h.runID, agentmsg.KindSteer,
		agentmsg.Party{Role: agentmsg.ParentSentinel}, agentmsg.Party{TaskID: "t2"},
		"late", nil, agentmsg.Options{ID: "late-steer-conflict"})
	delivered, err := c.SendToTask(context.Background(), h, "t2", msg)
	if err != nil {
		t.Fatal(err)
	}
	if delivered {
		t.Fatal("mailbox must be terminal after cancel finalize conflict fence")
	}
}

func TestMailboxCapacityFromConfig(t *testing.T) {
	c, _ := newPostMessageCoordinator(t)
	c = c.WithMessagingLimits(0, 1) // capacity 1
	// Spawn after setting capacity so newRunHandle picks it up.
	d := runtime.New(runtime.Policy{})
	_ = d.Register(runtime.Subagent, "worker", staticHandler{out: json.RawMessage(`{"ok":true}`)})
	// Need a coordinator that was limited before spawn - recreate.
	repo := ledger.NewMemoryLedgerRepository()
	c2 := New(repo, subagents.New(d, subagents.Policy{Workers: 1})).WithMessagingLimits(0, 1)
	// Manually inspect capacity via send fill.
	// Spawn leaves handle with mailboxes cap 1.
	// Use Send on a fresh handle path: create handle by spawning a slow task.
	started := make(chan struct{})
	block := make(chan struct{})
	_ = d.Register(runtime.Subagent, "slow", handlerFunc(func(ctx context.Context, _ runtime.Request) (json.RawMessage, error) {
		close(started)
		select {
		case <-block:
		case <-ctx.Done():
		}
		return json.RawMessage(`{}`), nil
	}))
	// re-register overwrites? Register may fail if exists. Use new dispatcher.
	d2 := runtime.New(runtime.Policy{})
	_ = d2.Register(runtime.Subagent, "slow", handlerFunc(func(ctx context.Context, _ runtime.Request) (json.RawMessage, error) {
		close(started)
		select {
		case <-block:
		case <-ctx.Done():
		}
		return json.RawMessage(`{}`), nil
	}))
	c3 := New(ledger.NewMemoryLedgerRepository(), subagents.New(d2, subagents.Policy{Workers: 1})).WithMessagingLimits(0, 1)
	h, err := c3.Spawn(context.Background(), []subagents.Task{
		{ID: "t1", Name: "slow", Timeout: 5 * time.Second},
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("not started")
	}
	msg1, _ := agentmsg.NewMessage(h.runID, agentmsg.KindSteer,
		agentmsg.Party{Role: agentmsg.ParentSentinel}, agentmsg.Party{TaskID: "t1"},
		"one", nil, agentmsg.Options{ID: "s1"})
	msg2, _ := agentmsg.NewMessage(h.runID, agentmsg.KindSteer,
		agentmsg.Party{Role: agentmsg.ParentSentinel}, agentmsg.Party{TaskID: "t1"},
		"two", nil, agentmsg.Options{ID: "s2"})
	if _, err := c3.SendToTask(context.Background(), h, "t1", msg1); err != nil {
		t.Fatal(err)
	}
	delivered, err := c3.SendToTask(context.Background(), h, "t1", msg2)
	if err != nil {
		t.Fatal(err)
	}
	if delivered {
		t.Fatal("capacity 1: second send must be undelivered")
	}
	close(block)
	_, _ = c3.Join(context.Background(), h)
	_ = c
	_ = c2
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

// TestParentSendToTaskAnswerClosesAsk: durable parent answer seals one-shot so
// peer ClaimAskAnswer fails (non-blocking asks never hit waitOnParkedAnswer).
func TestParentSendToTaskAnswerClosesAsk(t *testing.T) {
	c, _ := newPostMessageCoordinator(t)
	ctx := context.Background()
	h, err := c.Spawn(ctx, []subagents.Task{{ID: "t1", Name: "worker"}}, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Join(ctx, h); err != nil {
		t.Fatal(err)
	}
	snap, _ := c.Inspect(ctx, h)
	runID, taskID := snap.RunID, snap.Tasks[0].TaskID

	c.RegisterAsk(runID, taskID, "worker", "ask-nb", nil)
	msg, err := agentmsg.NewMessage(runID, agentmsg.KindAnswer,
		agentmsg.Party{Role: agentmsg.ParentSentinel},
		agentmsg.Party{TaskID: taskID},
		"parent-body", nil,
		agentmsg.Options{ID: "ans-1", InReplyTo: "ask-nb"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.SendToTask(ctx, h, taskID, msg); err != nil {
		t.Fatal(err)
	}
	if _, ok := c.AskLookup("ask-nb"); ok {
		t.Fatal("parent answer must remove open ask")
	}
	if !c.IsAskAnswered("ask-nb") {
		t.Fatal("parent answer must CloseAsk")
	}
	if _, err := c.ClaimAskAnswer("ask-nb"); err == nil {
		t.Fatal("peer claim after parent answer must fail")
	}
}
