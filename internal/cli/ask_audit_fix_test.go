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

// TestAskTimeoutClosesAsk ensures that after no_answer the ask is closed and a
// late answer is acknowledged with the structured notice (nil error) instead of
// being persisted or reported as delivered.
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
	before, err := c.ListRunMessages(context.Background(), result.Snapshot.RunID, "")
	if err != nil {
		t.Fatal(err)
	}
	out, err := tool.Execute(ctx, json.RawMessage(
		`{"kind":"answer","body":"late","in_reply_to":"`+mid+`"}`,
	))
	if err != nil {
		t.Fatalf("late answer must not error: %v", err)
	}
	var late map[string]any
	if err := json.Unmarshal([]byte(out), &late); err != nil {
		t.Fatalf("structured result expected, got %q: %v", out, err)
	}
	if late["status"] != "answered" || late["delivered"] != false {
		t.Fatalf("late answer result=%+v, want status=answered delivered=false (out=%s)", late, out)
	}
	notice, _ := late["notice"].(string)
	if !strings.Contains(notice, "timed out") {
		t.Fatalf("notice must explain the asker timed out, notice=%q (out=%s)", notice, out)
	}
	if late["in_reply_to"] != mid {
		t.Fatalf("in_reply_to=%v, want %s", late["in_reply_to"], mid)
	}
	// The late answer is acknowledged but never persisted: no new run message.
	after, err := c.ListRunMessages(context.Background(), result.Snapshot.RunID, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Fatalf("late answer must not be persisted, messages before=%d after=%d", len(before), len(after))
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

// TestWaitPrefersAnswerOverTimeout: dual-ready timer+channel; nested drain wins
// when outer select picks the timer (askWaitUnit=0 → NewTimer(0)).
func TestWaitPrefersAnswerOverTimeout(t *testing.T) {
	prev := askWaitUnit
	askWaitUnit = 0 // zero-duration timer is immediately ready with full park
	t.Cleanup(func() { askWaitUnit = prev })

	cfg := config.DefaultSubagentConfig
	tool, c, _, runID, taskID, _ := setupPostMessageEnv(t, cfg)
	for i := 0; i < 40; i++ {
		askID := "ask-pref-" + time.Now().Format("150405.000000") + strings.Repeat("x", i%5+1)
		c.RegisterAsk(runID, taskID, "worker", askID, nil)
		ch, unpark, err := c.ParkQuestion(runID, taskID, askID)
		if err != nil {
			t.Fatal(err)
		}
		if !c.DeliverAnswer(runID, taskID, askID, "nested-ready") {
			unpark()
			t.Fatal("deliver")
		}
		parked := true
		msg, _ := agentmsg.NewMessage(runID, agentmsg.KindAsk,
			agentmsg.Party{TaskID: taskID}, agentmsg.Party{Role: "p"},
			"q", nil, agentmsg.Options{ID: askID})
		msg.ID = askID
		out, err := tool.waitOnParkedAnswer(context.Background(), c,
			runtime.TaskIdentity{RunID: runID, TaskID: taskID}, msg, 1, ch, &parked, unpark)
		unpark()
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out, "answered") || !strings.Contains(out, "nested-ready") {
			t.Fatalf("out=%s", out)
		}
	}
}

