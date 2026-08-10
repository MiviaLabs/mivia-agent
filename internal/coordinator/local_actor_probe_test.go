package coordinator

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
)

// TestEnsureRunRepairedZeroTaskAdmissionReportsLocalActor is the H1 probe
// (DC-9/DC-13): resumeEmptyRun repairs a durable run stranded in the
// abandoned-creation window (status=created, zero tasks) and launches its
// execution with `go c.executeRun(h, tasks)`, but it never stamps
// h.localActor = true, unlike the three sibling local-execution entry points
// (createAndStartRunWithID in spawn.go, EnsureSingleTaskRun in ensure.go,
// resumeInterruptedRun in recovery.go). The repaired run therefore executes
// in-process yet reports LocalActor()==false. That dishonest status is a
// routing defect: panel_runner.attachPanelMemberLease releases the panel
// actor permit for a run that really executes in this process, and
// NeedsActorPermit reports the run as remote, so the panel actor limit is
// bypassed. The test fails on the pre-fix code and passes once resumeEmptyRun
// stamps localActor before the run goroutine starts.
func TestEnsureRunRepairedZeroTaskAdmissionReportsLocalActor(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	task := idempotencyTask()
	ctx := context.Background()
	runID := NewRunID()
	seedEnsuredRun(t, repo, ctx, runID, "step", []subagents.Task{task}, 0)

	// Keep the repaired run non-terminal while we probe the local-actor stamp
	// and the actor-permit path, so NeedsActorPermit observes a runnable run.
	started := make(chan struct{})
	release := make(chan struct{})
	var startedOnce, releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()

	dispatcher := runtime.New(runtime.Policy{})
	if err := dispatcher.Register(runtime.Subagent, "worker", invoker(func(ctx context.Context, _ runtime.Request) (json.RawMessage, error) {
		startedOnce.Do(func() { close(started) })
		<-release
		return json.RawMessage(`{"ok":true}`), nil
	})); err != nil {
		t.Fatal(err)
	}
	c := New(repo, subagents.New(dispatcher, subagents.Policy{Workers: 1})).(*coordinator)

	h, err := c.EnsureRun(ctx, EnsureRunRequest{RunID: runID, Tasks: []subagents.Task{task}, IdempotencyKey: "step"})
	if err != nil {
		t.Fatal(err)
	}
	if h == nil {
		t.Fatal("EnsureRun returned a nil handle")
	}
	<-started

	// The repaired run executes in this process; LocalActor must report the
	// truth. On the pre-fix code resumeEmptyRun never stamps localActor, so
	// this assertion fails.
	if !h.LocalActor() {
		t.Fatal("repaired zero-task run executes locally but LocalActor()=false: resumeEmptyRun launches go c.executeRun without stamping h.localActor")
	}

	// NeedsActorPermit must route the in-process run to the local-actor
	// permit. It resolves the registered handle first, so it is a pure read of
	// the same stamp and fails on the pre-fix code too.
	needs, err := c.NeedsActorPermit(ctx, EnsureRunRequest{RunID: runID, Tasks: []subagents.Task{task}, IdempotencyKey: "step"})
	if err != nil {
		t.Fatal(err)
	}
	if !needs {
		t.Fatal("NeedsActorPermit = false for an in-process repaired run; panel_runner would skip the panel actor limit")
	}

	unblock()
	if _, err := c.Join(ctx, h); err != nil {
		t.Fatal(err)
	}
	tasks, err := repo.ListTasks(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 {
		t.Fatalf("tasks = %d, want 1 after repair", len(tasks))
	}
	if tasks[0].Status != string(ledger.TaskStatusCompleted) {
		t.Fatalf("task status = %q, want completed", tasks[0].Status)
	}
}
