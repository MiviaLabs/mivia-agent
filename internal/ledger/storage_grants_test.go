package ledger

import (
	"context"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/storage"
)

// TestStorageGrantSpoolNoOpWithoutSpoolGrantStore pins the no-op forward
// branches of GrantSpool / CheckSpoolGrant: a repository over a store that
// does not implement the optional spool-grant surface (the memory backend)
// must ignore durable grants instead of failing or fabricating them. Only
// sqlite-backed stores persist spool grants; every other backend keeps
// in-process-only visibility.
func TestStorageGrantSpoolNoOpWithoutSpoolGrantStore(t *testing.T) {
	repo := NewStorageLedgerRepository(storage.NewMemory())
	ctx := context.Background()

	if err := repo.GrantSpool(ctx, "ref:output:mem", "session-a"); err != nil {
		t.Fatalf("GrantSpool over a memory-backed store: %v, want a nil no-op", err)
	}

	granted, err := repo.CheckSpoolGrant(ctx, "ref:output:mem", "session-a")
	if err != nil {
		t.Fatalf("CheckSpoolGrant over a memory-backed store: %v, want nil", err)
	}
	if granted {
		t.Fatal("memory-backed repository reported a durable grant it never persisted")
	}
}
