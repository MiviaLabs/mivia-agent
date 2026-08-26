package clichat

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	cliorchestrate "github.com/MiviaLabs/mivia-agent/internal/cliorchestrate"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
)

// TestDispatchTasksSyncCancelPropagatesToOrphanedRun is the regression for
// two reported symptoms that turned out to be the same bug: a subagent
// dispatched via dispatch_tasks stuck "running" long after the user
// canceled, and nothing about it preserved in history on quit/resume.
//
// dispatch_tasks' default (wait="run", synchronous) path blocks the caller
// on coordinator.Join, but the run's OWN execution context
// (RunHandle.poolCtx) was rooted in context.Background(), never derived
// from the caller's own context. So when the caller's context died (turn
// canceled, or the tool's own outer timeout fired), Join returned promptly
// - but the coordinator's actual subagent work kept running, orphaned and
// invisible, forever: nothing told it to stop.
//
// For a synchronous dispatch this is wrong: the caller's mental model is
// "this is part of my current turn, canceling should stop it" - unlike
// wait="none", which is explicitly designed to survive the calling turn
// and is stopped only via the separate cancel_run tool. This test proves
// the synchronous path now propagates the caller's own cancellation into
// the run.
func TestDispatchTasksSyncCancelPropagatesToOrphanedRun(t *testing.T) {
	unblocked := make(chan struct{})
	d := runtime.New(runtime.Policy{MaxDepth: 3})
	err := d.Register(runtime.Subagent, "oneshot", handlerFunc(func(ctx context.Context, req runtime.Request) (json.RawMessage, error) {
		<-ctx.Done()
		close(unblocked)
		return nil, ctx.Err()
	}))
	if err != nil {
		t.Fatal(err)
	}
	tool := cliorchestrate.NewDispatchTasksToolConfigured(d, config.DefaultSubagentConfig, ledger.NewMemoryLedgerRepository(), testAgentRegistry(t, "oneshot"))

	// No timeout_seconds override: the TASK's own deadline stays at the
	// ~12h orchestration default (DispatchOrchestrationBudgetOutlivesTaskBudget),
	// deliberately far longer than this test's patience. The outer caller
	// ctx below is canceled independently and much sooner, on a clock that
	// shares nothing with the task's own budget - isolating "did the
	// caller's cancellation propagate into the run" from "did the task's
	// own pre-existing timeout happen to fire around the same time" (a
	// confound an earlier version of this test had when both used the same
	// timeout_seconds value).
	args := json.RawMessage(`{"tasks": [{"id":"slow","agent":"oneshot","prompt":"block forever"}]}`)
	ctx, cancel := context.WithCancel(context.Background())

	execDone := make(chan struct{})
	go func() {
		_, _ = tool.Execute(ctx, args)
		close(execDone)
	}()
	// Give Execute a moment to actually start the run and block on Join,
	// then cancel the CALLER's context independently of any task timeout.
	time.Sleep(200 * time.Millisecond)
	cancel()
	<-execDone

	// The caller's own context has now died. The blocked subagent handler
	// must be unblocked within a bounded grace window, not leak forever
	// waiting on its own ~12h task-level deadline.
	select {
	case <-unblocked:
	case <-time.After(10 * time.Second):
		t.Fatal("blocked task handler never unblocked after the caller's context died - the run is orphaned, not canceled")
	}
}
