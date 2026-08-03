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

func TestAskRegistryOneAnswer(t *testing.T) {
	c := New(ledger.NewMemoryLedgerRepository(), subagents.New(runtime.New(runtime.Policy{}), subagents.Policy{Workers: 1})).(*coordinator)
	c.RegisterAsk("run", "task-a", "reviewer", "msg-ask-1", nil)
	if n := c.AsksUsedByTask("run", "task-a"); n != 1 {
		t.Fatalf("asks used = %d", n)
	}
	asker, ok := c.AskLookup("msg-ask-1")
	if !ok || asker != "task-a" {
		t.Fatalf("lookup = %q %v", asker, ok)
	}
	if err := c.CompleteAskAnswer("msg-ask-1"); err != nil {
		t.Fatal(err)
	}
	if err := c.CompleteAskAnswer("msg-ask-1"); err == nil {
		t.Fatal("second complete must fail")
	}
	if !c.IsAskAnswered("msg-ask-1") {
		t.Fatal("expected answered")
	}
	if _, ok := c.AskLookup("msg-ask-1"); ok {
		t.Fatal("open lookup after answer")
	}
	// Nil asks registry paths.
	bare := &coordinator{}
	if _, err := bare.ClaimAskAnswer("x"); err == nil {
		t.Fatal("nil asks")
	}
	if err := bare.CompleteAskAnswer("x"); err == nil {
		t.Fatal("nil complete")
	}
	// Complete on open ask.
	c2 := New(ledger.NewMemoryLedgerRepository(), subagents.New(runtime.New(runtime.Policy{}), subagents.Policy{Workers: 1})).(*coordinator)
	c2.RegisterAsk("r", "t", "a", "m-open", nil)
	if err := c2.CompleteAskAnswer("m-open"); err != nil {
		t.Fatal(err)
	}
	if err := c2.CompleteAskAnswer("m-open"); err == nil {
		t.Fatal("second complete")
	}
	// Complete on claimed ask.
	c2.RegisterAsk("r", "t", "a", "m-claim", nil)
	if _, err := c2.ClaimAskAnswer("m-claim"); err != nil {
		t.Fatal(err)
	}
	if err := c2.CompleteAskAnswer("m-claim"); err != nil {
		t.Fatal(err)
	}
	if err := c2.CompleteAskAnswer("missing"); err == nil {
		t.Fatal("unknown complete")
	}
}

func TestAskChainInfoCycleAndDepth(t *testing.T) {
	c := New(ledger.NewMemoryLedgerRepository(), subagents.New(runtime.New(runtime.Policy{}), subagents.Policy{Workers: 1})).(*coordinator)
	c.RegisterAsk("run", "t1", "a", "ask1", nil)
	depth, cycle, anc := c.AskChainInfo("ask1", "b")
	if depth != 1 || cycle || len(anc) != 1 {
		t.Fatalf("depth=%d cycle=%v anc=%v", depth, cycle, anc)
	}
	c.RegisterAsk("run", "t2", "b", "ask2", anc)
	depth, cycle, anc = c.AskChainInfo("ask2", "a")
	if !cycle || depth != 2 {
		t.Fatalf("expected cycle at depth 2, got depth=%d cycle=%v", depth, cycle)
	}
	_ = anc
}

func TestSpawnReferralFromAskRunsHandler(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	d := runtime.New(runtime.Policy{})
	done := make(chan struct{})
	_ = d.Register(runtime.Subagent, "auditor", handlerFunc(func(ctx context.Context, req runtime.Request) (json.RawMessage, error) {
		close(done)
		return json.RawMessage(`{"ok":true}`), nil
	}))
	pool := subagents.New(d, subagents.Policy{Workers: 1})
	c := New(repo, pool)
	// Parent task keeps the run alive.
	_ = d.Register(runtime.Subagent, "parent", handlerFunc(func(ctx context.Context, _ runtime.Request) (json.RawMessage, error) {
		select {
		case <-done:
		case <-time.After(3 * time.Second):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return json.RawMessage(`{"ok":true}`), nil
	}))
	h, err := c.Spawn(context.Background(), []subagents.Task{
		{ID: "p1", Name: "parent", AgentName: "parent", Timeout: 5 * time.Second},
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	ask, err := agentmsg.NewMessage(
		h.RunID(), agentmsg.KindAsk,
		agentmsg.Party{TaskID: "p1", Role: "reviewer"},
		agentmsg.Party{Role: "auditor"},
		"please check", nil, agentmsg.Options{},
	)
	if err != nil {
		t.Fatal(err)
	}
	tid, err := c.SpawnReferralFromAsk(context.Background(), h.RunID(), "auditor", ask)
	if err != nil {
		t.Fatal(err)
	}
	if tid == "" {
		t.Fatal("empty task id")
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("referral handler did not run")
	}
	_, _ = c.Join(context.Background(), h)
}

func TestFindLiveTaskByRole(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	d := runtime.New(runtime.Policy{})
	started := make(chan struct{})
	_ = d.Register(runtime.Subagent, "auditor", handlerFunc(func(ctx context.Context, _ runtime.Request) (json.RawMessage, error) {
		close(started)
		<-ctx.Done()
		return json.RawMessage(`{"ok":true}`), nil
	}))
	c := New(repo, subagents.New(d, subagents.Policy{Workers: 1}))
	h, err := c.Spawn(context.Background(), []subagents.Task{
		{ID: "a1", Name: "auditor", AgentName: "auditor", Timeout: 2 * time.Second},
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("handler did not start")
	}
	id, ok, err := c.FindLiveTaskByRole(context.Background(), h.RunID(), "auditor")
	if err != nil {
		t.Fatal(err)
	}
	if !ok || id != "a1" {
		t.Fatalf("live = %q %v", id, ok)
	}
	_ = c.Cancel(context.Background(), h)
	_, _ = c.Join(context.Background(), h)
}
