package coordinator

import (
	"context"
	"encoding/json"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
)

// TestWatchJoinedRunReclaimsOrphanedClaim reproduces a crashed claim holder:
// process A admits a run and its claim is then handed to an orphan holder
// that never heartbeats again (simulating A dying mid-flight, as opposed to
// a clean release). Process B joins the same admission, sees the claim held,
// and must not wait forever for a terminal status that will never come from
// a dead owner - it must eventually reclaim the expired claim and finish the
// work itself.
func TestWatchJoinedRunReclaimsOrphanedClaim(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ledger.db")
	aStore, err := storage.OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	bStore, err := storage.OpenSQLite(path)
	if err != nil {
		_ = aStore.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = aStore.Close(); _ = bStore.Close() })

	var calls atomic.Int32
	block := make(chan struct{}) // never closed: A's invocation never returns
	dispatcher := runtime.New(runtime.Policy{})
	if err := dispatcher.Register(runtime.Subagent, "worker", invoker(func(ctx context.Context, _ runtime.Request) (json.RawMessage, error) {
		if calls.Add(1) == 1 {
			<-block
			return nil, ctx.Err()
		}
		return json.RawMessage(`{"ok":true}`), nil
	})); err != nil {
		t.Fatal(err)
	}

	aRepo := ledger.NewStorageLedgerRepository(aStore)
	a := New(aRepo, subagents.New(dispatcher, subagents.Policy{Workers: 1}))
	req := EnsureRunRequest{
		RunID:          NewRunID(),
		Tasks:          []subagents.Task{{ID: "task-1", Name: "worker", AgentName: "worker", AgentDigest: "digest-1", Input: json.RawMessage(`"work"`)}},
		IdempotencyKey: "orphan-reclaim",
	}
	if _, err := a.EnsureSingleTaskRun(context.Background(), req); err != nil {
		t.Fatalf("admit via A: %v", err)
	}

	waitForTaskRunning(t, aRepo, req.RunID)

	// Simulate A crashing without ever releasing or re-heartbeating its
	// claim: hand it to an orphan holder that will never refresh it again.
	if err := aRepo.TakeoverExpiredRunClaim(context.Background(), req.RunID, "orphan-holder", 0); err != nil {
		t.Fatalf("orphan claim: %v", err)
	}

	bRepo := ledger.NewStorageLedgerRepository(bStore)
	b := New(bRepo, subagents.New(dispatcher, subagents.Policy{Workers: 1})).(*coordinator)
	b.claimLease = 30 * time.Millisecond // the orphan claim ages past this almost immediately

	h, err := b.EnsureSingleTaskRun(context.Background(), req)
	if err != nil {
		t.Fatalf("join via B: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result, err := b.Join(ctx, h)
	if err != nil {
		t.Fatalf("B never reclaimed the orphaned run: %v", err)
	}
	if len(result.Results) != 1 || result.Results[0].Err != nil {
		t.Fatalf("unexpected result: %+v", result.Results)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("want 2 handler invocations (orphaned + reclaimed), got %d", got)
	}
}

// waitForTaskRunning polls until runID's single task reaches status running,
// or fails the test after 2s.
func waitForTaskRunning(t *testing.T, repo *ledger.StorageLedgerRepository, runID string) {
	t.Helper()
	pollTicker := time.NewTicker(5 * time.Millisecond)
	defer pollTicker.Stop()
	deadline := time.After(2 * time.Second)
	for {
		snaps, err := repo.ListTasks(context.Background(), runID)
		if err != nil {
			t.Fatal(err)
		}
		if len(snaps) == 1 && snaps[0].Status == string(ledger.TaskStatusRunning) {
			return
		}
		select {
		case <-pollTicker.C:
		case <-deadline:
			t.Fatalf("task never reached running")
		}
	}
}
