package storage

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
)

// TestCommitSiblingSessionsConcurrent reproduces the lost-turn failure of
// mivia-agent-desktop: sibling mivia chat sidecars share one workspace, so
// their two store handles write the same context.db file. Both sessions end
// a turn at the same time. Each commit transaction reads its session head,
// then writes. When the other handle commits between those two steps, the
// read snapshot is stale and the write-lock upgrade fails at once with
// SQLITE_BUSY_SNAPSHOT, which busy_timeout cannot clear - before the busy
// retry existed, the losing turn was lost, even though the sibling commit
// touched a different session. The retry repeats the transaction on a fresh
// snapshot, so both turns persist.
func TestCommitSiblingSessionsConcurrent(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "context.db")
	first, principalA := openContextStoreAt(t, path)
	defer first.Close()
	second, err := OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	principalB, err := contextstate.NewPrincipal("workspace", "session-b", "subject")
	if err != nil {
		t.Fatal(err)
	}
	binding := contextTestBinding(t)
	ensureContextSession(t, first, principalA, binding)
	ensureContextSession(t, second, principalB, binding)
	const iterations = 25
	for i := 0; i < iterations; i++ {
		expected := contextstate.Revision{Session: uint64(i), Durable: uint64(i), Source: uint64(i)}
		requestA := contextCommitRequest(t, principalA, expected, binding, fmt.Sprintf("commit-a-%d", i), "state-a")
		requestB := contextCommitRequest(t, principalB, expected, binding, fmt.Sprintf("commit-b-%d", i), "state-b")
		start := make(chan struct{})
		errs := make([]error, 2)
		commits := []func() error{
			func() error { return first.Commit(ctx, requestA) },
			func() error { return second.Commit(ctx, requestB) },
		}
		var wg sync.WaitGroup
		for gi, commit := range commits {
			wg.Add(1)
			go func(slot int, fn func() error) {
				defer wg.Done()
				<-start
				errs[slot] = fn()
			}(gi, commit)
		}
		close(start)
		wg.Wait()
		if errs[0] != nil || errs[1] != nil {
			t.Fatalf("iteration %d: sibling commits failed: %v / %v", i, errs[0], errs[1])
		}
	}
	var revisions int
	if err := first.db.QueryRow(`SELECT count(*) FROM context_sessions WHERE session_revision=?`, iterations).Scan(&revisions); err != nil {
		t.Fatal(err)
	}
	if revisions != 2 {
		t.Fatalf("sessions at revision %d = %d, want 2", iterations, revisions)
	}
}

// TestSaveSessionSiblingCatalogAutosaveConcurrent covers the catalog
// autosave that runs after every turn: both handles upsert their session row
// at the same turn boundary, and a busy collision must not keep either
// session out of the catalog.
func TestSaveSessionSiblingCatalogAutosaveConcurrent(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "context.db")
	first, principalA := openContextStoreAt(t, path)
	defer first.Close()
	second, err := OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	const iterations = 25
	for i := 0; i < iterations; i++ {
		state := fmt.Sprintf(`[{"role":"user","content":"turn %d"}]`, i)
		start := make(chan struct{})
		errs := make([]error, 2)
		saves := []func() error{
			func() error {
				return first.SaveSession(ctx, principalA, "sess-a", []byte(state), "model", "provider", i+1, 1, 1, contextstate.SessionSaveOptions{Dir: "/tmp/a"})
			},
			func() error {
				return second.SaveSession(ctx, principalA, "sess-a", []byte(state), "model", "provider", i+1, 1, 1, contextstate.SessionSaveOptions{Dir: "/tmp/a"})
			},
		}
		var wg sync.WaitGroup
		for gi, save := range saves {
			wg.Add(1)
			go func(slot int, fn func() error) {
				defer wg.Done()
				<-start
				errs[slot] = fn()
			}(gi, save)
		}
		close(start)
		wg.Wait()
		if errs[0] != nil || errs[1] != nil {
			t.Fatalf("iteration %d: sibling saves failed: %v / %v", i, errs[0], errs[1])
		}
	}
	var rows int
	if err := first.db.QueryRow(`SELECT count(*) FROM chat_sessions WHERE name='sess-a'`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Fatalf("catalog rows for sess-a = %d, want 1", rows)
	}
}
