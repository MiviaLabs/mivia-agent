package cliorchestrate

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/coordinator"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
)

// wedgedCoordinator embeds the Coordinator interface and overrides only
// Join/Inspect/Cancel to simulate the exact production failure observed on
// run-BAUJAKBOCGZDDXLQRWDJN436OE: a task stuck "running" forever (e.g. an
// upstream HTTP call ignoring its context) so coordinator.Join never fires
// h.done. Embedded interface = unexpected method calls panic loudly.
type wedgedCoordinator struct {
	coordinator.Coordinator

	mu       sync.Mutex
	canceled int
}

func (w *wedgedCoordinator) Join(ctx context.Context, _ *coordinator.RunHandle) (*coordinator.RunResult, error) {
	<-ctx.Done() // wedged: returns only when the join budget escapes
	return nil, ctx.Err()
}

func (w *wedgedCoordinator) Inspect(_ context.Context, _ *coordinator.RunHandle) (ledger.RunSnapshot, error) {
	return ledger.RunSnapshot{RunID: "run-wedged", Status: ledger.RunStatusRunning}, nil
}

func (w *wedgedCoordinator) Cancel(_ context.Context, _ *coordinator.RunHandle) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.canceled++
	return nil
}

// TestJoinRunTool_TimeoutEscapesAndCancelsWedgedRun pins the BUG-D fix: a
// join against a wedged run must (1) return within timeout_seconds instead
// of trapping the caller until their whole turn dies, (2) answer with a
// join_timeout envelope carrying run_id + latest status + inspect hint, and
// (3) fire a graceful detached cancel so the wedged run finalizes instead of
// leaking. This is the live regression from tonight's auditor-task runaway.
func TestJoinRunTool_TimeoutEscapesAndCancelsWedgedRun(t *testing.T) {
	// Mirror the proven fixture shape from orchestrate_lifecycle_test.go
	// (real repo row + real Spawn-produced handle), swapping ONLY the
	// stored coordinator for the wedge stub.
	repo := ledger.NewMemoryLedgerRepository()
	const runID = "run-wedged"
	if err := repo.CreateRun(context.Background(), "wedge", ledger.RunSnapshot{RunID: runID, Status: ledger.RunStatusRunning}); err != nil {
		t.Fatal(err)
	}
	d := runtime.New(runtime.Policy{})
	spawnCoord := coordinator.New(repo, subagents.New(runtime.New(runtime.Policy{}), subagents.Policy{Workers: 1}))
	h, err := spawnCoord.Spawn(context.Background(), nil, "wedge")
	if err != nil {
		t.Fatal(err)
	}
	wc := &wedgedCoordinator{}
	runHandles.Store(runID, &orchestrationHandle{
		coord: wc, handle: h, repo: repo, dispatcher: d,
		principal: orchestrationPrincipal{sessionID: "session-wedge"},
	})
	defer runHandles.Delete(runID)

	ctx := runtime.ContextWithCaller(context.Background(), runtime.Caller{SessionID: "session-wedge"})
	start := time.Now()
	out, err := (&joinRunTool{dispatcher: d, cfg: config.SubagentConfig{}, repo: repo}).Execute(ctx,
		json.RawMessage(`{"run_id":"run-wedged","timeout_seconds":1}`))
	if err != nil {
		t.Fatalf("join-timeout must be a modeled outcome, not a Go error: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("join blocked %v; BUG-D says it must escape at ~timeout_seconds", elapsed)
	}
	for _, want := range []string{"join_timeout", "run-wedged", "running", "inspect_agents"} {
		if !strings.Contains(out, want) {
			t.Errorf("envelope missing %q: %s", want, out)
		}
	}
	deadline := time.Now().Add(3 * time.Second)
	for wc.canceledCount() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if n := wc.canceledCount(); n == 0 {
		t.Error("graceful cancel was never dispatched for the wedged run")
	}
}

func configForJoinTest() struct{} { return struct{}{} } // removed below

// canceledCount accessor keeps the mutex discipline local to the stub.
func (w *wedgedCoordinator) canceledCount() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.canceled
}
