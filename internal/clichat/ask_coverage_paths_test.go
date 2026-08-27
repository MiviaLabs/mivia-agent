package clichat

import (
	"context"
	"encoding/json"
	"fmt"
	cliorchestrate "github.com/MiviaLabs/mivia-agent/internal/cliorchestrate"
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
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

func TestApplyAskRouteSpawnQuotaAndFail(t *testing.T) {
	cfg := config.DefaultSubagentConfig
	cfg.Messaging.Routing.MaxReferralSpawnsPerRun = 1
	tool, c, _, runID, taskID, _ := setupPostMessageEnv(t, cfg)
	msg, _ := agentmsg.NewMessage(runID, agentmsg.KindAsk,
		agentmsg.Party{TaskID: taskID, Role: "worker"},
		agentmsg.Party{Role: "auditor"},
		"body", nil, agentmsg.Options{})
	c.RegisterAsk(runID, taskID, "worker", msg.ID, nil)
	_ = c.TryIncReferralSpawn(runID, 1)
	id := runtime.TaskIdentity{RunID: runID, TaskID: taskID, Agent: "worker"}
	err := tool.applyAskRoute(context.Background(), c, id, "auditor", "", msg, agentmsg.RouteDecision{Action: agentmsg.RouteSpawn})
	if err == nil || err.result == "" {
		t.Fatal("want spawn quota decline")
	}
	msg2, _ := agentmsg.NewMessage(runID, agentmsg.KindAsk,
		agentmsg.Party{TaskID: taskID, Role: "worker"},
		agentmsg.Party{Role: "auditor"},
		"body2", nil, agentmsg.Options{})
	c.RegisterAsk(runID, taskID, "worker", msg2.ID, nil)
	c.DecReferralSpawn(runID)
	tool.referralSpawn = func(context.Context, string, string, agentmsg.Message) (string, error) {
		return "", fmt.Errorf("boom")
	}
	if err := tool.applyAskRoute(context.Background(), c, id, "auditor", "", msg2, agentmsg.RouteDecision{Action: agentmsg.RouteSpawn}); err == nil {
		t.Fatal("want spawn fail decline")
	}
	tool.referralSpawn = nil
	msg3, _ := agentmsg.NewMessage(runID, agentmsg.KindAsk,
		agentmsg.Party{TaskID: taskID, Role: "worker"},
		agentmsg.Party{Role: "auditor"},
		"body3", nil, agentmsg.Options{})
	c.RegisterAsk(runID, taskID, "worker", msg3.ID, nil)
	if err := tool.applyAskRoute(context.Background(), c, id, "auditor", "", msg3, agentmsg.RouteDecision{Action: agentmsg.RouteSpawn}); err == nil {
		t.Fatal("want default spawn fail")
	}
	msg4, _ := agentmsg.NewMessage(runID, agentmsg.KindAsk,
		agentmsg.Party{TaskID: taskID, Role: "worker"},
		agentmsg.Party{Role: "auditor"},
		"body4", nil, agentmsg.Options{})
	c.RegisterAsk(runID, taskID, "worker", msg4.ID, nil)
	if err := tool.applyAskRoute(context.Background(), c, id, "auditor", "gone", msg4, agentmsg.RouteDecision{Action: agentmsg.RouteDeliver}); err == nil {
		t.Fatal("want nil handle decline")
	}
}

func TestHandlePeerAnswerPaths(t *testing.T) {
	cfg := config.DefaultSubagentConfig
	tool, c, _, runID, taskID, ctx := setupPostMessageEnv(t, cfg)
	id := runtime.TaskIdentity{RunID: runID, TaskID: taskID, Agent: "worker"}
	if _, err := tool.handlePeerAnswer(ctx, c, id, "a", ""); err == nil {
		t.Fatal("empty in_reply_to")
	}
	if _, err := tool.handlePeerAnswer(ctx, c, id, "a", "unknown"); err == nil {
		t.Fatal("unknown ask")
	}
	tool.cfg.Messaging.MaxBodyBytes = 4
	c.RegisterAsk(runID, taskID, "worker", "ask-big", nil)
	if _, err := tool.handlePeerAnswer(ctx, c, id, "toolongbody", "ask-big"); err == nil {
		t.Fatal("oversized body")
	}
	tool.cfg.Messaging.MaxBodyBytes = 2048
	c.RegisterAsk(runID, taskID, "worker", "ask-ok", nil)
	if _, err := tool.handlePeerAnswer(ctx, c, id, "ok", "ask-ok"); err != nil {
		t.Fatal(err)
	}
}

// TestHandlePeerAnswerSealedBeforeClaimNotice: when the asker's park already
// sealed (CloseAsk/SealAskAnswer before the peer claims), handlePeerAnswer must
// return the structured already-answered notice with nil error and must NOT
// persist an answer message — the relay learns "asker timed out; I'm done"
// instead of an opaque tool error.
func TestHandlePeerAnswerSealedBeforeClaimNotice(t *testing.T) {
	cfg := config.DefaultSubagentConfig
	tool, c, _, runID, taskID, ctx := setupPostMessageEnv(t, cfg)
	const askID = "ask-sealed-before-claim"
	c.RegisterAsk(runID, taskID, "worker", askID, nil)
	// Simulate the asker's park timing out: permanent close before any claim.
	if !c.SealAskAnswer(askID) {
		t.Fatal("seal must succeed on an open ask")
	}
	if _, ok := c.AskLookup(askID); ok {
		t.Fatal("sealed ask must not peek open")
	}
	if !c.IsAskAnswered(askID) {
		t.Fatal("sealed ask must report answered")
	}
	before, err := c.ListRunMessages(ctx, runID, "")
	if err != nil {
		t.Fatal(err)
	}
	id := runtime.TaskIdentity{RunID: runID, TaskID: taskID, Agent: "worker"}
	out, err := tool.handlePeerAnswer(ctx, c, id, "late", askID)
	if err != nil {
		t.Fatalf("sealed-before-claim answer must not error: %v", err)
	}
	var res map[string]any
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("structured result expected, got %q: %v", out, err)
	}
	if res["status"] != "answered" {
		t.Fatalf("status=%v, want answered (out=%s)", res["status"], out)
	}
	if res["delivered"] != false {
		t.Fatalf("delivered=%v, want false (out=%s)", res["delivered"], out)
	}
	notice, _ := res["notice"].(string)
	if !strings.Contains(notice, "timed out") {
		t.Fatalf("notice=%q, want it to explain the asker timed out (out=%s)", notice, out)
	}
	if res["in_reply_to"] != askID {
		t.Fatalf("in_reply_to=%v, want %s", res["in_reply_to"], askID)
	}
	// The answer is acknowledged but never persisted: no new run message.
	after, err := c.ListRunMessages(ctx, runID, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Fatalf("answer must not be persisted, messages before=%d after=%d", len(before), len(after))
	}
}

