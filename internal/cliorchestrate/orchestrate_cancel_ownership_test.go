package cliorchestrate

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
)

// TestRunThroughCoordinator_DoesNotCancelIdempotentlySharedRun is the
// regression for a gap a code review of the orphaned-run cancellation fix
// (cancelOrphanedRun) found: Spawn's idempotency-key lookup can hand a
// synchronous (wait=run) caller the SAME *RunHandle a DIFFERENT, concurrent
// caller already owns (e.g. an async wait=none dispatch reusing the same
// idempotency_key). Without the isNew signal, RunThroughCoordinator's own
// context dying would cancel a run it never created - stopping work an
// unrelated caller is still relying on, exactly the cross-caller
// cancellation its own reasoning said could not happen.
func TestRunThroughCoordinator_DoesNotCancelIdempotentlySharedRun(t *testing.T) {
	unblocked := make(chan struct{})
	d := runtime.New(runtime.Policy{MaxDepth: 3})
	if err := d.Register(runtime.Subagent, "oneshot", handlerFunc(func(ctx context.Context, req runtime.Request) (json.RawMessage, error) {
		<-ctx.Done()
		close(unblocked)
		return nil, ctx.Err()
	})); err != nil {
		t.Fatal(err)
	}
	repo := ledger.NewMemoryLedgerRepository()
	tasks := []subagents.Task{{ID: "slow", Name: "oneshot", AgentName: "oneshot", Input: json.RawMessage(`"block forever"`)}}

	// Both calls must scope the idempotency key identically. scopedKey
	// (internal/coordinator/spawn.go) mixes in the ctx's caller session id
	// when one is present, and RunThroughCoordinator synthesizes a FRESH
	// random session id for any ctx that carries none - so two calls each
	// passing a bare context.Background() would silently scope to two
	// DIFFERENT keys and spawn two unrelated runs, defeating this test's
	// premise. Stamp one shared caller identity up front so both calls
	// resolve to the same scoped key, the same way two turns in one real
	// session actually would.
	sharedCaller := runtime.Caller{SessionID: "shared-session"}
	baseCtx := runtime.ContextWithCaller(context.Background(), sharedCaller)

	// Simulate a concurrent caller (e.g. an async wait=none dispatch) that
	// already owns this run under the shared idempotency key.
	c := InitCoordinator(d, config.DefaultSubagentConfig, repo)
	preExisting, err := c.Spawn(baseCtx, tasks, "shared-key")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = c.Cancel(cleanupCtx, preExisting)
	})

	// RunThroughCoordinator (the synchronous wait=run path) now hits the
	// SAME idempotent handle - isNew must be false - and its own caller
	// context dies independently and much sooner than the task's own
	// long-lived deadline.
	ctx, cancel := context.WithCancel(baseCtx)
	execDone := make(chan struct{})
	go func() {
		_, _, _ = RunThroughCoordinator(ctx, d, config.DefaultSubagentConfig, tasks, "shared-key", repo)
		close(execDone)
	}()
	time.Sleep(200 * time.Millisecond)
	cancel()
	<-execDone

	// The run must NOT have been canceled by RunThroughCoordinator, since
	// it did not create it - it only hit the existing handle.
	select {
	case <-unblocked:
		t.Fatal("RunThroughCoordinator canceled a run it did not create (isNew=false was not honored)")
	case <-time.After(500 * time.Millisecond):
		// Expected: the task is still blocked, untouched by the caller
		// that merely hit the same idempotency key.
	}
}
