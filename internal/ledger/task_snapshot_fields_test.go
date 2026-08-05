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
		HandlerName:  "worker",
		AgentName:    "worker",
		AgentDigest:  "sha256:agent-v1",
		ProviderName: "deepseek",
		Model:        "deepseek-v4-flash",
		Skill:        "audit",
		Scope:        "resource-a",
		OutputSchema: map[string]any{"type": "object", "additionalProperties": false},
		Input:        json.RawMessage(`{"prompt":"payload"}`),
		Timeout:      7 * time.Second,
		Budget:       42,
		Depth:        3,
	}

	// reopen forces a real serialization round trip. StorageLedgerRepository
	// serves reads from an in-process projection, so writing and reading through
	// one instance never touches marshalTaskSnapshot at all - tagging every new
	// field json:"-" still passed before this was added.
	cases := map[string]func(t *testing.T) (LedgerRepository, func(LedgerRepository) LedgerRepository){
		"memory": func(*testing.T) (LedgerRepository, func(LedgerRepository) LedgerRepository) {
			return NewMemoryLedgerRepository(), func(r LedgerRepository) LedgerRepository { return r }
		},
		"storage_after_reopen": func(t *testing.T) (LedgerRepository, func(LedgerRepository) LedgerRepository) {
			store := storage.NewMemory()
			repo := NewStorageLedgerRepository(store)
			return repo, func(old LedgerRepository) LedgerRepository {
				_ = old.(*StorageLedgerRepository).Close()
				fresh := NewStorageLedgerRepository(store)
				t.Cleanup(func() { _ = fresh.Close() })
				return fresh
			}
		},
	}

	for name, build := range cases {
		t.Run(name, func(t *testing.T) {
			repo, reopen := build(t)
			if err := repo.CreateRun(ctx, "", RunSnapshot{RunID: "r1", Status: RunStatusRunning}); err != nil {
				t.Fatal(err)
			}
			if err := repo.CreateTask(ctx, want); err != nil {
				t.Fatal(err)
			}
			repo = reopen(repo)
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
			if got[0].AgentName != want.AgentName || got[0].AgentDigest != want.AgentDigest || got[0].Skill != want.Skill {
				t.Errorf("routing metadata lost: %#v", got[0])
			}
			if got[0].ProviderName != want.ProviderName || got[0].Model != want.Model {
				t.Errorf("resolved binding lost: provider=%q model=%q", got[0].ProviderName, got[0].Model)
			}
			if got[0].OutputSchema["type"] != "object" {
				t.Errorf("output schema lost: %#v", got[0].OutputSchema)
			}
			if got[0].Scope != want.Scope {
				t.Errorf("scope lost: %q", got[0].Scope)
			}
		})
	}
}

// Clone must deep-copy Input, or a mutation through one snapshot corrupts the
// other - snapshots are handed out by value from the in-memory repo.
func TestTaskSnapshotCloneDeepCopiesInput(t *testing.T) {
	original := TaskSnapshot{Input: json.RawMessage(`{"a":1}`)}
	clone := original.Clone()
	clone.Input[2] = 'X'
	if string(original.Input) == string(clone.Input) {
		t.Fatalf("Clone shares the Input backing array: %q", original.Input)
	}
}