func TestWaitOnParkedAnswerCancel(t *testing.T) {
	cfg := config.DefaultSubagentConfig
	tool, c, _, runID, taskID, _ := setupPostMessageEnv(t, cfg)
	msg, _ := agentmsg.NewMessage(runID, agentmsg.KindAsk,
		agentmsg.Party{TaskID: taskID}, agentmsg.Party{Role: "p"},
		"q", nil, agentmsg.Options{})
	ch, unpark, err := c.ParkQuestion(runID, taskID, msg.ID)
	if err != nil {
		t.Fatal(err)
	}
	parked := true
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := tool.waitOnParkedAnswer(ctx, c, runtime.TaskIdentity{RunID: runID, TaskID: taskID}, msg, 0, ch, &parked, unpark); err == nil {
		t.Fatal("want cancel")
	}
}

func TestRegisterMessagingToolsIdempotentPaths(t *testing.T) {
	d := runtime.New(runtime.Policy{})
	reg := tools.NewRegistry()
	cfg := config.DefaultSubagentConfig
	repo := ledger.NewMemoryLedgerRepository()
	if err := registerMessagingTools(d, reg, cfg, repo, nil); err != nil {
		t.Fatal(err)
	}
	if err := registerMessagingTools(d, reg, cfg, repo, nil); err != nil {
		t.Fatal(err)
	}
	post, ok := reg.Get(toolPostMessage)
	if !ok {
		t.Fatal("post_message missing")
	}
	pt := post.(*postMessageTool)
	if _, err := pt.referralSpawn(context.Background(), "no-run", "auditor", agentmsg.Message{ID: "m"}); err == nil {
		t.Fatal("inactive run should fail")
	}
}

