package ledger

import (
	"context"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/storage"
)

func TestCoverageHelpersResetAndClearRunClaims(t *testing.T) {
	ctx := context.Background()

	t.Run("display name reset restarts allocation", func(t *testing.T) {
		generator := NewDisplayNameGenerator()
		generator.Reserve("agent-1")
		if got := generator.Generate("agent"); got != "agent-2" {
			t.Fatalf("Generate before Reset = %q, want agent-2", got)
		}
		generator.Reset()
		if got := generator.Generate("agent"); got != "agent-1" {
			t.Fatalf("Generate after Reset = %q, want agent-1", got)
		}
	})

	for _, test := range []struct {
		name string
		repo LedgerRepository
	}{
		{name: "memory", repo: NewMemoryLedgerRepository()},
		{name: "storage", repo: NewStorageLedgerRepository(storage.NewMemory())},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := test.repo.ClaimRun(ctx, "run-1", "first"); err != nil {
				t.Fatalf("ClaimRun(first): %v", err)
			}
			if err := test.repo.ClearRunClaim(ctx, "run-1"); err != nil {
				t.Fatalf("ClearRunClaim: %v", err)
			}
			if err := test.repo.ClaimRun(ctx, "run-1", "second"); err != nil {
				t.Fatalf("ClaimRun(second) after clear: %v", err)
			}
		})
	}
}

func TestCoverageHelpersBorrowedStorageOwnership(t *testing.T) {
	store := storage.NewMemory()
	repo := NewBorrowedStorageLedgerRepository(store)
	if repo.UnderlyingStore() != store {
		t.Fatal("UnderlyingStore did not return the injected store")
	}
	if repo.ownsStore {
		t.Fatal("borrowed repository unexpectedly owns its store")
	}
}
