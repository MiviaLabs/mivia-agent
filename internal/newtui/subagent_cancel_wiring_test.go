// subagent_cancel_wiring_test.go is the end-to-end proof for the TUI's
// per-subagent cancel keys. It exercises every seam in production order:
// registerSubagentProgress installs the real registrar,
// uiadapter.NewSubagentThreads hands its route table to the CLI side
// through it, a REAL dispatch_tasks Execute publishes its tasks' routes
// into that table, and ports.SubagentThreads' two cancel entry points then
// reach the coordinator.
//
// Both entry points matter: CancelSubagentTask and CancelSubagentToolCall
// share one route lookup (uiadapter's resolveTaskRoute), so an empty route
// table made BOTH inert, not just the whole-task one.
//
// This package is where the test belongs: it is the only one that imports
// both halves (internal/cli and internal/uiadapter), which is exactly why
// it is the package that wires them - internal/uiadapter must never import
// internal/cli* (INV-TUI-29).
package newtui

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-ai-sdk/provider"
	"github.com/MiviaLabs/mivia-ai-sdk/toolcallctx"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/cliorchestrate"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/uiadapter"
)

// wiredRowID is the panel row id a dispatch of task "alpha" under tool
// call "call_wire_1" produces. Not a value this test invents: it is what
// internal/ui/screen/conversation's dispatchTaskIDsAndNames builds live,
// and what internal/uiadapter/subagent_reconstruct.go rebuilds after a
// resume. Cancelling by it is what pressing the key on that row does.
const wiredRowID = "call_wire_1:alpha"

// cancelWiringHandler is a runtime.Handler that reports when it starts and
// then blocks until its context is cancelled, so a test can act on a task
// that is genuinely in flight.
type cancelWiringHandler struct {
	started chan struct{}
}

func (h cancelWiringHandler) Invoke(ctx context.Context, _ runtime.Request) (json.RawMessage, error) {
	close(h.started)
	<-ctx.Done()
	return nil, ctx.Err()
}

// restoreRegistrars puts every package-level uiadapter registrar back the
// way it was; registerSubagentProgress writes three of them.
func restoreRegistrars(t *testing.T) {
	t.Helper()
	prevProgress := uiadapter.SubagentProgressRegistrar
	prevBus := uiadapter.SessionBusRegistrar
	prevRoutes := uiadapter.SubagentTaskRouteRegistrar
	t.Cleanup(func() {
		uiadapter.SubagentProgressRegistrar = prevProgress
		uiadapter.SessionBusRegistrar = prevBus
		uiadapter.SubagentTaskRouteRegistrar = prevRoutes
		cliorchestrate.SetSubagentTaskRouteSink(nil)
	})
}

// dispatchOneBlockingTask runs the whole production wiring and dispatches
// one still-running task through the real dispatch_tasks tool, returning
// the UI registry its route was published into and the ledger repository
// holding it.
func dispatchOneBlockingTask(t *testing.T) (*uiadapter.SubagentThreads, ledger.LedgerRepository) {
	t.Helper()
	restoreRegistrars(t)
	// The real production wiring, called exactly as RunTUI calls it.
	registerSubagentProgress()

	// The real registry the conversation screen holds. SessionPool builds
	// one of these; building it directly here keeps the dispatch half
	// unmocked rather than the UI half.
	threads := uiadapter.NewSubagentThreads()

	d := runtime.New(runtime.Policy{MaxDepth: 4})
	t.Cleanup(d.Close)
	repo := ledger.NewMemoryLedgerRepository()
	started := make(chan struct{})
	if err := d.Register(runtime.Subagent, "blocker", cancelWiringHandler{started: started}); err != nil {
		t.Fatal(err)
	}
	reg := agents.NewRegistry()
	if err := reg.Publish(agents.ResolvedAgent{Name: "blocker"}); err != nil {
		t.Fatal(err)
	}
	tool := cliorchestrate.NewDispatchTasksToolConfigured(d, config.DefaultSubagentConfig, repo, reg)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	ctx = runtime.ContextWithCaller(ctx, runtime.Caller{SessionID: "cancel-wiring-session"})
	ctx = toolcallctx.WithToolCall(ctx, provider.ToolCall{ID: "call_wire_1", Name: cliorchestrate.ToolDispatchTasks})

	// wait="none" so Execute returns while the task is still running -
	// the only state in which cancelling it is meaningful.
	if _, err := tool.Execute(ctx, json.RawMessage(
		`{"wait":"none","tasks":[{"id":"alpha","agent":"blocker","prompt":"a"}]}`)); err != nil {
		t.Fatalf("dispatch_tasks Execute: %v", err)
	}
	select {
	case <-started:
	case <-time.After(30 * time.Second):
		t.Fatal("dispatched task never started")
	}
	return threads, repo
}

