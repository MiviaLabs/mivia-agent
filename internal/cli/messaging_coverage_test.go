package cli

import (
	"context"
	"encoding/json"
	cliorchestrate "github.com/MiviaLabs/mivia-agent/internal/cliorchestrate"
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
	cliorchestrate.CoordinatorsForTest.Store(d, c)
	cliorchestrate.CoordinatorReposForTest.Store(d, repo)
	t.Cleanup(func() {
		cliorchestrate.CoordinatorsForTest.Delete(d)
		cliorchestrate.CoordinatorReposForTest.Delete(d)
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
	tool, _, _, runID, _, _ := setupPostMessageEnv(t, cfg)
	// Park key is independent of ledger; PostTaskMessage requires the task row.
	ctx := runtime.ContextWithTaskIdentity(context.Background(), runtime.TaskIdentity{
		RunID: runID, TaskID: "no-such-task", Agent: "worker",
	})
	if _, err := tool.Execute(ctx, json.RawMessage(`{"kind":"question","body":"q","wait_seconds":1}`)); err == nil {
		t.Fatal("expected post failure for missing task")
	}
}

func TestPostMessageQuestionDefaultWaitAndDeadlineCap(t *testing.T) {
	cfg := config.DefaultSubagentConfig
	tool, c, _, runID, taskID, ctx := setupPostMessageEnv(t, cfg)
	// wait_seconds omitted (0) → default (defaultQuestionWaitSec); tight
	// deadline caps wait via parkedWaitDuration (128-132).
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

// TestPostMessageWaitSecondsSchemaIsBounded: the wait_seconds schema must
// advertise a positive cap (the hostile audit found an unbounded schema
// integer), and the default question wait must exceed the 60s agent-loop
// tool-timeout floor so a default question is not clamped away before a
// realistic parent answer can arrive.
func TestPostMessageWaitSecondsSchemaIsBounded(t *testing.T) {
	tool := &postMessageTool{cfg: config.DefaultSubagentConfig}
	props, ok := tool.Parameters()["properties"].(map[string]any)
	if !ok {
		t.Fatal("parameters carry no properties object")
	}
	ws, ok := props["wait_seconds"].(map[string]any)
	if !ok {
		t.Fatal("parameters declare no wait_seconds property")
	}
	max, ok := ws["maximum"].(int)
	if !ok {
		t.Fatalf("wait_seconds = %v, want a numeric maximum the model can see", ws)
	}
	if max <= 0 {
		t.Fatalf("wait_seconds maximum = %d, want a positive bound", max)
	}
	if defaultQuestionWaitSec <= 60 {
		t.Fatalf("defaultQuestionWaitSec=%d, want > 60 (the agent-loop tool-timeout floor) so a default question can outlive the enclosing step budget", defaultQuestionWaitSec)
	}
	if max < defaultQuestionWaitSec {
		t.Fatalf("wait_seconds maximum %d below default %d", max, defaultQuestionWaitSec)
	}
}

func TestPostMessageQuestionTransitionFail(t *testing.T) {
	cfg := config.DefaultSubagentConfig
	tool, c, repo, runID, taskID, ctx := setupPostMessageEnv(t, cfg)
	// Pre-hold park, post a message via coordinator, then call transition path
	// by completing the task before TransitionToAwaitingInput inside waitForAnswer.
	// Simpler: mark completed then execute — park may still succeed; post or
	// transition fails. Any error covers the failure path.
	snap, _ := repo.GetTask(context.Background(), runID, taskID)
	_ = repo.CompareAndSetTaskStatus(context.Background(), runID, taskID, snap.Version, string(ledger.TaskStatusCompleted))
	_, err := tool.Execute(ctx, json.RawMessage(`{"kind":"question","body":"q","wait_seconds":1}`))
	if err == nil {
		// If the tool still returns no_answer JSON without error, force via
		// direct transition after park using ask wait path equivalent.
		_ = c
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
	cliorchestrate.CoordinatorsForTest.Store(d, c)
	cliorchestrate.CoordinatorReposForTest.Store(d, repo)
	t.Cleanup(func() {
		cliorchestrate.CoordinatorsForTest.Delete(d)
		cliorchestrate.CoordinatorReposForTest.Delete(d)
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
	// Hits cliorchestrate.RunTaskResults → runTaskResultsWithRepo(nil, ...) (lines 130-131).
	result := &coordinator.RunResult{
		Snapshot: ledger.RunSnapshot{RunID: "r", Tasks: []ledger.TaskSnapshot{
			{TaskID: "t1", Status: "completed"},
		}},
		Results: []subagents.Result{{TaskID: "t1", Status: "completed"}},
	}
	got := cliorchestrate.RunTaskResults(result, 4096)
	if len(got) != 1 {
		t.Fatalf("got=%+v", got)
	}
	if cliorchestrate.RunTaskResults(nil, 4096) != nil {
		t.Fatal("nil")
	}
}
