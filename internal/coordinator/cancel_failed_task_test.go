package coordinator

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
)

// cancelRaceOutcome records one spawn-cancel iteration's observed state.
type cancelRaceOutcome struct {
	invalidTransition bool
	canceled          bool
	terminal          bool
	sampleErr         error
}

// runCancelFailedRaceOnce spawns a failing task and cancels immediately, on a
// separate goroutine so Cancel's reconcileCancellation CAS races the pool's
// failure result and processResults' retry transition (no sleep-based pacing).
func runCancelFailedRaceOnce(c Coordinator) cancelRaceOutcome {
	h, err := c.Spawn(context.Background(), []subagents.Task{
		{ID: "t1", Name: "alwaysfail"},
	}, "")
	if err != nil {
		return cancelRaceOutcome{sampleErr: err}
	}
	go func(h *RunHandle) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = c.Cancel(ctx, h)
	}(h)

	result, err := c.Join(context.Background(), h)
	if err != nil || result == nil {
		return cancelRaceOutcome{sampleErr: err}
	}
	out := cancelRaceOutcome{canceled: true, terminal: result.Snapshot.Status == ledger.RunStatusCanceled}
	if result.Err != nil {
		msg := result.Err.Error()
		out.invalidTransition = strings.Contains(msg, "invalid state transition") ||
			strings.Contains(msg, "version conflict")
		if out.invalidTransition {
			out.sampleErr = result.Err
		}
	}
	for _, r := range result.Results {
		if r.TaskID == "t1" && r.Status != "canceled" {
			out.canceled = false
		}
	}
	return out
}

// TestCancelRacingFailedTask is the R2A-1 regression test: a Cancel racing a
// GENUINELY-FAILED task (the pool computed Status "failed" while poolCtx was
// still alive) must not let processResults attempt a retry transition against
// a task reconcileCancellation already claimed (running -> cancel_requested).
// That lost retry CAS previously joined an "invalid state transition" /
// "version conflict" artifact into the run error even though recordRunResults
// repaired the result surface. Every iteration must surface the task as
// canceled on a terminal run with a clean run error.
func TestCancelRacingFailedTask(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	d := runtime.New(runtime.Policy{})
	_ = d.Register(runtime.Subagent, "alwaysfail", staticHandler{err: errors.New("always fail")})
	p := subagents.New(d, subagents.Policy{Workers: 1})
	// Default retry policy (MaxRetries=3) via New(): a failed task is retryable,
	// which is what makes the retry transition race the cancellation.
	c := New(repo, p)

	// The interleaving (pool returns failed, reconcileCancellation CASes
	// running -> cancel_requested, processResults attempts retry_pending) is a
	// small fraction of runs under -race (the round-2 audit saw ~492/4000), so
	// loop enough iterations to observe it while keeping the test well under 30s.
	const iterations = 300
	var (
		invalidTransition int
		notCanceled       int
		notTerminal       int
		sampleErr         error
	)
	for i := 0; i < iterations; i++ {
		out := runCancelFailedRaceOnce(c)
		if out.invalidTransition {
			invalidTransition++
			if sampleErr == nil {
				sampleErr = out.sampleErr
			}
		}
		if !out.canceled {
			notCanceled++
		}
		if !out.terminal {
			notTerminal++
		}
	}

	if invalidTransition > 0 {
		t.Fatalf("cancel racing a failed task produced %d/%d iterations with 'invalid state transition'/'version conflict' in the run error (sample: %v)",
			invalidTransition, iterations, sampleErr)
	}
	if notCanceled > 0 {
		t.Fatalf("cancel racing a failed task left %d/%d iterations with task status != canceled", notCanceled, iterations)
	}
	if notTerminal > 0 {
		t.Fatalf("cancel racing a failed task left %d/%d iterations with a non-terminal run (want %q)",
			notTerminal, iterations, ledger.RunStatusCanceled)
	}
}
