package cli

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/coordinator"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
)

// setupPostMessageEnv seeds a live run/task and wires the dispatcher→coord map
// so postMessageTool.Execute hits the real tool path (not just coordinator APIs).
func setupPostMessageEnv(t *testing.T, cfg config.SubagentConfig) (
	*postMessageTool, coordinator.Coordinator, ledger.LedgerRepository, string, string, context.Context,
) {
	t.Helper()
	repo := ledger.NewMemoryLedgerRepository()
	d := runtime.New(runtime.Policy{})
	_ = d.Register(runtime.Subagent, "worker", handlerFunc(func(context.Context, runtime.Request) (json.RawMessage, error) {
		return json.RawMessage(`{"ok":true}`), nil
	}))
	c := coordinator.New(repo, subagents.New(d, subagents.Policy{Workers: 1}))
	coordinators.Store(d, c)
	coordinatorRepos.Store(d, repo)
	t.Cleanup(func() {
		coordinators.Delete(d)
		coordinatorRepos.Delete(d)
	})
	// Seed durable run/task for PostTaskMessage.
	const runID, taskID = "cov-run", "cov-task"
	if err := repo.CreateRun(context.Background(), "", ledger.RunSnapshot{
		RunID: runID, Status: ledger.RunStatusRunning,
	}); err != nil {
		t.Fatal(err)
	}
	if err := repo.CreateTask(context.Background(), ledger.TaskSnapshot{
		RunID: runID, TaskID: taskID, Status: string(ledger.TaskStatusRunning), Version: 1,
	}); err != nil {
		t.Fatal(err)
	}
	ctx := runtime.ContextWithTaskIdentity(context.Background(), runtime.TaskIdentity{
		RunID: runID, TaskID: taskID, Agent: "worker",
	})
	tool := &postMessageTool{dispatcher: d, cfg: cfg, repo: repo}
	return tool, c, repo, runID, taskID, ctx
}

func TestPostMessageFindingQuotaExhausted(t *testing.T) {
	cfg := config.DefaultSubagentConfig
	cfg.Messaging.MaxMessagesPerTask = 1
	tool, _, _, _, _, ctx := setupPostMessageEnv(t, cfg)
	if _, err := tool.Execute(ctx, json.RawMessage(`{"kind":"finding","body":"first"}`)); err != nil {
		t.Fatal(err)
	}
	// Second finding burns quota at ConsumeMessageQuota (lines 108-109).
	if _, err := tool.Execute(ctx, json.RawMessage(`{"kind":"finding","body":"second"}`)); err == nil {
		t.Fatal("expected quota exceeded")
	} else if !strings.Contains(err.Error(), "max_messages_per_task") {
		t.Fatalf("err=%v", err)
	}
}

func TestPostMessageFindingPostFailsMissingTask(t *testing.T) {
	cfg := config.DefaultSubagentConfig
	tool, _, _, _, _, _ := setupPostMessageEnv(t, cfg)
	// Identity points at a task that does not exist → PostTaskMessage fails after quota.
	ctx := runtime.ContextWithTaskIdentity(context.Background(), runtime.TaskIdentity{
		RunID: "cov-run", TaskID: "no-such-task", Agent: "worker",
	})
	if _, err := tool.Execute(ctx, json.RawMessage(`{"kind":"finding","body":"x"}`)); err == nil {
		t.Fatal("expected post failure")
	}
}

func TestPostMessageQuestionParkAlreadyHeld(t *testing.T) {
	cfg := config.DefaultSubagentConfig
	tool, c, _, runID, taskID, ctx := setupPostMessageEnv(t, cfg)
	// Pre-hold park so waitForAnswer's ParkQuestion fails (lines 138-140).
	_, unpark, err := c.ParkQuestion(runID, taskID, "already")
	if err != nil {
		t.Fatal(err)
	}
	defer unpark()
	if _, err := tool.Execute(ctx, json.RawMessage(`{"kind":"question","body":"q","wait_seconds":1}`)); err == nil {
		t.Fatal("expected park held error")
	}
}

func TestPostMessageQuestionQuotaAfterPark(t *testing.T) {
	cfg := config.DefaultSubagentConfig
	cfg.Messaging.MaxMessagesPerTask = 1
	tool, _, _, _, _, ctx := setupPostMessageEnv(t, cfg)
	// Burn quota with a finding first.
	if _, err := tool.Execute(ctx, json.RawMessage(`{"kind":"finding","body":"burn"}`)); err != nil {
		t.Fatal(err)
	}
	// Question parks first, then ConsumeMessageQuota fails (150-152); defer unpark (146-147).
	if _, err := tool.Execute(ctx, json.RawMessage(`{"kind":"question","body":"q","wait_seconds":1}`)); err == nil {
		t.Fatal("expected quota error after park")
	}
}

