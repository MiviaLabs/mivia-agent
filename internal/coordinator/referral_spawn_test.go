package coordinator

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/agentmsg"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
)

func TestSpawnReferralValidation(t *testing.T) {
	c := New(ledger.NewMemoryLedgerRepository(), subagents.New(runtime.New(runtime.Policy{}), subagents.Policy{Workers: 1})).(*coordinator)
	if _, err := c.SpawnReferral(context.Background(), "", subagents.Task{Name: "x"}, ""); err == nil {
		t.Fatal("empty run id")
	}
	if _, err := c.SpawnReferral(context.Background(), "missing", subagents.Task{Name: "x"}, ""); err == nil {
		t.Fatal("inactive run")
	}
	// Active run without agent name.
	d := runtime.New(runtime.Policy{})
	_ = d.Register(runtime.Subagent, "p", handlerFunc(func(context.Context, runtime.Request) (json.RawMessage, error) {
		return json.RawMessage(`{}`), nil
	}))
	c2 := New(ledger.NewMemoryLedgerRepository(), subagents.New(d, subagents.Policy{Workers: 1}))
	h, err := c2.Spawn(context.Background(), []subagents.Task{
		{ID: "p1", Name: "p", AgentName: "p", Timeout: 2 * time.Second},
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c2.SpawnReferral(context.Background(), h.RunID(), subagents.Task{}, ""); err == nil {
		t.Fatal("empty agent name")
	}
	// Name-only fills AgentName.
	done := make(chan struct{})
	_ = d.Register(runtime.Subagent, "ref", handlerFunc(func(context.Context, runtime.Request) (json.RawMessage, error) {
		close(done)
		return json.RawMessage(`{}`), nil
	}))
	tid, err := c2.SpawnReferral(context.Background(), h.RunID(), subagents.Task{Name: "ref"}, "")
	if err != nil || tid == "" {
		t.Fatalf("name-only spawn: %v %q", err, tid)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("ref handler")
	}
	_, _ = c2.Join(context.Background(), h)
}

func TestSpawnReferralAgentNameOnly(t *testing.T) {
	d := runtime.New(runtime.Policy{})
	_ = d.Register(runtime.Subagent, "p", handlerFunc(func(context.Context, runtime.Request) (json.RawMessage, error) {
		return json.RawMessage(`{}`), nil
	}))
	_ = d.Register(runtime.Subagent, "ref", handlerFunc(func(context.Context, runtime.Request) (json.RawMessage, error) {
		return json.RawMessage(`{}`), nil
	}))
	c := New(ledger.NewMemoryLedgerRepository(), subagents.New(d, subagents.Policy{Workers: 1}))
	h, err := c.Spawn(context.Background(), []subagents.Task{
		{ID: "p1", Name: "p", AgentName: "p", Timeout: 2 * time.Second},
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	tid, err := c.SpawnReferral(context.Background(), h.RunID(), subagents.Task{AgentName: "ref"}, "")
	if err != nil || tid == "" {
		t.Fatalf("agent-only: %v %q", err, tid)
	}
	_, _ = c.Join(context.Background(), h)
}

func TestReferralTrackerNilSafe(t *testing.T) {
	var h *RunHandle
	h.referralAdd()
	h.referralDone()
	h.waitReferrals()
	// Non-nil handle without tracker.
	h2 := &RunHandle{}
	h2.referralDone()
	h2.waitReferrals()
	h2.referralAdd()
	h2.referralDone()
	h2.waitReferrals()
}

func TestRunReferralTaskNilBaseCtx(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	d := runtime.New(runtime.Policy{})
	_ = d.Register(runtime.Subagent, "w", handlerFunc(func(context.Context, runtime.Request) (json.RawMessage, error) {
		return json.RawMessage(`{}`), nil
	}))
	c := New(repo, subagents.New(d, subagents.Policy{Workers: 1})).(*coordinator)
	h, err := c.Spawn(context.Background(), []subagents.Task{
		{ID: "p1", Name: "w", AgentName: "w", Timeout: 2 * time.Second},
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	// Direct run with nil baseCtx covers Background fallback.
	named, err := c.createTask(context.Background(), h.RunID(), subagents.Task{
		ID: "ref-nil", Name: "w", AgentName: "w",
	}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	named.task.ID = named.taskID
	h.setAttempt(named.taskID, named.attemptID)
	c.runReferralTask(h, named.task, nil)
	_, _ = c.Join(context.Background(), h)
}

func TestTryRegisterAskQuota(t *testing.T) {
	c := New(ledger.NewMemoryLedgerRepository(), subagents.New(runtime.New(runtime.Policy{}), subagents.Policy{Workers: 1})).(*coordinator)
	if !c.TryRegisterAsk("r", "t", "a", "m1", nil, 1) {
		t.Fatal("first")
	}
	if c.TryRegisterAsk("r", "t", "a", "m2", nil, 1) {
		t.Fatal("quota")
	}
	c.CloseAsk("m1")
	if !c.IsAskAnswered("m1") {
		t.Fatal("closed")
	}
	// Close unknown is no-op.
	c.CloseAsk("nope")
	c.CloseAsk("")
}

func TestAskRegistryNilPaths(t *testing.T) {
	c := &coordinator{} // asks nil
	if c.AsksUsedByTask("r", "t") != 0 {
		t.Fatal()
	}
	if c.ReferralSpawnsUsed("r") != 0 {
		t.Fatal()
	}
	if _, ok := c.AskLookup("x"); ok {
		t.Fatal()
	}
	if c.IsAskAnswered("x") {
		t.Fatal()
	}
	c.DecReferralSpawn("")
	c.DecReferralSpawn("r")
	c.CloseAsk("x")
	// Default maxasks when <=0
	if !c.TryRegisterAsk("r", "t", "a", "m1", nil, 0) {
		t.Fatal("default max")
	}
	// Inc unbounded path
	c.IncReferralSpawn("r")
	if c.ReferralSpawnsUsed("r") != 1 {
		t.Fatal()
	}
	// TryInc default max
	c2 := &coordinator{}
	if !c2.TryIncReferralSpawn("r", 0) {
		t.Fatal()
	}
}

func TestHandleForRunEmptyAndRunIDNil(t *testing.T) {
	c := New(ledger.NewMemoryLedgerRepository(), subagents.New(runtime.New(runtime.Policy{}), subagents.Policy{Workers: 1})).(*coordinator)
	if c.HandleForRun("") != nil {
		t.Fatal()
	}
	var h *RunHandle
	if h.RunID() != "" {
		t.Fatal()
	}
	// FindLive empty role.
	id, ok, err := c.FindLiveTaskByRole(context.Background(), "missing", "")
	if err != nil || ok || id != "" {
		t.Fatalf("%v %v %q", err, ok, id)
	}
	// Missing run: ListTasks may err or return empty.
	_, ok, _ = c.FindLiveTaskByRole(context.Background(), "missing", "role")
	if ok {
		t.Fatal("not found")
	}
}

func TestMailboxSendNilHandle(t *testing.T) {
	c := New(ledger.NewMemoryLedgerRepository(), subagents.New(runtime.New(runtime.Policy{}), subagents.Policy{Workers: 1})).(*coordinator)
	ok, err := c.MailboxSend(nil, "t", agentmsg.Message{})
	if ok || err != nil {
		t.Fatalf("%v %v", ok, err)
	}
	// nil mailboxes
	ok, err = c.MailboxSend(&RunHandle{}, "t", agentmsg.Message{})
	if ok || err != nil {
		t.Fatalf("%v %v", ok, err)
	}
}

func TestSpawnReferralCreateTaskFail(t *testing.T) {
	d := runtime.New(runtime.Policy{})
	_ = d.Register(runtime.Subagent, "p", handlerFunc(func(context.Context, runtime.Request) (json.RawMessage, error) {
		return json.RawMessage(`{}`), nil
	}))
	repo := ledger.NewMemoryLedgerRepository()
	c := New(repo, subagents.New(d, subagents.Policy{Workers: 1}))
	h, err := c.Spawn(context.Background(), []subagents.Task{
		{ID: "p1", Name: "p", AgentName: "p", Timeout: 2 * time.Second},
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	// Duplicate task id → createTask fails.
	if _, err := c.SpawnReferral(context.Background(), h.RunID(), subagents.Task{
		ID: "p1", Name: "p", AgentName: "p",
	}, ""); err == nil {
		t.Fatal("want create fail")
	}
	_, _ = c.Join(context.Background(), h)
}

func TestRunReferralTaskTransitionFail(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	d := runtime.New(runtime.Policy{})
	_ = d.Register(runtime.Subagent, "w", handlerFunc(func(context.Context, runtime.Request) (json.RawMessage, error) {
		return json.RawMessage(`{}`), nil
	}))
	c := New(repo, subagents.New(d, subagents.Policy{Workers: 1})).(*coordinator)
	// Finished parent task so Join is idle; use handle only as mailbox/owner context.
	h, err := c.Spawn(context.Background(), []subagents.Task{
		{ID: "p1", Name: "w", AgentName: "w", Timeout: 2 * time.Second},
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = c.Join(context.Background(), h)
	// Snapshot poolCtx under lock after Join (stable; no concurrent rewrite).
	h.mu.RLock()
	base := h.poolCtx
	h.mu.RUnlock()
	// Missing task → transitionTask fails; bind ask so early CloseAsk runs.
	c.RegisterAsk(h.RunID(), "p1", "w", "ask-early", nil)
	c.bindReferralAsk("no-such-task", "ask-early")
	c.runReferralTask(h, subagents.Task{ID: "no-such-task", Name: "w", AgentName: "w"}, base)
	if !c.IsAskAnswered("ask-early") {
		t.Fatal("early transition fail must CloseAsk")
	}
	// Terminal task → transition fails too.
	named, err := c.createTask(context.Background(), h.RunID(), subagents.Task{
		ID: "dead", Name: "w", AgentName: "w",
	}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	named.task.ID = named.taskID
	snap, _ := repo.GetTask(context.Background(), h.RunID(), named.taskID)
	_ = repo.CompareAndSetTaskStatus(context.Background(), h.RunID(), named.taskID, snap.Version, string(ledger.TaskStatusCompleted))
	c.runReferralTask(h, named.task, base)
}

func TestPoolContextNilSafe(t *testing.T) {
	var h *RunHandle
	if h.poolContext() == nil {
		t.Fatal()
	}
	h2 := &RunHandle{}
	if h2.poolContext() == nil {
		t.Fatal()
	}
}

func TestBindReferralAskEmptyNoop(t *testing.T) {
	c := New(ledger.NewMemoryLedgerRepository(), subagents.New(runtime.New(runtime.Policy{}), subagents.Policy{Workers: 1})).(*coordinator)
	c.bindReferralAsk("", "a")
	c.bindReferralAsk("t", "")
	c.bindReferralAsk("t1", "ask1")
	if got := c.takeReferralAsk("t1"); got != "ask1" {
		t.Fatalf("got %q", got)
	}
	if got := c.takeReferralAsk("t1"); got != "" {
		t.Fatal("second take")
	}
	// Nil map branch after clearing.
	c.asks.mu.Lock()
	c.asks.referralTaskAsk = nil
	c.asks.mu.Unlock()
	if got := c.takeReferralAsk("x"); got != "" {
		t.Fatal(got)
	}
	// Nil asks / empty task id
	bare := &coordinator{}
	if bare.takeReferralAsk("x") != "" {
		t.Fatal()
	}
	if c.takeReferralAsk("") != "" {
		t.Fatal()
	}
}

func TestSpawnReferralFromAskMeta(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	d := runtime.New(runtime.Policy{})
	gotDigest := make(chan string, 1)
	_ = d.Register(runtime.Subagent, "aud", handlerFunc(func(_ context.Context, req runtime.Request) (json.RawMessage, error) {
		gotDigest <- req.AgentDigest
		return json.RawMessage(`{}`), nil
	}))
	_ = d.Register(runtime.Subagent, "p", handlerFunc(func(context.Context, runtime.Request) (json.RawMessage, error) {
		return json.RawMessage(`{}`), nil
	}))
	c := New(repo, subagents.New(d, subagents.Policy{Workers: 2}))
	h, err := c.Spawn(context.Background(), []subagents.Task{
		{ID: "p1", Name: "p", AgentName: "p", Timeout: 3 * time.Second},
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	ask, _ := agentmsg.NewMessage(h.RunID(), agentmsg.KindAsk,
		agentmsg.Party{TaskID: "p1", Role: "rev"}, agentmsg.Party{Role: "aud"},
		"", nil, agentmsg.Options{}) // empty body path
	if _, err := c.SpawnReferralFromAsk(context.Background(), h.RunID(), "aud", ask, ReferralSpawnMeta{
		AgentDigest: "sha256:test", ProviderName: "p", Model: "m",
	}); err != nil {
		t.Fatal(err)
	}
	_, _ = c.Join(context.Background(), h)
	// The handler runs on a worker goroutine; the channel is the
	// synchronization edge. Join guarantees task completion, not
	// visibility of a plain variable written by the handler.
	select {
	case digest := <-gotDigest:
		if digest != "sha256:test" {
			t.Fatalf("digest=%q", digest)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("referral handler did not run")
	}
}

func TestReferralFailClosesAsk(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	d := runtime.New(runtime.Policy{})
	// Handler that fails → non-completed status → CloseAsk.
	_ = d.Register(runtime.Subagent, "aud", handlerFunc(func(context.Context, runtime.Request) (json.RawMessage, error) {
		return nil, fmt.Errorf("boom")
	}))
	_ = d.Register(runtime.Subagent, "p", handlerFunc(func(context.Context, runtime.Request) (json.RawMessage, error) {
		return json.RawMessage(`{}`), nil
	}))
	c := New(repo, subagents.New(d, subagents.Policy{Workers: 2}))
	h, err := c.Spawn(context.Background(), []subagents.Task{
		{ID: "p1", Name: "p", AgentName: "p", Timeout: 3 * time.Second},
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	ask, _ := agentmsg.NewMessage(h.RunID(), agentmsg.KindAsk,
		agentmsg.Party{TaskID: "p1", Role: "rev"}, agentmsg.Party{Role: "aud"},
		"body", nil, agentmsg.Options{})
	c.RegisterAsk(h.RunID(), "p1", "rev", ask.ID, nil)
	if _, err := c.SpawnReferralFromAsk(context.Background(), h.RunID(), "aud", ask); err != nil {
		t.Fatal(err)
	}
	_, _ = c.Join(context.Background(), h)
	if !c.IsAskAnswered(ask.ID) {
		t.Fatal("failed referral must close ask")
	}
}

// TestSpawnReferralFromAskBoundsTimeout guards against a referral task
// running with an unbounded context. Task.Timeout is never set on the
// referral construction path (SpawnReferralFromAsk), and the pool here is
// configured with Timeout: 0 (mirrors the real-world zero-config default -
// config.DefaultTimeout is 0 unless explicitly set), so before the fix the
// handler's context has no deadline at all: a stalled referral can block
// forever and hold the whole run's Join open, invisible in the UI because
// referral tasks are not part of the dispatched batch's panel rows.
func TestSpawnReferralFromAskBoundsTimeout(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	d := runtime.New(runtime.Policy{})
	type deadlineObservation struct {
		ok       bool
		deadline time.Time
	}
	deadlineCh := make(chan deadlineObservation, 1)
	_ = d.Register(runtime.Subagent, "aud-deadline", handlerFunc(func(ctx context.Context, _ runtime.Request) (json.RawMessage, error) {
		dl, ok := ctx.Deadline()
		deadlineCh <- deadlineObservation{ok: ok, deadline: dl}
		return json.RawMessage(`{}`), nil
	}))
	_ = d.Register(runtime.Subagent, "p-deadline", handlerFunc(func(context.Context, runtime.Request) (json.RawMessage, error) {
		return json.RawMessage(`{}`), nil
	}))
	// subagents.Policy{} leaves Timeout at its zero value, matching a pool
	// with no configured default (config.DefaultTimeout == 0).
	c := New(repo, subagents.New(d, subagents.Policy{Workers: 2}))
	h, err := c.Spawn(context.Background(), []subagents.Task{
		{ID: "p1", Name: "p-deadline", AgentName: "p-deadline", Timeout: 3 * time.Second},
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	ask, _ := agentmsg.NewMessage(h.RunID(), agentmsg.KindAsk,
		agentmsg.Party{TaskID: "p1", Role: "rev"}, agentmsg.Party{Role: "aud-deadline"},
		"body", nil, agentmsg.Options{})
	if _, err := c.SpawnReferralFromAsk(context.Background(), h.RunID(), "aud-deadline", ask); err != nil {
		t.Fatal(err)
	}
	_, _ = c.Join(context.Background(), h)
	select {
	case got := <-deadlineCh:
		if !got.ok {
			t.Fatal("referral task context has no deadline: an unbounded referral can block forever and hold the run's Join open")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("referral handler did not run")
	}
}

func TestUnclaimWhenAlreadyOpen(t *testing.T) {
	c := New(ledger.NewMemoryLedgerRepository(), subagents.New(runtime.New(runtime.Policy{}), subagents.Policy{Workers: 1})).(*coordinator)
	c.RegisterAsk("r", "t", "a", "m", nil)
	// Force answered+open inconsistency path: claim then re-open manually via Unclaim when open already
	// First claim then unclaim puts open again; second unclaim after Register while open hits open branch.
	_, _ = c.ClaimAskAnswer("m")
	c.UnclaimAskAnswer("m", "t")
	// Now open again — unclaim no-op because not answered
	c.UnclaimAskAnswer("m", "t")
}

func TestSpawnReferralFromAskBrief(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	d := runtime.New(runtime.Policy{})
	var (
		mu       sync.Mutex
		gotInput json.RawMessage
	)
	_ = d.Register(runtime.Subagent, "aud-brief", handlerFunc(func(_ context.Context, req runtime.Request) (json.RawMessage, error) {
		mu.Lock()
		gotInput = append(json.RawMessage(nil), req.Input...)
		mu.Unlock()
		return json.RawMessage(`{}`), nil
	}))
	_ = d.Register(runtime.Subagent, "p-brief", handlerFunc(func(context.Context, runtime.Request) (json.RawMessage, error) {
		return json.RawMessage(`{}`), nil
	}))
	c := New(repo, subagents.New(d, subagents.Policy{Workers: 2}))
	h, err := c.Spawn(context.Background(), []subagents.Task{
		{ID: "p1", Name: "p-brief", AgentName: "p-brief", Timeout: 3 * time.Second},
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	ask, _ := agentmsg.NewMessage(h.RunID(), agentmsg.KindAsk,
		agentmsg.Party{TaskID: "p1", Role: "rev"}, agentmsg.Party{Role: "aud-brief"},
		"body", nil, agentmsg.Options{})
	if _, err := c.SpawnReferralFromAsk(context.Background(), h.RunID(), "aud-brief", ask); err != nil {
		t.Fatal(err)
	}
	_, _ = c.Join(context.Background(), h)
	mu.Lock()
	in := append(json.RawMessage(nil), gotInput...)
	mu.Unlock()
	if !json.Valid(in) || len(in) == 0 {
		t.Fatalf("brief=%s", in)
	}
	// Production handlers require a JSON string prompt containing ask_id.
	var prompt string
	if err := json.Unmarshal(in, &prompt); err != nil {
		t.Fatalf("input must be JSON string: %v raw=%s", err, in)
	}
	if !strings.Contains(prompt, "ask_id: "+ask.ID) {
		t.Fatalf("prompt=%q", prompt)
	}
	if !strings.Contains(prompt, "body") {
		t.Fatalf("prompt missing body: %q", prompt)
	}
}
