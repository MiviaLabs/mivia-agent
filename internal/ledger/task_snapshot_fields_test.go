package ledger

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/storage"
)

// Plan 12: the work-describing fields must survive a round trip through both
// repositories, or resume silently rebuilds an empty task again.
func TestTaskSnapshotRoundTripsNewFields(t *testing.T) {
	ctx := context.Background()
	want := TaskSnapshot{
		RunID: "r1", TaskID: "t1", Status: string(TaskStatusQueued), Version: 1,
		HandlerName: "worker",
		Input:       json.RawMessage(`{"prompt":"payload"}`),
		Timeout:     7 * time.Second,
		Budget:      42,
		Depth:       3,
	}

	repos := map[string]LedgerRepository{"memory": NewMemoryLedgerRepository()}
	storeRepo := NewStorageLedgerRepository(storage.NewMemory())
	t.Cleanup(func() { _ = storeRepo.Close() })
	repos["storage"] = storeRepo

	for name, repo := range repos {
		t.Run(name, func(t *testing.T) {
			if err := repo.CreateRun(ctx, "", RunSnapshot{RunID: "r1", Status: RunStatusRunning}); err != nil {
				t.Fatal(err)
			}
			if err := repo.CreateTask(ctx, want); err != nil {
				t.Fatal(err)
			}
			got, err := repo.ListTasks(ctx, "r1")
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != 1 {
				t.Fatalf("expected 1 task, got %d", len(got))
			}
			if string(got[0].Input) != string(want.Input) {
				t.Errorf("Input lost: %q", got[0].Input)
			}
			if got[0].Timeout != want.Timeout || got[0].Budget != want.Budget || got[0].Depth != want.Depth {
				t.Errorf("limits lost: timeout=%s budget=%d depth=%d", got[0].Timeout, got[0].Budget, got[0].Depth)
			}
		})
	}
}

// Clone must deep-copy Input, or a mutation through one snapshot corrupts the
// other — snapshots are handed out by value from the in-memory repo.
func TestTaskSnapshotCloneDeepCopiesInput(t *testing.T) {
	original := TaskSnapshot{Input: json.RawMessage(`{"a":1}`)}
	clone := original.Clone()
	clone.Input[2] = 'X'
	if string(original.Input) == string(clone.Input) {
		t.Fatalf("Clone shares the Input backing array: %q", original.Input)
	}
}
