package ledger

// Regression guard for F8 (MEDIUM): truncation-notice refs must resolve after
// a process restart. Spool grants live in memory, so a fresh spool over the
// same durable sqlite file starts with an empty grant map; without a durable
// grant the fresh spool would probe the content table, find the bytes, and
// misreport the notice's own owner as denied.

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/remainder"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
)

// newSQLiteRemainderSpool opens a sqlite-backed ledger repository and wraps it
// in a remainder spool exactly as the CLI wiring does (ContentStoreAdapter),
// so the test exercises the full durable path: store → repo → adapter → spool.
func newSQLiteRemainderSpool(t *testing.T, path string) (*remainder.Spool, *StorageLedgerRepository) {
	t.Helper()
	store, err := storage.OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	repo := NewStorageLedgerRepository(store)
	spool := remainder.NewSpool(remainder.ContentStoreAdapter{
		Store:         repo,
		NotFoundError: ErrContentNotFound,
	})
	return spool, repo
}

func TestSpoolGrantSurvivesRestartOverSQLite(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "remainder.db")
	body := []byte(strings.Repeat("restart-surviving-remainder-", 32))

	spool, repo := newSQLiteRemainderSpool(t, path)
	const principal = "session-a"
	ref := spool.Spool(ctx, principal, body)
	if ref == "" {
		t.Fatal("spool minted no ref")
	}
	// The bytes and the durable grant must be on disk before the process
	// (here: this repository plus its owned store handle) goes away.
	if err := repo.Close(); err != nil {
		t.Fatal(err)
	}

	// Restart: a fresh repository over the same sqlite file, and a fresh spool
	// whose in-memory grant map is empty.
	freshSpool, freshRepo := newSQLiteRemainderSpool(t, path)
	t.Cleanup(func() { _ = freshRepo.Close() })

	got, err := freshSpool.Load(ctx, principal, ref)
	if err != nil {
		t.Fatalf("Load after restart: %v (want the bytes, never ErrDenied)", err)
	}
	if string(got) != string(body) {
		t.Fatalf("loaded body mismatch: got %d bytes want %d", len(got), len(body))
	}
}

func TestSpoolDeniesUngrantedPrincipalAfterRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "remainder.db")
	body := []byte(strings.Repeat("deny-ungranted-after-restart-", 16))

	spool, repo := newSQLiteRemainderSpool(t, path)
	ref := spool.Spool(ctx, "session-a", body)
	if ref == "" {
		t.Fatal("spool minted no ref")
	}
	if err := repo.Close(); err != nil {
		t.Fatal(err)
	}

	freshSpool, freshRepo := newSQLiteRemainderSpool(t, path)
	t.Cleanup(func() { _ = freshRepo.Close() })

	// session-b never held a grant for ref; the bytes exist, so the semantics
	// are unchanged: ErrDenied, not a false grant and not not-found.
	if _, err := freshSpool.Load(ctx, "session-b", ref); !errors.Is(err, remainder.ErrDenied) {
		t.Fatalf("Load by un-granted principal after restart = %v, want ErrDenied", err)
	}
}
