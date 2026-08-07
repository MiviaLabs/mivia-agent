package controller

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/coordinator"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// neverSettlingHandler never returns on its own: it blocks until its context
// is canceled, simulating a child whose coordinator run handle never settles
// (hung pool worker, stuck referral wait, dead executor). Without a
// controller-side join watchdog the coordinator's Join blocks on <-h.done
// forever and the workflow run parks at the current attempt; with the watchdog
// the controller cancels the child and settles the attempt timed_out within
// the bound instead of blocking indefinitely.
type neverSettlingHandler struct {
	mu      sync.Mutex
	invoked int
}

func (h *neverSettlingHandler) Invoke(ctx context.Context, _ runtime.Request) (json.RawMessage, error) {
	h.mu.Lock()
	h.invoked++
	h.mu.Unlock()
	<-ctx.Done()
	return nil, ctx.Err()
}

func (h *neverSettlingHandler) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.invoked
}

// TestAgentStepJoinWatchdogSettlesNeverSettlingChild pins the controller-side
// join bound: a child that never settles (no task timeout, no parent deadline,
// so coordinator.Join has no bound of its own) must not park the controller
// forever. The runner's injected join watchdog cancels the child and the
// attempt settles timed_out with an error naming the join timeout, so the run
// reaches a terminal status within the bound instead of staying 'running' at
// the current attempt.
func TestAgentStepJoinWatchdogSettlesNeverSettlingChild(t *testing.T) {
	d := runtime.New(runtime.Policy{})
	handler := &neverSettlingHandler{}
	if err := d.Register(runtime.Subagent, "dev", handler); err != nil {
		t.Fatal(err)
	}
	p := subagents.New(d, subagents.Policy{Workers: 1})
	coordRepo := ledger.NewMemoryLedgerRepository()
	coord := coordinator.New(coordRepo, p).WithRetryPolicy(coordinator.NoRetry)

	// The watchdog is the ONLY bound here: no parent deadline (Background),
	// no per-agent timeout (agentStepRequest derives none without a deadline),
	// so the old code blocks in coordinator.Join forever.
	runner := NewCoordinatorRunner(coord)
	runner.JoinWatchdog = 200 * time.Millisecond
	ctrl, repo := newErrorController(t, runner, "wfr-join-watchdog")

	started := time.Now()
	got, err := ctrl.Run(context.Background())
	elapsed := time.Since(started)

	if err == nil {
		t.Fatalf("run succeeded = %+v; want failure for a never-settling child", got)
	}
	if !strings.Contains(err.Error(), "join timed out") {
		t.Fatalf("error = %v, want it to name the join timeout", err)
	}
	if got.Status != workflowledger.RunStatusTimedOut {
		t.Fatalf("status = %q, want timed_out (a join watchdog expiry is a run timeout, not a cancel)", got.Status)
	}
	if elapsed > 15*time.Second {
		t.Fatalf("run settled after %s; want it bounded by the injected watchdog (~200ms), not blocking", elapsed)
	}
	if n := handler.count(); n != 1 {
		t.Fatalf("child invocations = %d, want exactly 1 (no re-dispatch, no retry)", n)
	}
	attempts, err := repo.ListStepAttempts(context.Background(), ctrl.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if len(attempts) != 1 || attempts[0].Status != workflowledger.AttemptStatusTimedOut {
		t.Fatalf("attempts = %+v, want exactly one timed_out attempt (wf_attempt_completed must be durable)", attempts)
	}
	if attempts[0].ErrorRef == "" {
		t.Fatal("attempt ErrorRef is empty; want the join-timeout detail persisted")
	}
	body, err := repo.LoadContent(context.Background(), attempts[0].ErrorRef)
	if err != nil {
		t.Fatalf("load error content: %v", err)
	}
	if !strings.Contains(string(body), "join timed out") {
		t.Fatalf("error content %q does not name the join timeout", body)
	}
}