func TestPostMessageQuestionPostFailsAfterPark(t *testing.T) {
	cfg := config.DefaultSubagentConfig
	tool, _, repo, runID, taskID, ctx := setupPostMessageEnv(t, cfg)
	// Delete task so PostTaskMessage fails after successful Park (153-155).
	// Memory repo has no DeleteTask — use wrong run identity that has park key
	// but GetTask fails. Park uses id from context; Post uses same.
	// Instead: delete by overwriting is hard. Point Post at wrong run via
	// removing the task from status by creating a second env.
	_ = repo
	// Force TransitionToAwaitingInput failure: park succeeds, post succeeds, but
	// task is already terminal so Transition fails (157-158).
	if err := repo.CompareAndSetTaskStatus(context.Background(), runID, taskID, 1, string(ledger.TaskStatusCompleted)); err != nil {
		// version may differ - get current
		snap, _ := repo.GetTask(context.Background(), runID, taskID)
		_ = repo.CompareAndSetTaskStatus(context.Background(), runID, taskID, snap.Version, string(ledger.TaskStatusCompleted))
	}
	// Park needs live registry; completed task still allows park in registry.
	// PostTaskMessage requires task exists (it does). TransitionToAwaitingInput
	// fails because status is completed not running.
	if _, err := tool.Execute(ctx, json.RawMessage(`{"kind":"question","body":"q","wait_seconds":1}`)); err == nil {
		t.Fatal("expected transition/post failure for terminal task")
	}
}

func TestPostMessageQuestionDefaultWaitAndDeadlineCap(t *testing.T) {
	cfg := config.DefaultSubagentConfig
	tool, c, _, runID, taskID, ctx := setupPostMessageEnv(t, cfg)
	// wait_seconds omitted (0) → default 60; tight deadline caps wait (128-132).
	dctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	// Answer quickly so test finishes.
	go func() {
		// Wait until park exists.
		deadline := time.After(2 * time.Second)
		for {
			if c.CountPendingQuestions(runID, taskID) == 1 {
				_ = c.DeliverAnswer(runID, taskID, "", "ok")
				return
			}
			select {
			case <-deadline:
				return
			case <-time.After(5 * time.Millisecond):
			}
		}
	}()
	// Empty wait_seconds in JSON → 0 → default path.
	out, err := tool.Execute(dctx, json.RawMessage(`{"kind":"question","body":"need help"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "answered") && !strings.Contains(out, "no_answer") {
		t.Fatalf("out=%s", out)
	}
}

func TestPostMessageAskInvalidKindAndNoIdentity(t *testing.T) {
	cfg := config.DefaultSubagentConfig
	tool, _, _, _, _, ctx := setupPostMessageEnv(t, cfg)
	if _, err := tool.Execute(ctx, json.RawMessage(`{"kind":"nope","body":"x"}`)); err == nil {
		t.Fatal("invalid kind")
	}
	if _, err := tool.Execute(context.Background(), json.RawMessage(`{"kind":"finding","body":"x"}`)); err == nil {
		t.Fatal("no identity")
	}
	if _, err := tool.Execute(ctx, json.RawMessage(`{"kind":"ask","body":"x"}`)); err == nil {
		t.Fatal("ask without to_role")
	}
}

func TestPostMessageAskCancelWhileParked(t *testing.T) {
	cfg := config.DefaultSubagentConfig
	cfg.Messaging.Routing.Allow = []string{"worker->peer"}
	// Live peer that never answers.
	d := runtime.New(runtime.Policy{})
	repo := ledger.NewMemoryLedgerRepository()
	hold := make(chan struct{})
	_ = d.Register(runtime.Subagent, "peer", handlerFunc(func(ctx context.Context, _ runtime.Request) (json.RawMessage, error) {
		<-hold
		return json.RawMessage(`{}`), nil
	}))
	_ = d.Register(runtime.Subagent, "worker", handlerFunc(func(ctx context.Context, _ runtime.Request) (json.RawMessage, error) {
		tool := &postMessageTool{dispatcher: d, cfg: cfg, repo: repo}
		// Short cancelable wait.
		cctx, cancel := context.WithCancel(ctx)
		go func() {
			select {
			case <-time.After(50 * time.Millisecond):
				cancel()
			case <-ctx.Done():
			}
		}()
		_, err := tool.Execute(cctx, json.RawMessage(
			`{"kind":"ask","to_role":"peer","body":"q","wait_seconds":30}`,
		))
		close(hold)
		if err == nil {
			return nil, context.Canceled
		}
		return json.RawMessage(`{"canceled":true}`), nil
	}))
	c := coordinator.New(repo, subagents.New(d, subagents.Policy{Workers: 2}))
	coordinators.Store(d, c)
	coordinatorRepos.Store(d, repo)
	t.Cleanup(func() {
		coordinators.Delete(d)
		coordinatorRepos.Delete(d)
	})
	h, err := c.Spawn(context.Background(), []subagents.Task{
		{ID: "peer-1", Name: "peer", AgentName: "peer", Timeout: 5 * time.Second},
		{ID: "w1", Name: "worker", AgentName: "worker", Timeout: 5 * time.Second},
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = c.Join(context.Background(), h)
}

func TestRunTaskResultsNilRepoWrapper(t *testing.T) {
	// Hits runTaskResults → runTaskResultsWithRepo(nil, ...) (lines 130-131).
	result := &coordinator.RunResult{
		Snapshot: ledger.RunSnapshot{RunID: "r", Tasks: []ledger.TaskSnapshot{
			{TaskID: "t1", Status: "completed"},
		}},
		Results: []subagents.Result{{TaskID: "t1", Status: "completed"}},
	}
	got := runTaskResults(result, 4096)
	if len(got) != 1 {
		t.Fatalf("got=%+v", got)
	}
	if runTaskResults(nil, 4096) != nil {
		t.Fatal("nil")
	}
}
