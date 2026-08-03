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

func askTestEnv(t *testing.T, cfg config.SubagentConfig, workers int) (
	*runtime.Dispatcher, coordinator.Coordinator, ledger.LedgerRepository,
) {
	t.Helper()
	repo := ledger.NewMemoryLedgerRepository()
	d := runtime.New(runtime.Policy{})
	pool := subagents.New(d, subagents.Policy{Workers: workers})
	c := coordinator.New(repo, pool)
	coordinators.Store(d, c)
	coordinatorRepos.Store(d, repo)
	t.Cleanup(func() {
		coordinators.Delete(d)
		coordinatorRepos.Delete(d)
	})
	return d, c, repo
}

func registerAskHandler(d *runtime.Dispatcher, name string, fn handlerFunc) {
	_ = d.Register(runtime.Subagent, name, fn)
}

// TestAskLiveRoundTrip: reviewer asks live auditor; auditor answers; chain in run_messages.
func TestAskLiveRoundTrip(t *testing.T) {
	cfg := config.DefaultSubagentConfig
	cfg.Messaging.Routing.Allow = []string{"reviewer->auditor"}
	d, c, repo := askTestEnv(t, cfg, 2)
	var askID string
	var askIDMu sync.Mutex

	registerAskHandler(d, "auditor", func(ctx context.Context, _ runtime.Request) (json.RawMessage, error) {
		return answerFirstAsk(ctx, d, cfg, repo)
	})
	registerAskHandler(d, "reviewer", func(ctx context.Context, _ runtime.Request) (json.RawMessage, error) {
		select {
		case <-time.After(50 * time.Millisecond):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		tool := &postMessageTool{dispatcher: d, cfg: cfg, repo: repo}
		out, err := tool.Execute(ctx, json.RawMessage(
			`{"kind":"ask","to_role":"auditor","body":"please review finding X","wait_seconds":5}`,
		))
		if err != nil {
			return nil, err
		}
		var res map[string]any
		_ = json.Unmarshal([]byte(out), &res)
		if mid, _ := res["message_id"].(string); mid != "" {
			askIDMu.Lock()
			askID = mid
			askIDMu.Unlock()
		}
		return json.RawMessage(out), nil
	})

	result := spawnJoin(t, c, []subagents.Task{
		{ID: "rev-1", Name: "reviewer", AgentName: "reviewer", Timeout: 10 * time.Second},
		{ID: "aud-1", Name: "auditor", AgentName: "auditor", Timeout: 10 * time.Second},
	})
	assertKindsContain(t, c, result.Snapshot.RunID, "ask", "answer")
	askIDMu.Lock()
	gotAsk := askID
	askIDMu.Unlock()
	if gotAsk == "" {
		t.Fatal("ask message_id not captured")
	}
	// Second answer must fail (one-shot).
	ctx := runtime.ContextWithTaskIdentity(context.Background(), runtime.TaskIdentity{
		RunID: result.Snapshot.RunID, TaskID: "aud-1", Agent: "auditor",
	})
	tool := &postMessageTool{dispatcher: d, cfg: cfg, repo: repo}
	if _, err := tool.Execute(ctx, json.RawMessage(
		`{"kind":"answer","body":"second try","in_reply_to":"`+gotAsk+`"}`,
	)); err == nil {
		t.Fatal("second answer must be refused")
	}
}

func answerFirstAsk(ctx context.Context, d *runtime.Dispatcher, cfg config.SubagentConfig, repo ledger.LedgerRepository) (json.RawMessage, error) {
	tool := &postMessageTool{dispatcher: d, cfg: cfg, repo: repo}
	coord := initCoordinator(d, cfg, repo)
	id, ok := runtime.TaskIdentityFrom(ctx)
	if !ok {
		return nil, context.Canceled
	}
	deadline := time.After(3 * time.Second)
	var mid string
	for mid == "" {
		msgs, _ := coord.ListRunMessages(ctx, id.RunID, "")
		for _, m := range msgs {
			if m.Kind == agentmsg.KindAsk {
				mid = m.MessageID
				break
			}
		}
		if mid != "" {
			break
		}
		select {
		case <-deadline:
			return nil, context.DeadlineExceeded
		case <-time.After(20 * time.Millisecond):
		}
	}
	out, err := tool.Execute(ctx, json.RawMessage(`{"kind":"answer","body":"looks solid","in_reply_to":"`+mid+`"}`))
	if err != nil {
		return nil, err
	}
	return json.RawMessage(out), nil
}

func spawnJoin(t *testing.T, c coordinator.Coordinator, tasks []subagents.Task) *coordinator.RunResult {
	t.Helper()
	h, err := c.Spawn(context.Background(), tasks, "")
	if err != nil {
		t.Fatal(err)
	}
	result, err := c.Join(context.Background(), h)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range result.Results {
		if r.Err != nil {
			t.Fatalf("task %s err: %v output=%s", r.TaskID, r.Err, string(r.Output))
		}
	}
	return result
}

func assertKindsContain(t *testing.T, c coordinator.Coordinator, runID string, want ...string) {
	t.Helper()
	msgs, err := c.ListRunMessages(context.Background(), runID, "")
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, m := range msgs {
		kinds = append(kinds, string(m.Kind))
	}
	joined := strings.Join(kinds, ",")
	for _, w := range want {
		if !strings.Contains(joined, w) {
			t.Fatalf("expected %q in kinds=%v", w, kinds)
		}
	}
}

// TestAskDeclineTargetNotRunningBlocking ensures blocking ask never spawns.
func TestAskDeclineTargetNotRunningBlocking(t *testing.T) {
	cfg := config.DefaultSubagentConfig
	cfg.Messaging.Routing.Allow = []string{"reviewer->auditor"}
	d, c, repo := askTestEnv(t, cfg, 1)
	registerAskHandler(d, "reviewer", func(ctx context.Context, _ runtime.Request) (json.RawMessage, error) {
		tool := &postMessageTool{dispatcher: d, cfg: cfg, repo: repo}
		out, err := tool.Execute(ctx, json.RawMessage(
			`{"kind":"ask","to_role":"auditor","body":"are you there?","wait_seconds":1}`,
		))
		if err != nil {
			return nil, err
		}
		return json.RawMessage(out), nil
	})
	result := spawnJoin(t, c, []subagents.Task{
		{ID: "rev-1", Name: "reviewer", AgentName: "reviewer", Timeout: 5 * time.Second},
	})
	var res map[string]any
	if err := json.Unmarshal(result.Results[0].Output, &res); err != nil {
		t.Fatal(err)
	}
	if res["status"] != "declined" || res["reason"] != agentmsg.DeclineTargetNotRunning {
		t.Fatalf("want declined target_not_running, got %+v", res)
	}
	if c.ReferralSpawnsUsed(result.Snapshot.RunID) != 0 {
		t.Fatal("blocking ask must not referral-spawn")
	}
}

// TestAskReferralSpawnNonBlocking: non-blocking ask with allow pair spawns auditor.
func TestAskReferralSpawnNonBlocking(t *testing.T) {
	cfg := config.DefaultSubagentConfig
	cfg.Messaging.Routing.Allow = []string{"reviewer->auditor"}
	d, c, repo := askTestEnv(t, cfg, 2)
	registerAskHandler(d, "auditor", func(ctx context.Context, req runtime.Request) (json.RawMessage, error) {
		return answerFromBrief(ctx, d, cfg, repo, req.Input)
	})
	registerAskHandler(d, "reviewer", func(ctx context.Context, _ runtime.Request) (json.RawMessage, error) {
		tool := &postMessageTool{dispatcher: d, cfg: cfg, repo: repo}
		out, err := tool.Execute(ctx, json.RawMessage(
			`{"kind":"ask","to_role":"auditor","body":"please audit"}`,
		))
		if err != nil {
			return nil, err
		}
		return json.RawMessage(out), nil
	})
	h, err := c.Spawn(context.Background(), []subagents.Task{
		{ID: "rev-1", Name: "reviewer", AgentName: "reviewer", Timeout: 8 * time.Second},
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	waitReferralAndAnswer(t, c, h.RunID())
	result, err := c.Join(context.Background(), h)
	if err != nil {
		t.Fatal(err)
	}
	if c.ReferralSpawnsUsed(result.Snapshot.RunID) < 1 {
		t.Fatal("referral spawn counter")
	}
	assertKindsContain(t, c, result.Snapshot.RunID, "ask", "answer")
}

func answerFromBrief(ctx context.Context, d *runtime.Dispatcher, cfg config.SubagentConfig, repo ledger.LedgerRepository, input json.RawMessage) (json.RawMessage, error) {
	tool := &postMessageTool{dispatcher: d, cfg: cfg, repo: repo}
	// Production shape: JSON string prompt with "ask_id: <id>\n...".
	var prompt string
	if err := json.Unmarshal(input, &prompt); err != nil {
		// Backward-compatible object brief for older tests.
		var brief map[string]any
		_ = json.Unmarshal(input, &brief)
		if id, _ := brief["ask_id"].(string); id != "" {
			prompt = "ask_id: " + id
		}
	}
	askID := ""
	for _, line := range strings.Split(prompt, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "ask_id:") {
			askID = strings.TrimSpace(strings.TrimPrefix(line, "ask_id:"))
			break
		}
	}
	if askID == "" {
		return json.RawMessage(`{"ok":true}`), nil
	}
	out, err := tool.Execute(ctx, json.RawMessage(
		`{"kind":"answer","body":"spawned reply","in_reply_to":"`+askID+`"}`,
	))
	if err != nil {
		return nil, err
	}
	return json.RawMessage(out), nil
}

func waitReferralAndAnswer(t *testing.T, c coordinator.Coordinator, runID string) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for c.ReferralSpawnsUsed(runID) < 1 {
		select {
		case <-deadline:
			t.Fatal("referral spawn did not increment counter")
		case <-time.After(20 * time.Millisecond):
		}
	}
	deadline2 := time.After(5 * time.Second)
	for {
		msgs, _ := c.ListRunMessages(context.Background(), runID, "")
		for _, m := range msgs {
			if m.Kind == agentmsg.KindAnswer {
				return
			}
		}
		select {
		case <-deadline2:
			t.Fatal("no answer from referral spawn")
		case <-time.After(30 * time.Millisecond):
		}
	}
}

// TestAskSpoofFromRoleServerStamp: From is always server-stamped from identity.
func TestAskSpoofFromRoleServerStamp(t *testing.T) {
	cfg := config.DefaultSubagentConfig
	tool, c, _, runID, taskID, ctx := setupPostMessageEnv(t, cfg)
	if _, err := tool.Execute(ctx, json.RawMessage(
		`{"kind":"ask","to_role":"auditor","body":"x","wait_seconds":0}`,
	)); err != nil {
		t.Fatal(err)
	}
	msgs, err := c.ListRunMessages(context.Background(), runID, taskID)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) == 0 {
		t.Fatal("expected persisted ask")
	}
	full, err := c.LoadMessageBody(context.Background(), msgs[0].ContentRef)
	if err != nil {
		t.Fatal(err)
	}
	if full.From.TaskID != taskID || full.From.Agent != "worker" {
		t.Fatalf("From=%+v, want task=%s agent=worker", full.From, taskID)
	}
}
