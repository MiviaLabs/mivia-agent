package ledger

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/storage"
)

func TestSharedContentRefSurvivesOneRunDeletion(t *testing.T) {
	ctx := context.Background()
	payload := []byte("shared recorded task output")
	ref := Reference(RefKindOutput, payload)
	if ref == "" {
		t.Fatal("Reference returned an empty reference")
	}

	t.Run("memory", func(t *testing.T) {
		repo := NewMemoryLedgerRepository()
		assertSharedContentSurvivesRunDeletion(t, ctx, repo, ref, payload)
	})

	t.Run("sqlite_reopen", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "ledger.db")
		store, err := storage.OpenSQLite(path)
		if err != nil {
			t.Fatal(err)
		}
		repo := NewStorageLedgerRepository(store)
		seedRunTaskWithContent(t, ctx, repo, "run-a", "task-a", ref, payload)
		seedRunTaskWithContent(t, ctx, repo, "run-b", "task-b", ref, payload)
		if err := repo.DeleteRun(ctx, "run-a"); err != nil {
			t.Fatal(err)
		}
		if err := repo.Close(); err != nil {
			t.Fatal(err)
		}

		reopenedStore, err := storage.OpenSQLite(path)
		if err != nil {
			t.Fatal(err)
		}
		reopened := NewStorageLedgerRepository(reopenedStore)
		t.Cleanup(func() {
			if err := reopened.Close(); err != nil {
				t.Errorf("Close reopened repository: %v", err)
			}
		})
		assertSurvivingRunContent(t, ctx, reopened, "run-b", "task-b", ref, payload)
	})
}

func TestContentStoreIsNeverReclaimed(t *testing.T) {
	ctx := context.Background()
	repo := NewMemoryLedgerRepository()
	contents := [][]byte{[]byte("first recorded output"), []byte("second recorded output")}
	refs := make([]string, len(contents))

	for i, content := range contents {
		refs[i] = Reference(RefKindOutput, content)
		if refs[i] == "" {
			t.Fatalf("Reference(%q) returned an empty reference", content)
		}
		runID := "run-" + string(rune('a'+i))
		seedRunTaskWithContent(t, ctx, repo, runID, "task", refs[i], content)
		if err := repo.DeleteRun(ctx, runID); err != nil {
			t.Fatal(err)
		}
	}

	for i, ref := range refs {
		got, err := repo.LoadContent(ctx, ref)
		if err != nil {
			t.Fatalf("LoadContent(%q) after all runs were deleted: %v", ref, err)
		}
		if !bytes.Equal(got, contents[i]) {
			t.Fatalf("LoadContent(%q) = %q, want %q", ref, got, contents[i])
		}
	}
}

func assertSharedContentSurvivesRunDeletion(t *testing.T, ctx context.Context, repo LedgerRepository, ref string, payload []byte) {
	t.Helper()
	seedRunTaskWithContent(t, ctx, repo, "run-a", "task-a", ref, payload)
	seedRunTaskWithContent(t, ctx, repo, "run-b", "task-b", ref, payload)
	if err := repo.DeleteRun(ctx, "run-a"); err != nil {
		t.Fatal(err)
	}
	assertSurvivingRunContent(t, ctx, repo, "run-b", "task-b", ref, payload)
}

func seedRunTaskWithContent(t *testing.T, ctx context.Context, repo LedgerRepository, runID, taskID, ref string, content []byte) {
	t.Helper()
	if err := repo.CreateRun(ctx, "", RunSnapshot{RunID: runID, Status: RunStatusCreated}); err != nil {
		t.Fatal(err)
	}
	if err := repo.CreateTask(ctx, TaskSnapshot{RunID: runID, TaskID: taskID, Status: string(TaskStatusQueued)}); err != nil {
		t.Fatal(err)
	}
	if err := repo.StoreContent(ctx, ref, content); err != nil {
		t.Fatal(err)
	}
	if err := repo.SetTaskOutput(ctx, runID, taskID, ref, "", ""); err != nil {
		t.Fatal(err)
	}
}

func assertSurvivingRunContent(t *testing.T, ctx context.Context, repo LedgerRepository, runID, taskID, ref string, want []byte) {
	t.Helper()
	task, err := repo.GetTask(ctx, runID, taskID)
	if err != nil {
		t.Fatal(err)
	}
	if task.OutputRef != ref {
		t.Fatalf("surviving task OutputRef = %q, want %q", task.OutputRef, ref)
	}
	got, err := repo.LoadContent(ctx, task.OutputRef)
	if err != nil {
		t.Fatalf("LoadContent(%q): %v", task.OutputRef, err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("LoadContent(%q) = %q, want %q", task.OutputRef, got, want)
	}
}