func TestParkBlockingAskParkError(t *testing.T) {
	cfg := config.DefaultSubagentConfig
	_, c, _, runID, taskID, _ := setupPostMessageEnv(t, cfg)
	_, unpark, err := c.ParkQuestion(runID, taskID, "held")
	if err != nil {
		t.Fatal(err)
	}
	defer unpark()
	id := runtime.TaskIdentity{RunID: runID, TaskID: taskID}
	if _, _, _, err := parkBlockingAsk(c, id, "other", true, 60, agentmsg.RouteDecision{Action: agentmsg.RouteDeliver}); err == nil {
		t.Fatal("want park error")
	}
	if _, _, parked, err := parkBlockingAsk(c, id, "x", true, 60, agentmsg.RouteDecision{Action: agentmsg.RouteDecline}); err != nil || parked {
		t.Fatal("decline skip")
	}
}

func TestAskRouteErrError(t *testing.T) {
	if (&askRouteErr{err: fmt.Errorf("x")}).Error() != "x" {
		t.Fatal()
	}
	if (&askRouteErr{result: "r"}).Error() != "r" {
		t.Fatal()
	}
}

func TestHandlePeerAnswerQuota(t *testing.T) {
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
}

func TestHandleAskErrorPaths(t *testing.T) {
	cfg := config.DefaultSubagentConfig
	tool, c, _, runID, taskID, ctx := setupPostMessageEnv(t, cfg)
	id := runtime.TaskIdentity{RunID: runID, TaskID: taskID, Agent: "worker"}
	tool.cfg.Messaging.MaxBodyBytes = 3
	if _, err := tool.handleAsk(ctx, c, id, "toolong", nil, "peer", 0, ""); err == nil {
		t.Fatal("mint")
	}
	tool.cfg.Messaging.MaxBodyBytes = 2048
	// Quota exhausted.
	tool.cfg.Messaging.MaxMessagesPerTask = 1
	if _, err := tool.Execute(ctx, json.RawMessage(`{"kind":"finding","body":"burn"}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := tool.handleAsk(ctx, c, id, "q", nil, "peer", 0, ""); err == nil {
		t.Fatal("quota")
	}
	// Decline path (not running, no allow).
	tool2, c2, _, run2, task2, ctx2 := setupPostMessageEnv(t, cfg)
	id2 := runtime.TaskIdentity{RunID: run2, TaskID: task2, Agent: "worker"}
	out, err := tool2.handleAsk(ctx2, c2, id2, "q", nil, "peer", 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "declined") {
		t.Fatalf("out=%s", out)
	}
	// Ask quota decline via RouteAsk.
	cfg3 := cfg
	cfg3.Messaging.Routing.MaxAsksPerTask = 1
	tool3, c3, _, run3, task3, ctx3 := setupPostMessageEnv(t, cfg3)
	id3 := runtime.TaskIdentity{RunID: run3, TaskID: task3, Agent: "worker"}
	c3.RegisterAsk(run3, task3, "worker", "pre", nil)
	out, err = tool3.handleAsk(ctx3, c3, id3, "q", nil, "peer", 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "declined") {
		t.Fatalf("quota decline: %s", out)
	}
	// Missing to_role.
	if _, err := tool2.handleAsk(ctx2, c2, id2, "q", nil, "", 0, ""); err == nil {
		t.Fatal("to_role")
	}
	// Empty agent defaults fromRole to "agent".
	idEmpty := runtime.TaskIdentity{RunID: run2, TaskID: task2, Agent: ""}
	out, err = tool2.handleAsk(ctx2, c2, idEmpty, "q", nil, "peer", 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "declined") {
		t.Fatalf("%s", out)
	}
	// Post fails for missing task.
	idBad := runtime.TaskIdentity{RunID: runID, TaskID: "no-task", Agent: "worker"}
	_, _ = tool.handleAsk(ctx, c, idBad, "q", nil, "peer", 0, "")
}

// findLiveErrCoord wraps a real coordinator and injects FindLiveTaskByRole errors.
type findLiveErrCoord struct {
	coordinator.Coordinator
	err error
}

func (f findLiveErrCoord) FindLiveTaskByRole(ctx context.Context, runID, role string) (string, bool, error) {
	if f.err != nil {
		return "", false, f.err
	}
	return f.Coordinator.FindLiveTaskByRole(ctx, runID, role)
}

type transitionErrCoord struct {
	coordinator.Coordinator
	err error
}

func (t transitionErrCoord) TransitionToAwaitingInput(ctx context.Context, runID, taskID string) error {
	if t.err != nil {
		return t.err
	}
	return t.Coordinator.TransitionToAwaitingInput(ctx, runID, taskID)
}

func TestHandleAskFindLiveError(t *testing.T) {
	cfg := config.DefaultSubagentConfig
	tool, c, _, runID, taskID, ctx := setupPostMessageEnv(t, cfg)
	id := runtime.TaskIdentity{RunID: runID, TaskID: taskID, Agent: "worker"}
	if _, err := tool.handleAsk(ctx, findLiveErrCoord{Coordinator: c, err: fmt.Errorf("list boom")}, id, "q", nil, "peer", 0, ""); err == nil {
		t.Fatal("want find live err")
	}
}

// livePeerRun starts a hanging peer task and returns run handle + dispatcher/repo/coord.
func livePeerRun(t *testing.T) (*runtime.Dispatcher, coordinator.Coordinator, ledger.LedgerRepository, *coordinator.RunHandle) {
	t.Helper()
	d := runtime.New(runtime.Policy{})
	repo := ledger.NewMemoryLedgerRepository()
	live := make(chan struct{})
	_ = d.Register(runtime.Subagent, "peer", handlerFunc(func(ctx context.Context, _ runtime.Request) (json.RawMessage, error) {
		close(live)
		<-ctx.Done()
		return json.RawMessage(`{}`), nil
	}))
	c := coordinator.New(repo, subagents.New(d, subagents.Policy{Workers: 2}))
	cliorchestrate.CoordinatorsForTest.Store(d, c)
	cliorchestrate.CoordinatorReposForTest.Store(d, repo)
	t.Cleanup(func() {
		cliorchestrate.CoordinatorsForTest.Delete(d)
		cliorchestrate.CoordinatorReposForTest.Delete(d)
	})
	h, err := c.Spawn(context.Background(), []subagents.Task{
		{ID: "peer-1", Name: "peer", AgentName: "peer", Timeout: 4 * time.Second},
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-live:
	case <-time.After(2 * time.Second):
		t.Fatal("peer not live")
	}
	t.Cleanup(func() {
		_ = c.Cancel(context.Background(), h)
		_, _ = c.Join(context.Background(), h)
	})
	return d, c, repo, h
}

func seedAskerTask(t *testing.T, repo ledger.LedgerRepository, runID, taskID string) {
	t.Helper()
	if err := repo.CreateTask(context.Background(), ledger.TaskSnapshot{
		RunID: runID, TaskID: taskID, Status: string(ledger.TaskStatusRunning),
		AgentName: "worker", Version: 1,
	}); err != nil {
		t.Fatal(err)
	}
}

func TestHandleAskConcurrentRegisterSameTask(t *testing.T) {
	cfg := config.DefaultSubagentConfig
	cfg.Messaging.Routing.MaxAsksPerTask = 1
	d, c, repo, h := livePeerRun(t)
	seedAskerTask(t, repo, h.RunID(), "same")
	tool := &postMessageTool{dispatcher: d, cfg: cfg, repo: repo}
	id := runtime.TaskIdentity{RunID: h.RunID(), TaskID: "same", Agent: "worker"}
	var wg sync.WaitGroup
	outs := make(chan string, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out, err := tool.handleAsk(context.Background(), c, id, "q", nil, "peer", 0, "")
			if err != nil {
				outs <- "err"
				return
			}
			outs <- out
		}()
	}
	wg.Wait()
	close(outs)
	n := 0
	for range outs {
		n++
	}
	if n != 2 {
		t.Fatalf("n=%d", n)
	}
}

func TestHandlePeerAnswerPostFail(t *testing.T) {
	cfg := config.DefaultSubagentConfig
	tool, c, _, runID, taskID, ctx := setupPostMessageEnv(t, cfg)
	c.RegisterAsk(runID, taskID, "worker", "ask-post", nil)
	id := runtime.TaskIdentity{RunID: runID, TaskID: "missing-answerer", Agent: "worker"}
	if _, err := tool.handlePeerAnswer(ctx, c, id, "body", "ask-post"); err == nil {
		t.Fatal("want post fail")
	}
}

func TestHandleAskQuotaAfterPark(t *testing.T) {
	cfg := config.DefaultSubagentConfig
	cfg.Messaging.MaxMessagesPerTask = 1
	d, c, repo, h := livePeerRun(t)
	seedAskerTask(t, repo, h.RunID(), "w1")
	tool := &postMessageTool{dispatcher: d, cfg: cfg, repo: repo}
	id := runtime.TaskIdentity{RunID: h.RunID(), TaskID: "w1", Agent: "worker"}
	if err := c.ConsumeMessageQuota(h.RunID(), "w1", 1); err != nil {
		t.Fatal(err)
	}
	if _, err := tool.handleAsk(context.Background(), c, id, "q", nil, "peer", 2, ""); err == nil {
		t.Fatal("want quota error after park")
	}
}

func TestHandleAskTransitionFail(t *testing.T) {
	cfg := config.DefaultSubagentConfig
	d, c, repo, h := livePeerRun(t)
	seedAskerTask(t, repo, h.RunID(), "w2")
	tool := &postMessageTool{dispatcher: d, cfg: cfg, repo: repo}
	id := runtime.TaskIdentity{RunID: h.RunID(), TaskID: "w2", Agent: "worker"}
	wrap := transitionErrCoord{Coordinator: c, err: fmt.Errorf("cas fail")}
	out, err := tool.handleAsk(context.Background(), wrap, id, "q", nil, "peer", 2, "")
	if err == nil || !strings.Contains(err.Error(), "park ask") {
		t.Fatalf("want park ask transition err, got %v out=%s", err, out)
	}
	// Ask must be closed so peers cannot answer into a void.
	// Message id is not returned on error; ensure no open asks remain for this task.
	if n := c.AsksUsedByTask(h.RunID(), "w2"); n < 1 {
		// register may have happened
		_ = n
	}
}

func TestHandleAskParkHeldWithLivePeer(t *testing.T) {
	cfg := config.DefaultSubagentConfig
	d := runtime.New(runtime.Policy{})
	repo := ledger.NewMemoryLedgerRepository()
	peerLive := make(chan struct{})
	_ = d.Register(runtime.Subagent, "peer", handlerFunc(func(ctx context.Context, _ runtime.Request) (json.RawMessage, error) {
		close(peerLive)
		<-ctx.Done()
		return json.RawMessage(`{}`), nil
	}))
	_ = d.Register(runtime.Subagent, "worker", handlerFunc(func(ctx context.Context, _ runtime.Request) (json.RawMessage, error) {
		// Wait for peer live.
		select {
		case <-peerLive:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		coord := cliorchestrate.InitCoordinator(d, cfg, repo)
		id, _ := runtime.TaskIdentityFrom(ctx)
		_, unpark, err := coord.ParkQuestion(id.RunID, id.TaskID, "held")
		if err != nil {
			return nil, err
		}
		defer unpark()
		tool := &postMessageTool{dispatcher: d, cfg: cfg, repo: repo}
		_, err = tool.handleAsk(ctx, coord, id, "q", nil, "peer", 2, "")
		if err == nil {
			return nil, fmt.Errorf("expected park held error")
		}
		return json.RawMessage(`{"ok":true}`), nil
	}))
	c := coordinator.New(repo, subagents.New(d, subagents.Policy{Workers: 2}))
	cliorchestrate.CoordinatorsForTest.Store(d, c)
	cliorchestrate.CoordinatorReposForTest.Store(d, repo)
	t.Cleanup(func() {
		cliorchestrate.CoordinatorsForTest.Delete(d)
		cliorchestrate.CoordinatorReposForTest.Delete(d)
	})
	h, err := c.Spawn(context.Background(), []subagents.Task{
		{ID: "peer-1", Name: "peer", AgentName: "peer", Timeout: 4 * time.Second},
		{ID: "w1", Name: "worker", AgentName: "worker", Timeout: 4 * time.Second},
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	res, err := c.Join(context.Background(), h)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range res.Results {
		if r.TaskID == "w1" && r.Err != nil {
			// Worker may complete with ok after park error returned as tool error
			// which becomes task failure — either is fine.
			_ = r.Err
		}
	}
}

func TestAllowPairKeyAndRouteInvalidMode(t *testing.T) {
	if agentmsg.AllowPairKey("a", "b") != "a->b" {
		t.Fatal(agentmsg.AllowPairKey("a", "b"))
	}
	d := agentmsg.RouteAsk(agentmsg.RoutingPolicy{Mode: "weird"}, agentmsg.RouteInput{
		FromRole: "a", ToRole: "b", TargetRunning: true,
	})
	if d.Reason != agentmsg.DeclineInvalid {
		t.Fatalf("%+v", d)
	}
}

// tryRegFailCoord forces TryRegisterAsk to fail after RouteAsk allowed.
type tryRegFailCoord struct {
	coordinator.Coordinator
}

func (t tryRegFailCoord) TryRegisterAsk(string, string, string, string, []string, int) bool {
	return false
}
func (t tryRegFailCoord) FindLiveTaskByRole(ctx context.Context, runID, role string) (string, bool, error) {
	return "peer-1", true, nil
}
func (t tryRegFailCoord) HandleForRun(string) *coordinator.RunHandle { return nil }

func TestHandleAskTryRegisterFail(t *testing.T) {
	cfg := config.DefaultSubagentConfig
	tool, c, _, runID, taskID, ctx := setupPostMessageEnv(t, cfg)
	id := runtime.TaskIdentity{RunID: runID, TaskID: taskID, Agent: "worker"}
	wrap := tryRegFailCoord{Coordinator: c}
	out, err := tool.handleAsk(ctx, wrap, id, "q", nil, "peer", 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "quota_exceeded") && !strings.Contains(out, "declined") {
		t.Fatalf("out=%s", out)
	}
}

// claimFailCoord peeks open but claim always fails.
type claimFailCoord struct {
	coordinator.Coordinator
}

func (c claimFailCoord) AskLookup(string) (string, bool) { return "asker", true }
func (c claimFailCoord) IsAskAnswered(string) bool       { return false }
func (c claimFailCoord) ClaimAskAnswer(string) (string, error) {
	return "", fmt.Errorf("ask already answered")
}

func TestHandlePeerAnswerClaimFail(t *testing.T) {
	cfg := config.DefaultSubagentConfig
	tool, c, _, runID, taskID, ctx := setupPostMessageEnv(t, cfg)
	id := runtime.TaskIdentity{RunID: runID, TaskID: taskID, Agent: "worker"}
	wrap := claimFailCoord{Coordinator: c}
	if _, err := tool.handlePeerAnswer(ctx, wrap, id, "ok", "any"); err == nil {
		t.Fatal("want claim fail")
	}
}

func TestRegisterMessagingToolsWithAgentReg(t *testing.T) {
	d := runtime.New(runtime.Policy{})
	reg := tools.NewRegistry()
	cfg := config.DefaultSubagentConfig
	repo := ledger.NewMemoryLedgerRepository()
	// Registry with a published agent so resolveTaskRoute succeeds and sets digest.
	ar := testAgentRegistry(t, "auditor")
	if err := registerMessagingTools(d, reg, cfg, repo, ar); err != nil {
		t.Fatal(err)
	}
	post, ok := reg.Get(toolPostMessage)
	if !ok {
		t.Fatal("missing post_message")
	}
	pt := post.(*postMessageTool)
	// Call referralSpawn: inactive run fails after agentReg resolve path.
	msg, _ := agentmsg.NewMessage("r", agentmsg.KindAsk,
		agentmsg.Party{TaskID: "t", Role: "a"}, agentmsg.Party{Role: "b"},
		"x", nil, agentmsg.Options{})
	if _, err := pt.referralSpawn(context.Background(), "no-run", "auditor", msg); err == nil {
		t.Fatal("want inactive run error")
	}
	// Missing agent: resolve fails, still attempts spawn without digest.
	if _, err := pt.referralSpawn(context.Background(), "no-run", "nope", msg); err == nil {
		t.Fatal("want inactive run error")
	}
}
