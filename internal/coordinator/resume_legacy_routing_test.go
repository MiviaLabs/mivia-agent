package coordinator

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
)

// TestCoordinator_ResumeLegacyBuiltinRuns pins the recovery contract for runs
// created by paths that never carry agent routing metadata: the delegate tool
// routes to the fixed built-in runners (multi_step/delegate/oneshot) and
// persisted snapshots with HandlerName set and AgentName/AgentDigest empty.
// A crash mid-execution must leave such a run resumable, not condemned as
// "created by an older mivia version".
func TestCoordinator_ResumeLegacyBuiltinRuns(t *testing.T) {
	for _, handlerName := range []string{subagents.HandlerMultiStep, subagents.HandlerDelegate, subagents.HandlerOneshot} {
		t.Run(handlerName, func(t *testing.T) {
			store := storage.NewMemory()
			repo := ledger.NewStorageLedgerRepository(store)
			ctx := context.Background()
			runID := "run-legacy-" + handlerName
			if err := repo.CreateRun(ctx, "", ledger.RunSnapshot{RunID: runID, Status: ledger.RunStatusRunning}); err != nil {
				t.Fatal(err)
			}
			// Snapshot shape exactly as delegateTool/runThroughCoordinator persist:
			// HandlerName names the built-in runner; no agent routing metadata.
			if err := repo.CreateTask(ctx, ledger.TaskSnapshot{
				RunID: runID, TaskID: "d1", Status: string(ledger.TaskStatusQueued), Version: 1,
				HandlerName: handlerName, Input: json.RawMessage(`{"prompt":"re-run"}`),
			}); err != nil {
				t.Fatal(err)
			}
			if err := repo.CompareAndSetTaskStatus(ctx, runID, "d1", 1, string(ledger.TaskStatusRunning)); err != nil {
				t.Fatal(err)
			}

			d := runtime.New(runtime.Policy{})
			if err := d.Register(runtime.Subagent, handlerName, staticHandler{out: json.RawMessage(`{"ok":true}`)}); err != nil {
				t.Fatal(err)
			}
			c := New(repo, subagents.New(d, subagents.Policy{Workers: 1}))
			h, err := c.ResumeInterruptedRun(ctx, runID)
			if err != nil {
				t.Fatalf("legacy builtin run %q must resume: %v", handlerName, err)
			}
			if _, err := c.Join(ctx, h); err != nil {
				t.Fatal(err)
			}
			snap, err := c.Inspect(ctx, h)
			if err != nil {
				t.Fatal(err)
			}
			if len(snap.Tasks) != 1 || snap.Tasks[0].Status != string(ledger.TaskStatusCompleted) {
				t.Fatalf("task status = %#v, want completed", snap.Tasks)
			}
		})
	}
}

// TestCoordinator_ResumeRefusesLegacyNonBuiltinHandler pins the fail-closed
// guard: a ledger snapshot without routing metadata may only re-derive the
// fixed built-in runner names. Any other legacy handler name (old skill
// handlers, arbitrary names) must be refused BEFORE the ledger is mutated.
func TestCoordinator_ResumeRefusesLegacyNonBuiltinHandler(t *testing.T) {
	store := storage.NewMemory()
	repo := ledger.NewStorageLedgerRepository(store)
	ctx := context.Background()
	runID := "run-legacy-unresolvable"
	if err := repo.CreateRun(ctx, "", ledger.RunSnapshot{RunID: runID, Status: ledger.RunStatusRunning}); err != nil {
		t.Fatal(err)
	}
	if err := repo.CreateTask(ctx, ledger.TaskSnapshot{
		RunID: runID, TaskID: "t1", Status: string(ledger.TaskStatusQueued), Version: 1,
		HandlerName: "legacy-skill", Input: json.RawMessage(`{"prompt":"work"}`),
	}); err != nil {
		t.Fatal(err)
	}
	if err := repo.CompareAndSetTaskStatus(ctx, runID, "t1", 1, string(ledger.TaskStatusRunning)); err != nil {
		t.Fatal(err)
	}

	d := runtime.New(runtime.Policy{})
	c := New(repo, subagents.New(d, subagents.Policy{Workers: 1}))
	if _, err := c.ResumeInterruptedRun(ctx, runID); err == nil {
		t.Fatal("resume must fail closed for non-builtin legacy handler")
	} else if !strings.Contains(err.Error(), "no agent routing snapshot") {
		t.Fatalf("unexpected error: %v", err)
	}

	// Validation precedes mutation: the task must be untouched. Setup left it
	// running at version 2 (queued→running CAS); markInterruptedTasks would
	// have driven running → failed → retry_pending → queued, so a still-running
	// task proves nothing was re-queued before authorization failed.
	snap, err := repo.GetTask(ctx, runID, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if snap.Status != string(ledger.TaskStatusRunning) || snap.Version != 2 || snap.HandlerName != "legacy-skill" {
		t.Fatalf("resume mutated task before authorization: %#v", snap)
	}
}
