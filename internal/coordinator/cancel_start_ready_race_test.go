package coordinator

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
)

// TestCancelRacingStartReady is the D2 regression test: an immediate Cancel
// racing a task's queued -> running dispatch (startReady) must not record the
// task as failed with an "invalid state transition" run error. When
// reconcileCancellation CASes queued -> cancel_requested first, startReady's
// queued -> running CAS loses and the task must instead surface as canceled
// without joining an invalid-transition artifact into the run error.
func TestCancelRacingStartReady(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	d := runtime.New(runtime.Policy{})
	_ = d.Register(runtime.Subagent, "worker", staticHandler{out: json.RawMessage(`{"ok":true}`)})
	p := subagents.New(d, subagents.Policy{Workers: 1})
	c := New(repo, p)

	const iterations = 100
	var (
		mu                sync.Mutex
		invalidTransition int
	)
	for i := 0; i < iterations; i++ {
		h, err := c.Spawn(context.Background(), []subagents.Task{
			{ID: "t1", Name: "worker"},
		}, "")
		if err != nil {
			t.Fatal(err)
		}

		// Cancel immediately after spawn, on a separate goroutine so Cancel and
		// startReady's queued -> running dispatch race.
		go func(h *RunHandle) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = c.Cancel(ctx, h)
		}(h)

		result, err := c.Join(context.Background(), h)
		if err != nil {
			t.Fatal(err)
		}
		if result == nil {
			t.Fatal("expected non-nil result")
		}
		if result.Err != nil && strings.Contains(result.Err.Error(), "invalid state transition") {
			mu.Lock()
			invalidTransition++
			mu.Unlock()
		}
	}

	if invalidTransition > 0 {
		t.Fatalf("cancel racing startReady produced %d/%d iterations with 'invalid state transition' in the run error",
			invalidTransition, iterations)
	}
}