// TestSubagentCancelWiring_TaskIsCancellableThroughTheUIRegistry proves the
// whole-task cancel key (2b) now reaches coordinator.CancelTask for a real
// dispatched task, and that the task ends canceled in the ledger.
func TestSubagentCancelWiring_TaskIsCancellableThroughTheUIRegistry(t *testing.T) {
	threads, repo := dispatchOneBlockingTask(t)

	ok, err := threads.CancelSubagentTask(wiredRowID)
	if err != nil {
		t.Fatalf("CancelSubagentTask: %v", err)
	}
	if !ok {
		t.Fatal("CancelSubagentTask reported ok=false; the live dispatch published no route for this row id")
	}
	assertTaskCanceled(t, repo, wiredRowID)
}

// TestSubagentCancelWiring_ToolCallCancelResolvesTheSameRoute proves the
// per-tool-call cancel key (2c) was unblocked by the same write site: it
// resolves through the identical route table, so before routes were
// published it could never reach the ToolCanceler its own producer half
// (cliorchestrate.ToolCancelReadyHook) was already registering. Here a
// canceler is registered directly on the run handle - standing in for the
// nested agent loop that registers one in production - and the UI call
// must reach it.
func TestSubagentCancelWiring_ToolCallCancelResolvesTheSameRoute(t *testing.T) {
	threads, repo := dispatchOneBlockingTask(t)

	// Exactly one coordinator is registered at this point: InitCoordinator
	// keys the package map on the dispatcher, and each helper call's
	// t.Cleanup(d.Close) deletes its own entry before the next test runs.
	coord, found := cliorchestrate.ActiveCoordinator()
	if !found {
		t.Fatal("no coordinator registered after a real dispatch")
	}
	runID := singleRunID(t, repo)
	if coord.HandleForRun(runID) == nil {
		t.Fatalf("the registered coordinator does not own run %q; this test resolved the wrong one", runID)
	}
	var gotToolCallID string
	coord.RegisterSubagentToolCanceler(runID, wiredRowID, func(id string) bool {
		gotToolCallID = id
		return true
	})

	ok, err := threads.CancelSubagentToolCall(wiredRowID, "tool-call-1")
	if err != nil {
		t.Fatalf("CancelSubagentToolCall: %v", err)
	}
	if !ok {
		t.Fatal("CancelSubagentToolCall reported ok=false; the route table did not resolve this row id")
	}
	if gotToolCallID != "tool-call-1" {
		t.Fatalf("ToolCanceler invoked with %q, want %q", gotToolCallID, "tool-call-1")
	}

	if _, err := threads.CancelSubagentTask(wiredRowID); err != nil {
		t.Fatalf("cleanup CancelSubagentTask: %v", err)
	}
}

// singleRunID returns the id of the only run in repo.
func singleRunID(t *testing.T, repo ledger.LedgerRepository) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	runs, err := repo.ListRuns(ctx)
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("ListRuns returned %d runs, want exactly 1", len(runs))
	}
	return runs[0].RunID
}

// assertTaskCanceled polls the ledger until taskID is recorded canceled.
func assertTaskCanceled(t *testing.T, repo ledger.LedgerRepository, taskID string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	deadline := time.Now().Add(20 * time.Second)
	var last string
	for time.Now().Before(deadline) {
		runs, err := repo.ListRuns(ctx)
		if err != nil {
			t.Fatalf("ListRuns: %v", err)
		}
		for _, r := range runs {
			tasks, err := repo.ListTasks(ctx, r.RunID)
			if err != nil {
				t.Fatalf("ListTasks: %v", err)
			}
			for _, task := range tasks {
				if task.TaskID != taskID {
					continue
				}
				last = string(task.Status)
				if last == string(ledger.TaskStatusCanceled) {
					return
				}
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("task %q status = %q, want %q", taskID, last, ledger.TaskStatusCanceled)
}