// TestWaitPrefersAnswerOverCancel: cancelled ctx + full park prefers answer.
func TestWaitPrefersAnswerOverCancel(t *testing.T) {
	cfg := config.DefaultSubagentConfig
	tool, c, _, runID, taskID, _ := setupPostMessageEnv(t, cfg)
	answered := 0
	for i := 0; i < 50; i++ {
		askID := "ask-cancel-" + time.Now().Format("150405.000000") + strings.Repeat("y", i%5+1)
		c.RegisterAsk(runID, taskID, "worker", askID, nil)
		ch, unpark, err := c.ParkQuestion(runID, taskID, askID)
		if err != nil {
			t.Fatal(err)
		}
		if !c.DeliverAnswer(runID, taskID, askID, "cancel-ready") {
			unpark()
			t.Fatal("deliver")
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		parked := true
		msg, _ := agentmsg.NewMessage(runID, agentmsg.KindAsk,
			agentmsg.Party{TaskID: taskID}, agentmsg.Party{Role: "p"},
			"q", nil, agentmsg.Options{ID: askID})
		msg.ID = askID
		out, err := tool.waitOnParkedAnswer(ctx, c,
			runtime.TaskIdentity{RunID: runID, TaskID: taskID}, msg, 5, ch, &parked, unpark)
		unpark()
		if err == nil && strings.Contains(out, "answered") {
			answered++
		}
	}
	if answered == 0 {
		t.Fatal("expected at least one answered path under cancel+full park")
	}
}

func TestTryRecvAnswer(t *testing.T) {
	ch := make(chan string, 1)
	if _, ok := tryRecvAnswer(ch); ok {
		t.Fatal("empty")
	}
	ch <- "x"
	got, ok := tryRecvAnswer(ch)
	if !ok || got != "x" {
		t.Fatalf("got=%q ok=%v", got, ok)
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

// TestHandlePeerAnswerNoticeOnUndelivered: when neither the parked-answer
// channel nor the mailbox delivers (asker already timed out / not live), the
// result must surface an explicit notice so the responder understands the
// asker may not see the durable answer live. The notice must not promise
// step-boundary delivery: a false MailboxSend means the message never entered
// the mailbox, so no later drain can inject it.
func TestHandlePeerAnswerNoticeOnUndelivered(t *testing.T) {
	cfg := config.DefaultSubagentConfig
	tool, c, _, runID, taskID, ctx := setupPostMessageEnv(t, cfg)
	c.RegisterAsk(runID, taskID, "worker", "ask-nd", nil)
	// No park (DeliverAnswer=false) and no live handle (HandleForRun=nil), so
	// neither park nor mailbox delivery succeeds while the ask stays open.
	id := runtime.TaskIdentity{RunID: runID, TaskID: taskID, Agent: "worker"}
	out, err := tool.handlePeerAnswer(ctx, c, id, "yes", "ask-nd")
	if err != nil {
		t.Fatal(err)
	}
	var res map[string]any
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatal(err)
	}
	if res["status"] != "answered" {
		t.Fatalf("status=%v", res["status"])
	}
	if res["delivered"] != false {
		t.Fatalf("delivered=%v, want false", res["delivered"])
	}
	notice, _ := res["notice"].(string)
	if !strings.Contains(notice, "asker not live") {
		t.Fatalf("undelivered answer must include a notice, out=%s", out)
	}
	if strings.Contains(notice, "next step boundary") || strings.Contains(notice, "delivered at") {
		t.Fatalf("notice must not promise step-boundary delivery, notice=%q", notice)
	}
	if !strings.Contains(notice, "may not reach the asker") || !strings.Contains(notice, "run ledger") {
		t.Fatalf("notice must state the answer stays durable in the run ledger and may not reach the asker, notice=%q", notice)
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
	// Peer answer when already answered: the ask is sealed before claim, so the
	// peer gets the structured notice with nil error — not an opaque failure.
	cfg := config.DefaultSubagentConfig
	tool, c2, _, runID, taskID, ctx := setupPostMessageEnv(t, cfg)
	id := runtime.TaskIdentity{RunID: runID, TaskID: taskID, Agent: "worker"}
	assertSealedBeforeClaimAnswer(t, tool, c2, runID, taskID, id, ctx)
	// Concurrent answers: exactly one answer wins the claim and persists; the
	// loser must never persist a duplicate (one-shot invariant).
	assertConcurrentOneShot(t, tool, c2, runID, taskID, id, ctx)
}

// assertSealedBeforeClaimAnswer pins the structured notice a peer receives when
// the ask was already sealed (timeout/close) before the answer could be claimed.
func assertSealedBeforeClaimAnswer(t *testing.T, tool *postMessageTool, c coordinator.Coordinator, runID, taskID string, id runtime.TaskIdentity, ctx context.Context) {
	t.Helper()
	c.RegisterAsk(runID, taskID, "worker", "done", nil)
	_ = c.CompleteAskAnswer("done")
	out, err := tool.handlePeerAnswer(ctx, c, id, "x", "done")
	if err != nil {
		t.Fatalf("sealed-before-claim answer must not error: %v", err)
	}
	var doneRes map[string]any
	if err := json.Unmarshal([]byte(out), &doneRes); err != nil {
		t.Fatalf("structured result expected, got %q: %v", out, err)
	}
	if doneRes["status"] != "answered" || doneRes["delivered"] != false {
		t.Fatalf("done result=%+v, want status=answered delivered=false", doneRes)
	}
	if notice, _ := doneRes["notice"].(string); !strings.Contains(notice, "timed out") {
		t.Fatalf("notice=%q, want it to explain the asker timed out (out=%s)", notice, out)
	}
}

// assertConcurrentOneShot runs two concurrent answers for one open ask and pins
// the one-shot invariant: exactly one answer wins the claim and persists. The
// loser surfaces either the ClaimAskAnswer race error (ask still open/claimed
// at peek) or, if it peeked after the winner sealed, the structured
// already-answered notice — both mean the duplicate was dropped.
func assertConcurrentOneShot(t *testing.T, tool *postMessageTool, c coordinator.Coordinator, runID, taskID string, id runtime.TaskIdentity, ctx context.Context) {
	t.Helper()
	c.RegisterAsk(runID, taskID, "worker", "race", nil)
	beforeRace, err := c.ListRunMessages(ctx, runID, "")
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan string, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out, err := tool.handlePeerAnswer(ctx, c, id, "a", "race")
			if err != nil {
				results <- "err:" + err.Error()
				return
			}
			results <- "out:" + out
		}()
	}
	wg.Wait()
	close(results)
	var success, dropped int
	for r := range results {
		if strings.HasPrefix(r, "out:") {
			var parsed map[string]any
			if err := json.Unmarshal([]byte(strings.TrimPrefix(r, "out:")), &parsed); err != nil {
				t.Fatalf("loser structured result invalid: %s", r)
			}
			success++
			if parsed["status"] != "answered" {
				t.Fatalf("unexpected answer result: %s", r)
			}
		} else {
			dropped++
		}
	}
	if success < 1 {
		t.Fatalf("want one successful answer, got success=%d dropped=%d", success, dropped)
	}
	afterRace, err := c.ListRunMessages(ctx, runID, "")
	if err != nil {
		t.Fatal(err)
	}
	if added := len(afterRace) - len(beforeRace); added != 1 {
		t.Fatalf("exactly one answer must be persisted, added=%d", added)
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

// sealAfterClaimCoord pretends the waiter CloseAsk'd immediately after claim.
type sealAfterClaimCoord struct {
	coordinator.Coordinator
	afterClaim bool
}

func (s *sealAfterClaimCoord) ClaimAskAnswer(askID string) (string, error) {
	asker, err := s.Coordinator.ClaimAskAnswer(askID)
	if err == nil {
		s.afterClaim = true
	}
	return asker, err
}

func (s *sealAfterClaimCoord) IsAskAnswered(askID string) bool {
	if s.afterClaim {
		return true
	}
	return s.Coordinator.IsAskAnswered(askID)
}

// sealFailCoord claims/posts normally but loses SealAskAnswer (waiter won).
type sealFailCoord struct {
	coordinator.Coordinator
}

func (s sealFailCoord) SealAskAnswer(string) bool { return false }

// TestPeerAnswerAbortsWhenWaiterSealed: after claim, if waiter seals before
// persist, peer must refuse without reporting answered.
func TestPeerAnswerAbortsWhenWaiterSealed(t *testing.T) {
	cfg := config.DefaultSubagentConfig
	tool, c, _, runID, askerTask, ctx := setupPostMessageEnv(t, cfg)
	const askID = "ask-timeout-seal"
	c.RegisterAsk(runID, askerTask, "worker", askID, nil)
	wrap := &sealAfterClaimCoord{Coordinator: c}
	peerID := runtime.TaskIdentity{RunID: runID, TaskID: "peer-1", Agent: "peer"}
	if _, err := tool.handlePeerAnswer(ctx, wrap, peerID, "late", askID); err == nil {
		t.Fatal("peer must fail when waiter sealed after claim")
	} else if !strings.Contains(err.Error(), "already answered") {
		t.Fatalf("err=%v", err)
	}
}

// TestPeerAnswerAbortsWhenSealLostAfterPost: SealAskAnswer false skips inject.
func TestPeerAnswerAbortsWhenSealLostAfterPost(t *testing.T) {
	cfg := config.DefaultSubagentConfig
	tool, c, repo, runID, askerTask, ctx := setupPostMessageEnv(t, cfg)
	// Peer needs a durable task for PostTaskMessage.
	const peerTask = "peer-seal"
	if err := repo.CreateTask(context.Background(), ledger.TaskSnapshot{
		RunID: runID, TaskID: peerTask, Status: string(ledger.TaskStatusRunning), Version: 1,
	}); err != nil {
		t.Fatal(err)
	}
	const askID = "ask-seal-after-post"
	c.RegisterAsk(runID, askerTask, "worker", askID, nil)
	wrap := sealFailCoord{Coordinator: c}
	peerID := runtime.TaskIdentity{RunID: runID, TaskID: peerTask, Agent: "peer"}
	if _, err := tool.handlePeerAnswer(ctx, wrap, peerID, "body", askID); err == nil {
		t.Fatal("peer must fail when seal lost after post")
	} else if !strings.Contains(err.Error(), "already answered") {
		t.Fatalf("err=%v", err)
	}
}

// TestParentAnswerClosesAsk_NonBlocking: parent send_to_task answer seals the
// ask so a later peer answer is not delivered or persisted (one-shot; no wait
// path to CloseAsk). The peer gets the structured notice, not an opaque error.
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
	before, err := c.ListRunMessages(ctx, runID, "")
	if err != nil {
		t.Fatal(err)
	}
	out, err := tool.handlePeerAnswer(ctx, c, peerID, "from-peer", askID)
	if err != nil {
		t.Fatalf("peer answer after parent must not error: %v", err)
	}
	var peerRes map[string]any
	if err := json.Unmarshal([]byte(out), &peerRes); err != nil {
		t.Fatalf("structured result expected, got %q: %v", out, err)
	}
	if peerRes["status"] != "answered" || peerRes["delivered"] != false {
		t.Fatalf("peer result=%+v, want status=answered delivered=false (out=%s)", peerRes, out)
	}
	if notice, _ := peerRes["notice"].(string); !strings.Contains(notice, "timed out") {
		t.Fatalf("notice=%q, want it to explain the asker timed out (out=%s)", notice, out)
	}
	// The peer answer must not be persisted: the parent already sealed the ask.
	after, err := c.ListRunMessages(ctx, runID, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Fatalf("peer answer must not be persisted, messages before=%d after=%d", len(before), len(after))
	}
}
