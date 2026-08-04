package coordinator

import (
	"context"
	"encoding/json"
	"fmt"
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

func TestFindLiveTaskByRoleQueuedAndRetryPending(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	d := runtime.New(runtime.Policy{})
	c := New(repo, subagents.New(d, subagents.Policy{Workers: 1})).(*coordinator)
	// Active run (parent keeps handle live).
	_ = d.Register(runtime.Subagent, "p", handlerFunc(func(ctx context.Context, _ runtime.Request) (json.RawMessage, error) {
		<-ctx.Done()
		return json.RawMessage(`{}`), nil
	}))
	h, err := c.Spawn(context.Background(), []subagents.Task{
		{ID: "p1", Name: "p", AgentName: "p", Timeout: 3 * time.Second},
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	// Seed queued auditor (createTask default status) — must count as live.
	if err := repo.CreateTask(context.Background(), ledger.TaskSnapshot{
		RunID: h.RunID(), TaskID: "aud-q", Status: string(ledger.TaskStatusQueued),
		AgentName: "auditor", Version: 1,
	}); err != nil {
		t.Fatal(err)
	}
	id, ok, err := c.FindLiveTaskByRole(context.Background(), h.RunID(), "auditor")
	if err != nil || !ok || id != "aud-q" {
		t.Fatalf("queued live = %q %v %v", id, ok, err)
	}
	// Terminal must not match (valid transition: queued→running→completed).
	snap, _ := repo.GetTask(context.Background(), h.RunID(), "aud-q")
	if err := repo.CompareAndSetTaskStatus(context.Background(), h.RunID(), "aud-q", snap.Version, string(ledger.TaskStatusRunning)); err != nil {
		t.Fatal(err)
	}
	snap, _ = repo.GetTask(context.Background(), h.RunID(), "aud-q")
	if err := repo.CompareAndSetTaskStatus(context.Background(), h.RunID(), "aud-q", snap.Version, string(ledger.TaskStatusCompleted)); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := c.FindLiveTaskByRole(context.Background(), h.RunID(), "auditor"); err != nil || ok {
		t.Fatalf("completed must not be live ok=%v err=%v", ok, err)
	}
	// retry_pending is non-terminal.
	if err := repo.CreateTask(context.Background(), ledger.TaskSnapshot{
		RunID: h.RunID(), TaskID: "aud-r", Status: string(ledger.TaskStatusRetryPending),
		AgentName: "auditor", Version: 1,
	}); err != nil {
		t.Fatal(err)
	}
	id, ok, err = c.FindLiveTaskByRole(context.Background(), h.RunID(), "auditor")
	if err != nil || !ok || id != "aud-r" {
		t.Fatalf("retry_pending live = %q %v %v", id, ok, err)
	}
	_ = c.Cancel(context.Background(), h)
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

// === RED-phase tests: ask slots must be released when an ask terminal-ends ===

// TestAskSlotReleasedOnAnswer: answering every ask must release its per-task
// quota slot, so a task can post a fresh ask up to maxAsks again. RED on HEAD
// (CompleteAskAnswer does not decrement asksByTask → the 5th register is
// refused).
func TestAskSlotReleasedOnAnswer(t *testing.T) {
	c := New(ledger.NewMemoryLedgerRepository(), subagents.New(runtime.New(runtime.Policy{}), subagents.Policy{Workers: 1})).(*coordinator)
	const maxAsks = 4
	for i := 0; i < maxAsks; i++ {
		if !c.TryRegisterAsk("run", "t1", "worker", fmt.Sprintf("ask-%d", i), nil, maxAsks) {
			t.Fatalf("register ask-%d failed", i)
		}
	}
	if c.TryRegisterAsk("run", "t1", "worker", "ask-over", nil, maxAsks) {
		t.Fatal("quota must be exhausted at 4 asks")
	}
	// Answer every ask: each completion must release its slot.
	for i := 0; i < maxAsks; i++ {
		if err := c.CompleteAskAnswer(fmt.Sprintf("ask-%d", i)); err != nil {
			t.Fatalf("complete ask-%d: %v", i, err)
		}
	}
	if n := c.AsksUsedByTask("run", "t1"); n != 0 {
		t.Fatalf("asks used after completing all asks = %d, want 0", n)
	}
	if !c.TryRegisterAsk("run", "t1", "worker", "ask-4", nil, maxAsks) {
		t.Fatal("slot must be released after completing all asks")
	}
}

// TestAskSlotReleasedOnSeal: sealing every ask (SealAskAnswer for open asks,
// SealOpenAskAnswer for a never-claimed ask) must release its per-task quota
// slot. RED on HEAD (no decrement → the 5th register is refused).
func TestAskSlotReleasedOnSeal(t *testing.T) {
	c := New(ledger.NewMemoryLedgerRepository(), subagents.New(runtime.New(runtime.Policy{}), subagents.Policy{Workers: 1})).(*coordinator)
	const maxAsks = 4
	for i := 0; i < 3; i++ {
		if !c.TryRegisterAsk("run", "t1", "worker", fmt.Sprintf("ask-%d", i), nil, maxAsks) {
			t.Fatalf("register ask-%d failed", i)
		}
	}
	if !c.TryRegisterAsk("run", "t1", "worker", "ask-3", nil, maxAsks) {
		t.Fatal("register ask-3 failed")
	}
	if c.TryRegisterAsk("run", "t1", "worker", "ask-over", nil, maxAsks) {
		t.Fatal("quota must be exhausted at 4 asks")
	}
	for i := 0; i < 3; i++ {
		if !c.SealAskAnswer(fmt.Sprintf("ask-%d", i)) {
			t.Fatalf("seal ask-%d failed", i)
		}
	}
	// ask-3 was never claimed: the open-only seal variant retires it.
	if !c.SealOpenAskAnswer("ask-3") {
		t.Fatal("seal-open ask-3 failed")
	}
	if n := c.AsksUsedByTask("run", "t1"); n != 0 {
		t.Fatalf("asks used after sealing all asks = %d, want 0", n)
	}
	if !c.TryRegisterAsk("run", "t1", "worker", "ask-4", nil, maxAsks) {
		t.Fatal("slot must be released after sealing all asks")
	}
}

// TestUnclaimDoesNotReleaseSlot: reopening an ask via UnclaimAskAnswer is not a
// terminal end — it must NOT release the quota slot (a claimed ask is still
// occupying its budget).
func TestUnclaimDoesNotReleaseSlot(t *testing.T) {
	c := New(ledger.NewMemoryLedgerRepository(), subagents.New(runtime.New(runtime.Policy{}), subagents.Policy{Workers: 1})).(*coordinator)
	if !c.TryRegisterAsk("run", "t1", "worker", "ask-a", nil, 1) {
		t.Fatal("register ask-a failed")
	}
	if _, err := c.ClaimAskAnswer("ask-a"); err != nil {
		t.Fatal(err)
	}
	c.UnclaimAskAnswer("ask-a", "t1")
	if n := c.AsksUsedByTask("run", "t1"); n != 1 {
		t.Fatalf("asks used after unclaim = %d, want 1 (claim+unclaim must not release)", n)
	}
	if c.TryRegisterAsk("run", "t1", "worker", "ask-b", nil, 1) {
		t.Fatal("unclaim must not release the slot")
	}
}

// TestResetTaskAsksPurgesStaleOwners (regression pin): a slot release must be
// attributed to the attempt that registered the ask. Completing an attempt-1
// ask after resetTaskAsks must NOT decrement the attempt-2 counter — the
// attempt-1 owner was purged at the retry boundary. Passes trivially on HEAD
// (no release mechanism at all); pins the purge behavior of the new mechanism.
func TestResetTaskAsksPurgesStaleOwners(t *testing.T) {
	c := New(ledger.NewMemoryLedgerRepository(), subagents.New(runtime.New(runtime.Policy{}), subagents.Policy{Workers: 1})).(*coordinator)
	const maxAsks = 4
	// Attempt 1: register one ask.
	if !c.TryRegisterAsk("run", "t1", "worker", "ask-0", nil, maxAsks) {
		t.Fatal("attempt-1 register ask-0 failed")
	}
	// Retry boundary: the per-attempt counter resets.
	c.resetTaskAsks("run", "t1")
	// Attempt 2: fill the fresh budget.
	for i := 1; i <= maxAsks; i++ {
		if !c.TryRegisterAsk("run", "t1", "worker", fmt.Sprintf("ask-%d", i), nil, maxAsks) {
			t.Fatalf("attempt-2 register ask-%d failed", i)
		}
	}
	// Completing the attempt-1 ask must not release an attempt-2 slot: its
	// owner was purged at resetTaskAsks.
	if err := c.CompleteAskAnswer("ask-0"); err != nil {
		t.Fatal(err)
	}
	if n := c.AsksUsedByTask("run", "t1"); n != maxAsks {
		t.Fatalf("asks used = %d, want %d (stale attempt-1 owner must not decrement attempt-2)", n, maxAsks)
	}
	if c.TryRegisterAsk("run", "t1", "worker", "ask-over", nil, maxAsks) {
		t.Fatal("attempt-2 quota must remain exhausted")
	}
}
