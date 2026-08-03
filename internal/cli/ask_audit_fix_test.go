package cli

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/agentmsg"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/coordinator"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
)

// TestAskTimeoutClosesAsk ensures late answers are refused after no_answer.
func TestAskTimeoutClosesAsk(t *testing.T) {
	cfg := config.DefaultSubagentConfig
	cfg.Messaging.Routing.Allow = []string{"reviewer->auditor"}
	d, c, repo := askTestEnv(t, cfg, 2)
	registerAskHandler(d, "auditor", func(ctx context.Context, _ runtime.Request) (json.RawMessage, error) {
		select {
		case <-time.After(3 * time.Second):
		case <-ctx.Done():
		}
		return json.RawMessage(`{"ok":true}`), nil
	})
	registerAskHandler(d, "reviewer", func(ctx context.Context, _ runtime.Request) (json.RawMessage, error) {
		tool := &postMessageTool{dispatcher: d, cfg: cfg, repo: repo}
		out, err := tool.Execute(ctx, json.RawMessage(
			`{"kind":"ask","to_role":"auditor","body":"ping","wait_seconds":1}`,
		))
		if err != nil {
			return nil, err
		}
		return json.RawMessage(out), nil
	})
	result := spawnJoin(t, c, []subagents.Task{
		{ID: "rev-1", Name: "reviewer", AgentName: "reviewer", Timeout: 8 * time.Second},
		{ID: "aud-1", Name: "auditor", AgentName: "auditor", Timeout: 8 * time.Second},
	})
	var res map[string]any
	for _, r := range result.Results {
		if r.TaskID == "rev-1" {
			_ = json.Unmarshal(r.Output, &res)
		}
	}
	if res["status"] != "no_answer" {
		t.Fatalf("want no_answer, got %+v", res)
	}
	mid, _ := res["message_id"].(string)
	if mid == "" {
		t.Fatal("missing message_id")
	}
	if !c.IsAskAnswered(mid) {
		t.Fatal("timeout must close ask")
	}
	ctx := runtime.ContextWithTaskIdentity(context.Background(), runtime.TaskIdentity{
		RunID: result.Snapshot.RunID, TaskID: "aud-1", Agent: "auditor",
	})
	tool := &postMessageTool{dispatcher: d, cfg: cfg, repo: repo}
	if _, err := tool.Execute(ctx, json.RawMessage(
		`{"kind":"answer","body":"late","in_reply_to":"`+mid+`"}`,
	)); err == nil {
		t.Fatal("late answer must fail")
	}
}

