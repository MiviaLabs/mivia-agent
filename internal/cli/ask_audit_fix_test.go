package cli

import (
	"context"
	"encoding/json"
	"strings"
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
