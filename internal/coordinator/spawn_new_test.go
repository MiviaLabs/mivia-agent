package coordinator

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
)

// TestSpawnNew_ReportsWhetherThisCallCreatedTheRun is the regression for a
// gap a code review of commit 1eb82d31 found: cancelOrphanedRun (dispatch_
// tasks' synchronous wait=run path) fires whenever its own Join fails, but
// Spawn's idempotency-key lookup can hand TWO DIFFERENT callers - a
// synchronous wait=run call and a concurrent async wait=none call reusing
// the same idempotency_key - the SAME *RunHandle. Without a way to tell
// "did I just create this run" from "I hit an existing one", the
// synchronous caller's own context dying would cancel a run the async
// caller is still relying on, exactly the cross-caller cancellation the
// original fix's own reasoning said could not happen.
//
// SpawnNew is additive: every existing Spawn caller (140+ across this
// package's own tests) is untouched, since Spawn's signature never
// changed - it now just delegates to the same underlying logic SpawnNew
// exposes with the isNew signal attached.
func TestSpawnNew_ReportsWhetherThisCallCreatedTheRun(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	d := runtime.New(runtime.Policy{})
	_ = d.Register(runtime.Subagent, "test", staticHandler{out: json.RawMessage(`{"ok":true}`)})
	p := subagents.New(d, subagents.Policy{Workers: 1})
	c := New(repo, p)

	h1, isNew1, err := c.SpawnNew(context.Background(), []subagents.Task{
		{ID: "t1", Name: "test"},
	}, "key-1")
	if err != nil {
		t.Fatal(err)
	}
	if !isNew1 {
		t.Fatal("expected the first SpawnNew call to report isNew=true (it created the run)")
	}

	h2, isNew2, err := c.SpawnNew(context.Background(), []subagents.Task{
		{ID: "t1", Name: "test"},
	}, "key-1")
	if err != nil {
		t.Fatal(err)
	}
	if h1 != h2 {
		t.Fatal("expected the idempotent hit to return the SAME handle as the first call")
	}
	if isNew2 {
		t.Fatal("expected the idempotent hit to report isNew=false (it did not create the run)")
	}
}

// TestSpawn_StillWorksUnchanged pins that Spawn's own public signature and
// behavior are completely untouched by SpawnNew's addition.
func TestSpawn_StillWorksUnchanged(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	d := runtime.New(runtime.Policy{})
	_ = d.Register(runtime.Subagent, "test", staticHandler{out: json.RawMessage(`{"ok":true}`)})
	p := subagents.New(d, subagents.Policy{Workers: 1})
	c := New(repo, p)

	h1, err := c.Spawn(context.Background(), []subagents.Task{{ID: "t1", Name: "test"}}, "key-2")
	if err != nil {
		t.Fatal(err)
	}
	h2, err := c.Spawn(context.Background(), []subagents.Task{{ID: "t1", Name: "test"}}, "key-2")
	if err != nil {
		t.Fatal(err)
	}
	if h1 != h2 {
		t.Fatal("Spawn's own idempotency behavior regressed")
	}
}