// TestAskMailboxFullDeclines checks undelivered live asks are declined.
func TestAskMailboxFullDeclines(t *testing.T) {
	cfg := config.DefaultSubagentConfig
	cfg.Messaging.MailboxCapacity = 1
	hold := make(chan struct{})
	d := runtime.New(runtime.Policy{})
	repo := ledger.NewMemoryLedgerRepository()
	_ = d.Register(runtime.Subagent, "peer", handlerFunc(func(ctx context.Context, _ runtime.Request) (json.RawMessage, error) {
		select {
		case <-hold:
		case <-ctx.Done():
		}
		return json.RawMessage(`{"ok":true}`), nil
	}))
	_ = d.Register(runtime.Subagent, "asker", handlerFunc(func(ctx context.Context, _ runtime.Request) (json.RawMessage, error) {
		coord := initCoordinator(d, cfg, repo)
		id, _ := runtime.TaskIdentityFrom(ctx)
		deadline := time.After(2 * time.Second)
		for {
			if _, ok, _ := coord.FindLiveTaskByRole(ctx, id.RunID, "peer"); ok {
				break
			}
			select {
			case <-deadline:
				return nil, context.DeadlineExceeded
			case <-time.After(5 * time.Millisecond):
			}
		}
		h := coord.HandleForRun(id.RunID)
		steer, _ := agentmsg.NewMessage(id.RunID, agentmsg.KindSteer,
			agentmsg.Party{Role: "parent"}, agentmsg.Party{TaskID: "peer-1"},
			"pad", nil, agentmsg.Options{})
		if _, err := coord.SendToTask(ctx, h, "peer-1", steer); err != nil {
			return nil, err
		}
		tool := &postMessageTool{dispatcher: d, cfg: cfg, repo: repo}
		out, err := tool.Execute(ctx, json.RawMessage(`{"kind":"ask","to_role":"peer","body":"q"}`))
		close(hold) // release peer regardless
		if err != nil {
			return nil, err
		}
		return json.RawMessage(out), nil
	}))
	c := coordinator.New(repo, subagents.New(d, subagents.Policy{Workers: 2})).WithMessagingLimits(2048, 1)
	coordinators.Store(d, c)
	coordinatorRepos.Store(d, repo)
	t.Cleanup(func() {
		coordinators.Delete(d)
		coordinatorRepos.Delete(d)
	})
	h, err := c.Spawn(context.Background(), []subagents.Task{
		{ID: "peer-1", Name: "peer", AgentName: "peer", Timeout: 5 * time.Second},
		{ID: "ask-1", Name: "asker", AgentName: "asker", Timeout: 5 * time.Second},
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	res, err := c.Join(context.Background(), h)
	if err != nil {
		t.Fatal(err)
	}
	var askOut map[string]any
	for _, r := range res.Results {
		if r.TaskID == "ask-1" {
			_ = json.Unmarshal(r.Output, &askOut)
		}
	}
	if askOut["status"] != "declined" {
		t.Fatalf("want declined on full mailbox, got %+v", askOut)
	}
}

// TestClaimAskAnswerOneShot ensures concurrent claim leaves one winner only.
func TestClaimAskAnswerOneShot(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	d := runtime.New(runtime.Policy{})
	c := coordinator.New(repo, subagents.New(d, subagents.Policy{Workers: 1}))
	c.RegisterAsk("r", "t-ask", "reviewer", "msg-1", nil)
	if _, err := c.ClaimAskAnswer("msg-1"); err != nil {
		t.Fatalf("first claim: %v", err)
	}
	if _, err := c.ClaimAskAnswer("msg-1"); err == nil {
		t.Fatal("second claim must fail")
	}
}

// TestHandlePeerAnswerOversizedBodyDoesNotRetireAsk: validation fails before claim.
func TestHandlePeerAnswerOversizedBodyDoesNotRetireAsk(t *testing.T) {
	cfg := config.DefaultSubagentConfig
	tool, c, _, runID, taskID, ctx := setupPostMessageEnv(t, cfg)
	tool.cfg.Messaging.MaxBodyBytes = 4
	c.RegisterAsk(runID, taskID, "worker", "ask-big", nil)
	id := runtime.TaskIdentity{RunID: runID, TaskID: taskID, Agent: "worker"}
	if _, err := tool.handlePeerAnswer(ctx, c, id, "toolongbody", "ask-big"); err == nil {
		t.Fatal("oversized")
	}
	if _, ok := c.AskLookup("ask-big"); !ok {
		t.Fatal("ask must remain open after validation failure")
	}
	// Valid second attempt still works after restoring budget.
	tool.cfg.Messaging.MaxBodyBytes = 2048
	if _, err := tool.handlePeerAnswer(ctx, c, id, "ok", "ask-big"); err != nil {
		t.Fatal(err)
	}
}

// TestHandlePeerAnswerQuotaFailureKeepsAskOpen: unclaim after quota burn failure.
func TestHandlePeerAnswerQuotaFailureKeepsAskOpen(t *testing.T) {
	cfg := config.DefaultSubagentConfig
	cfg.Messaging.MaxMessagesPerTask = 1
	tool, c, _, runID, taskID, ctx := setupPostMessageEnv(t, cfg)
	id := runtime.TaskIdentity{RunID: runID, TaskID: taskID, Agent: "worker"}
	if _, err := tool.Execute(ctx, json.RawMessage(`{"kind":"finding","body":"burn"}`)); err != nil {
		t.Fatal(err)
	}
	c.RegisterAsk(runID, taskID, "worker", "ask-q", nil)
	if _, err := tool.handlePeerAnswer(ctx, c, id, "a", "ask-q"); err == nil {
		t.Fatal("quota")
	}
	if _, ok := c.AskLookup("ask-q"); !ok {
		t.Fatal("ask must reopen after quota failure")
	}
}

// TestUnclaimDoesNotReopenAfterTimeoutClose: claim + concurrent CloseAsk + unclaim.
func TestUnclaimDoesNotReopenAfterTimeoutClose(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	d := runtime.New(runtime.Policy{})
	c := coordinator.New(repo, subagents.New(d, subagents.Policy{Workers: 1}))
	c.RegisterAsk("r", "t", "a", "ask-to", nil)
	if _, err := c.ClaimAskAnswer("ask-to"); err != nil {
		t.Fatal(err)
	}
	// Waiter timeout wins: permanent close while claim in flight.
	c.CloseAsk("ask-to")
	// Answerer post fails and unclaims — must not reopen.
	c.UnclaimAskAnswer("ask-to", "t")
	if _, ok := c.AskLookup("ask-to"); ok {
		t.Fatal("timeout CloseAsk must win over Unclaim")
	}
}

// TestParkedAnswerClosesAsk: successful wait retires the ask (parent answer path).
func TestParkedAnswerClosesAsk(t *testing.T) {
	cfg := config.DefaultSubagentConfig
	tool, c, _, runID, taskID, ctx := setupPostMessageEnv(t, cfg)
	c.RegisterAsk(runID, taskID, "worker", "ask-park", nil)
	ch, unpark, err := c.ParkQuestion(runID, taskID, "ask-park")
	if err != nil {
		t.Fatal(err)
	}
	defer unpark()
	parked := true
	go func() {
		// Deliver like parent send_to_task (no claim).
		_ = c.DeliverAnswer(runID, taskID, "ask-park", "parent-yes")
	}()
	msg, _ := agentmsg.NewMessage(runID, agentmsg.KindAsk,
		agentmsg.Party{TaskID: taskID}, agentmsg.Party{Role: "p"},
		"q", nil, agentmsg.Options{ID: "ask-park"})
	// Force message id for wait.
	msg.ID = "ask-park"
	out, err := tool.waitOnParkedAnswer(ctx, c, runtime.TaskIdentity{RunID: runID, TaskID: taskID}, msg, 2, ch, &parked, unpark)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "answered") {
		t.Fatalf("out=%s", out)
	}
	if _, ok := c.AskLookup("ask-park"); ok {
		t.Fatal("successful park answer must CloseAsk")
	}
	if !c.IsAskAnswered("ask-park") {
		t.Fatal("must report closed")
	}
}

// TestHandlePeerAnswerDeliveredFlag: successful answer reports delivered.
func TestHandlePeerAnswerDeliveredFlag(t *testing.T) {
	cfg := config.DefaultSubagentConfig
	tool, c, _, runID, taskID, ctx := setupPostMessageEnv(t, cfg)
	c.RegisterAsk(runID, taskID, "worker", "ask-d", nil)
	// Park so DeliverAnswer succeeds.
	ch, unpark, err := c.ParkQuestion(runID, taskID, "ask-d")
	if err != nil {
		t.Fatal(err)
	}
	defer unpark()
	id := runtime.TaskIdentity{RunID: runID, TaskID: taskID, Agent: "worker"}
	out, err := tool.handlePeerAnswer(ctx, c, id, "yes", "ask-d")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"delivered":true`) {
		t.Fatalf("out=%s", out)
	}
	select {
	case a := <-ch:
		if a != "yes" {
			t.Fatalf("answer=%q", a)
		}
	default:
		t.Fatal("expected parked answer")
	}
}

func TestUnclaimAskAnswerEdges(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	d := runtime.New(runtime.Policy{})
	c := coordinator.New(repo, subagents.New(d, subagents.Policy{Workers: 1}))
	c.UnclaimAskAnswer("", "t")
	c.UnclaimAskAnswer("x", "")
	c.UnclaimAskAnswer("x", "t") // never claimed
	c.RegisterAsk("r", "t", "a", "m1", nil)
	c.UnclaimAskAnswer("m1", "t") // open, not answered — no-op
	if _, err := c.ClaimAskAnswer("m1"); err != nil {
		t.Fatal(err)
	}
	// Unclaim after claim (no CloseAsk) reopens.
	c.UnclaimAskAnswer("m1", "t")
	if _, ok := c.AskLookup("m1"); !ok {
		t.Fatal("reopened after unclaim")
	}
	// Timeout-style CloseAsk then Unclaim must NOT reopen.
	if _, err := c.ClaimAskAnswer("m1"); err != nil {
		t.Fatal(err)
	}
	c.CloseAsk("m1")
	c.UnclaimAskAnswer("m1", "t")
	if _, ok := c.AskLookup("m1"); ok {
		t.Fatal("must not reopen after CloseAsk")
	}
	if !c.IsAskAnswered("m1") {
		t.Fatal("closed asks report answered")
	}
	// Claim on unknown / already closed.
	if _, err := c.ClaimAskAnswer("missing"); err == nil {
		t.Fatal("unknown")
	}
	c.RegisterAsk("r", "t", "a", "m2", nil)
	_ = c.CompleteAskAnswer("m2")
	if _, err := c.ClaimAskAnswer("m2"); err == nil {
		t.Fatal("already answered")
	}
	// Peer answer when already answered.
	cfg := config.DefaultSubagentConfig
	tool, c2, _, runID, taskID, ctx := setupPostMessageEnv(t, cfg)
	c2.RegisterAsk(runID, taskID, "worker", "done", nil)
	_ = c2.CompleteAskAnswer("done")
	id := runtime.TaskIdentity{RunID: runID, TaskID: taskID, Agent: "worker"}
	if _, err := tool.handlePeerAnswer(ctx, c2, id, "x", "done"); err == nil {
		t.Fatal("already answered")
	}
	// Concurrent answers: second claim fails after both peeked open.
	c2.RegisterAsk(runID, taskID, "worker", "race", nil)
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := tool.handlePeerAnswer(ctx, c2, id, "a", "race")
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	var fail, ok int
	for e := range errs {
		if e != nil {
			fail++
		} else {
			ok++
		}
	}
	if ok < 1 || fail < 1 {
		t.Fatalf("want one success one claim fail, ok=%d fail=%d", ok, fail)
	}
}

// TestTryIncReferralSpawnCap enforces atomic cap.
func TestTryIncReferralSpawnCap(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	d := runtime.New(runtime.Policy{})
	c := coordinator.New(repo, subagents.New(d, subagents.Policy{Workers: 1}))
	if !c.TryIncReferralSpawn("run", 1) {
		t.Fatal("first inc")
	}
	if c.TryIncReferralSpawn("run", 1) {
		t.Fatal("second must fail at cap 1")
	}
	c.DecReferralSpawn("run")
	if !c.TryIncReferralSpawn("run", 1) {
		t.Fatal("after dec")
	}
}

// TestLiveAskDrainIncludesMessageID pins drain → inject correlation.
func TestLiveAskDrainIncludesMessageID(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	d := runtime.New(runtime.Policy{})
	_ = d.Register(runtime.Subagent, "peer", handlerFunc(func(ctx context.Context, _ runtime.Request) (json.RawMessage, error) {
		drain, ok := runtime.MailboxDrainFrom(ctx)
		if !ok {
			return nil, context.Canceled
		}
		deadline := time.After(2 * time.Second)
		for {
			pending := drain()
			for _, m := range pending {
				if m.Kind == "ask" && m.MessageID != "" {
					return json.RawMessage(`{"ask_id":"` + m.MessageID + `"}`), nil
				}
			}
			select {
			case <-deadline:
				return json.RawMessage(`{"ask_id":""}`), nil
			case <-time.After(10 * time.Millisecond):
			}
		}
	}))
	_ = d.Register(runtime.Subagent, "asker", handlerFunc(func(ctx context.Context, _ runtime.Request) (json.RawMessage, error) {
		select {
		case <-time.After(40 * time.Millisecond):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		tool := &postMessageTool{dispatcher: d, cfg: config.DefaultSubagentConfig, repo: repo}
		out, err := tool.Execute(ctx, json.RawMessage(`{"kind":"ask","to_role":"peer","body":"hi"}`))
		if err != nil {
			return nil, err
		}
		return json.RawMessage(out), nil
	}))
	pool := subagents.New(d, subagents.Policy{Workers: 2})
	c := coordinator.New(repo, pool)
	coordinators.Store(d, c)
	coordinatorRepos.Store(d, repo)
	t.Cleanup(func() {
		coordinators.Delete(d)
		coordinatorRepos.Delete(d)
	})
	h, err := c.Spawn(context.Background(), []subagents.Task{
		{ID: "peer-1", Name: "peer", AgentName: "peer", Timeout: 5 * time.Second},
		{ID: "ask-1", Name: "asker", AgentName: "asker", Timeout: 5 * time.Second},
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	res, err := c.Join(context.Background(), h)
	if err != nil {
		t.Fatal(err)
	}
	var peerOut string
	for _, r := range res.Results {
		if r.TaskID == "peer-1" {
			peerOut = string(r.Output)
		}
	}
	if !strings.Contains(peerOut, "msg-") {
		t.Fatalf("peer did not observe ask MessageID via drain: %s", peerOut)
	}
}

// TestParentAnswerClosesAsk_NonBlocking: parent send_to_task answer seals the
// ask so a later peer answer is refused (one-shot; no wait path to CloseAsk).
func TestParentAnswerClosesAsk_NonBlocking(t *testing.T) {
	cfg := config.DefaultSubagentConfig
	repo := ledger.NewMemoryLedgerRepository()
	d := runtime.New(runtime.Policy{})
	_ = d.Register(runtime.Subagent, "worker", handlerFunc(func(context.Context, runtime.Request) (json.RawMessage, error) {
		return json.RawMessage(`{"ok":true}`), nil
	}))
	c := coordinator.New(repo, subagents.New(d, subagents.Policy{Workers: 1}))
	ctx := context.Background()
	h, err := c.Spawn(ctx, []subagents.Task{
		{ID: "asker-1", Name: "worker", AgentName: "worker", Timeout: 3 * time.Second},
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	// Join finishes the worker; handle remains usable for SendToTask ledger path
	// while ask registry is independent of task liveness.
	if _, err := c.Join(ctx, h); err != nil {
		t.Fatal(err)
	}
	snap, _ := c.Inspect(ctx, h)
	runID, askerTask := snap.RunID, snap.Tasks[0].TaskID
	const askID = "ask-parent-nb"
	c.RegisterAsk(runID, askerTask, "worker", askID, nil)

	ans, err := agentmsg.NewMessage(runID, agentmsg.KindAnswer,
		agentmsg.Party{Role: agentmsg.ParentSentinel},
		agentmsg.Party{TaskID: askerTask},
		"from-parent", nil,
		agentmsg.Options{InReplyTo: askID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.SendToTask(ctx, h, askerTask, ans); err != nil {
		t.Fatal(err)
	}
	if !c.IsAskAnswered(askID) {
		t.Fatal("parent SendToTask must CloseAsk for non-blocking path")
	}
	tool := &postMessageTool{dispatcher: d, cfg: cfg, repo: repo}
	peerID := runtime.TaskIdentity{RunID: runID, TaskID: "peer-1", Agent: "peer"}
	if _, err := tool.handlePeerAnswer(ctx, c, peerID, "from-peer", askID); err == nil {
		t.Fatal("peer answer after parent must fail")
	} else if !strings.Contains(err.Error(), "already answered") &&
		!strings.Contains(err.Error(), "unknown or closed") &&
		!strings.Contains(err.Error(), "unknown ask") {
		t.Fatalf("want already-answered/closed, got %v", err)
	}
}
